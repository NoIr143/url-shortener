package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu            sync.Mutex
	checkpoints   map[string]Checkpoint
	acknowledged  map[string]int
	commitBarrier chan struct{}
}

func newMemoryStore() *memoryStore {
	return &memoryStore{checkpoints: map[string]Checkpoint{}, acknowledged: map[string]int{}}
}

func (s *memoryStore) Load(_ context.Context, key string) (Checkpoint, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint, found := s.checkpoints[key]
	return checkpoint, found, nil
}

func (s *memoryStore) Commit(_ context.Context, event Event, expected int64, _, _ time.Time) error {
	if s.commitBarrier != nil {
		<-s.commitBarrier
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint, found := s.checkpoints[event.AggregateKey]
	current := int64(0)
	if found {
		current = checkpoint.Version
	}
	if current != expected {
		return ErrCheckpointConflict
	}
	s.checkpoints[event.AggregateKey] = Checkpoint{Version: event.AggregateVersion, EventID: event.EventID, EventType: event.EventType}
	s.acknowledged[event.EventID]++
	return nil
}

func (s *memoryStore) Acknowledge(_ context.Context, event Event, _, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acknowledged[event.EventID]++
	return nil
}

func streamBody(t *testing.T, key string, version int64, eventID string) string {
	t.Helper()
	eventKey := fmt.Sprintf("%020d#MappingCreated", version)
	payload := fmt.Sprintf(`{"shortKey":%q,"status":"Active","version":%d}`, key, version)
	record := map[string]any{
		"eventID":   "stream-sequence-id",
		"eventName": "INSERT",
		"awsRegion": "us-east-1",
		"dynamodb": map[string]any{
			"Keys": map[string]any{
				"aggregate_key": map[string]string{"S": key},
				"event_key":     map[string]string{"S": eventKey},
			},
			"NewImage": map[string]any{
				"aggregate_key":     map[string]string{"S": key},
				"event_key":         map[string]string{"S": eventKey},
				"aggregate_version": map[string]string{"N": fmt.Sprintf("%d", version)},
				"event_id":          map[string]string{"S": eventID},
				"event_type":        map[string]string{"S": MappingCreatedEventType},
				"schema_version":    map[string]string{"N": "1"},
				"payload":           map[string]string{"S": payload},
				"created_at":        map[string]string{"S": "2026-08-25T12:30:45.123456789Z"},
			},
		},
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestProcessorDuplicateReorderGapAndConflict(t *testing.T) {
	store := newMemoryStore()
	processor := NewProcessor(store)

	if _, err := processor.Process(context.Background(), streamBody(t, "CaseA1", 2, "event-2")); !errors.Is(err, ErrVersionGap) {
		t.Fatalf("version 2 before version 1: got %v, want ErrVersionGap", err)
	}
	first, err := processor.Process(context.Background(), streamBody(t, "CaseA1", 1, "event-1"))
	if err != nil || first.Outcome != OutcomeApplied {
		t.Fatalf("apply version 1: result=%+v err=%v", first, err)
	}
	second, err := processor.Process(context.Background(), streamBody(t, "CaseA1", 2, "event-2"))
	if err != nil || second.Outcome != OutcomeApplied {
		t.Fatalf("apply version 2: result=%+v err=%v", second, err)
	}

	duplicate, err := processor.Process(context.Background(), streamBody(t, "CaseA1", 2, "event-2"))
	if err != nil || duplicate.Outcome != OutcomeDuplicate {
		t.Fatalf("duplicate version 2: result=%+v err=%v", duplicate, err)
	}
	reordered, err := processor.Process(context.Background(), streamBody(t, "CaseA1", 1, "event-1"))
	if err != nil || reordered.Outcome != OutcomeDuplicate {
		t.Fatalf("reordered version 1: result=%+v err=%v", reordered, err)
	}
	if _, err := processor.Process(context.Background(), streamBody(t, "CaseA1", 2, "conflicting-event")); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("same version with different identity: got %v, want ErrVersionConflict", err)
	}

	checkpoint := store.checkpoints["CaseA1"]
	if checkpoint.Version != 2 || checkpoint.EventID != "event-2" {
		t.Fatalf("checkpoint moved incorrectly: %+v", checkpoint)
	}
}

func TestProcessorConcurrentDuplicateIsIdempotent(t *testing.T) {
	store := newMemoryStore()
	store.commitBarrier = make(chan struct{})
	processor := NewProcessor(store)
	body := streamBody(t, "RaceA1", 1, "same-event")

	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := processor.Process(context.Background(), body)
			results <- err
		}()
	}
	close(store.commitBarrier)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent duplicate failed: %v", err)
		}
	}
	if got := store.checkpoints["RaceA1"].Version; got != 1 {
		t.Fatalf("checkpoint version=%d, want 1", got)
	}
}

func TestDecodeRejectsDestinationOrMismatchedPayload(t *testing.T) {
	body := streamBody(t, "SafeA1", 1, "event-safe")
	var record map[string]any
	if err := json.Unmarshal([]byte(body), &record); err != nil {
		t.Fatal(err)
	}
	image := record["dynamodb"].(map[string]any)["NewImage"].(map[string]any)
	image["payload"] = map[string]string{"S": `{"shortKey":"SafeA1","status":"Active","version":1,"destination":"https://secret.example/?token=no"}`}
	malicious, _ := json.Marshal(record)
	if _, err := DecodeStreamRecord(string(malicious)); !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("destination-bearing payload: got %v, want ErrMalformedEvent", err)
	}

	if _, err := DecodeStreamRecord(`{"eventName":"MODIFY","dynamodb":{}}`); !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("non-INSERT record: got %v, want ErrMalformedEvent", err)
	}
}
