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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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

// TestConcurrentSameKeyDifferentDestinationsCommitsOneMapping is T9-02's
// mapping-table concurrency proof. Destination-claim contention is covered by
// TestConcurrentExactRepeatYieldsOneMapping; this test instead makes every
// destination distinct while every writer races for the same short key. The
// mapping condition must select one winner, reject every loser with
// ErrKeyCollision, and leave no claim behind for a transaction that lost.
func TestConcurrentSameKeyDifferentDestinationsCommitsOneMapping(t *testing.T) {
	r := freshRepo(t)
	const (
		key        = "sameKey"
		goroutines = 32
	)

	destinations := make([]string, goroutines)
	results := make([]CreateResult, goroutines)
	errs := make([]error, goroutines)
	start := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		destinations[i] = fmt.Sprintf("https://example.com/concurrent-key-owner/%d", i)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx], errs[idx] = r.Create(context.Background(), key, destinations[idx])
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		switch {
		case err == nil:
			if winner != -1 {
				t.Fatalf("expected one successful writer, but writers %d and %d both succeeded", winner, i)
			}
			if !results[i].Created || results[i].ShortKey != key {
				t.Fatalf("writer %d returned an invalid success result: %+v", i, results[i])
			}
			winner = i
		case errors.Is(err, ErrKeyCollision):
			// Expected for every writer that lost the mapping-table CAS.
		default:
			t.Fatalf("writer %d returned an unexpected error: %v", i, err)
		}
	}
	if winner == -1 {
		t.Fatal("expected exactly one successful writer, got none")
	}

	stored, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName:      aws.String(r.mappingTable),
		Key:            map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: key}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("strongly read winning mapping: %v", err)
	}
	storedDestination, ok := stored.Item["destination"].(*types.AttributeValueMemberS)
	if !ok || storedDestination.Value != destinations[winner] {
		t.Fatalf("expected winning destination %q, got item %#v", destinations[winner], stored.Item)
	}

	claimCount := 0
	for i, destination := range destinations {
		claim, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
			TableName:      aws.String(r.claimTable),
			Key:            map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: digestOf(destination)}},
			ConsistentRead: aws.Bool(true),
		})
		if err != nil {
			t.Fatalf("strongly read claim %d: %v", i, err)
		}
		if claim.Item == nil {
			continue
		}
		claimCount++
		if i != winner {
			t.Fatalf("losing writer %d left a partial destination claim", i)
		}
	}
	if claimCount != 1 {
		t.Fatalf("expected exactly one committed destination claim, got %d", claimCount)
	}
}

// TestCaseSensitiveKeysRemainDistinct proves DR-001 at the DynamoDB boundary:
// keys differing only by ASCII case remain separate partition-key values and
// resolve to their own immutable destinations.
func TestCaseSensitiveKeysRemainDistinct(t *testing.T) {
	r := freshRepo(t)

	for _, tc := range []struct {
		key         string
		destination string
	}{
		{key: "CaseA1", destination: "https://example.com/upper"},
		{key: "caseA1", destination: "https://example.com/lower"},
	} {
		result, err := r.Create(context.Background(), tc.key, tc.destination)
		if err != nil {
			t.Fatalf("Create(%q): %v", tc.key, err)
		}
		if !result.Created || result.ShortKey != tc.key {
			t.Fatalf("Create(%q) returned %+v", tc.key, result)
		}
	}

	for _, tc := range []struct {
		key         string
		destination string
	}{
		{key: "CaseA1", destination: "https://example.com/upper"},
		{key: "caseA1", destination: "https://example.com/lower"},
	} {
		destination, found, err := r.Get(context.Background(), tc.key)
		if err != nil {
			t.Fatalf("Get(%q): %v", tc.key, err)
		}
		if !found || destination != tc.destination {
			t.Fatalf("Get(%q): found=%v destination=%q, want %q", tc.key, found, destination, tc.destination)
		}
	}
}

// TestDestinationBoundaryRoundTripsExactBytes proves DR-002 at the durable
// repository boundary. The largest accepted destination is returned byte for
// byte, while limit+1 is rejected before either table receives a partial item.
func TestDestinationBoundaryRoundTripsExactBytes(t *testing.T) {
	r := freshRepo(t)
	const prefix = "https://example.com/"
	atLimit := prefix + strings.Repeat("a", maxDestinationLength-len(prefix))

	result, err := r.Create(context.Background(), "limit01", atLimit)
	if err != nil {
		t.Fatalf("Create(destination at limit): %v", err)
	}
	if !result.Created {
		t.Fatal("expected destination at the limit to create a mapping")
	}

	got, found, err := r.Get(context.Background(), result.ShortKey)
	if err != nil {
		t.Fatalf("Get(destination at limit): %v", err)
	}
	if !found || got != atLimit {
		t.Fatalf("destination round trip mismatch: found=%v got length=%d want length=%d", found, len(got), len(atLimit))
	}

	tooLong := atLimit + "b"
	if _, err := r.Create(context.Background(), "limit02", tooLong); !errors.Is(err, ErrDestinationTooLong) {
		t.Fatalf("Create(destination over limit): got %v, want ErrDestinationTooLong", err)
	}

	for table, key := range map[string]string{
		r.mappingTable: "limit02",
		r.claimTable:   digestOf(tooLong),
	} {
		out, err := r.client.GetItem(context.Background(), &dynamodb.GetItemInput{
			TableName:      aws.String(table),
			Key:            map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: key}},
			ConsistentRead: aws.Bool(true),
		})
		if err != nil {
			t.Fatalf("read %s after rejected destination: %v", table, err)
		}
		if out.Item != nil {
			t.Fatalf("rejected destination left an item in %s: %#v", table, out.Item)
		}
	}
}

// TestGetWithStatus_DistinguishesActiveFromSuspended is T8-02's core
// finding: the pre-existing Get() silently drops the stored status
// field entirely — every existing mapping looks "resolvable" to it
// regardless of status. GetWithStatus is the fix; this proves it
// actually reports Suspended, not just Active, for a real stored
// record — no suspend functionality exists yet to produce one through
// normal use, so this seeds the Suspended row directly, the same
// "poison a real record" technique T7-03 used to force a collision.
func TestGetWithStatus_DistinguishesActiveFromSuspended(t *testing.T) {
	r := freshRepo(t)

	active, err := r.Create(context.Background(), "activeK", "https://example.com/active")
	if err != nil {
		t.Fatalf("create active mapping: %v", err)
	}

	const suspendedKey = "suspendK"
	const suspendedDest = "https://example.com/suspended"
	_, err = r.client.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(r.mappingTable),
		Item: map[string]types.AttributeValue{
			"pk":          &types.AttributeValueMemberS{Value: suspendedKey},
			"destination": &types.AttributeValueMemberS{Value: suspendedDest},
			"status":      &types.AttributeValueMemberS{Value: "Suspended"},
			"version":     &types.AttributeValueMemberN{Value: "2"},
		},
	})
	if err != nil {
		t.Fatalf("seed suspended mapping directly: %v", err)
	}

	activeRecord, found, err := r.GetWithStatus(context.Background(), active.ShortKey)
	if err != nil {
		t.Fatalf("GetWithStatus(active): unexpected error: %v", err)
	}
	if !found || activeRecord.Status != "Active" || activeRecord.Destination != "https://example.com/active" {
		t.Fatalf("expected an Active record for %q, got found=%v record=%+v", active.ShortKey, found, activeRecord)
	}

	suspendedRecord, found, err := r.GetWithStatus(context.Background(), suspendedKey)
	if err != nil {
		t.Fatalf("GetWithStatus(suspended): unexpected error: %v", err)
	}
	if !found || suspendedRecord.Status != "Suspended" || suspendedRecord.Destination != suspendedDest {
		t.Fatalf("expected a Suspended record for %q, got found=%v record=%+v", suspendedKey, found, suspendedRecord)
	}

	_, found, err = r.GetWithStatus(context.Background(), "totallyUnknownKey")
	if err != nil {
		t.Fatalf("GetWithStatus(unknown): unexpected error: %v", err)
	}
	if found {
		t.Fatalf("expected found=false for a genuinely unknown key")
	}
	t.Log("PASS: GetWithStatus correctly distinguished Active, Suspended, and unknown for real stored records")
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
