//go:build integration

// Requires DynamoDB Local by default: export DYNAMODB_ENDPOINT (e.g.
// http://localhost:8000, docker compose -f docker-compose.yml up -d)
// before running. If DYNAMODB_ENDPOINT is left unset, this talks to
// real AWS DynamoDB instead, using the ambient AWS credential chain
// (T5-08's ephemeral-AWS integration workflow).
//
// Wires CreationUseCase against the real internal/mapping.Repository
// and internal/keyalloc.Allocator via small, test-local adapters — not
// a production-shipped adapter (deciding where a permanent one lives is
// T6-04's job, when the JSON handler actually needs one). This proves
// the ports are genuinely implementable against real infrastructure,
// not just satisfiable by test doubles.
// Run with: go test -tags=integration ./internal/application/...
package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"url-shortener/internal/base62"
	"url-shortener/internal/domain"
	"url-shortener/internal/keyalloc"
	"url-shortener/internal/mapping"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func integrationClient(t *testing.T) *dynamodb.Client {
	t.Helper()
	endpoint, isLocal := os.LookupEnv("DYNAMODB_ENDPOINT")
	if !isLocal {
		cfg, err := awsconfig.LoadDefaultConfig(context.Background())
		if err != nil {
			t.Fatalf("load AWS config: %v", err)
		}
		return dynamodb.NewFromConfig(cfg)
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
}

func deleteTable(t *testing.T, client *dynamodb.Client, table string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)}); err != nil {
			t.Errorf("cleanup: delete table %s: %v", table, err)
		}
	})
}

// mappingRepositoryAdapter satisfies application.MappingRepository over
// a real *mapping.Repository, translating its raw-string API to domain
// types and its ErrDigestCollision into application.ErrConflict at
// exactly this boundary — the use case itself never imports
// internal/mapping.
type mappingRepositoryAdapter struct {
	repo *mapping.Repository
}

func (a mappingRepositoryAdapter) Create(ctx context.Context, shortKey domain.ShortKey, destination domain.Destination) (domain.ShortKey, bool, error) {
	result, err := a.repo.Create(ctx, shortKey.String(), destination.String())
	if err != nil {
		if errors.Is(err, mapping.ErrDigestCollision) {
			return domain.ShortKey{}, false, ErrConflict
		}
		return domain.ShortKey{}, false, err
	}
	committed, err := domain.NewShortKey(result.ShortKey)
	if err != nil {
		return domain.ShortKey{}, false, fmt.Errorf("repository returned an invalid short key %q: %w", result.ShortKey, err)
	}
	return committed, result.Created, nil
}

// keyAllocatorAdapter satisfies application.KeyGenerator over a real
// *keyalloc.Allocator, refilling from a fresh lease when exhausted —
// the same pattern cmd/creation's sequentialKeyGen already uses.
type keyAllocatorAdapter struct {
	alloc     *keyalloc.Allocator
	leaseSize int64
	next, end int64
}

func (a *keyAllocatorAdapter) Next() (domain.ShortKey, error) {
	if a.next >= a.end {
		lease, err := a.alloc.AcquireLease(context.Background(), a.leaseSize, 10)
		if err != nil {
			return domain.ShortKey{}, err
		}
		a.next, a.end = lease.Start, lease.End
	}
	id := a.next
	a.next++
	encoded, err := base62.Encode(id)
	if err != nil {
		return domain.ShortKey{}, err
	}
	return domain.NewShortKey(encoded)
}

// TestCreationUseCase_RealRepository proves Create's success and
// FR-004 repeat behavior against real DynamoDB — not just fakes.
func TestCreationUseCase_RealRepository(t *testing.T) {
	client := integrationClient(t)
	suffix := time.Now().UnixNano()
	mappingTable := fmt.Sprintf("app_creation_mapping_%d", suffix)
	claimTable := fmt.Sprintf("app_creation_claim_%d", suffix)
	counterTable := fmt.Sprintf("app_creation_counter_%d", suffix)

	repo := mapping.New(client, mappingTable, claimTable)
	if err := repo.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	deleteTable(t, client, mappingTable)
	deleteTable(t, client, claimTable)

	alloc := keyalloc.New(client, counterTable, 1_000_000)
	if err := alloc.EnsureTable(context.Background()); err != nil {
		t.Fatalf("ensure counter table: %v", err)
	}
	deleteTable(t, client, counterTable)

	uc := NewCreationUseCase(
		mappingRepositoryAdapter{repo: repo},
		&keyAllocatorAdapter{alloc: alloc, leaseSize: 10},
	)

	dest := mustDestination(t, "https://example.com/creation-use-case-integration")

	first, err := uc.Create(context.Background(), dest)
	if err != nil {
		t.Fatalf("first Create: unexpected error: %v", err)
	}
	if !first.Created {
		t.Fatalf("expected the first call to create a new mapping, got Created=false")
	}

	second, err := uc.Create(context.Background(), dest)
	if err != nil {
		t.Fatalf("second Create (exact repeat): unexpected error: %v", err)
	}
	if second.Created {
		t.Fatalf("expected the second call (exact repeat) to report Created=false")
	}
	if second.ShortKey != first.ShortKey {
		t.Fatalf("expected the repeat to resolve to the first call's key %q, got %q", first.ShortKey, second.ShortKey)
	}
}
