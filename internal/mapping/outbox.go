package mapping

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
)

const (
	MappingCreatedEventType = "MappingCreated"
	OutboxSchemaVersion     = int64(1)
	OutboxShardCount        = 16
)

// OutboxEvent is DATA-005's durable, destination-free propagation intent.
// The aggregate key and version are repeated in the payload deliberately so
// consumers can validate an envelope without reading the authoritative table.
type OutboxEvent struct {
	AggregateKey     string `dynamodbav:"aggregate_key"`
	EventKey         string `dynamodbav:"event_key"`
	AggregateVersion int64  `dynamodbav:"aggregate_version"`
	EventID          string `dynamodbav:"event_id"`
	EventType        string `dynamodbav:"event_type"`
	SchemaVersion    int64  `dynamodbav:"schema_version"`
	Payload          string `dynamodbav:"payload"`
	CreatedAt        string `dynamodbav:"created_at"`
	UnpublishedShard string `dynamodbav:"unpublished_shard"`
	UnpublishedKey   string `dynamodbav:"unpublished_key"`
}

type mappingCreatedPayload struct {
	ShortKey string `json:"shortKey"`
	Status   string `json:"status"`
	Version  int64  `json:"version"`
}

func newMappingCreatedEvent(shortKey, createdAt string) (OutboxEvent, error) {
	eventID, shard, err := newEventIdentity(rand.Reader)
	if err != nil {
		return OutboxEvent{}, err
	}
	payload, err := json.Marshal(mappingCreatedPayload{
		ShortKey: shortKey,
		Status:   "Active",
		Version:  1,
	})
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("marshal mapping-created payload: %w", err)
	}
	return OutboxEvent{
		AggregateKey:     shortKey,
		EventKey:         fmt.Sprintf("%020d#%s", 1, MappingCreatedEventType),
		AggregateVersion: 1,
		EventID:          eventID,
		EventType:        MappingCreatedEventType,
		SchemaVersion:    OutboxSchemaVersion,
		Payload:          string(payload),
		CreatedAt:        createdAt,
		UnpublishedShard: shard,
		UnpublishedKey:   createdAt + "#" + eventID,
	}, nil
}

func newEventIdentity(reader io.Reader) (eventID, shard string, err error) {
	var id [16]byte
	if _, err := io.ReadFull(reader, id[:]); err != nil {
		return "", "", fmt.Errorf("generate outbox event id: %w", err)
	}
	// RFC 4122 UUID version 4 and variant bits.
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	eventID = fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
	shard = fmt.Sprintf("%02d", int(id[0])%OutboxShardCount)
	return eventID, shard, nil
}
