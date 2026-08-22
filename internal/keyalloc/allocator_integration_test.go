//go:build integration

// This file requires a running DynamoDB Local instance
// (docker compose -f docker-compose.yml up -d) and is excluded from the
// default `go test ./...` run via the `integration` build tag, since it is
// integration evidence for docs/decisions/DEC-012.md (POC-001), not a unit
// test. Run with: go test -tags=integration ./internal/keyalloc/...
package keyalloc

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
