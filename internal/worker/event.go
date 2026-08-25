// Package worker consumes the destination-free mapping lifecycle envelope
// carried from the transactional outbox through DynamoDB Streams,
// EventBridge Pipes, and SQS (ARC-009/ARC-010, INT-008).
package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"url-shortener/internal/domain"
)

const (
	MappingCreatedEventType = "MappingCreated"
	SchemaVersionV1         = int64(1)
)

var ErrMalformedEvent = errors.New("worker: malformed event")

// Event is the validated mapping.lifecycle.v1 envelope used by the worker.
// Payload is intentionally reduced to status: destinations never belong in
// this transport contract.
type Event struct {
	EventID          string
	EventKey         string
	AggregateKey     string
	AggregateVersion int64
	EventType        string
	SchemaVersion    int64
	Status           string
	OccurredAt       time.Time
}

type streamAttribute struct {
	S *string `json:"S"`
	N *string `json:"N"`
}

type streamRecord struct {
	EventName string `json:"eventName"`
	DynamoDB  struct {
		Keys     map[string]streamAttribute `json:"Keys"`
		NewImage map[string]streamAttribute `json:"NewImage"`
	} `json:"dynamodb"`
}

type lifecyclePayload struct {
	ShortKey string `json:"shortKey"`
	Status   string `json:"status"`
	Version  int64  `json:"version"`
}

// DecodeStreamRecord validates one unmodified DynamoDB Streams record. The
// EventBridge Pipe filters to INSERT records, but this boundary repeats that
// check so a misconfigured or manually replayed message cannot mutate worker
// state. Only the schema-v1 MappingCreated contract implemented by T9-04 is
// accepted; unknown schema/event types fail closed for later explicit rollout.
func DecodeStreamRecord(body string) (Event, error) {
	var record streamRecord
	decoder := json.NewDecoder(strings.NewReader(body))
	if err := decoder.Decode(&record); err != nil {
		return Event{}, fmt.Errorf("%w: decode stream record: %v", ErrMalformedEvent, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Event{}, fmt.Errorf("%w: multiple JSON values", ErrMalformedEvent)
	}
	if record.EventName != "INSERT" {
		return Event{}, fmt.Errorf("%w: unsupported stream operation", ErrMalformedEvent)
	}

	image := record.DynamoDB.NewImage
	event := Event{
		EventID:      stringAttribute(image, "event_id"),
		EventKey:     stringAttribute(image, "event_key"),
		AggregateKey: stringAttribute(image, "aggregate_key"),
		EventType:    stringAttribute(image, "event_type"),
		OccurredAt:   time.Time{},
	}
	var err error
	event.AggregateVersion, err = numberAttribute(image, "aggregate_version")
	if err != nil {
		return Event{}, err
	}
	event.SchemaVersion, err = numberAttribute(image, "schema_version")
	if err != nil {
		return Event{}, err
	}
	createdAt := stringAttribute(image, "created_at")
	event.OccurredAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || event.OccurredAt.Location() != time.UTC {
		return Event{}, fmt.Errorf("%w: created_at must be UTC RFC3339Nano", ErrMalformedEvent)
	}

	if event.EventID == "" || event.AggregateVersion < 1 {
		return Event{}, fmt.Errorf("%w: missing event identity or aggregate version", ErrMalformedEvent)
	}
	if _, err := domain.NewShortKey(event.AggregateKey); err != nil {
		return Event{}, fmt.Errorf("%w: invalid aggregate key", ErrMalformedEvent)
	}
	if event.SchemaVersion != SchemaVersionV1 || event.EventType != MappingCreatedEventType {
		return Event{}, fmt.Errorf("%w: unsupported schema or event type", ErrMalformedEvent)
	}
	wantEventKey := fmt.Sprintf("%020d#%s", event.AggregateVersion, event.EventType)
	if event.EventKey != wantEventKey || stringAttribute(record.DynamoDB.Keys, "event_key") != wantEventKey ||
		stringAttribute(record.DynamoDB.Keys, "aggregate_key") != event.AggregateKey {
		return Event{}, fmt.Errorf("%w: stream keys do not match envelope", ErrMalformedEvent)
	}

	payloadText := stringAttribute(image, "payload")
	var payload lifecyclePayload
	payloadDecoder := json.NewDecoder(strings.NewReader(payloadText))
	payloadDecoder.DisallowUnknownFields()
	if err := payloadDecoder.Decode(&payload); err != nil {
		return Event{}, fmt.Errorf("%w: invalid payload", ErrMalformedEvent)
	}
	if err := payloadDecoder.Decode(&struct{}{}); err != io.EOF {
		return Event{}, fmt.Errorf("%w: multiple payload values", ErrMalformedEvent)
	}
	if payload.ShortKey != event.AggregateKey || payload.Version != event.AggregateVersion || payload.Status != "Active" {
		return Event{}, fmt.Errorf("%w: payload does not match envelope", ErrMalformedEvent)
	}
	event.Status = payload.Status
	return event, nil
}

func stringAttribute(attributes map[string]streamAttribute, name string) string {
	attribute, ok := attributes[name]
	if !ok || attribute.S == nil {
		return ""
	}
	return *attribute.S
}

func numberAttribute(attributes map[string]streamAttribute, name string) (int64, error) {
	attribute, ok := attributes[name]
	if !ok || attribute.N == nil {
		return 0, fmt.Errorf("%w: missing numeric attribute %s", ErrMalformedEvent, name)
	}
	value, err := strconv.ParseInt(*attribute.N, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid numeric attribute %s", ErrMalformedEvent, name)
	}
	return value, nil
}
