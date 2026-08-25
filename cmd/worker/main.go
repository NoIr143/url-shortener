// cmd/worker is ARC-009's background SQS consumer. It has no HTTP surface.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"url-shortener/internal/worker"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	queueURL := os.Getenv("OUTBOX_QUEUE_URL")
	if queueURL == "" {
		log.Fatal("OUTBOX_QUEUE_URL is required")
	}
	cfg := awsConfig(ctx)
	dynamo := dynamodb.NewFromConfig(cfg, func(options *dynamodb.Options) {
		if endpoint := os.Getenv("DYNAMODB_ENDPOINT"); endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	})
	queue := sqs.NewFromConfig(cfg, func(options *sqs.Options) {
		if endpoint := os.Getenv("SQS_ENDPOINT"); endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	})

	store := worker.NewDynamoDBStore(dynamo, envOr("WORKER_CHECKPOINT_TABLE", "worker_checkpoint"), envOr("OUTBOX_TABLE", "outbox_event"))
	if os.Getenv("DYNAMODB_ENDPOINT") != "" {
		if err := store.EnsureCheckpointTable(ctx); err != nil {
			log.Fatalf("ensure local checkpoint table: %v", err)
		}
	}
	runner := worker.NewRunner(queue, queueURL, worker.NewProcessor(store), log.Default())
	log.Print("outbox/control worker started (ARC-009, INT-008)")
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("worker stopped: %v", err)
	}
}

func awsConfig(ctx context.Context) aws.Config {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(envOr("AWS_REGION", "us-east-1"))}
	if os.Getenv("DYNAMODB_ENDPOINT") != "" || os.Getenv("SQS_ENDPOINT") != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}
	return cfg
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
