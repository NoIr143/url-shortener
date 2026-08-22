// cmd/creation is the Creation API deployment unit (ARC-002), packaging
// the public creation Web UI (ARC-014) in the same deployment per its
// stated boundary ("packaged in the Creation API deployment, not a
// separate frontend service"). It depends only on internal/mapping,
// internal/keyalloc, internal/base62, internal/api, and internal/webui —
// it does not import internal/cache (that belongs to the Redirect
// Resolver) or anything privileged-plane related.
//
// Local dev requires DynamoDB Local: export DYNAMODB_ENDPOINT (e.g.
// http://localhost:8000) or run via docker-compose.yml, which sets it
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
	"url-shortener/internal/base62"
	"url-shortener/internal/keyalloc"
	"url-shortener/internal/mapping"
	"url-shortener/internal/webui"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
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

// sequentialKeyGen hands out Base62-encoded short keys from leased
// numeric ranges (internal/keyalloc), refilling its local buffer from a
// fresh lease when exhausted. This is the real key-generation path
// (docs/decisions/DEC-012.md), replacing cmd/server's plain counter.
type sequentialKeyGen struct {
	alloc     *keyalloc.Allocator
	leaseSize int64
	next      int64
	end       int64
}

func newSequentialKeyGen(alloc *keyalloc.Allocator, leaseSize int64) *sequentialKeyGen {
	return &sequentialKeyGen{alloc: alloc, leaseSize: leaseSize}
}

func (g *sequentialKeyGen) Next() string {
	if g.next >= g.end {
		lease, err := g.alloc.AcquireLease(context.Background(), g.leaseSize, 10)
		if err != nil {
			// A scaffold-level fallback: production would surface this as
			// a 503 at the handler level (FR-016) rather than panic. Kept
			// simple here since key generation is invoked from a plain
			// func() string in internal/api/internal/webui.
			log.Printf("key allocation failed: %v", err)
			return ""
		}
		g.next, g.end = lease.Start, lease.End
	}
	id := g.next
	g.next++
	key, err := base62.Encode(id)
	if err != nil {
		log.Printf("base62 encode failed for id %d: %v", id, err)
		return ""
	}
	return key
}

// storeAdapter adapts *mapping.Repository's CreateResult-returning
// Create to the (created bool, resolvedKey string, err error) shape
// internal/api.MappingStore and internal/webui.Store expect.
type storeAdapter struct {
	repo *mapping.Repository
}

func (s storeAdapter) Create(ctx context.Context, shortKey, destination string) (bool, string, error) {
	res, err := s.repo.Create(ctx, shortKey, destination)
	if err != nil {
		return false, "", err
	}
	return res.Created, res.ShortKey, nil
}

func (s storeAdapter) Get(ctx context.Context, shortKey string) (string, bool, error) {
	return s.repo.Get(ctx, shortKey)
}

func main() {
	client := dynamoClient()
	repo := mapping.New(client, "mapping", "destination_claim")
	if err := repo.EnsureTables(context.Background()); err != nil {
		log.Fatalf("ensure tables: %v", err)
	}
	store := storeAdapter{repo: repo}

	alloc := keyalloc.New(client, "id_lease_counter", 1<<48)
	if err := alloc.EnsureTable(context.Background()); err != nil {
		log.Fatalf("ensure allocator table: %v", err)
	}
	keyGen := newSequentialKeyGen(alloc, 100).Next

	createLimiter := api.NewIPRateLimiter(10, time.Minute)
	createHandler := api.NewCreateHandler(store, keyGen, createLimiter)
	uiHandler := webui.New(store, keyGen, createLimiter)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", uiHandler.ServeCreatePage)
	mux.HandleFunc("POST /{$}", uiHandler.HandleSubmit)
	mux.HandleFunc("POST /api/v1/urls", createHandler.Create)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	addr := ":8081"
	log.Printf("creation API + UI listening on http://localhost%s (ARC-002/ARC-014)", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
