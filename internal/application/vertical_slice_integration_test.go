//go:build integration

// Requires DynamoDB Local by default: export DYNAMODB_ENDPOINT (e.g.
// http://localhost:8000, docker compose -f docker-compose.yml up -d)
// before running. If DYNAMODB_ENDPOINT is left unset, this talks to
// real AWS DynamoDB instead, using the ambient AWS credential chain
// (T5-08's ephemeral-AWS integration workflow).
//
// T8-04: vertical-slice E2E evidence (FR-007/FR-008/FR-023) — proves
// the full create -> resolve chain through real DynamoDB, using the
// SAME underlying tables across independent CreationUseCase and
// ResolveUseCase instances, exactly as cmd/creation and cmd/redirect
// are two separate processes in production (ADR-001) sharing nothing
// but the durable store. T6-03's and T8-02's own integration tests
// each verified their use case in isolation; nothing before this
// proved they actually interoperate.
// Run with: go test -tags=integration ./internal/application/...
package application

import (
	"context"
	"fmt"
	"testing"
	"time"

	"url-shortener/internal/domain"
	"url-shortener/internal/keyalloc"
	"url-shortener/internal/mapping"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// verticalSliceTables bundles the real table names one test run needs
// — returned (not just constructed and hidden) so tests that need a
// "fresh instance, same tables" restart simulation can rebuild a
// *mapping.Repository pointed at the identical tables.
type verticalSliceTables struct {
	client       *dynamodb.Client
	mappingTable string
	claimTable   string
	outboxTable  string
}

func newVerticalSliceRepo(t *testing.T) (verticalSliceTables, *mapping.Repository, *keyalloc.Allocator) {
	t.Helper()
	client := integrationClient(t)
	suffix := time.Now().UnixNano()
	mappingTable := fmt.Sprintf("app_e2e_mapping_%d", suffix)
	claimTable := fmt.Sprintf("app_e2e_claim_%d", suffix)
	outboxTable := fmt.Sprintf("app_e2e_outbox_%d", suffix)
	counterTable := fmt.Sprintf("app_e2e_counter_%d", suffix)

	repo := mapping.New(client, mappingTable, claimTable, outboxTable)
	if err := repo.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	deleteTable(t, client, mappingTable)
	deleteTable(t, client, claimTable)
	deleteTable(t, client, outboxTable)

	alloc := keyalloc.New(client, counterTable, 1_000_000)
	if err := alloc.EnsureTable(context.Background()); err != nil {
		t.Fatalf("ensure counter table: %v", err)
	}
	deleteTable(t, client, counterTable)

	return verticalSliceTables{client: client, mappingTable: mappingTable, claimTable: claimTable, outboxTable: outboxTable}, repo, alloc
}

// recordingKeyGenerator wraps a real KeyGenerator and remembers every
// candidate key it ever handed out, in order — used to know exactly
// which candidate a later Create call requested even when that
// candidate is discarded (an exact-repeat) and never appears in any
// Result the caller sees.
type recordingKeyGenerator struct {
	inner KeyGenerator
	calls []domain.ShortKey
}

func (r *recordingKeyGenerator) Next() (domain.ShortKey, error) {
	k, err := r.inner.Next()
	if err == nil {
		r.calls = append(r.calls, k)
	}
	return k, err
}

// TestVerticalSlice_CreateThenResolve proves FR-007/FR-009: a
// committed result resolves to exactly the destination it was created
// with, through the same real DynamoDB tables, using CreationUseCase
// and ResolveUseCase as two genuinely independent use-case instances.
func TestVerticalSlice_CreateThenResolve(t *testing.T) {
	_, repo, alloc := newVerticalSliceRepo(t)

	creationUC := NewCreationUseCase(
		mappingRepositoryAdapter{repo: repo},
		&keyAllocatorAdapter{alloc: alloc, leaseSize: 10},
	)
	resolveUC := NewResolveUseCase(mappingReaderAdapter{repo: repo})

	dest := mustDestination(t, "https://example.com/vertical-slice-check")
	created, err := creationUC.Create(context.Background(), dest)
	if err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}
	if !created.Created {
		t.Fatalf("expected a new mapping to be created")
	}

	resolved, err := resolveUC.Resolve(context.Background(), created.ShortKey)
	if err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}
	if resolved.Status != ResolveStatusActive {
		t.Fatalf("expected the just-created key to resolve Active, got %+v", resolved)
	}
	if resolved.Destination != dest {
		t.Fatalf("expected the resolved destination to exactly match what was created: want %q, got %q", dest, resolved.Destination)
	}
}

// TestVerticalSlice_PrecommitFailureExposesNoKey proves FR-008: a
// candidate key whose commit is canceled before completing must never
// become resolvable. Forced here via the exact-repeat mechanism
// (docs/decisions/DEC-006.md) — the ordinary, real way a candidate
// key's own TransactWriteItems Put is ever canceled: a second request
// for the same destination gets a fresh candidate key from the
// allocator, but the transaction commits the *first* request's key
// instead, canceling the second request's Put entirely. This proves
// the second request's candidate key was never written to DynamoDB at
// all — not merely that the caller didn't see it returned.
func TestVerticalSlice_PrecommitFailureExposesNoKey(t *testing.T) {
	_, repo, alloc := newVerticalSliceRepo(t)

	keyGen := &recordingKeyGenerator{inner: &keyAllocatorAdapter{alloc: alloc, leaseSize: 10}}
	creationUC := NewCreationUseCase(mappingRepositoryAdapter{repo: repo}, keyGen)
	resolveUC := NewResolveUseCase(mappingReaderAdapter{repo: repo})

	dest := mustDestination(t, "https://example.com/precommit-failure-check")

	first, err := creationUC.Create(context.Background(), dest)
	if err != nil {
		t.Fatalf("first Create: unexpected error: %v", err)
	}

	second, err := creationUC.Create(context.Background(), dest)
	if err != nil {
		t.Fatalf("second (exact-repeat) Create: unexpected error: %v", err)
	}
	if second.Created {
		t.Fatalf("expected the second call to be an exact-repeat (Created=false)")
	}
	if second.ShortKey != first.ShortKey {
		t.Fatalf("expected the repeat to resolve to the first key %q, got %q", first.ShortKey, second.ShortKey)
	}

	if len(keyGen.calls) != 2 {
		t.Fatalf("expected exactly 2 candidate keys to have been requested, got %d: %v", len(keyGen.calls), keyGen.calls)
	}
	orphanCandidate := keyGen.calls[1] // requested for the second call, but never committed
	if orphanCandidate == first.ShortKey {
		t.Fatalf("test construction bug: the second candidate must differ from the first, got %q for both", orphanCandidate)
	}

	orphanCheck, err := resolveUC.Resolve(context.Background(), orphanCandidate)
	if err != nil {
		t.Fatalf("Resolve(candidate that was never committed): unexpected error: %v", err)
	}
	if orphanCheck.Status != ResolveStatusUnknown {
		t.Fatalf("expected the never-committed candidate key %q to be Unknown, got %+v", orphanCandidate, orphanCheck)
	}
	t.Logf("PASS: candidate key %q was requested but never committed (exact-repeat resolved to %q instead) and is correctly Unknown", orphanCandidate, first.ShortKey)
}

// TestVerticalSlice_RestartPersistence proves FR-023: a successfully
// committed mapping survives being read by a brand-new
// ResolveUseCase/Repository instance pointed at the same tables —
// modeling an application restart or fresh deployment the only way
// that's actually observable: nothing but DynamoDB's own durable state
// carries over (mirrors T7-02's TestRestartResumesWithoutOverlap for
// the allocator).
func TestVerticalSlice_RestartPersistence(t *testing.T) {
	tables, repo, alloc := newVerticalSliceRepo(t)

	creationUC := NewCreationUseCase(
		mappingRepositoryAdapter{repo: repo},
		&keyAllocatorAdapter{alloc: alloc, leaseSize: 10},
	)
	dest := mustDestination(t, "https://example.com/restart-persistence-check")
	result, err := creationUC.Create(context.Background(), dest)
	if err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	// "Restart": a brand-new Repository value, pointed at the identical
	// tables, sharing no in-memory state with the one Create used above.
	restartedRepo := mapping.New(tables.client, tables.mappingTable, tables.claimTable, tables.outboxTable)
	restartedResolveUC := NewResolveUseCase(mappingReaderAdapter{repo: restartedRepo})

	resolved, err := restartedResolveUC.Resolve(context.Background(), result.ShortKey)
	if err != nil {
		t.Fatalf("Resolve after simulated restart: unexpected error: %v", err)
	}
	if resolved.Status != ResolveStatusActive || resolved.Destination != dest {
		t.Fatalf("expected the mapping to survive a simulated restart unchanged, got %+v", resolved)
	}
	t.Log("PASS: a committed mapping resolved correctly through a brand-new Repository instance pointed at the same tables")
}
