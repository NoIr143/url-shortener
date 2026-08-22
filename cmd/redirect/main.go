// cmd/redirect is the Redirect Resolver deployment unit (ARC-003). It
// depends on internal/cache (wrapping internal/mapping as the
// authoritative fallback) and internal/api's Resolver-only surface — it
// does not import internal/keyalloc or internal/webui, and cannot create
// a mapping even by mistake, since api.Resolver has no Create method.
//
// Requires DynamoDB Local and Valkey (docker compose up -d).
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"url-shortener/internal/api"
	"url-shortener/internal/cache"
	"url-shortener/internal/mapping"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/redis/go-redis/v9"
)

func dynamoClient() *dynamodb.Client {
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8000"
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")),
	)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(endpoint) })
}

func main() {
	repo := mapping.New(dynamoClient(), "mapping", "destination_claim")
	if err := repo.EnsureTables(context.Background()); err != nil {
		log.Fatalf("ensure tables: %v", err)
	}

	valkeyAddr := os.Getenv("VALKEY_ADDR")
	if valkeyAddr == "" {
		valkeyAddr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: valkeyAddr})

	// 60-second TTL target per docs/decisions/DEC-008.md, pending
	// production-measured evidence (docs/poc/POC-003-results.md).
	redirectCache := cache.New(rdb, repo, 60*time.Second)

	resolveLimiter := api.NewIPRateLimiter(100, time.Minute)
	resolveHandler := api.NewResolveHandler(redirectCache, resolveLimiter)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{shortKey}", func(w http.ResponseWriter, r *http.Request) {
		resolveHandler.Resolve(w, r, r.PathValue("shortKey"))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	addr := ":8082"
	log.Printf("redirect resolver listening on http://localhost%s (ARC-003)", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
