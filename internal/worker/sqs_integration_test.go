//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func localSQSClient(t *testing.T) *sqs.Client {
	t.Helper()
	endpoint := os.Getenv("SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("SQS_ENDPOINT is required for the local SQS redrive integration test")
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return sqs.NewFromConfig(cfg, func(options *sqs.Options) { options.BaseEndpoint = aws.String(endpoint) })
}

func TestSQSDLQRedriveAndReplay(t *testing.T) {
	client := localSQSClient(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	dlq, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(fmt.Sprintf("worker-dlq-%d", suffix))})
	if err != nil {
		t.Fatal(err)
	}
	dlqAttributes, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       dlq.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatal(err)
	}
	redrivePolicy, _ := json.Marshal(map[string]string{
		"deadLetterTargetArn": dlqAttributes.Attributes["QueueArn"],
		"maxReceiveCount":     "2",
	})
	source, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(fmt.Sprintf("worker-source-%d", suffix)),
		Attributes: map[string]string{
			"RedrivePolicy":     string(redrivePolicy),
			"VisibilityTimeout": "0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, queueURL := range []*string{source.QueueUrl, dlq.QueueUrl} {
			if _, err := client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: queueURL}); err != nil {
				t.Errorf("delete queue: %v", err)
			}
		}
	})

	body := streamBody(t, "SQSRep", 2, "event-sqs-2")
	if _, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: source.QueueUrl, MessageBody: aws.String(body)}); err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	runner := NewRunner(client, aws.ToString(source.QueueUrl), NewProcessor(store), log.New(io.Discard, "", 0))
	runner.waitTime = 0
	runner.visibilityTimeout = 0

	var deadLetterBody string
	for attempt := 0; attempt < 10 && deadLetterBody == ""; attempt++ {
		if _, err := runner.PollOnce(ctx); err != nil {
			t.Fatal(err)
		}
		dead, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: dlq.QueueUrl, WaitTimeSeconds: 0})
		if err != nil {
			t.Fatal(err)
		}
		if len(dead.Messages) > 0 {
			deadLetterBody = aws.ToString(dead.Messages[0].Body)
			if _, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: dlq.QueueUrl, ReceiptHandle: dead.Messages[0].ReceiptHandle}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if deadLetterBody == "" {
		t.Fatal("SQS did not move the version-gap message to the DLQ")
	}

	if _, err := NewProcessor(store).Process(ctx, streamBody(t, "SQSRep", 1, "event-sqs-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: source.QueueUrl, MessageBody: aws.String(deadLetterBody)}); err != nil {
		t.Fatal(err)
	}
	processed, err := runner.PollOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || store.checkpoints["SQSRep"].Version != 2 {
		t.Fatalf("replayed DLQ message did not advance to version 2: processed=%d checkpoint=%+v", processed, store.checkpoints["SQSRep"])
	}
}
