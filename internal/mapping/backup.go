package mapping

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// snapshotRecord is a backup-format row for one repository table. Mapping and
// outbox state are retained exactly so restore cannot silently reset metadata.
type snapshotRecord struct {
	Kind             string `json:"kind"` // "mapping", "claim", or "outbox"
	ShortKey         string `json:"shortKey,omitempty"`
	Destination      string `json:"destination,omitempty"`
	Digest           string `json:"digest,omitempty"`
	Status           string `json:"status,omitempty"`
	Version          int64  `json:"version,omitempty"`
	CreatedAt        string `json:"createdAt,omitempty"`
	EventKey         string `json:"eventKey,omitempty"`
	EventID          string `json:"eventId,omitempty"`
	EventType        string `json:"eventType,omitempty"`
	SchemaVersion    int64  `json:"schemaVersion,omitempty"`
	Payload          string `json:"payload,omitempty"`
	UnpublishedShard string `json:"unpublishedShard,omitempty"`
	UnpublishedKey   string `json:"unpublishedKey,omitempty"`
}

// Backup exports every Mapping, Destination Claim, and Outbox Event as a JSON
// snapshot. This is a local-testing substitute for DynamoDB's managed
// point-in-time recovery, which DynamoDB Local does not implement — it
// proves the reconciliation *invariant* (DR-009: zero unexplained
// differences after restore), not AWS's actual continuous-backup
// mechanism.
func (r *Repository) Backup(ctx context.Context) ([]byte, error) {
	var records []snapshotRecord

	mappings, err := r.client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(r.mappingTable)})
	if err != nil {
		return nil, fmt.Errorf("scan mappings: %w", err)
	}
	for _, item := range mappings.Items {
		var m struct {
			PK          string `dynamodbav:"pk"`
			Destination string `dynamodbav:"destination"`
			Status      string `dynamodbav:"status"`
			Version     int64  `dynamodbav:"version"`
			CreatedAt   string `dynamodbav:"created_at"`
		}
		if err := attributevalue.UnmarshalMap(item, &m); err != nil {
			return nil, fmt.Errorf("unmarshal mapping for backup: %w", err)
		}
		records = append(records, snapshotRecord{
			Kind:        "mapping",
			ShortKey:    m.PK,
			Destination: m.Destination,
			Status:      m.Status,
			Version:     m.Version,
			CreatedAt:   m.CreatedAt,
		})
	}

	claims, err := r.client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(r.claimTable)})
	if err != nil {
		return nil, fmt.Errorf("scan claims: %w", err)
	}
	for _, item := range claims.Items {
		var c struct {
			PK          string `dynamodbav:"pk"`
			ShortKey    string `dynamodbav:"short_key"`
			Destination string `dynamodbav:"destination"`
		}
		if err := attributevalue.UnmarshalMap(item, &c); err != nil {
			return nil, fmt.Errorf("unmarshal claim for backup: %w", err)
		}
		records = append(records, snapshotRecord{Kind: "claim", Digest: c.PK, ShortKey: c.ShortKey, Destination: c.Destination})
	}

	outbox, err := r.client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(r.outboxTable)})
	if err != nil {
		return nil, fmt.Errorf("scan outbox: %w", err)
	}
	for _, item := range outbox.Items {
		var event OutboxEvent
		if err := attributevalue.UnmarshalMap(item, &event); err != nil {
			return nil, fmt.Errorf("unmarshal outbox event for backup: %w", err)
		}
		records = append(records, snapshotRecord{
			Kind:             "outbox",
			ShortKey:         event.AggregateKey,
			Version:          event.AggregateVersion,
			CreatedAt:        event.CreatedAt,
			EventKey:         event.EventKey,
			EventID:          event.EventID,
			EventType:        event.EventType,
			SchemaVersion:    event.SchemaVersion,
			Payload:          event.Payload,
			UnpublishedShard: event.UnpublishedShard,
			UnpublishedKey:   event.UnpublishedKey,
		})
	}

	return json.Marshal(records)
}

// Restore replays a Backup snapshot into (empty) tables. Intended for a
// fresh Repository pointed at newly created/empty tables, simulating
// "restore into a new environment" — it does not attempt to merge with
// existing data.
func (r *Repository) Restore(ctx context.Context, snapshot []byte) error {
	var records []snapshotRecord
	if err := json.Unmarshal(snapshot, &records); err != nil {
		return fmt.Errorf("unmarshal snapshot: %w", err)
	}

	for _, rec := range records {
		switch rec.Kind {
		case "mapping":
			if rec.Status == "" || rec.Version < 1 || rec.CreatedAt == "" {
				return fmt.Errorf("restore mapping %s: snapshot is missing lifecycle metadata", rec.ShortKey)
			}
			createdAt, err := time.Parse(time.RFC3339Nano, rec.CreatedAt)
			if err != nil {
				return fmt.Errorf("restore mapping %s: invalid createdAt: %w", rec.ShortKey, err)
			}
			_, offset := createdAt.Zone()
			if offset != 0 {
				return fmt.Errorf("restore mapping %s: createdAt must be UTC", rec.ShortKey)
			}
			item, err := attributevalue.MarshalMap(struct {
				PK          string `dynamodbav:"pk"`
				Destination string `dynamodbav:"destination"`
				Status      string `dynamodbav:"status"`
				Version     int64  `dynamodbav:"version"`
				CreatedAt   string `dynamodbav:"created_at"`
			}{
				PK:          rec.ShortKey,
				Destination: rec.Destination,
				Status:      rec.Status,
				Version:     rec.Version,
				CreatedAt:   rec.CreatedAt,
			})
			if err != nil {
				return fmt.Errorf("marshal restored mapping %s: %w", rec.ShortKey, err)
			}
			if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
				TableName: aws.String(r.mappingTable), Item: item,
			}); err != nil {
				return fmt.Errorf("restore mapping %s: %w", rec.ShortKey, err)
			}
		case "claim":
			item, err := attributevalue.MarshalMap(struct {
				PK          string `dynamodbav:"pk"`
				ShortKey    string `dynamodbav:"short_key"`
				Destination string `dynamodbav:"destination"`
			}{PK: rec.Digest, ShortKey: rec.ShortKey, Destination: rec.Destination})
			if err != nil {
				return fmt.Errorf("marshal restored claim %s: %w", rec.Digest, err)
			}
			if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
				TableName: aws.String(r.claimTable), Item: item,
			}); err != nil {
				return fmt.Errorf("restore claim %s: %w", rec.Digest, err)
			}
		case "outbox":
			if rec.ShortKey == "" || rec.Version < 1 || rec.CreatedAt == "" || rec.EventKey == "" ||
				rec.EventID == "" || rec.EventType == "" || rec.SchemaVersion < 1 || rec.Payload == "" ||
				rec.UnpublishedShard == "" || rec.UnpublishedKey == "" {
				return fmt.Errorf("restore outbox event %s: snapshot is missing event metadata", rec.EventID)
			}
			item, err := attributevalue.MarshalMap(OutboxEvent{
				AggregateKey:     rec.ShortKey,
				EventKey:         rec.EventKey,
				AggregateVersion: rec.Version,
				EventID:          rec.EventID,
				EventType:        rec.EventType,
				SchemaVersion:    rec.SchemaVersion,
				Payload:          rec.Payload,
				CreatedAt:        rec.CreatedAt,
				UnpublishedShard: rec.UnpublishedShard,
				UnpublishedKey:   rec.UnpublishedKey,
			})
			if err != nil {
				return fmt.Errorf("marshal restored outbox event %s: %w", rec.EventID, err)
			}
			if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
				TableName: aws.String(r.outboxTable), Item: item,
			}); err != nil {
				return fmt.Errorf("restore outbox event %s: %w", rec.EventID, err)
			}
		default:
			return fmt.Errorf("unknown snapshot record kind %q", rec.Kind)
		}
	}
	return nil
}

// CountOutboxItems returns the durable unpublished/published event count for
// backup and reconciliation evidence.
func (r *Repository) CountOutboxItems(ctx context.Context) (int, error) {
	out, err := r.client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(r.outboxTable), Select: types.SelectCount})
	if err != nil {
		return 0, fmt.Errorf("count outbox events: %w", err)
	}
	return int(out.Count), nil
}

// CountItems returns the number of items currently in each table — a
// cheap reconciliation signal alongside Reconcile's orphan check.
func (r *Repository) CountItems(ctx context.Context) (mappingCount, claimCount int, err error) {
	m, err := r.client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(r.mappingTable), Select: types.SelectCount})
	if err != nil {
		return 0, 0, fmt.Errorf("count mappings: %w", err)
	}
	c, err := r.client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(r.claimTable), Select: types.SelectCount})
	if err != nil {
		return 0, 0, fmt.Errorf("count claims: %w", err)
	}
	return int(m.Count), int(c.Count), nil
}
