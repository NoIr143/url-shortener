// Package mapping implements the transactional creation path described as
// DATA-001/DATA-002 in docs/SYSTEM_DESIGN.md and ADR-007: a Mapping and its
// Destination Claim are committed atomically, giving exact-repeat
// deduplication (docs/decisions/DEC-006.md) via a conditional write rather
// than a read-then-write race. Validated by integration tests against a
// local DynamoDB instance; not yet a production-hardened repository (no
// pagination, no retry/backoff tuning).
package mapping

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ErrDestinationTooLong mirrors docs/decisions/DEC-007.md's 2048-character
// limit; enforced here as defense in depth, not the sole validation layer.
var ErrDestinationTooLong = errors.New("mapping: destination exceeds 2048 characters")

// ErrDigestCollision is returned by Create when an existing claim is
// found for the same destination digest but with a genuinely different
// stored destination — a SHA-256 collision or data corruption, not a
// legitimate exact-repeat (ADR-007: never assume a digest alone proves
// semantic equivalence). A sentinel, not an ad-hoc formatted error, so
// callers (internal/application, T6-03) can distinguish this specific,
// unresolvable outcome from a generic dependency failure.
var ErrDigestCollision = errors.New("mapping: digest collision with non-equal destination")

// ErrKeyCollision is returned by Create when the *mapping* table's own
// attribute_not_exists(pk) condition is the one that failed — a
// short key that already names a different mapping (BR-003: "a short
// key identifies at most one destination mapping at a time"). The
// leased-range allocator (docs/decisions/DEC-012.md, POC-001) is
// supposed to make this impossible; reaching it means something has
// gone wrong upstream of this repository (a corrupted counter, an
// out-of-band write, a bug), not a routine outcome like an exact
// repeat. A sentinel, distinct from ErrDigestCollision, so a caller can
// tell "this specific key is taken — a fresh key would resolve it"
// apart from "this destination already has a different key — no key
// change would help" (T7-03).
var ErrKeyCollision = errors.New("mapping: short key already maps to a different destination")

const maxDestinationLength = 2048

// Repository stores Mappings and Destination Claims in two DynamoDB
// tables, committed transactionally.
type Repository struct {
	client       *dynamodb.Client
	mappingTable string
	claimTable   string
}

func New(client *dynamodb.Client, mappingTable, claimTable string) *Repository {
	return &Repository{client: client, mappingTable: mappingTable, claimTable: claimTable}
}

// EnsureTables creates both tables if they do not already exist. Test
// helper only — production table provisioning is infrastructure-as-code,
// not application code.
func (r *Repository) EnsureTables(ctx context.Context) error {
	for _, t := range []string{r.mappingTable, r.claimTable} {
		_, err := r.client.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName: aws.String(t),
			AttributeDefinitions: []types.AttributeDefinition{
				{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			},
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			},
			BillingMode: types.BillingModePayPerRequest,
		})
		var inUse *types.ResourceInUseException
		if err != nil && !errors.As(err, &inUse) {
			return fmt.Errorf("create table %s: %w", t, err)
		}
	}
	return nil
}

// CreateResult reports whether a new Mapping was committed or an existing
// one was found for a byte-identical destination (BR-005/DEC-006).
type CreateResult struct {
	ShortKey string
	Created  bool // false means an exact-repeat claim already existed
}

func digestOf(destination string) string {
	sum := sha256.Sum256([]byte(destination))
	return hex.EncodeToString(sum[:])
}

// Create attempts to atomically commit a new Mapping (shortKey ->
// destination) plus its Destination Claim. If a claim for the exact same
// destination (byte equality, per DR-005) already exists, it returns the
// winning shortKey with Created=false instead of creating a second
// mapping — this is FR-004/FR-005's concurrency invariant, enforced by a
// DynamoDB transaction rather than an application-level lock.
func (r *Repository) Create(ctx context.Context, shortKey, destination string) (CreateResult, error) {
	if len(destination) > maxDestinationLength {
		return CreateResult{}, ErrDestinationTooLong
	}
	digest := digestOf(destination)

	mappingItem, err := attributevalue.MarshalMap(struct {
		PK          string `dynamodbav:"pk"`
		Destination string `dynamodbav:"destination"`
		Status      string `dynamodbav:"status"`
		Version     int64  `dynamodbav:"version"`
	}{PK: shortKey, Destination: destination, Status: "Active", Version: 1})
	if err != nil {
		return CreateResult{}, fmt.Errorf("marshal mapping: %w", err)
	}

	claimItem, err := attributevalue.MarshalMap(struct {
		PK          string `dynamodbav:"pk"`
		ShortKey    string `dynamodbav:"short_key"`
		Destination string `dynamodbav:"destination"`
	}{PK: digest, ShortKey: shortKey, Destination: destination})
	if err != nil {
		return CreateResult{}, fmt.Errorf("marshal claim: %w", err)
	}

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Put: &types.Put{
					TableName:           aws.String(r.claimTable),
					Item:                claimItem,
					ConditionExpression: aws.String("attribute_not_exists(pk)"),
				},
			},
			{
				Put: &types.Put{
					TableName:           aws.String(r.mappingTable),
					Item:                mappingItem,
					ConditionExpression: aws.String("attribute_not_exists(pk)"),
				},
			},
		},
	})
	if err == nil {
		return CreateResult{ShortKey: shortKey, Created: true}, nil
	}

	var txCanceled *types.TransactionCanceledException
	if errors.As(err, &txCanceled) {
		// T7-03: distinguish *which* condition actually failed via
		// TransactWriteItems' per-item CancellationReasons (index 1 is
		// the mapping-table Put below) rather than assuming — as before
		// this task — that the claim table is always the cause. A
		// mapping-table collision is checked first and takes priority:
		// it is the more serious, less-expected outcome (BR-003), and
		// must never be misdiagnosed as a routine exact-repeat by
		// blindly looking up a claim that has nothing to do with why
		// the transaction actually failed.
		if len(txCanceled.CancellationReasons) > 1 {
			reason := txCanceled.CancellationReasons[1]
			if reason.Code != nil && *reason.Code == "ConditionalCheckFailed" {
				return CreateResult{}, ErrKeyCollision
			}
		}

		// The claim-table condition is the one expected to fail on an
		// exact repeat; look up the winning mapping and verify full byte
		// equality before trusting the digest (ADR-007: never assume a
		// digest alone proves semantic equivalence).
		existing, getErr := r.client.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(r.claimTable),
			Key:       map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: digest}},
		})
		if getErr != nil || existing.Item == nil {
			return CreateResult{}, fmt.Errorf("transaction canceled and claim lookup failed: %w", err)
		}
		var claim struct {
			ShortKey    string `dynamodbav:"short_key"`
			Destination string `dynamodbav:"destination"`
		}
		if unmarshalErr := attributevalue.UnmarshalMap(existing.Item, &claim); unmarshalErr != nil {
			return CreateResult{}, fmt.Errorf("unmarshal existing claim: %w", unmarshalErr)
		}
		if claim.Destination != destination {
			// Digest collision with a different destination: fail closed
			// rather than silently returning the wrong mapping.
			return CreateResult{}, ErrDigestCollision
		}
		return CreateResult{ShortKey: claim.ShortKey, Created: false}, nil
	}
	return CreateResult{}, fmt.Errorf("transact write: %w", err)
}

// Get returns the stored destination for a short key, or ok=false if no
// mapping exists. It deliberately does not report status — callers
// that need to distinguish Active from Suspended (T8-02's "safe status
// outcomes", BR-007/FR-012) must use GetWithStatus instead. Kept
// unchanged for existing callers (internal/cache.RedirectCache,
// cmd/creation's storeAdapter) that predate that distinction.
func (r *Repository) Get(ctx context.Context, shortKey string) (destination string, ok bool, err error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.mappingTable),
		Key:       map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: shortKey}},
	})
	if err != nil {
		return "", false, fmt.Errorf("get mapping: %w", err)
	}
	if out.Item == nil {
		return "", false, nil
	}
	var m struct {
		Destination string `dynamodbav:"destination"`
	}
	if err := attributevalue.UnmarshalMap(out.Item, &m); err != nil {
		return "", false, fmt.Errorf("unmarshal mapping: %w", err)
	}
	return m.Destination, true, nil
}

// MappingRecord is a stored mapping's full state relevant to
// resolution (T8-02) — unlike Get, GetWithStatus does not silently
// drop the status field.
type MappingRecord struct {
	Destination string `dynamodbav:"destination"`
	Status      string `dynamodbav:"status"`
}

// GetWithStatus returns the stored mapping's destination and status
// for a short key, or found=false if no mapping exists. This is the
// repository-only read T8-02's resolver depends on: it makes no
// assumption about status meaning "safe to redirect" — that
// determination belongs to the caller (BR-007: a suspended mapping
// must never redirect, and this method's own job is only to report
// what's actually stored, not to interpret it).
func (r *Repository) GetWithStatus(ctx context.Context, shortKey string) (MappingRecord, bool, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.mappingTable),
		Key:       map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: shortKey}},
	})
	if err != nil {
		return MappingRecord{}, false, fmt.Errorf("get mapping: %w", err)
	}
	if out.Item == nil {
		return MappingRecord{}, false, nil
	}
	var m MappingRecord
	if err := attributevalue.UnmarshalMap(out.Item, &m); err != nil {
		return MappingRecord{}, false, fmt.Errorf("unmarshal mapping: %w", err)
	}
	return m, true, nil
}

// Reconcile scans both tables and reports Destination Claims that point to
// a Mapping which does not exist — the local-substitute equivalent of
// DR-009's restore/migration reconciliation, since DynamoDB Local does not
// support point-in-time recovery for a true backup/restore drill.
func (r *Repository) Reconcile(ctx context.Context) (orphanedClaims []string, err error) {
	claims, err := r.client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(r.claimTable)})
	if err != nil {
		return nil, fmt.Errorf("scan claims: %w", err)
	}
	for _, item := range claims.Items {
		var claim struct {
			ShortKey string `dynamodbav:"short_key"`
		}
		if err := attributevalue.UnmarshalMap(item, &claim); err != nil {
			return nil, fmt.Errorf("unmarshal claim: %w", err)
		}
		_, ok, err := r.Get(ctx, claim.ShortKey)
		if err != nil {
			return nil, err
		}
		if !ok {
			orphanedClaims = append(orphanedClaims, claim.ShortKey)
		}
	}
	return orphanedClaims, nil
}
