// Package keyalloc implements the leased-numeric-range ID allocator
// mandated by docs/decisions/DEC-012.md and specified as ARC-006 in
// docs/SYSTEM_DESIGN.md. Validated by integration tests proving
// collision-free concurrent allocation with a fencing token, and safe
// behavior at exhaustion — it is not a production-hardened implementation
// (no retry backoff tuning, no metrics, no observability).
package keyalloc

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ErrExhausted is returned when the configured maximum ID space has no
// room left for another lease of the requested size.
var ErrExhausted = errors.New("keyalloc: id space exhausted")

// ErrFencedOut is returned when a lease renewal loses a conditional write
// race after exhausting its retry budget — evidence that a stale/split-brain
// allocator cannot successfully extend an out-of-date lease.
var ErrFencedOut = errors.New("keyalloc: fenced out by a newer lease holder")

const counterPK = "GLOBAL_ID_COUNTER"

// Allocator leases non-overlapping numeric ID ranges from a single
// DynamoDB counter item, using a version attribute as a fencing token.
type Allocator struct {
	client    *dynamodb.Client
	tableName string
	maxID     int64 // exclusive upper bound on the leasable ID space
}

// New constructs an Allocator against the given DynamoDB table and ID-space
// ceiling (exclusive). Call EnsureTable first in tests.
func New(client *dynamodb.Client, tableName string, maxID int64) *Allocator {
	return &Allocator{client: client, tableName: tableName, maxID: maxID}
}

// EnsureTable creates the counter table if it does not already exist and
// seeds the counter item at zero. Test helper only.
func (a *Allocator) EnsureTable(ctx context.Context) error {
	_, err := a.client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(a.tableName),
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
		return fmt.Errorf("create table: %w", err)
	}

	_, err = a.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(a.tableName),
		Item: map[string]types.AttributeValue{
			"pk":         &types.AttributeValueMemberS{Value: counterPK},
			"next_start": &types.AttributeValueMemberN{Value: "0"},
			"version":    &types.AttributeValueMemberN{Value: "0"},
		},
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	var ccf *types.ConditionalCheckFailedException
	if err != nil && !errors.As(err, &ccf) {
		return fmt.Errorf("seed counter: %w", err)
	}
	return nil
}

// Lease describes a non-overlapping numeric ID range this allocator
// instance now owns exclusively, plus the fencing token (version) that
// was current at acquisition time.
type Lease struct {
	Start   int64 // inclusive
	End     int64 // exclusive
	Version int64 // fencing token at acquisition
}

// AcquireLease atomically claims the next `size` IDs. It retries on
// conditional-write contention (another allocator won the race) up to
// maxRetries times, then returns ErrFencedOut. It returns ErrExhausted if
// the remaining ID space is smaller than the requested size.
func (a *Allocator) AcquireLease(ctx context.Context, size int64, maxRetries int) (Lease, error) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		item, err := a.client.GetItem(ctx, &dynamodb.GetItemInput{
			TableName:      aws.String(a.tableName),
			Key:            map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: counterPK}},
			ConsistentRead: aws.Bool(true),
		})
		if err != nil {
			return Lease{}, fmt.Errorf("get counter: %w", err)
		}

		var current struct {
			NextStart int64 `dynamodbav:"next_start"`
			Version   int64 `dynamodbav:"version"`
		}
		if err := attributevalue.UnmarshalMap(item.Item, &current); err != nil {
			return Lease{}, fmt.Errorf("unmarshal counter: %w", err)
		}

		if current.NextStart+size > a.maxID {
			return Lease{}, ErrExhausted
		}

		newStart := current.NextStart + size
		newVersion := current.Version + 1

		cond := expression.Name("version").Equal(expression.Value(current.Version))
		update := expression.Set(expression.Name("next_start"), expression.Value(newStart)).
			Set(expression.Name("version"), expression.Value(newVersion))
		builder, err := expression.NewBuilder().WithCondition(cond).WithUpdate(update).Build()
		if err != nil {
			return Lease{}, fmt.Errorf("build expression: %w", err)
		}

		_, err = a.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:                 aws.String(a.tableName),
			Key:                       map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: counterPK}},
			ConditionExpression:       builder.Condition(),
			UpdateExpression:          builder.Update(),
			ExpressionAttributeNames:  builder.Names(),
			ExpressionAttributeValues: builder.Values(),
		})
		if err != nil {
			var ccf *types.ConditionalCheckFailedException
			if errors.As(err, &ccf) {
				// Another allocator (or a stale fenced-out holder retrying)
				// won this round; retry against fresh state.
				continue
			}
			return Lease{}, fmt.Errorf("conditional update: %w", err)
		}

		return Lease{Start: current.NextStart, End: newStart, Version: newVersion}, nil
	}
	return Lease{}, ErrFencedOut
}
