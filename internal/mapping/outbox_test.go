package mapping

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestNewEventIdentityProducesUUIDv4AndBalancedShardMapping(t *testing.T) {
	counts := make(map[string]int)
	for firstByte := 0; firstByte < 256; firstByte++ {
		seed := make([]byte, 16)
		seed[0] = byte(firstByte)
		eventID, shard, err := newEventIdentity(bytes.NewReader(seed))
		if err != nil {
			t.Fatalf("first byte %d: %v", firstByte, err)
		}
		parts := strings.Split(eventID, "-")
		if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 ||
			len(parts[3]) != 4 || len(parts[4]) != 12 || parts[2][0] != '4' || parts[3][0] != '8' {
			t.Fatalf("invalid deterministic UUIDv4 %q", eventID)
		}
		counts[shard]++
	}
	if len(counts) != OutboxShardCount {
		t.Fatalf("expected %d shards, got %d: %v", OutboxShardCount, len(counts), counts)
	}
	for shard, count := range counts {
		if count != 16 {
			t.Fatalf("shard %s received %d of 256 deterministic prefixes, want 16", shard, count)
		}
	}
}

func TestNewEventIdentityPropagatesEntropyFailure(t *testing.T) {
	_, _, err := newEventIdentity(failingReader{})
	if err == nil || !strings.Contains(err.Error(), "generate outbox event id") {
		t.Fatalf("expected event identity error, got %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
