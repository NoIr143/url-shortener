//go:build integration

// POC-005 evidence, scoped to what is honestly testable against local
// substitutes. See docs/poc/POC-005-results.md for what this explicitly
// does NOT cover (real AZ loss, ECS task loss, DynamoDB PITR, SQS/EventBridge
// queue lag, and deployment rollback) and why.
package mapping

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestBackupRestoreReconcile drills the restore/reconciliation
// invariant DR-009 requires: after backing up a populated repository and
// restoring it into fresh tables, every mapping and claim must be present
// with zero unexplained differences.
func TestBackupRestoreReconcile(t *testing.T) {
	client := localClient(t)
	suffix := time.Now().UnixNano()
	src := New(client, fmt.Sprintf("backup_src_mapping_%d", suffix), fmt.Sprintf("backup_src_claim_%d", suffix))
	if err := src.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure source tables: %v", err)
	}

	const n = 25
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("bk%02d", i)
		dest := fmt.Sprintf("https://example.com/backup-drill/%d", i)
		if _, err := src.Create(context.Background(), key, dest); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	snapshot, err := src.Backup(context.Background())
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Simulate restoring into a freshly provisioned environment (new
	// tables — the local substitute for "new AWS account/region" since
	// DynamoDB Local has no cross-instance restore to exercise).
	dst := New(client, fmt.Sprintf("backup_dst_mapping_%d", suffix), fmt.Sprintf("backup_dst_claim_%d", suffix))
	if err := dst.EnsureTables(context.Background()); err != nil {
		t.Fatalf("ensure destination tables: %v", err)
	}
	if err := dst.Restore(context.Background(), snapshot); err != nil {
		t.Fatalf("restore: %v", err)
	}

	srcMappingCount, srcClaimCount, err := src.CountItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dstMappingCount, dstClaimCount, err := dst.CountItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if srcMappingCount != dstMappingCount || srcClaimCount != dstClaimCount {
		t.Fatalf("count mismatch after restore: src=(%d mappings,%d claims) dst=(%d mappings,%d claims)",
			srcMappingCount, srcClaimCount, dstMappingCount, dstClaimCount)
	}

	for i := 0; i < n; i++ {
		key := fmt.Sprintf("bk%02d", i)
		wantDest := fmt.Sprintf("https://example.com/backup-drill/%d", i)
		gotDest, ok, err := dst.Get(context.Background(), key)
		if err != nil {
			t.Fatalf("get restored %s: %v", key, err)
		}
		if !ok {
			t.Fatalf("restored mapping %s missing", key)
		}
		if gotDest != wantDest {
			t.Fatalf("restored mapping %s: destination mismatch: got %q want %q", key, gotDest, wantDest)
		}
	}

	orphans, err := dst.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("expected zero unexplained differences after restore, found orphaned claims: %v", orphans)
	}
	t.Logf("PASS: backed up %d mappings, restored into fresh tables, zero unexplained differences (DR-009)", n)
}
