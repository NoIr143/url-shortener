// cmd/redirect is the Redirect Resolver deployment unit (ARC-003). It
// depends on internal/mapping and internal/api's Resolver-only surface
// — it does not import internal/keyalloc or internal/webui, and cannot
// create a mapping even by mistake, since api.Resolver has no Create
// method.
//
// T8-03: wired onto internal/application.ResolveUseCase (T8-02), a
// repository-only resolver — internal/cache.RedirectCache (Valkey
// cache-aside) is deliberately NOT wired in here. T9-06 ("Implement
// Valkey status-aware cache-aside and bounded negative caching") is the
// separate, later task that adds a cache layer back on top of this
// resolver; until then every resolution reads DynamoDB directly. This
// is a deliberate, task-breakdown-sequenced latency/read-load
// trade-off, not a correctness regression — and it also happens to be
// what actually fixes a real bug T8-01 found: the previous wiring
// (*cache.RedirectCache passed directly as api.Resolver) didn't
// correctly signal "not found" to the handler, so unknown keys
// incorrectly returned 503 instead of 404. See
// docs/poc/T8-01-short-key-parser.md and
// docs/poc/T8-02-repository-only-redirect-resolver.md.
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
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"url-shortener/internal/api"
	"url-shortener/internal/application"
	"url-shortener/internal/domain"
	"url-shortener/internal/mapping"

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

// mappingReaderAdapter satisfies application.MappingReader (T8-02) over
// a real *mapping.Repository, translating its raw-string
// GetWithStatus into domain types — the use case itself never imports
// internal/mapping. Promoted from internal/application's test-local
// adapter of the same shape (T8-02's integration test) into real,
// shipped code, since this is the task that actually needs one.
type mappingReaderAdapter struct {
	repo *mapping.Repository
}

func (a mappingReaderAdapter) Get(ctx context.Context, shortKey domain.ShortKey) (domain.Destination, domain.Status, bool, error) {
	record, found, err := a.repo.GetWithStatus(ctx, shortKey.String())
	if err != nil {
		return domain.Destination{}, "", false, err
	}
	if !found {
		return domain.Destination{}, "", false, nil
	}
	dest, err := domain.NewDestination(record.Destination)
	if err != nil {
		return domain.Destination{}, "", false, fmt.Errorf("repository returned an invalid destination for key %q: %w", shortKey, err)
	}
	return dest, domain.Status(record.Status), true, nil
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
	repo := mapping.New(dynamoClient(), "mapping", "destination_claim", "outbox_event")

	useCase := application.NewResolveUseCase(mappingReaderAdapter{repo: repo})

	resolveLimiter := api.NewIPRateLimiter(100, time.Minute)
	resolveHandler := api.NewResolveHandler(useCase, resolveLimiter)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{shortKey}", func(w http.ResponseWriter, r *http.Request) {
		resolveHandler.Resolve(w, r, r.PathValue("shortKey"))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	addr := ":8082"
	log.Printf("redirect resolver listening on http://localhost%s (ARC-003)", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
