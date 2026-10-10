package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/nguyen-anh-hao/api-gateway/internal/middleware"
)

var accountSeq atomic.Uint64

// CreateAccount handles POST /accounts (business group 1: Treasury & Account).
// It is public (registration) and returns a fresh API key/secret so the new
// account can authenticate immediately — no pre-existing key is required.
func (h *Handler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	id := accountSeq.Add(1) + 1000 // start accounts above the seeded demo account

	apiKey := randomHex(16)
	secret := randomHex(32)
	permissions := []string{"read_only", "trade", "withdraw"}
	h.Auth.Register(apiKey, middleware.KeyInfo{
		AccountID:   id,
		Secret:      secret,
		Permissions: permissions,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account_id":  id,
		"api_key":     apiKey,
		"secret_key":  secret,
		"permissions": permissions,
	})
}

// CreateApiKey handles POST /accounts/api-keys. It generates an API key/secret
// pair for a target account (defaults to the authenticated account) and
// registers it into the auth store so the new key can authenticate.
func (h *Handler) CreateApiKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID   uint64   `json:"account_id"`
		Permissions []string `json:"permissions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.AccountID == 0 {
		req.AccountID = accountID(r)
	}
	if len(req.Permissions) == 0 {
		req.Permissions = []string{"read_only", "trade", "withdraw"}
	}

	apiKey := randomHex(16)
	secret := randomHex(32)
	h.Auth.Register(apiKey, middleware.KeyInfo{
		AccountID:   req.AccountID,
		Secret:      secret,
		Permissions: req.Permissions,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account_id":  req.AccountID,
		"api_key":     apiKey,
		"secret_key":  secret,
		"permissions": req.Permissions,
	})
}

// Deposit handles POST /deposit (faucet/deposit of cash or virtual stock).
func (h *Handler) Deposit(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	var req struct {
		Asset  string `json:"asset"`
		Amount string `json:"amount"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := h.Risk.Deposit(r.Context(), acct, req.Asset, req.Amount); err != nil {
		writeErr(w, http.StatusBadGateway, "risk engine deposit failed")
		return
	}
	_ = h.Settlement.Deposit(r.Context(), acct, req.Asset, req.Amount)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// Withdraw handles POST /withdraw.
func (h *Handler) Withdraw(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	var req struct {
		Asset  string `json:"asset"`
		Amount string `json:"amount"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := h.Risk.Withdraw(r.Context(), acct, req.Asset, req.Amount); err != nil {
		writeErr(w, http.StatusBadGateway, "risk engine withdraw failed")
		return
	}
	_ = h.Settlement.Withdraw(r.Context(), acct, req.Asset, req.Amount)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// GetBalances handles GET /balances (reads from the in-memory risk engine).
func (h *Handler) GetBalances(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	balances, err := h.Risk.GetBalances(r.Context(), acct)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "risk engine unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"balances": balances})
}

// GetLedger handles GET /ledger (reads from the settlement service).
func (h *Handler) GetLedger(w http.ResponseWriter, r *http.Request) {
	acct := accountID(r)
	entries, err := h.Settlement.GetLedger(r.Context(), acct)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "settlement service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
