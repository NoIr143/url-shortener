package mapping

import (
	"context"
	"strings"
	"testing"
)

func TestRestoreRejectsMappingWithoutLifecycleMetadata(t *testing.T) {
	r := &Repository{}
	err := r.Restore(context.Background(), []byte(`[{"kind":"mapping","shortKey":"abc","destination":"https://example.com"}]`))
	if err == nil || !strings.Contains(err.Error(), "missing lifecycle metadata") {
		t.Fatalf("expected missing lifecycle metadata error, got %v", err)
	}
}

func TestRestoreRejectsInvalidCreationTime(t *testing.T) {
	r := &Repository{}
	err := r.Restore(context.Background(), []byte(`[{"kind":"mapping","shortKey":"abc","destination":"https://example.com","status":"Active","version":1,"createdAt":"not-a-timestamp"}]`))
	if err == nil || !strings.Contains(err.Error(), "invalid createdAt") {
		t.Fatalf("expected invalid createdAt error, got %v", err)
	}
}

func TestRestoreRejectsNonUTCCreationTime(t *testing.T) {
	r := &Repository{}
	err := r.Restore(context.Background(), []byte(`[{"kind":"mapping","shortKey":"abc","destination":"https://example.com","status":"Active","version":1,"createdAt":"2026-08-25T09:00:00+07:00"}]`))
	if err == nil || !strings.Contains(err.Error(), "createdAt must be UTC") {
		t.Fatalf("expected non-UTC createdAt error, got %v", err)
	}
}
