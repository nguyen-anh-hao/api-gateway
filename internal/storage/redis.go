// Package storage contains optional Redis wiring for hot-path state (auth keys,
// rate-limit buckets). When REDIS_ADDR is unset, services fall back to their
// in-memory implementations so they remain runnable standalone.
package storage

import (
	"context"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient returns a Redis client when REDIS_ADDR is configured, or nil
// to keep services runnable without Redis.
func NewRedisClient() *redis.Client {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		return nil
	}
	return redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       0,
	})
}

// Available reports whether rdb is non-nil and reachable within a short budget.
func Available(ctx context.Context, rdb *redis.Client) bool {
	if rdb == nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	return rdb.Ping(cctx).Err() == nil
}
