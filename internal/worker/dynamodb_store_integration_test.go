//go:build integration

package worker

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func workerIntegrationClient(t *testing.T) *dynamodb.Client {
	t.Helper()
	endpoint, local := os.LookupEnv("DYNAMODB_ENDPOINT")
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion("us-east-1")}
	if local {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	return dynamodb.NewFromConfig(cfg, func(clientOptions *dynamodb.Options) {
		if local {
			clientOptions.BaseEndpoint = aws.String(endpoint)
		}
	})
}

func freshWorkerStore(t *testing.T) (*DynamoDBStore, *dynamodb.Client, string, string) {
	t.Helper()
	client := workerIntegrationClient(t)
	suffix := time.Now().UnixNano()
	checkpointTable := fmt.Sprintf("worker_checkpoint_%d", suffix)
	outboxTable := fmt.Sprintf("worker_outbox_%d", suffix)
	store := NewDynamoDBStore(client, checkpointTable, outboxTable)
	if err := store.EnsureCheckpointTable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateTable(context.Background(), &dynamodb.CreateTableInput{
		TableName: aws.String(outboxTable),
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("aggregate_key"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("event_key"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("aggregate_key"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("event_key"), KeyType: types.KeyTypeRange},
		},
		BillingMode: types.BillingModePayPerRequest,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{checkpointTable, outboxTable} {
			if _, err := client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)}); err != nil {
				t.Errorf("delete %s: %v", table, err)
			}
		}
	})
	return store, client, checkpointTable, outboxTable
}

func putOutboxFixture(t *testing.T, client *dynamodb.Client, table, key string, version int64, eventID string) {
	t.Helper()
	eventKey := fmt.Sprintf("%020d#MappingCreated", version)
	_, err := client.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item: map[string]types.AttributeValue{
			"aggregate_key":     &types.AttributeValueMemberS{Value: key},
			"event_key":         &types.AttributeValueMemberS{Value: eventKey},
			"aggregate_version": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", version)},
			"event_id":          &types.AttributeValueMemberS{Value: eventID},
			"event_type":        &types.AttributeValueMemberS{Value: MappingCreatedEventType},
			"schema_version":    &types.AttributeValueMemberN{Value: "1"},
			"payload":           &types.AttributeValueMemberS{Value: fmt.Sprintf(`{"shortKey":%q,"status":"Active","version":%d}`, key, version)},
			"created_at":        &types.AttributeValueMemberS{Value: "2026-08-25T12:30:45.123456789Z"},
			"unpublished_shard": &types.AttributeValueMemberS{Value: "03"},
			"unpublished_key":   &types.AttributeValueMemberS{Value: "2026-08-25T12:30:45.123456789Z#" + eventID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDynamoDBStoreAtomicCheckpointAndOutboxAcknowledgement(t *testing.T) {
	store, client, _, outboxTable := freshWorkerStore(t)
	putOutboxFixture(t, client, outboxTable, "AtomicA", 1, "event-atomic")
	processor := NewProcessor(store)
	processedAt := time.Date(2026, time.August, 25, 15, 0, 0, 0, time.UTC)
	processor.now = func() time.Time { return processedAt }

	result, err := processor.Process(context.Background(), streamBody(t, "AtomicA", 1, "event-atomic"))
	if err != nil || result.Outcome != OutcomeApplied {
		t.Fatalf("process: result=%+v err=%v", result, err)
	}
	checkpoint, found, err := store.Load(context.Background(), "AtomicA")
	if err != nil || !found || checkpoint.Version != 1 || checkpoint.EventID != "event-atomic" {
		t.Fatalf("checkpoint: found=%v value=%+v err=%v", found, checkpoint, err)
	}

	item, err := client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(outboxTable),
		Key: map[string]types.AttributeValue{
			"aggregate_key": &types.AttributeValueMemberS{Value: "AtomicA"},
			"event_key":     &types.AttributeValueMemberS{Value: "00000000000000000001#MappingCreated"},
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := item.Item["unpublished_shard"]; exists {
		t.Fatal("published outbox row remains in sparse unpublished index")
	}
	if _, exists := item.Item["unpublished_key"]; exists {
		t.Fatal("published outbox row retains unpublished ordering key")
	}
	if got := item.Item["published_at"].(*types.AttributeValueMemberS).Value; got != processedAt.Format(time.RFC3339Nano) {
		t.Fatalf("published_at=%q, want %q", got, processedAt.Format(time.RFC3339Nano))
	}
	wantExpiry := fmt.Sprintf("%d", processedAt.Add(defaultOutboxRetention).Unix())
	if got := item.Item["expires_at"].(*types.AttributeValueMemberN).Value; got != wantExpiry {
		t.Fatalf("expires_at=%q, want %q", got, wantExpiry)
	}
}

func TestDynamoDBStoreMissingOutboxDoesNotAdvanceCheckpoint(t *testing.T) {
	store, _, _, _ := freshWorkerStore(t)
	_, err := NewProcessor(store).Process(context.Background(), streamBody(t, "Missing", 1, "missing-event"))
	if err == nil {
		t.Fatal("missing outbox row unexpectedly succeeded")
	}
	if checkpoint, found, loadErr := store.Load(context.Background(), "Missing"); loadErr != nil || found {
		t.Fatalf("checkpoint advanced without outbox acknowledgement: found=%v checkpoint=%+v err=%v", found, checkpoint, loadErr)
	}
}
