//go:build poc

// Requires DynamoDB Local (docker compose -f docker-compose.poc.yml up -d).
// Run with: go test -tags=poc ./internal/mapping/...
package mapping

import (
	"context"
	"fmt"
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
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String("http://localhost:8000")
	})
}

func freshRepo(t *testing.T) *Repository {
	t.Helper()
	client := localClient(t)
	suffix := time.Now().UnixNano()
	r := New(client, fmt.Sprintf("poc002_mapping_%d", suffix), fmt.Sprintf("poc002_claim_%d", suffix))
	if err := r.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	return r
}

// TestPOC002_ConcurrentExactRepeatYieldsOneMapping proves FR-004/FR-005:
// at least 100 concurrent byte-identical creation requests must produce
// exactly one active mapping and one returned key value.
func TestPOC002_ConcurrentExactRepeatYieldsOneMapping(t *testing.T) {
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

// TestPOC002_DistinctDestinationsGetDistinctMappings is the negative case:
// two non-identical destinations must not collide.
func TestPOC002_DistinctDestinationsGetDistinctMappings(t *testing.T) {
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

// TestPOC002_ByteEquivalenceNotSemanticEquivalence proves DR-005: a
// trailing-slash variant is a *different* destination under the confirmed
// byte-equality rule (docs/decisions/DEC-006.md), so it gets its own
// mapping rather than being silently merged.
func TestPOC002_ByteEquivalenceNotSemanticEquivalence(t *testing.T) {
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

// TestPOC002_ReconciliationFindsNoOrphans is the local substitute for
// DR-009 restore/reconciliation evidence (DynamoDB Local has no PITR to
// exercise a true backup/restore drill against).
func TestPOC002_ReconciliationFindsNoOrphans(t *testing.T) {
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
