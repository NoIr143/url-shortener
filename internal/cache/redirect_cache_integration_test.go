//go:build integration

// Requires Valkey (docker compose -f docker-compose.yml up -d).
// Run with: go test -tags=integration ./internal/cache/...
package cache

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// fakeSource counts calls so tests can prove the cache actually absorbs
// load (POC-003's hot-key requirement) rather than merely "not erroring".
type fakeSource struct {
	calls int64
	data  map[string]string
}

func (f *fakeSource) Get(ctx context.Context, key string) (string, bool, error) {
	atomic.AddInt64(&f.calls, 1)
	d, ok := f.data[key]
	return d, ok, nil
}

func newValkeyClient(t *testing.T) *redis.Client {
	t.Helper()
	return redis.NewClient(&redis.Options{Addr: "localhost:6379"})
}

func uniqueKey(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// TestMissThenHit proves the basic cache-aside contract: a first
// read is a genuine miss (falls through to Source), a second read for the
// same key is served from cache without calling Source again.
func TestMissThenHit(t *testing.T) {
	rdb := newValkeyClient(t)
	src := &fakeSource{data: map[string]string{"k1": "https://example.com/1"}}
	c := New(rdb, src, time.Minute)
	key := uniqueKey("k1")
	src.data[key] = "https://example.com/1"

	dest1, fromCache1, err := c.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if fromCache1 {
		t.Fatal("expected first read to be a genuine miss, not served from cache")
	}
	if dest1 != "https://example.com/1" {
		t.Fatalf("unexpected destination: %q", dest1)
	}

	dest2, fromCache2, err := c.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !fromCache2 {
		t.Fatal("expected second read to be served from cache")
	}
	if dest2 != dest1 {
		t.Fatalf("cached value diverged: %q vs %q", dest2, dest1)
	}
	if atomic.LoadInt64(&src.calls) != 1 {
		t.Fatalf("expected exactly 1 Source call (the miss), got %d", src.calls)
	}
	t.Log("PASS: first read = genuine miss (1 Source call), second read = cache hit (0 additional Source calls)")
}

// TestHotKeyAbsorbedByCache proves the hot-key requirement
// (NFR-CAP-002's "representative hot-key mix"): many concurrent reads for
// the same key should collapse to very few Source calls, not one per
// request.
func TestHotKeyAbsorbedByCache(t *testing.T) {
	rdb := newValkeyClient(t)
	key := uniqueKey("hot")
	src := &fakeSource{data: map[string]string{key: "https://example.com/hot"}}
	c := New(rdb, src, time.Minute)

	// Prime the cache once, then hammer it concurrently.
	if _, _, err := c.Get(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	callsBefore := atomic.LoadInt64(&src.calls)

	const concurrency = 200
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			if _, _, err := c.Get(context.Background(), key); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	callsAfter := atomic.LoadInt64(&src.calls)
	if callsAfter != callsBefore {
		t.Fatalf("expected zero additional Source calls once primed, got %d additional calls across %d concurrent reads", callsAfter-callsBefore, concurrency)
	}
	t.Logf("PASS: %d concurrent hot-key reads produced 0 additional Source calls beyond the initial prime", concurrency)
}

// TestCacheFailureFallsBackSafely simulates the cache being
// unavailable (wrong address, immediate connection failure) and proves
// reads still succeed via the authoritative Source rather than erroring.
func TestCacheFailureFallsBackSafely(t *testing.T) {
	brokenRdb := redis.NewClient(&redis.Options{
		Addr:        "localhost:1", // nothing listens here
		DialTimeout: 200 * time.Millisecond,
	})
	key := uniqueKey("fail")
	src := &fakeSource{data: map[string]string{key: "https://example.com/fallback"}}
	c := New(brokenRdb, src, time.Minute)

	dest, fromCache, err := c.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("expected safe fallback on cache failure, got error: %v", err)
	}
	if fromCache {
		t.Fatal("a broken cache cannot have served this from cache")
	}
	if dest != "https://example.com/fallback" {
		t.Fatalf("unexpected destination: %q", dest)
	}
	t.Log("PASS: cache connection failure degraded safely to the authoritative Source, no error surfaced")
}

// TestSuspensionPropagation measures explicit-invalidation latency
// (the privileged suspend path) and confirms it is nowhere near the
// 60-second target in docs/decisions/DEC-008.md — explicit invalidation is
// the fast path; TTL expiry (not exercised here at the real 60s value, to
// keep the test fast) is the worst-case bound.
func TestSuspensionPropagation(t *testing.T) {
	rdb := newValkeyClient(t)
	key := uniqueKey("suspend")
	src := &fakeSource{data: map[string]string{key: "https://example.com/before-suspend"}}
	c := New(rdb, src, time.Minute)

	if _, _, err := c.Get(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	// Confirm it is actually cached before measuring invalidation.
	_, fromCache, err := c.Get(context.Background(), key)
	if err != nil || !fromCache {
		t.Fatalf("expected key to be cached before invalidation test, fromCache=%v err=%v", fromCache, err)
	}

	start := time.Now()
	if err := c.Invalidate(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	_, fromCacheAfter, err := c.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if fromCacheAfter {
		t.Fatal("expected a cache miss immediately after explicit invalidation")
	}

	const target = 60 * time.Second
	if elapsed >= target {
		t.Fatalf("explicit invalidation took %v, at or beyond the 60s DEC-008 target", elapsed)
	}
	t.Logf("PASS: explicit invalidation completed in %v (DEC-008 target: <%v); this measures local Valkey round-trip only, not a distributed production deployment", elapsed, target)
}
