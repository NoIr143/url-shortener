// Package cache implements the status-aware redirect cache described as
// ARC-007/DATA-004 in docs/SYSTEM_DESIGN.md: a bounded, disposable
// derivative of the authoritative mapping repository (CLAUDE.md), never
// itself the source of truth. TTL and explicit-invalidation propagation
// target 60 seconds per docs/decisions/DEC-008.md. This is POC-003
// evidence, not a production-hardened cache layer.
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound is returned when neither the cache nor the fallback source
// has an entry for the key.
var ErrNotFound = errors.New("cache: no mapping for key")

// Source is the authoritative fallback the cache defers to on a miss or a
// cache failure — satisfied by *mapping.Repository, kept as an interface
// here so the cache package does not depend on the mapping package (and
// so unit tests can use a fake without a real DynamoDB Local instance).
type Source interface {
	Get(ctx context.Context, shortKey string) (destination string, ok bool, err error)
}

// RedirectCache wraps a Source with a bounded, TTL-based cache. Every read
// path degrades to the Source on any Valkey error rather than surfacing a
// cache failure to the caller — NFR-SAFE-001's fail-closed-on-ambiguity
// invariant applies to the redirect *destination*, not to the cache being
// merely unavailable; falling back to the authoritative source is the safe
// behavior, not a failure.
type RedirectCache struct {
	rdb    *redis.Client
	source Source
	ttl    time.Duration
}

func New(rdb *redis.Client, source Source, ttl time.Duration) *RedirectCache {
	return &RedirectCache{rdb: rdb, source: source, ttl: ttl}
}

// Get returns the destination for shortKey, preferring the cache and
// falling back to the authoritative Source on a miss or any cache error.
// It reports (destination, fromCache, error).
func (c *RedirectCache) Get(ctx context.Context, shortKey string) (destination string, fromCache bool, err error) {
	val, err := c.rdb.Get(ctx, shortKey).Result()
	if err == nil {
		return val, true, nil
	}
	if err != redis.Nil {
		// Cache unavailable/erroring: degrade to the authoritative source
		// rather than failing the request.
		return c.fallback(ctx, shortKey)
	}
	// redis.Nil: genuine cache miss.
	return c.fallback(ctx, shortKey)
}

func (c *RedirectCache) fallback(ctx context.Context, shortKey string) (string, bool, error) {
	dest, ok, err := c.source.Get(ctx, shortKey)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, ErrNotFound
	}
	// Populate the cache for next time; a populate failure must not fail
	// the read that is already safely served from the authoritative
	// source.
	_ = c.rdb.Set(ctx, shortKey, dest, c.ttl).Err()
	return dest, false, nil
}

// Invalidate removes a key from the cache immediately — the explicit
// invalidation path a privileged suspend/reinstate action uses so
// propagation does not depend solely on passive TTL expiry.
func (c *RedirectCache) Invalidate(ctx context.Context, shortKey string) error {
	return c.rdb.Del(ctx, shortKey).Err()
}
