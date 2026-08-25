// cmd/redirect is the Redirect Resolver deployment unit (ARC-003). It
// depends on internal/mapping and internal/api's Resolver-only surface
// — it does not import internal/keyalloc or internal/webui, and cannot
// create a mapping even by mistake, since api.Resolver has no Create
// method.
//
// T9-06 layers internal/cache.RedirectCache over the T8-02 application
// resolver. Valkey remains optional for correctness: unavailable, stale, or
// corrupt cache state falls back to DynamoDB and ambiguous state never
// redirects.
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
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"url-shortener/internal/api"
	"url-shortener/internal/application"
	redirectcache "url-shortener/internal/cache"
	"url-shortener/internal/domain"
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

// mappingReaderAdapter satisfies cache.Source over
// a real *mapping.Repository, translating its raw-string
// GetWithStatus into domain types — the use case itself never imports
// internal/mapping. Promoted from internal/application's test-local
// adapter of the same shape (T8-02's integration test) into real,
// shipped code, since this is the task that actually needs one.
type mappingReaderAdapter struct {
	repo *mapping.Repository
}

func (a mappingReaderAdapter) Get(ctx context.Context, shortKey domain.ShortKey) (domain.Destination, domain.Status, int64, bool, error) {
	record, found, err := a.repo.GetWithStatus(ctx, shortKey.String())
	if err != nil {
		return domain.Destination{}, "", 0, false, err
	}
	if !found {
		return domain.Destination{}, "", 0, false, nil
	}
	dest, err := domain.NewDestination(record.Destination)
	if err != nil {
		return domain.Destination{}, "", 0, false, fmt.Errorf("repository returned an invalid destination for key %q: %w", shortKey, err)
	}
	return dest, domain.Status(record.Status), record.Version, true, nil
}

func valkeyClient() (*redis.Client, error) {
	options := &redis.Options{
		Addr:         envOr("VALKEY_ADDR", "localhost:6379"),
		DialTimeout:  250 * time.Millisecond,
		ReadTimeout:  250 * time.Millisecond,
		WriteTimeout: 250 * time.Millisecond,
		MaxRetries:   -1,
	}
	tlsEnabled, err := strconv.ParseBool(envOr("VALKEY_TLS", "false"))
	if err != nil {
		return nil, fmt.Errorf("VALKEY_TLS must be a boolean: %w", err)
	}
	if tlsEnabled {
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return redis.NewClient(options), nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
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

	valkey, err := valkeyClient()
	if err != nil {
		log.Fatalf("configure Valkey client: %v", err)
	}
	redirectCache, err := redirectcache.New(valkey, mappingReaderAdapter{repo: repo}, redirectcache.MaxMappingTTL, redirectcache.DefaultNegativeTTL)
	if err != nil {
		log.Fatalf("configure redirect cache: %v", err)
	}
	useCase := application.NewResolveUseCase(redirectCache)

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
