// Package cache implements ARC-007's disposable, status-aware redirect
// cache. The authoritative mapping repository remains the source of truth;
// every cache miss, stale/corrupt value, or Valkey failure falls back to it.
package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"url-shortener/internal/domain"

	"github.com/redis/go-redis/v9"
)

const (
	entrySchemaVersion = 1
	entryKindMapping   = "mapping"
	entryKindUnknown   = "unknown"
	keyPrefix          = "redirect:v1:"

	// MaxMappingTTL is DEC-008's maximum propagation bound for active and
	// suspended cache entries. New rejects any longer configured TTL.
	MaxMappingTTL = 60 * time.Second
	// DefaultNegativeTTL is a conservative engineering default for unknown
	// keys. It is deliberately much shorter than the lifecycle-state bound.
	DefaultNegativeTTL = 5 * time.Second
)

var (
	ErrInvalidConfiguration = errors.New("cache: invalid configuration")
	ErrInvalidSourceRecord  = errors.New("cache: invalid authoritative record")
)

// Source is the authoritative mapping reader. Version is required even
// though application.MappingReader does not expose it: every positive cache
// entry is state-version aware per ADR-013.
type Source interface {
	Get(ctx context.Context, shortKey domain.ShortKey) (destination domain.Destination, status domain.Status, version int64, found bool, err error)
}

// RedirectCache satisfies application.MappingReader while keeping Valkey a
// derived optimization. It intentionally has no write-through behavior.
type RedirectCache struct {
	rdb         *redis.Client
	source      Source
	mappingTTL  time.Duration
	negativeTTL time.Duration
	now         func() time.Time
}

// New constructs a cache with explicit, bounded TTLs. Unknown-key caching is
// required to be no longer than mapping caching so a newly-created mapping is
// not hidden beyond the lifecycle-state freshness interval.
func New(rdb *redis.Client, source Source, mappingTTL, negativeTTL time.Duration) (*RedirectCache, error) {
	if rdb == nil || source == nil || mappingTTL <= 0 || mappingTTL > MaxMappingTTL || negativeTTL <= 0 || negativeTTL > mappingTTL {
		return nil, ErrInvalidConfiguration
	}
	return &RedirectCache{
		rdb:         rdb,
		source:      source,
		mappingTTL:  mappingTTL,
		negativeTTL: negativeTTL,
		now:         time.Now,
	}, nil
}

type cacheEntry struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Destination   string `json:"destination,omitempty"`
	Status        string `json:"status,omitempty"`
	Version       int64  `json:"version,omitempty"`
	ExpiresAt     string `json:"expires_at"`
}

type cachedResult struct {
	destination domain.Destination
	status      domain.Status
	found       bool
}

// Get returns a fresh, internally valid cache entry when possible. Any cache
// ambiguity becomes an authoritative read; only an authoritative failure is
// surfaced to the application, which then fails closed without a redirect.
func (c *RedirectCache) Get(ctx context.Context, shortKey domain.ShortKey) (domain.Destination, domain.Status, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Destination{}, "", false, err
	}

	cacheKey := keyPrefix + shortKey.String()
	encoded, err := c.rdb.Get(ctx, cacheKey).Bytes()
	if err == nil {
		result, decodeErr := decodeEntry(encoded, c.now())
		if decodeErr == nil {
			return result.destination, result.status, result.found, nil
		}
		// A stale or corrupt entry is not evidence. Remove it best-effort so
		// later requests do not repeatedly parse the same bad payload.
		_ = c.rdb.Del(ctx, cacheKey).Err()
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return domain.Destination{}, "", false, ctxErr
	}
	// redis.Nil and all operational Valkey errors safely converge here.
	return c.fallback(ctx, cacheKey, shortKey)
}

func (c *RedirectCache) fallback(ctx context.Context, cacheKey string, shortKey domain.ShortKey) (domain.Destination, domain.Status, bool, error) {
	destination, status, version, found, err := c.source.Get(ctx, shortKey)
	if err != nil {
		return domain.Destination{}, "", false, err
	}

	if !found {
		entry := cacheEntry{
			SchemaVersion: entrySchemaVersion,
			Kind:          entryKindUnknown,
			ExpiresAt:     c.now().Add(c.negativeTTL).UTC().Format(time.RFC3339Nano),
		}
		c.populate(ctx, cacheKey, entry, c.negativeTTL)
		return domain.Destination{}, "", false, nil
	}

	if version < 1 {
		return domain.Destination{}, "", false, fmt.Errorf("%w: non-positive version", ErrInvalidSourceRecord)
	}

	entry := cacheEntry{
		SchemaVersion: entrySchemaVersion,
		Kind:          entryKindMapping,
		Status:        string(status),
		Version:       version,
		ExpiresAt:     c.now().Add(c.mappingTTL).UTC().Format(time.RFC3339Nano),
	}
	switch status {
	case domain.StatusActive:
		validated, validationErr := domain.NewDestination(destination.String())
		if validationErr != nil {
			return domain.Destination{}, "", false, fmt.Errorf("%w: active destination: %v", ErrInvalidSourceRecord, validationErr)
		}
		entry.Destination = validated.String()
		c.populate(ctx, cacheKey, entry, c.mappingTTL)
		return validated, status, true, nil
	case domain.StatusSuspended:
		// A suspended cache entry never contains or returns the destination.
		c.populate(ctx, cacheKey, entry, c.mappingTTL)
		return domain.Destination{}, status, true, nil
	default:
		return domain.Destination{}, "", false, fmt.Errorf("%w: unrecognized status %q", ErrInvalidSourceRecord, status)
	}
}

func (c *RedirectCache) populate(ctx context.Context, key string, entry cacheEntry, ttl time.Duration) {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return
	}
	// Population is best-effort: an authoritative result must not fail only
	// because the disposable cache is unavailable.
	_ = c.rdb.Set(ctx, key, encoded, ttl).Err()
}

func decodeEntry(encoded []byte, now time.Time) (cachedResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var entry cacheEntry
	if err := decoder.Decode(&entry); err != nil {
		return cachedResult{}, fmt.Errorf("decode cache entry: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return cachedResult{}, err
	}
	if entry.SchemaVersion != entrySchemaVersion {
		return cachedResult{}, fmt.Errorf("unsupported cache schema version %d", entry.SchemaVersion)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, entry.ExpiresAt)
	if err != nil || !now.Before(expiresAt) {
		return cachedResult{}, errors.New("cache entry is stale or has invalid expiry")
	}

	switch entry.Kind {
	case entryKindUnknown:
		if entry.Destination != "" || entry.Status != "" || entry.Version != 0 {
			return cachedResult{}, errors.New("negative cache entry contains mapping fields")
		}
		return cachedResult{found: false}, nil
	case entryKindMapping:
		if entry.Version < 1 {
			return cachedResult{}, errors.New("mapping cache entry has invalid version")
		}
		switch domain.Status(entry.Status) {
		case domain.StatusActive:
			destination, validationErr := domain.NewDestination(entry.Destination)
			if validationErr != nil {
				return cachedResult{}, fmt.Errorf("active cache entry has invalid destination: %w", validationErr)
			}
			return cachedResult{destination: destination, status: domain.StatusActive, found: true}, nil
		case domain.StatusSuspended:
			if entry.Destination != "" {
				return cachedResult{}, errors.New("suspended cache entry contains a destination")
			}
			return cachedResult{status: domain.StatusSuspended, found: true}, nil
		default:
			return cachedResult{}, fmt.Errorf("mapping cache entry has invalid status %q", entry.Status)
		}
	default:
		return cachedResult{}, fmt.Errorf("cache entry has invalid kind %q", entry.Kind)
	}
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("cache entry contains multiple JSON values")
		}
		return fmt.Errorf("decode cache entry trailer: %w", err)
	}
	return nil
}

// Invalidate removes both positive and negative forms for the exact,
// case-preserved key. Lifecycle events call this as the fast path; TTL remains
// the safety bound if event delivery is delayed.
func (c *RedirectCache) Invalidate(ctx context.Context, shortKey domain.ShortKey) error {
	return c.rdb.Del(ctx, keyPrefix+shortKey.String()).Err()
}
