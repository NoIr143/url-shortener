package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type DynamoDBStore struct {
	client          *dynamodb.Client
	checkpointTable string
	outboxTable     string
}

func NewDynamoDBStore(client *dynamodb.Client, checkpointTable, outboxTable string) *DynamoDBStore {
	return &DynamoDBStore{client: client, checkpointTable: checkpointTable, outboxTable: outboxTable}
}

// EnsureCheckpointTable is an integration/local-development helper. Real AWS
// tables are provisioned by infra/environments/dev/events.tf.
func (s *DynamoDBStore) EnsureCheckpointTable(ctx context.Context) error {
	_, err := s.client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(s.checkpointTable),
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("aggregate_key"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("aggregate_key"), KeyType: types.KeyTypeHash},
		},
		BillingMode: types.BillingModePayPerRequest,
	})
	var inUse *types.ResourceInUseException
	if err != nil && !errors.As(err, &inUse) {
		return fmt.Errorf("create checkpoint table: %w", err)
	}
	return nil
}

func (s *DynamoDBStore) Load(ctx context.Context, aggregateKey string) (Checkpoint, bool, error) {
	output, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.checkpointTable),
		Key:            map[string]types.AttributeValue{"aggregate_key": &types.AttributeValueMemberS{Value: aggregateKey}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return Checkpoint{}, false, err
	}
	if len(output.Item) == 0 {
		return Checkpoint{}, false, nil
	}
	var record struct {
		Version   int64  `dynamodbav:"version"`
		EventID   string `dynamodbav:"event_id"`
		EventType string `dynamodbav:"event_type"`
	}
	if err := attributevalue.UnmarshalMap(output.Item, &record); err != nil {
		return Checkpoint{}, false, fmt.Errorf("decode checkpoint: %w", err)
	}
	if record.Version < 1 || record.EventID == "" || record.EventType == "" {
		return Checkpoint{}, false, errors.New("corrupt checkpoint")
	}
	return Checkpoint{Version: record.Version, EventID: record.EventID, EventType: record.EventType}, true, nil
}

func (s *DynamoDBStore) Commit(ctx context.Context, event Event, expectedVersion int64, processedAt, expiresAt time.Time) error {
	checkpointPut := &types.Put{
		TableName: aws.String(s.checkpointTable),
		Item:      checkpointItem(event, processedAt),
	}
	if expectedVersion == 0 {
		checkpointPut.ConditionExpression = aws.String("attribute_not_exists(aggregate_key)")
	} else {
		checkpointPut.ConditionExpression = aws.String("#version = :expected")
		checkpointPut.ExpressionAttributeNames = map[string]string{"#version": "version"}
		checkpointPut.ExpressionAttributeValues = map[string]types.AttributeValue{
			":expected": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", expectedVersion)},
		}
	}
	_, err := s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: checkpointPut},
			{Update: outboxAcknowledgement(s.outboxTable, event, processedAt, expiresAt)},
		},
	})
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) {
		if len(canceled.CancellationReasons) > 0 {
			reason := canceled.CancellationReasons[0]
			if reason.Code != nil && *reason.Code == "ConditionalCheckFailed" {
				return ErrCheckpointConflict
			}
		}
		return fmt.Errorf("transaction canceled without checkpoint conflict: %w", err)
	}
	return err
}

func (s *DynamoDBStore) Acknowledge(ctx context.Context, event Event, processedAt, expiresAt time.Time) error {
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(s.outboxTable),
		Key:                       outboxKey(event),
		ConditionExpression:       aws.String("event_id = :event_id"),
		UpdateExpression:          aws.String("SET published_at = if_not_exists(published_at, :published_at), expires_at = if_not_exists(expires_at, :expires_at) REMOVE unpublished_shard, unpublished_key"),
		ExpressionAttributeValues: acknowledgementValues(event, processedAt, expiresAt),
	})
	return err
}

func checkpointItem(event Event, processedAt time.Time) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"aggregate_key": &types.AttributeValueMemberS{Value: event.AggregateKey},
		"version":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", event.AggregateVersion)},
		"event_id":      &types.AttributeValueMemberS{Value: event.EventID},
		"event_type":    &types.AttributeValueMemberS{Value: event.EventType},
		"processed_at":  &types.AttributeValueMemberS{Value: processedAt.Format(time.RFC3339Nano)},
	}
}

func outboxAcknowledgement(table string, event Event, processedAt, expiresAt time.Time) *types.Update {
	return &types.Update{
		TableName:                 aws.String(table),
		Key:                       outboxKey(event),
		ConditionExpression:       aws.String("event_id = :event_id"),
		UpdateExpression:          aws.String("SET published_at = :published_at, expires_at = :expires_at REMOVE unpublished_shard, unpublished_key"),
		ExpressionAttributeValues: acknowledgementValues(event, processedAt, expiresAt),
	}
}

func outboxKey(event Event) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"aggregate_key": &types.AttributeValueMemberS{Value: event.AggregateKey},
		"event_key":     &types.AttributeValueMemberS{Value: event.EventKey},
	}
}

func acknowledgementValues(event Event, processedAt, expiresAt time.Time) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		":event_id":     &types.AttributeValueMemberS{Value: event.EventID},
		":published_at": &types.AttributeValueMemberS{Value: processedAt.Format(time.RFC3339Nano)},
		":expires_at":   &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", expiresAt.Unix())},
	}
}
