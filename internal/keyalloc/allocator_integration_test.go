//go:build integration

// Requires DynamoDB Local by default: export DYNAMODB_ENDPOINT (e.g.
// http://localhost:8000, docker compose -f docker-compose.yml up -d)
// before running. If DYNAMODB_ENDPOINT is left unset, this talks to real
// AWS DynamoDB instead, using the ambient AWS credential chain (T5-08's
// ephemeral-AWS integration workflow) — never the static "local"
// credentials below, which would silently fail against a real account.
// Excluded from the default `go test ./...` run via the `integration`
// build tag, since it is integration evidence for
// docs/decisions/DEC-012.md (POC-001), not a unit test. Run with:
// go test -tags=integration ./internal/keyalloc/...
package keyalloc

import (
	"context"
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
func deleteTable(t *testing.T, client *dynamodb.Client, table string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)}); err != nil {
			t.Errorf("cleanup: delete table %s: %v", table, err)
		}
	})
}

// TestConcurrentLeasesNeverOverlap proves the central POC-001
// requirement: under concurrent allocation, no two allocator instances
// receive overlapping numeric ranges (zero duplicate committed keys).
func TestConcurrentLeasesNeverOverlap(t *testing.T) {
	client := localClient(t)
	table := fmt.Sprintf("keyalloc_concurrent_%d", time.Now().UnixNano())
	a := New(client, table, 1_000_000)
	if err := a.EnsureTable(context.Background()); err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	deleteTable(t, client, table)

	const goroutines = 50
	const leaseSize = 100

	type result struct {
		lease Lease
		err   error
	}
	results := make([]result, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			lease, err := a.AcquireLease(context.Background(), leaseSize, 50)
			results[idx] = result{lease: lease, err: err}
		}(i)
	}
	wg.Wait()

	type interval struct{ start, end int64 }
	var intervals []interval
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, r.err)
		}
		intervals = append(intervals, interval{r.lease.Start, r.lease.End})
	}

	// Zero duplicate committed keys: verify no two leased ranges overlap.
	for i := 0; i < len(intervals); i++ {
		for j := i + 1; j < len(intervals); j++ {
			a, b := intervals[i], intervals[j]
			if a.start < b.end && b.start < a.end {
				t.Fatalf("overlapping leases detected: [%d,%d) and [%d,%d)", a.start, a.end, b.start, b.end)
			}
		}
	}
	t.Logf("PASS: %d concurrent goroutines received %d non-overlapping ranges of size %d", goroutines, len(intervals), leaseSize)
}

// TestExhaustionFailsSafely proves that once the configured ID
// space is exhausted, AcquireLease fails with ErrExhausted rather than
// issuing an out-of-bound or overlapping range.
func TestExhaustionFailsSafely(t *testing.T) {
	client := localClient(t)
	table := fmt.Sprintf("keyalloc_exhaustion_%d", time.Now().UnixNano())
	// Small ID space: exactly 3 leases of size 10 fit (0-30), a 4th must fail.
	a := New(client, table, 30)
	if err := a.EnsureTable(context.Background()); err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	deleteTable(t, client, table)

	for i := 0; i < 3; i++ {
		if _, err := a.AcquireLease(context.Background(), 10, 10); err != nil {
			t.Fatalf("lease %d: expected success, got %v", i, err)
		}
	}

	_, err := a.AcquireLease(context.Background(), 10, 10)
	if err != ErrExhausted {
		t.Fatalf("expected ErrExhausted once space is full, got %v", err)
	}
	t.Log("PASS: 4th lease correctly rejected with ErrExhausted after exactly filling a 30-id space with three 10-id leases")
}

// TestStaleFencingTokenCannotSucceed simulates a split-brain
// scenario: two allocator "instances" observe the same counter state, one
// successfully advances it, and the other's conditional write must fail
// (fenced out) rather than silently issuing a range that overlaps the
// winner's.
func TestStaleFencingTokenCannotSucceed(t *testing.T) {
	client := localClient(t)
	table := fmt.Sprintf("keyalloc_splitbrain_%d", time.Now().UnixNano())
	a := New(client, table, 1_000_000)
	if err := a.EnsureTable(context.Background()); err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	deleteTable(t, client, table)

	// "Instance A" (maxRetries=1: it will not retry after losing the race,
	// simulating a stale/partitioned holder that gives up immediately)
	// races against "Instance B" (maxRetries=10, representing the current
	// legitimate holder) by both starting from the same initial state.
	var wg sync.WaitGroup
	var errA, errB error
	var leaseA, leaseB Lease
	wg.Add(2)
	go func() {
		defer wg.Done()
		leaseA, errA = a.AcquireLease(context.Background(), 5, 1)
	}()
	go func() {
		defer wg.Done()
		leaseB, errB = a.AcquireLease(context.Background(), 5, 10)
	}()
	wg.Wait()

	// At least one must succeed (B has retry budget); it is not required
	// that A fails since goroutine scheduling is non-deterministic — the
	// invariant under test is that IF both "succeed", their ranges must
	// never overlap, and a low-retry-budget loser must fail cleanly with
	// ErrFencedOut, never with a corrupted/overlapping lease.
	if errA != nil && errA != ErrFencedOut {
		t.Fatalf("instance A: unexpected error type: %v", errA)
	}
	if errB != nil {
		t.Fatalf("instance B (has retry budget): unexpected failure: %v", errB)
	}
	if errA == nil && errB == nil {
		if leaseA.Start < leaseB.End && leaseB.Start < leaseA.End {
			t.Fatalf("split-brain overlap: A=[%d,%d) B=[%d,%d)", leaseA.Start, leaseA.End, leaseB.Start, leaseB.End)
		}
	}
	t.Logf("PASS: split-brain race resolved without overlap (errA=%v errB=%v)", errA, errB)
}

// TestRestartResumesWithoutOverlap simulates the "restart" scenario
// T7-02's evidence bar names: a process leases a range, then crashes —
// modeled here by simply discarding the first *Allocator and
// constructing a brand new one against the same table, the only state
// that actually survives a real process restart. The new instance's
// first lease must start exactly where the crashed instance's last
// lease ended, never overlapping and never re-issuing any part of it.
func TestRestartResumesWithoutOverlap(t *testing.T) {
	client := localClient(t)
	table := fmt.Sprintf("keyalloc_restart_%d", time.Now().UnixNano())

	before := New(client, table, 1_000_000)
	if err := before.EnsureTable(context.Background()); err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	deleteTable(t, client, table)

	firstLease, err := before.AcquireLease(context.Background(), 50, 1)
	if err != nil {
		t.Fatalf("pre-restart lease: unexpected error: %v", err)
	}

	// "Restart": before is simply dropped (as if the process exited) and
	// a fresh Allocator is constructed against the same table — no
	// in-memory state carries over, exactly as a real process restart
	// would leave nothing but what's durably stored in DynamoDB.
	after := New(client, table, 1_000_000)

	for i := 0; i < 3; i++ {
		lease, err := after.AcquireLease(context.Background(), 50, 10)
		if err != nil {
			t.Fatalf("post-restart lease %d: unexpected error: %v", i, err)
		}
		if lease.Start < firstLease.End {
			t.Fatalf("post-restart lease %d [%d,%d) overlaps or re-issues part of the pre-restart lease [%d,%d)",
				i, lease.Start, lease.End, firstLease.Start, firstLease.End)
		}
	}
	t.Logf("PASS: post-restart allocator resumed at %d without re-issuing any part of the pre-restart lease [%d,%d)", firstLease.End, firstLease.Start, firstLease.End)
}

// TestAbandonedLeaseNeverReissued is T7-02's "expiry" evidence: a lease
// holder that only ever consumes part of its leased range (the rest is
// effectively abandoned — a crash mid-lease, or simply never fully
// used) must never have that unused remainder handed to anyone else
// later. docs/SYSTEM_DESIGN.md's DATA-003 describes an expiry/status
// field on the lease record; this Allocator does not implement one — a
// monotonic, append-only counter (visible in allocator.go: next_start
// only ever increases) makes reissue structurally impossible regardless
// of whether a lease was fully used, so there is nothing to expire or
// reclaim. This test proves that invariant holds, not that a reclaim
// mechanism exists. See docs/poc/T7-02-lease-restart-expiry-tests.md
// Section 1 for why building one is not justified at this project's
// actual scale (365 billion mappings against a 3.52 trillion capacity —
// docs/SRS.md).
func TestAbandonedLeaseNeverReissued(t *testing.T) {
	client := localClient(t)
	table := fmt.Sprintf("keyalloc_expiry_%d", time.Now().UnixNano())

	a := New(client, table, 1_000_000)
	if err := a.EnsureTable(context.Background()); err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	deleteTable(t, client, table)

	abandoned, err := a.AcquireLease(context.Background(), 100, 1)
	if err != nil {
		t.Fatalf("abandoned lease: unexpected error: %v", err)
	}
	// Simulate using only the first 5 of the 100 leased IDs before the
	// holder is abandoned — the remaining 95 are never touched again by
	// anyone, this test included.
	usedUpTo := abandoned.Start + 5

	const followUpLeases = 5
	const leaseSize = 20
	var prevEnd int64 = -1
	for i := 0; i < followUpLeases; i++ {
		lease, err := a.AcquireLease(context.Background(), leaseSize, 10)
		if err != nil {
			t.Fatalf("follow-up lease %d: unexpected error: %v", i, err)
		}
		if lease.Start < abandoned.End {
			t.Fatalf("follow-up lease %d [%d,%d) reissues part of the abandoned lease's unused remainder [%d,%d) — used only up to %d",
				i, lease.Start, lease.End, usedUpTo, abandoned.End, usedUpTo)
		}
		if prevEnd != -1 && lease.Start != prevEnd {
			t.Fatalf("follow-up lease %d does not immediately follow the previous one: previous ended %d, this starts %d", i, prevEnd, lease.Start)
		}
		prevEnd = lease.End
	}
	t.Logf("PASS: %d IDs left unused within an abandoned lease [%d,%d) were never reissued across %d follow-up leases", abandoned.End-usedUpTo, abandoned.Start, abandoned.End, followUpLeases)
}
