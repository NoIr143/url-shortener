package application

import (
	"context"
	"errors"
	"testing"

	"url-shortener/internal/domain"
)

// fakeMappingReader lets each test script exactly one Get outcome.
type fakeMappingReader struct {
	destination domain.Destination
	status      domain.Status
	found       bool
	err         error
}

func (f *fakeMappingReader) Get(ctx context.Context, shortKey domain.ShortKey) (domain.Destination, domain.Status, bool, error) {
	if f.err != nil {
		return domain.Destination{}, "", false, f.err
	}
	return f.destination, f.status, f.found, nil
}

// TestResolve_Active proves FR-009: an active mapping resolves to
// exactly its stored destination.
func TestResolve_Active(t *testing.T) {
	dest := mustDestination(t, "https://example.com/active-target")
	repo := &fakeMappingReader{destination: dest, status: domain.StatusActive, found: true}
	uc := NewResolveUseCase(repo)

	result, err := uc.Resolve(context.Background(), mustKey(t, "abc1234"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != ResolveStatusActive {
		t.Fatalf("expected ResolveStatusActive, got %v", result.Status)
	}
	if result.Destination != dest {
		t.Fatalf("expected destination %q, got %q", dest, result.Destination)
	}
}

// TestResolve_Unknown proves FR-011: a syntactically valid key with no
// stored mapping is not an error — it's a distinct, safe outcome.
func TestResolve_Unknown(t *testing.T) {
	repo := &fakeMappingReader{found: false}
	uc := NewResolveUseCase(repo)

	result, err := uc.Resolve(context.Background(), mustKey(t, "abc1234"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != ResolveStatusUnknown {
		t.Fatalf("expected ResolveStatusUnknown, got %v", result.Status)
	}
	if result.Destination != (domain.Destination{}) {
		t.Fatalf("expected no destination for an unknown key, got %q", result.Destination)
	}
}

// TestResolve_Suspended proves FR-012: a suspended mapping is reported
// as Suspended, and — critically — its Destination is never exposed in
// the result, even though the fake reader has it available. This is
// what "no destination leaked" actually means structurally: there is
// no field on ResolveResult a caller could read it from for this
// outcome.
func TestResolve_Suspended(t *testing.T) {
	dest := mustDestination(t, "https://example.com/should-never-be-exposed")
	repo := &fakeMappingReader{destination: dest, status: domain.StatusSuspended, found: true}
	uc := NewResolveUseCase(repo)

	result, err := uc.Resolve(context.Background(), mustKey(t, "abc1234"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != ResolveStatusSuspended {
		t.Fatalf("expected ResolveStatusSuspended, got %v", result.Status)
	}
	if result.Destination != (domain.Destination{}) {
		t.Fatalf("expected no destination for a suspended mapping, got %q", result.Destination)
	}
}

// TestResolve_RepositoryUnavailable proves a repository error is
// classified as ErrUnavailable, with a zero ResolveResult.
func TestResolve_RepositoryUnavailable(t *testing.T) {
	underlying := errors.New("simulated dependency failure")
	repo := &fakeMappingReader{err: underlying}
	uc := NewResolveUseCase(repo)

	result, err := uc.Resolve(context.Background(), mustKey(t, "abc1234"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("expected the underlying error to remain inspectable via errors.Is, got %v", err)
	}
	if result != (ResolveResult{}) {
		t.Fatalf("expected a zero ResolveResult, got %+v", result)
	}
}

// TestResolve_UnrecognizedStatusFailsClosed proves NFR-SAFE-001: a
// stored status that is neither of the two confirmed domain.Status
// values (data corruption, a future status this use case doesn't know
// about yet) is never silently treated as Active — it fails with
// ErrUnavailable and a zero ResolveResult, the same as a genuine
// dependency failure, rather than guessing it's safe to redirect.
func TestResolve_UnrecognizedStatusFailsClosed(t *testing.T) {
	dest := mustDestination(t, "https://example.com/should-never-be-exposed")
	repo := &fakeMappingReader{destination: dest, status: domain.Status("SomethingUnexpected"), found: true}
	uc := NewResolveUseCase(repo)

	result, err := uc.Resolve(context.Background(), mustKey(t, "abc1234"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for an unrecognized status, got %v", err)
	}
	if result != (ResolveResult{}) {
		t.Fatalf("expected a zero ResolveResult, got %+v", result)
	}
}
