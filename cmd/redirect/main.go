// cmd/redirect is the Redirect Resolver deployment unit (ARC-003). It
// depends on internal/cache (wrapping internal/mapping as the
// authoritative fallback) and internal/api's Resolver-only surface — it
// does not import internal/keyalloc or internal/webui, and cannot create
// a mapping even by mistake, since api.Resolver has no Create method.
//
// Local dev requires DynamoDB Local and Valkey: export DYNAMODB_ENDPOINT
// and VALKEY_ADDR, or run via docker-compose.yml, which sets both
// automatically. If DYNAMODB_ENDPOINT is left unset (a real deployment,
// e.g. the ECS task in infra/environments/dev/ecs.tf), this resolves
// region/credentials from the real AWS default chain — the task's own
// IAM role (T5-05/T5-07) — never the static local credentials below.
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
	endpoint, isLocal := os.LookupEnv("DYNAMODB_ENDPOINT")
	if !isLocal {
		cfg, err := awsconfig.LoadDefaultConfig(context.Background())
		if err != nil {
			log.Fatalf("load AWS config: %v", err)
		}
		return dynamodb.NewFromConfig(cfg)
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
	// No EnsureTables here, deliberately: redirect never writes a table
	// into existence, only reads from tables cmd/creation already
	// ensured — matching its own least-privilege IAM policy
	// (docs/poc/T5-07-kms-secrets-taskroles-audit.md grants it GetItem
	// only, never CreateTable). Calling EnsureTables here would both
	// fatal-crash on startup in a real deployment (AccessDenied) and, in
	// local Compose, race cmd/creation's own EnsureTables call on the
	// same table names at container start.
	repo := mapping.New(dynamoClient(), "mapping", "destination_claim")

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
