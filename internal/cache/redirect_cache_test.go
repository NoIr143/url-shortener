package cache

import (
	"context"
	"testing"
	"time"

	"url-shortener/internal/domain"

	"github.com/redis/go-redis/v9"
)

type unitSource struct{}

func (unitSource) Get(context.Context, domain.ShortKey) (domain.Destination, domain.Status, int64, bool, error) {
	return domain.Destination{}, "", 0, false, nil
}

func TestDecodeEntryRejectsUnsafePayloads(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	tests := map[string]string{
		"unknown schema":              `{"schema_version":2,"kind":"unknown","expires_at":"2026-08-25T00:00:05Z"}`,
		"stale":                       `{"schema_version":1,"kind":"unknown","expires_at":"2026-08-25T00:00:00Z"}`,
		"negative with mapping state": `{"schema_version":1,"kind":"unknown","status":"Suspended","expires_at":"2026-08-25T00:00:05Z"}`,
		"active without destination":  `{"schema_version":1,"kind":"mapping","status":"Active","version":1,"expires_at":"2026-08-25T00:00:05Z"}`,
		"suspended with destination":  `{"schema_version":1,"kind":"mapping","destination":"https://example.com","status":"Suspended","version":2,"expires_at":"2026-08-25T00:00:05Z"}`,
		"unknown status":              `{"schema_version":1,"kind":"mapping","status":"Deleted","version":2,"expires_at":"2026-08-25T00:00:05Z"}`,
		"non-positive version":        `{"schema_version":1,"kind":"mapping","destination":"https://example.com","status":"Active","expires_at":"2026-08-25T00:00:05Z"}`,
		"unknown field":               `{"schema_version":1,"kind":"unknown","expires_at":"2026-08-25T00:00:05Z","extra":true}`,
		"multiple values":             `{"schema_version":1,"kind":"unknown","expires_at":"2026-08-25T00:00:05Z"} {}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeEntry([]byte(payload), now); err == nil {
				t.Fatal("expected unsafe payload to be rejected")
			}
		})
	}
}

func TestDecodeEntryAcceptsActiveAndDestinationFreeSuspension(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	active, err := decodeEntry([]byte(`{"schema_version":1,"kind":"mapping","destination":"https://example.com/path","status":"Active","version":4,"expires_at":"2026-08-25T00:00:05Z"}`), now)
	if err != nil || !active.found || active.status != domain.StatusActive || active.destination.String() != "https://example.com/path" {
		t.Fatalf("active entry rejected/changed: result=%+v err=%v", active, err)
	}

	suspended, err := decodeEntry([]byte(`{"schema_version":1,"kind":"mapping","status":"Suspended","version":5,"expires_at":"2026-08-25T00:00:05Z"}`), now)
	if err != nil || !suspended.found || suspended.status != domain.StatusSuspended || suspended.destination.String() != "" {
		t.Fatalf("suspended entry rejected/leaked: result=%+v err=%v", suspended, err)
	}
}

func TestNewRejectsConfigurationOutsideSafetyBounds(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	tests := []struct {
		name        string
		client      *redis.Client
		source      Source
		mappingTTL  time.Duration
		negativeTTL time.Duration
	}{
		{name: "nil client", source: unitSource{}, mappingTTL: time.Minute, negativeTTL: time.Second},
		{name: "nil source", client: rdb, mappingTTL: time.Minute, negativeTTL: time.Second},
		{name: "zero mapping TTL", client: rdb, source: unitSource{}, negativeTTL: time.Second},
		{name: "mapping TTL over DEC-008 bound", client: rdb, source: unitSource{}, mappingTTL: MaxMappingTTL + time.Nanosecond, negativeTTL: time.Second},
		{name: "zero negative TTL", client: rdb, source: unitSource{}, mappingTTL: time.Minute},
		{name: "negative TTL longer than mapping TTL", client: rdb, source: unitSource{}, mappingTTL: time.Second, negativeTTL: 2 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.client, test.source, test.mappingTTL, test.negativeTTL); err == nil {
				t.Fatal("expected invalid configuration to fail")
			}
		})
	}

	if _, err := New(rdb, unitSource{}, MaxMappingTTL, DefaultNegativeTTL); err != nil {
		t.Fatalf("safe boundary configuration rejected: %v", err)
	}
}
