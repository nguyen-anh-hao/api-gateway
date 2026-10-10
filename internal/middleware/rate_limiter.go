package middleware

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// TokenBucket is a classic token-bucket rate limiter.
type TokenBucket struct {
	mu       sync.Mutex
	capacity float64
	tokens   float64
	rate     float64 // tokens per second
	last     time.Time
}

func NewTokenBucket(capacity, rate float64) *TokenBucket {
	return &TokenBucket{capacity: capacity, tokens: capacity, rate: rate, last: time.Now()}
}

func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	now := time.Now()
	tb.tokens += now.Sub(tb.last).Seconds() * tb.rate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}
	tb.last = now
	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// tokenBucketScript implements a distributed token bucket in Redis so the rate
// limit is shared across gateway replicas. KEYS[1]=bucket key, ARGV=capacity,
// rate (tokens/sec), now (unix seconds).
var tokenBucketScript = redis.NewScript(`
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local bucket = redis.call('HMGET', key, 'tokens', 'last')
local tokens = tonumber(bucket[1])
local last = tonumber(bucket[2])
if tokens == nil then
  tokens = capacity
  last = now
end
tokens = math.min(capacity, tokens + (now - last) * rate)
local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end
redis.call('HMSET', key, 'tokens', tokens, 'last', now)
redis.call('EXPIRE', key, 2)
return allowed
`)

// redisAllow consumes one token from the shared bucket for ip. It returns an
// error when Redis is unreachable so the caller can fall back to in-memory.
func redisAllow(ctx context.Context, rdb *redis.Client, ip string, capacity, rate float64) (bool, error) {
	key := "ratelimit:" + ip
	now := float64(time.Now().UnixNano()) / 1e9
	res, err := tokenBucketScript.Run(ctx, rdb, []string{key}, capacity, rate, now).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func inMemAllow(buckets *sync.Map, ip string, capacity, rate float64) bool {
	v, _ := buckets.LoadOrStore(ip, NewTokenBucket(capacity, rate))
	return v.(*TokenBucket).Allow()
}

// RateLimiter wraps a handler with a per-client-IP token bucket. When Redis is
// configured it uses a shared distributed bucket; otherwise it falls back to an
// in-memory bucket (one per gateway instance).
func RateLimiter(rdb *redis.Client, capacity, rate float64) func(http.Handler) http.Handler {
	buckets := &sync.Map{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r)
			allowed := true
			if rdb != nil {
				if ok, err := redisAllow(r.Context(), rdb, ip, capacity, rate); err == nil {
					allowed = ok
				} else {
					allowed = inMemAllow(buckets, ip, capacity, rate)
				}
			} else {
				allowed = inMemAllow(buckets, ip, capacity, rate)
			}
			if !allowed {
				http.Error(w, `{"error":"rate_limited"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	host := r.RemoteAddr
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}
