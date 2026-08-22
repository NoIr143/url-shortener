//go:build integration

// Requires DynamoDB Local by default: export DYNAMODB_ENDPOINT (e.g.
// http://localhost:8000, docker compose -f docker-compose.yml up -d)
// before running. If DYNAMODB_ENDPOINT is left unset, this talks to real
// AWS DynamoDB instead, using the ambient AWS credential chain (T5-08's
// ephemeral-AWS integration workflow) — never the static "local"
// credentials below, which would silently fail against a real account.
// Run with: go test -tags=integration ./internal/mapping/...
package mapping

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func localClient(t *testing.T) *dynamodb.Client {
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

// deleteTable is real teardown for tests that create their own
// uniquely-named table — without it, repeated runs against real AWS
// (T5-08's ephemeral workflow) would accumulate orphaned tables forever.
// Best-effort: a delete failure fails the test loudly rather than
// silently leaking a real AWS resource.
func deleteTable(t *testing.T, client *dynamodb.Client, table string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)}); err != nil {
			t.Errorf("cleanup: delete table %s: %v", table, err)
		}
	})
}

func freshRepo(t *testing.T) *Repository {
	t.Helper()
	client := localClient(t)
	suffix := time.Now().UnixNano()
	mappingTable, claimTable := fmt.Sprintf("repo_mapping_%d", suffix), fmt.Sprintf("repo_claim_%d", suffix)
	r := New(client, mappingTable, claimTable)
	if err := r.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	deleteTable(t, client, mappingTable)
	deleteTable(t, client, claimTable)
	return r
}

// TestConcurrentExactRepeatYieldsOneMapping proves FR-004/FR-005:
// at least 100 concurrent byte-identical creation requests must produce
// exactly one active mapping and one returned key value.
func TestConcurrentExactRepeatYieldsOneMapping(t *testing.T) {
	r := freshRepo(t)
	const destination = "https://example.com/very/specific/repeated/path?query=1"
	const goroutines = 100

	results := make([]CreateResult, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			// Each goroutine proposes a different candidate short key;
			// only the winner's key should ever be stored.
			candidateKey := fmt.Sprintf("k%03d", idx)
			results[idx], errs[idx] = r.Create(context.Background(), candidateKey, destination)
		}(i)
	}
	wg.Wait()

	winners := map[string]int{}
	createdCount := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
		winners[results[i].ShortKey]++
		if results[i].Created {
			createdCount++
		}
	}

	if createdCount != 1 {
		t.Fatalf("expected exactly 1 goroutine to create the mapping, got %d", createdCount)
	}
	if len(winners) != 1 {
		t.Fatalf("expected all %d goroutines to agree on one short key, got %d distinct keys: %v", goroutines, len(winners), winners)
	}
	t.Logf("PASS: %d concurrent exact-repeat requests converged on exactly one mapping and one key", goroutines)
}

// TestDistinctDestinationsGetDistinctMappings is the negative case:
// two non-identical destinations must not collide.
func TestDistinctDestinationsGetDistinctMappings(t *testing.T) {
	r := freshRepo(t)
	res1, err := r.Create(context.Background(), "keyA", "https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	res2, err := r.Create(context.Background(), "keyB", "https://example.com/b")
	if err != nil {
		t.Fatal(err)
	}
	if !res1.Created || !res2.Created {
		t.Fatalf("expected both distinct destinations to create new mappings: res1=%+v res2=%+v", res1, res2)
	}
}

// TestByteEquivalenceNotSemanticEquivalence proves DR-005: a
// trailing-slash variant is a *different* destination under the confirmed
// byte-equality rule (docs/decisions/DEC-006.md), so it gets its own
// mapping rather than being silently merged.
func TestByteEquivalenceNotSemanticEquivalence(t *testing.T) {
	r := freshRepo(t)
	res1, err := r.Create(context.Background(), "keyC", "https://example.com/path")
	if err != nil {
		t.Fatal(err)
	}
	res2, err := r.Create(context.Background(), "keyD", "https://example.com/path/")
	if err != nil {
		t.Fatal(err)
	}
	if !res1.Created || !res2.Created {
		t.Fatalf("expected trailing-slash variant to be treated as a distinct destination, got res1=%+v res2=%+v", res1, res2)
	}
}

// TestForcedKeyCollisionRejectedSafely is T7-03's evidence: a genuine
// short-key collision (the same shortKey, two different destinations —
// something the leased-range allocator is supposed to make impossible,
// but this repository must still fail closed on rather than trust that
// promise blindly) must be rejected with ErrKeyCollision specifically,
// not misdiagnosed as an exact-repeat (ErrDigestCollision's job), and
// the original mapping must survive completely untouched — no partial
// overwrite, no corruption, BR-003 preserved.
func TestForcedKeyCollisionRejectedSafely(t *testing.T) {
	r := freshRepo(t)
	const key = "collide1"
	const original = "https://example.com/original-owner-of-this-key"
	const attacker = "https://example.com/a-completely-different-destination"

	first, err := r.Create(context.Background(), key, original)
	if err != nil {
		t.Fatalf("establishing the original mapping: unexpected error: %v", err)
	}
	if !first.Created {
		t.Fatalf("expected the first Create for a fresh key to report Created=true")
	}

	// Force the collision: same key, a destination with a different
	// digest, so the claim-table condition passes (no digest conflict)
	// and only the mapping-table condition can be the one that fails.
	_, err = r.Create(context.Background(), key, attacker)
	if !errors.Is(err, ErrKeyCollision) {
		t.Fatalf("expected ErrKeyCollision for a forced key collision, got %v", err)
	}

	// The original mapping must be exactly as it was — no partial
	// overwrite from the rejected attempt.
	dest, ok, getErr := r.Get(context.Background(), key)
	if getErr != nil {
		t.Fatalf("re-reading the mapping after the rejected collision: %v", getErr)
	}
	if !ok {
		t.Fatalf("expected the original mapping to still exist after the rejected collision")
	}
	if dest != original {
		t.Fatalf("expected the original destination %q to survive untouched, got %q", original, dest)
	}
	t.Logf("PASS: forced key collision on %q rejected with ErrKeyCollision; original mapping to %q survived untouched", key, original)
}

// TestReconciliationFindsNoOrphans is the local substitute for
// DR-009 restore/reconciliation evidence (DynamoDB Local has no PITR to
// exercise a true backup/restore drill against).
func TestReconciliationFindsNoOrphans(t *testing.T) {
	r := freshRepo(t)
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("recon%02d", i)
		dest := fmt.Sprintf("https://example.com/%d", i)
		if _, err := r.Create(context.Background(), key, dest); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	orphans, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("expected zero orphaned claims after normal transactional creation, found %v", orphans)
	}
	t.Log("PASS: reconciliation found zero orphaned claims across 20 transactionally-created mappings")
}
