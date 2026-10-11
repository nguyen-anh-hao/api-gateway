package middleware

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// KeyInfo describes an API key and its permissions.
type KeyInfo struct {
	AccountID   uint64   `json:"account_id"`
	Secret      string   `json:"secret"`
	Permissions []string `json:"permissions"`
}

// AuthManager is an API-key store. Keys live in Redis (auth:key:*), with an
// in-memory mirror acting as a local cache and a demo seed so the service still
// runs standalone when Redis is not configured.
type AuthManager struct {
	mu   sync.RWMutex
	keys map[string]KeyInfo
	rdb  *redis.Client
}

func NewAuthManager(rdb *redis.Client) *AuthManager {
	m := &AuthManager{rdb: rdb, keys: map[string]KeyInfo{
		"demo-key": {
			AccountID:   1,
			Secret:      "demo-secret",
			Permissions: []string{"read_only", "trade", "withdraw"},
		},
	}}
	if env := os.Getenv("GATEWAY_API_KEYS"); env != "" {
		var extra map[string]KeyInfo
		if err := json.Unmarshal([]byte(env), &extra); err == nil {
			for k, v := range extra {
				m.keys[k] = v
			}
		} else {
			slog.Warn("failed to parse GATEWAY_API_KEYS", "err", err)
		}
	}
	return m
}

// SignHMAC computes the HMAC-SHA256 signature of payload with the given secret.
func SignHMAC(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// Register adds (or replaces) an API key at runtime. Used by CreateApiKey so
// newly-created accounts can authenticate with their own key.
func (m *AuthManager) Register(apiKey string, info KeyInfo) {
	m.mu.Lock()
	m.keys[apiKey] = info
	m.mu.Unlock()
	if m.rdb == nil {
		return
	}
	if b, err := json.Marshal(info); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = m.rdb.Set(ctx, "auth:key:"+apiKey, b, 0).Err()
		cancel()
	}
}

// lookup resolves an API key from the local cache, falling back to Redis so
// keys created by other gateway replicas authenticate without a restart.
func (m *AuthManager) lookup(apiKey string) (KeyInfo, bool) {
	m.mu.RLock()
	info, ok := m.keys[apiKey]
	m.mu.RUnlock()
	if ok || m.rdb == nil {
		return info, ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	b, err := m.rdb.Get(ctx, "auth:key:"+apiKey).Bytes()
	cancel()
	if err != nil {
		return KeyInfo{}, false
	}
	var ki KeyInfo
	if json.Unmarshal(b, &ki) != nil {
		return KeyInfo{}, false
	}
	m.mu.Lock()
	m.keys[apiKey] = ki
	m.mu.Unlock()
	return ki, true
}

// Authenticate validates the API key (and signature when present / required)
// and injects the account id into the request context.
func (m *AuthManager) Authenticate(next http.Handler) http.Handler {
	requireSig := os.Getenv("AUTH_REQUIRE_SIGNATURE") == "true"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("X-API-Key")
		signature := r.Header.Get("X-Signature")

		info, ok := m.lookup(apiKey)
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// Read the body so we can sign it (and restore it for downstream).
		payload := r.URL.RequestURI()
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			b, err := io.ReadAll(r.Body)
			if err == nil {
				payload = string(b)
				r.Body = io.NopCloser(bytes.NewReader(b))
			}
		}

		if requireSig || signature != "" {
			expected := SignHMAC(info.Secret, payload)
			if !hmac.Equal([]byte(expected), []byte(signature)) {
				http.Error(w, `{"error":"bad_signature"}`, http.StatusUnauthorized)
				return
			}
		}

		ctx := context.WithValue(r.Context(), AccountIDKey{}, info.AccountID)
		ctx = context.WithValue(ctx, PermissionsKey{}, append([]string(nil), info.Permissions...))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type AccountIDKey struct{}
type PermissionsKey struct{}

func AccountIDFrom(ctx context.Context) uint64 {
	if v, ok := ctx.Value(AccountIDKey{}).(uint64); ok {
		return v
	}
	return 0
}

// HasPermission reports whether the authenticated API key holds the permission.
func HasPermission(ctx context.Context, perm string) bool {
	perms, _ := ctx.Value(PermissionsKey{}).([]string)
	for _, p := range perms {
		if p == perm {
			return true
		}
	}
	return false
}

// RequirePermission wraps a handler so that requests without the required API-key
// permission are rejected with 403. It must run after Authenticate.
func RequirePermission(perm string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !HasPermission(r.Context(), perm) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
