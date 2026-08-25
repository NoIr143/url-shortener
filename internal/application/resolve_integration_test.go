//go:build integration

// Requires DynamoDB Local by default: export DYNAMODB_ENDPOINT (e.g.
// http://localhost:8000, docker compose -f docker-compose.yml up -d)
// before running. If DYNAMODB_ENDPOINT is left unset, this talks to
// real AWS DynamoDB instead, using the ambient AWS credential chain
// (T5-08's ephemeral-AWS integration workflow).
//
// Wires ResolveUseCase against the real internal/mapping.Repository via
// a small, test-local adapter — not a production-shipped one (deciding
// where a permanent one lives is T8-03's job, when the redirect HTTP
// handler actually needs one). Run with:
// go test -tags=integration ./internal/application/...
package application

import (
	"context"
	"fmt"
	"testing"
	"time"

	"url-shortener/internal/domain"
	"url-shortener/internal/mapping"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// mappingReaderAdapter satisfies application.MappingReader over a real
// *mapping.Repository, translating its raw-string GetWithStatus into
// domain types — the use case itself never imports internal/mapping.
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

// TestResolveUseCase_RealRepository proves Resolve's Active, Suspended,
// and Unknown outcomes against real DynamoDB — not just fakes. The
// Suspended row is seeded directly (no suspend functionality exists
// yet to produce one through normal use), the same technique T7-03/T8-02's
// repository-level test already used.
func TestResolveUseCase_RealRepository(t *testing.T) {
	client := integrationClient(t)
	suffix := time.Now().UnixNano()
	mappingTable := fmt.Sprintf("app_resolve_mapping_%d", suffix)
	claimTable := fmt.Sprintf("app_resolve_claim_%d", suffix)
	outboxTable := fmt.Sprintf("app_resolve_outbox_%d", suffix)

	repo := mapping.New(client, mappingTable, claimTable, outboxTable)
	if err := repo.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	deleteTable(t, client, mappingTable)
	deleteTable(t, client, claimTable)
	deleteTable(t, client, outboxTable)

	uc := NewResolveUseCase(mappingReaderAdapter{repo: repo})

	activeKey := mustKey(t, "active1")
	activeDest := "https://example.com/resolve-integration-active"
	if _, err := repo.Create(context.Background(), activeKey.String(), activeDest); err != nil {
		t.Fatalf("create active mapping: %v", err)
	}

	suspendedKey := mustKey(t, "suspnd1")
	suspendedDest := "https://example.com/resolve-integration-suspended"
	_, err := client.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(mappingTable),
		Item: map[string]types.AttributeValue{
			"pk":          &types.AttributeValueMemberS{Value: suspendedKey.String()},
			"destination": &types.AttributeValueMemberS{Value: suspendedDest},
			"status":      &types.AttributeValueMemberS{Value: "Suspended"},
			"version":     &types.AttributeValueMemberN{Value: "2"},
		},
	})
	if err != nil {
		t.Fatalf("seed suspended mapping directly: %v", err)
	}

	active, err := uc.Resolve(context.Background(), activeKey)
	if err != nil {
		t.Fatalf("Resolve(active): unexpected error: %v", err)
	}
	if active.Status != ResolveStatusActive || active.Destination.String() != activeDest {
		t.Fatalf("expected Active with destination %q, got %+v", activeDest, active)
	}

	suspended, err := uc.Resolve(context.Background(), suspendedKey)
	if err != nil {
		t.Fatalf("Resolve(suspended): unexpected error: %v", err)
	}
	if suspended.Status != ResolveStatusSuspended {
		t.Fatalf("expected Suspended, got %+v", suspended)
	}
	if suspended.Destination != (domain.Destination{}) {
		t.Fatalf("expected no destination exposed for a suspended mapping, got %q", suspended.Destination)
	}

	unknown, err := uc.Resolve(context.Background(), mustKey(t, "nosuch1"))
	if err != nil {
		t.Fatalf("Resolve(unknown): unexpected error: %v", err)
	}
	if unknown.Status != ResolveStatusUnknown {
		t.Fatalf("expected Unknown, got %+v", unknown)
	}
	t.Log("PASS: ResolveUseCase correctly distinguished Active, Suspended, and Unknown against real DynamoDB")
}
