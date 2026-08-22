package mapping

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// snapshotRecord is a backup-format row for either table; only one of the
// two payload fields is populated depending on Kind.
type snapshotRecord struct {
	Kind        string `json:"kind"` // "mapping" or "claim"
	ShortKey    string `json:"shortKey,omitempty"`
	Destination string `json:"destination,omitempty"`
	Digest      string `json:"digest,omitempty"`
}

// Backup exports every Mapping and Destination Claim item as a JSON
// snapshot. This is a POC-005 substitute for DynamoDB's managed
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
		}
		if err := attributevalue.UnmarshalMap(item, &m); err != nil {
			return nil, fmt.Errorf("unmarshal mapping for backup: %w", err)
		}
		records = append(records, snapshotRecord{Kind: "mapping", ShortKey: m.PK, Destination: m.Destination})
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
			item, err := attributevalue.MarshalMap(struct {
				PK          string `dynamodbav:"pk"`
				Destination string `dynamodbav:"destination"`
				Status      string `dynamodbav:"status"`
				Version     int64  `dynamodbav:"version"`
			}{PK: rec.ShortKey, Destination: rec.Destination, Status: "Active", Version: 1})
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
		default:
			return fmt.Errorf("unknown snapshot record kind %q", rec.Kind)
		}
	}
	return nil
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
