//go:build integration

// Requires Valkey: docker compose up -d valkey
package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"url-shortener/internal/domain"

	"github.com/redis/go-redis/v9"
)

type sourceRecord struct {
	destination domain.Destination
	status      domain.Status
	version     int64
	found       bool
	err         error
}

type fakeSource struct {
	calls   int64
	records map[string]sourceRecord
}

var uniqueSequence uint64

func (f *fakeSource) Get(_ context.Context, key domain.ShortKey) (domain.Destination, domain.Status, int64, bool, error) {
	atomic.AddInt64(&f.calls, 1)
	record := f.records[key.String()]
	return record.destination, record.status, record.version, record.found, record.err
}

func newValkeyClient(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("Valkey is required for integration tests: %v", err)
	}
	return rdb
}

func testKey(t *testing.T, prefix string) domain.ShortKey {
	t.Helper()
	suffix := (time.Now().UnixNano() + int64(atomic.AddUint64(&uniqueSequence, 1))) % 1_000_000
	key, err := domain.NewShortKey(fmt.Sprintf("%c%06d", prefix[0], suffix))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func testDestination(t *testing.T, raw string) domain.Destination {
	t.Helper()
	destination, err := domain.NewDestination(raw)
	if err != nil {
		t.Fatal(err)
	}
	return destination
}

func newTestCache(t *testing.T, rdb *redis.Client, source Source, mappingTTL, negativeTTL time.Duration) *RedirectCache {
	t.Helper()
	c, err := New(rdb, source, mappingTTL, negativeTTL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestActiveMissThenStatusAwareHit(t *testing.T) {
	rdb := newValkeyClient(t)
	key := testKey(t, "Active")
	destination := testDestination(t, "https://example.com/active")
	source := &fakeSource{records: map[string]sourceRecord{
		key.String(): {destination: destination, status: domain.StatusActive, version: 7, found: true},
	}}
	c := newTestCache(t, rdb, source, time.Minute, DefaultNegativeTTL)

	for attempt := 0; attempt < 2; attempt++ {
		gotDestination, gotStatus, found, err := c.Get(context.Background(), key)
		if err != nil || !found || gotStatus != domain.StatusActive || gotDestination != destination {
			t.Fatalf("attempt %d: destination=%q status=%q found=%v err=%v", attempt+1, gotDestination, gotStatus, found, err)
		}
	}
	if calls := atomic.LoadInt64(&source.calls); calls != 1 {
		t.Fatalf("expected one authoritative read, got %d", calls)
	}
	ttl, err := rdb.TTL(context.Background(), keyPrefix+key.String()).Result()
	if err != nil || ttl <= 0 || ttl > MaxMappingTTL {
		t.Fatalf("unsafe mapping TTL %v (err=%v)", ttl, err)
	}
}

func TestSuspendedEntryNeverReturnsOrCachesDestination(t *testing.T) {
	rdb := newValkeyClient(t)
	key := testKey(t, "Suspended")
	const secretDestination = "https://example.com/must-not-leak"
	source := &fakeSource{records: map[string]sourceRecord{
		key.String(): {destination: testDestination(t, secretDestination), status: domain.StatusSuspended, version: 3, found: true},
	}}
	c := newTestCache(t, rdb, source, time.Minute, DefaultNegativeTTL)

	for attempt := 0; attempt < 2; attempt++ {
		destination, status, found, err := c.Get(context.Background(), key)
		if err != nil || !found || status != domain.StatusSuspended || destination.String() != "" {
			t.Fatalf("attempt %d leaked/changed suspended result: destination=%q status=%q found=%v err=%v", attempt+1, destination, status, found, err)
		}
	}
	encoded, err := rdb.Get(context.Background(), keyPrefix+key.String()).Result()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, secretDestination) {
		t.Fatal("suspended cache payload contains the destination")
	}
}

func TestNegativeCacheIsBoundedAndExpires(t *testing.T) {
	rdb := newValkeyClient(t)
	key := testKey(t, "Unknown")
	source := &fakeSource{records: map[string]sourceRecord{}}
	c := newTestCache(t, rdb, source, time.Minute, 50*time.Millisecond)

	for attempt := 0; attempt < 2; attempt++ {
		_, _, found, err := c.Get(context.Background(), key)
		if err != nil || found {
			t.Fatalf("attempt %d: found=%v err=%v", attempt+1, found, err)
		}
	}
	if calls := atomic.LoadInt64(&source.calls); calls != 1 {
		t.Fatalf("negative hit should avoid a second source read; got %d calls", calls)
	}

	time.Sleep(80 * time.Millisecond)
	if _, _, found, err := c.Get(context.Background(), key); err != nil || found {
		t.Fatalf("post-expiry lookup: found=%v err=%v", found, err)
	}
	if calls := atomic.LoadInt64(&source.calls); calls != 2 {
		t.Fatalf("expired negative entry must fall back; got %d calls", calls)
	}
}

func TestStaleAndCorruptEntriesFallBackAuthoritatively(t *testing.T) {
	for name, payload := range map[string]string{
		"stale":   `{"schema_version":1,"kind":"mapping","destination":"https://attacker.invalid","status":"Active","version":1,"expires_at":"2000-01-01T00:00:00Z"}`,
		"corrupt": `{"schema_version":1,"kind":"mapping","status":"Active","version":1,"expires_at":"2999-01-01T00:00:00Z","unexpected":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			rdb := newValkeyClient(t)
			key := testKey(t, "Fallback")
			destination := testDestination(t, "https://example.com/authoritative")
			source := &fakeSource{records: map[string]sourceRecord{
				key.String(): {destination: destination, status: domain.StatusActive, version: 9, found: true},
			}}
			c := newTestCache(t, rdb, source, time.Minute, DefaultNegativeTTL)
			if err := rdb.Set(context.Background(), keyPrefix+key.String(), payload, time.Minute).Err(); err != nil {
				t.Fatal(err)
			}

			gotDestination, status, found, err := c.Get(context.Background(), key)
			if err != nil || !found || status != domain.StatusActive || gotDestination != destination {
				t.Fatalf("unsafe fallback: destination=%q status=%q found=%v err=%v", gotDestination, status, found, err)
			}
			if calls := atomic.LoadInt64(&source.calls); calls != 1 {
				t.Fatalf("expected authoritative fallback, got %d calls", calls)
			}
		})
	}
}

func TestCacheUnavailableFallsBackAndRepositoryFailureFailsClosed(t *testing.T) {
	broken := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	key := testKey(t, "Unavailable")
	destination := testDestination(t, "https://example.com/fallback")
	source := &fakeSource{records: map[string]sourceRecord{
		key.String(): {destination: destination, status: domain.StatusActive, version: 1, found: true},
	}}
	c := newTestCache(t, broken, source, time.Minute, DefaultNegativeTTL)

	gotDestination, status, found, err := c.Get(context.Background(), key)
	if err != nil || !found || status != domain.StatusActive || gotDestination != destination {
		t.Fatalf("cache-outage fallback failed: destination=%q status=%q found=%v err=%v", gotDestination, status, found, err)
	}

	source.records[key.String()] = sourceRecord{err: errors.New("repository unavailable")}
	destinationAfterFailure, statusAfterFailure, foundAfterFailure, err := c.Get(context.Background(), key)
	if err == nil || foundAfterFailure || statusAfterFailure != "" || destinationAfterFailure.String() != "" {
		t.Fatalf("repository failure did not fail closed: destination=%q status=%q found=%v err=%v", destinationAfterFailure, statusAfterFailure, foundAfterFailure, err)
	}
}

func TestHotKeyAndExplicitInvalidation(t *testing.T) {
	rdb := newValkeyClient(t)
	key := testKey(t, "HotKey")
	source := &fakeSource{records: map[string]sourceRecord{
		key.String(): {destination: testDestination(t, "https://example.com/hot"), status: domain.StatusActive, version: 1, found: true},
	}}
	c := newTestCache(t, rdb, source, time.Minute, DefaultNegativeTTL)
	if _, _, _, err := c.Get(context.Background(), key); err != nil {
		t.Fatal(err)
	}

	const concurrency = 200
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			if _, _, _, err := c.Get(context.Background(), key); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls := atomic.LoadInt64(&source.calls); calls != 1 {
		t.Fatalf("hot cache caused %d authoritative reads", calls)
	}

	if err := c.Invalidate(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	source.records[key.String()] = sourceRecord{status: domain.StatusSuspended, version: 2, found: true}
	destination, status, found, err := c.Get(context.Background(), key)
	if err != nil || !found || status != domain.StatusSuspended || destination.String() != "" {
		t.Fatalf("invalidation did not reveal suspension safely: destination=%q status=%q found=%v err=%v", destination, status, found, err)
	}
}

func TestCacheKeysPreserveShortKeyCase(t *testing.T) {
	rdb := newValkeyClient(t)
	upper, err := domain.NewShortKey("Aa00001")
	if err != nil {
		t.Fatal(err)
	}
	lower, err := domain.NewShortKey("aa00001")
	if err != nil {
		t.Fatal(err)
	}
	upperDestination := testDestination(t, "https://example.com/upper")
	lowerDestination := testDestination(t, "https://example.com/lower")
	if err := rdb.Del(context.Background(), keyPrefix+upper.String(), keyPrefix+lower.String()).Err(); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{records: map[string]sourceRecord{
		upper.String(): {destination: upperDestination, status: domain.StatusActive, version: 1, found: true},
		lower.String(): {destination: lowerDestination, status: domain.StatusActive, version: 1, found: true},
	}}
	c := newTestCache(t, rdb, source, time.Minute, DefaultNegativeTTL)

	for _, test := range []struct {
		key         domain.ShortKey
		destination domain.Destination
	}{{upper, upperDestination}, {lower, lowerDestination}, {upper, upperDestination}, {lower, lowerDestination}} {
		destination, status, found, getErr := c.Get(context.Background(), test.key)
		if getErr != nil || !found || status != domain.StatusActive || destination != test.destination {
			t.Fatalf("case-sensitive lookup changed: key=%q destination=%q status=%q found=%v err=%v", test.key, destination, status, found, getErr)
		}
	}
	if calls := atomic.LoadInt64(&source.calls); calls != 2 {
		t.Fatalf("upper/lower keys did not receive independent cache entries: source calls=%d", calls)
	}
}

func TestInvalidAuthoritativeRecordIsNotCached(t *testing.T) {
	rdb := newValkeyClient(t)
	key := testKey(t, "Invalid")
	source := &fakeSource{records: map[string]sourceRecord{
		key.String(): {destination: testDestination(t, "https://example.com"), status: domain.StatusActive, version: 0, found: true},
	}}
	c := newTestCache(t, rdb, source, time.Minute, DefaultNegativeTTL)

	destination, status, found, err := c.Get(context.Background(), key)
	if !errors.Is(err, ErrInvalidSourceRecord) || found || status != "" || destination.String() != "" {
		t.Fatalf("invalid source record did not fail closed: destination=%q status=%q found=%v err=%v", destination, status, found, err)
	}
	if exists, existsErr := rdb.Exists(context.Background(), keyPrefix+key.String()).Result(); existsErr != nil || exists != 0 {
		t.Fatalf("invalid source record was cached: exists=%d err=%v", exists, existsErr)
	}
}
