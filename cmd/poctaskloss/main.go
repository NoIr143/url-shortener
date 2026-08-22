// Manual POC-005 exercise: run this while stopping/restarting the
// dynamodb-local container partway through to observe real behavior
// during a task-loss window. Not part of `go test` — invoked directly:
//
//	go run ./cmd/poctaskloss
package main

import (
	"context"
	"fmt"
	"time"

	"url-shortener/internal/mapping"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func client() *dynamodb.Client {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")),
	)
	if err != nil {
		panic(err)
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String("http://localhost:8000") })
}

func main() {
	r := mapping.New(client(), "taskloss_mapping", "taskloss_claim")
	ctx := context.Background()

	if err := r.EnsureTables(ctx); err != nil {
		fmt.Println("ensure tables:", err)
		return
	}
	res, err := r.Create(ctx, "tl1", "https://example.com/before-task-loss")
	fmt.Printf("[t=0s]  create before task loss: result=%+v err=%v\n", res, err)

	for i := 1; i <= 12; i++ {
		time.Sleep(1 * time.Second)
		dest, ok, err := r.Get(ctx, "tl1")
		fmt.Printf("[t=%ds] get during window: dest=%q ok=%v err=%v\n", i, dest, ok, err)
	}
}
