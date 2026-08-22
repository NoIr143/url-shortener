package application

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"url-shortener/internal/domain"
)

func mustKey(t *testing.T, s string) domain.ShortKey {
	t.Helper()
	k, err := domain.NewShortKey(s)
	if err != nil {
		t.Fatalf("NewShortKey(%q): %v", s, err)
	}
	return k
}

func mustDestination(t *testing.T, s string) domain.Destination {
	t.Helper()
	d, err := domain.NewDestination(s)
	if err != nil {
		t.Fatalf("NewDestination(%q): %v", s, err)
	}
	return d
}

// fakeRepository lets each test script exactly one repository outcome —
// success, repeat, conflict, or an arbitrary unknown error — without any
// real infrastructure.
type fakeRepository struct {
	committedKey domain.ShortKey
	created      bool
	err          error
	calledWith   domain.ShortKey
}

func (f *fakeRepository) Create(ctx context.Context, shortKey domain.ShortKey, destination domain.Destination) (domain.ShortKey, bool, error) {
	f.calledWith = shortKey
	if f.err != nil {
		return domain.ShortKey{}, false, f.err
	}
	return f.committedKey, f.created, nil
}

type fakeKeyGenerator struct {
	key domain.ShortKey
	err error
}

func (f *fakeKeyGenerator) Next() (domain.ShortKey, error) {
	if f.err != nil {
		return domain.ShortKey{}, f.err
	}
	return f.key, nil
}

// TestCreate_Success proves the plain success path (FR-007): a new key
// is generated, committed, and returned with Created=true.
func TestCreate_Success(t *testing.T) {
	candidate := mustKey(t, "abc1234")
	repo := &fakeRepository{committedKey: candidate, created: true}
	keys := &fakeKeyGenerator{key: candidate}
	uc := NewCreationUseCase(repo, keys)

	result, err := uc.Create(context.Background(), mustDestination(t, "https://example.com/new"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Created {
		t.Fatalf("expected Created=true, got false")
	}
	if result.ShortKey != candidate {
		t.Fatalf("expected ShortKey %q, got %q", candidate, result.ShortKey)
	}
	if repo.calledWith != candidate {
		t.Fatalf("expected repository to be called with the generated candidate key %q, got %q", candidate, repo.calledWith)
	}
}

// TestCreate_Repeat proves FR-004: an exact-repeat destination is not
// an error — the repository reports created=false and a possibly
// different (pre-existing) committed key, which Create must pass
// through unchanged.
func TestCreate_Repeat(t *testing.T) {
	candidate := mustKey(t, "new0001")
	existing := mustKey(t, "old9999")
	repo := &fakeRepository{committedKey: existing, created: false}
	keys := &fakeKeyGenerator{key: candidate}
	uc := NewCreationUseCase(repo, keys)

	result, err := uc.Create(context.Background(), mustDestination(t, "https://example.com/repeat"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Created {
		t.Fatalf("expected Created=false for an exact-repeat, got true")
	}
	if result.ShortKey != existing {
		t.Fatalf("expected the pre-existing key %q, got %q", existing, result.ShortKey)
	}
}

// TestCreate_Conflict proves a repository-reported ErrConflict is
// surfaced as-is, with a zero Result — FR-008: no key is ever returned
// alongside an error.
func TestCreate_Conflict(t *testing.T) {
	repo := &fakeRepository{err: ErrConflict}
	keys := &fakeKeyGenerator{key: mustKey(t, "abc1234")}
	uc := NewCreationUseCase(repo, keys)

	result, err := uc.Create(context.Background(), mustDestination(t, "https://example.com/conflict"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if result != (Result{}) {
		t.Fatalf("expected a zero Result alongside an error, got %+v", result)
	}
}

// TestCreate_UnknownRepositoryError proves any other repository error
// (a stand-in for a dependency-unavailable failure) is classified as
// ErrUnavailable, not silently treated as a conflict or a success —
// again with a zero Result.
func TestCreate_UnknownRepositoryError(t *testing.T) {
	underlying := errors.New("simulated network failure")
	repo := &fakeRepository{err: underlying}
	keys := &fakeKeyGenerator{key: mustKey(t, "abc1234")}
	uc := NewCreationUseCase(repo, keys)

	result, err := uc.Create(context.Background(), mustDestination(t, "https://example.com/unknown"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("expected the underlying error to remain inspectable via errors.Is, got %v", err)
	}
	if result != (Result{}) {
		t.Fatalf("expected a zero Result alongside an error, got %+v", result)
	}
}

// TestCreate_KeyGeneratorFailure proves a key-allocator failure (e.g.
// exhaustion) never reaches the repository at all, and is classified
// as ErrUnavailable — FR-008 applies here too: no key, no repository
// call, just a clean error.
func TestCreate_KeyGeneratorFailure(t *testing.T) {
	underlying := errors.New("simulated key-space exhaustion")
	repo := &fakeRepository{committedKey: mustKey(t, "should1"), created: true}
	keys := &fakeKeyGenerator{err: underlying}
	uc := NewCreationUseCase(repo, keys)

	result, err := uc.Create(context.Background(), mustDestination(t, "https://example.com/exhausted"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if result != (Result{}) {
		t.Fatalf("expected a zero Result, got %+v", result)
	}
	if repo.calledWith != (domain.ShortKey{}) {
		t.Fatalf("expected the repository never to be called when key generation fails, but it was called with %q", repo.calledWith)
	}
}

// repoOutcome scripts one Create response.
type repoOutcome struct {
	committedKey domain.ShortKey
	created      bool
	err          error
}

// sequenceRepository returns a different scripted outcome on each
// successive Create call, in order — needed to prove retry behavior,
// where the repository's response must genuinely differ attempt to
// attempt (fakeRepository above always returns the same one thing).
type sequenceRepository struct {
	outcomes []repoOutcome
	calls    []domain.ShortKey
}

func (f *sequenceRepository) Create(ctx context.Context, shortKey domain.ShortKey, destination domain.Destination) (domain.ShortKey, bool, error) {
	f.calls = append(f.calls, shortKey)
	idx := len(f.calls) - 1
	if idx >= len(f.outcomes) {
		return domain.ShortKey{}, false, errors.New("sequenceRepository: no more scripted outcomes")
	}
	o := f.outcomes[idx]
	if o.err != nil {
		return domain.ShortKey{}, false, o.err
	}
	return o.committedKey, o.created, nil
}

// sequenceKeyGenerator returns a different candidate key on each call,
// in order — proving a retry actually asked for a *fresh* key rather
// than reusing the one that just collided.
type sequenceKeyGenerator struct {
	keys []domain.ShortKey
	next int
}

func (f *sequenceKeyGenerator) Next() (domain.ShortKey, error) {
	if f.next >= len(f.keys) {
		return domain.ShortKey{}, errors.New("sequenceKeyGenerator: exhausted")
	}
	k := f.keys[f.next]
	f.next++
	return k, nil
}

// TestCreate_KeyCollisionRetriesWithFreshKey is T7-03's "safe retry"
// half: a key collision on the first attempt is retried with a
// genuinely different candidate key, and a second-attempt success is
// returned as the final Result — the caller never sees the collision.
func TestCreate_KeyCollisionRetriesWithFreshKey(t *testing.T) {
	first := mustKey(t, "taken01")
	second := mustKey(t, "fresh02")
	repo := &sequenceRepository{outcomes: []repoOutcome{
		{err: ErrKeyCollision},
		{committedKey: second, created: true},
	}}
	keys := &sequenceKeyGenerator{keys: []domain.ShortKey{first, second}}
	uc := NewCreationUseCase(repo, keys)

	result, err := uc.Create(context.Background(), mustDestination(t, "https://example.com/retry-success"))
	if err != nil {
		t.Fatalf("unexpected error after a successful retry: %v", err)
	}
	if !result.Created || result.ShortKey != second {
		t.Fatalf("expected the second attempt's result (Created=true, key=%q), got %+v", second, result)
	}
	if len(repo.calls) != 2 {
		t.Fatalf("expected exactly 2 repository calls (initial + 1 retry), got %d", len(repo.calls))
	}
	if repo.calls[0] != first || repo.calls[1] != second {
		t.Fatalf("expected the retry to use a genuinely different candidate key: calls=%v", repo.calls)
	}
}

// TestCreate_KeyCollisionRetriesAreBounded is T7-03's "safe failure"
// half: a repository that reports a key collision every single time
// must not retry forever — Create gives up after
// maxKeyCollisionRetries retries and returns ErrUnavailable (not a
// silent success, not an infinite loop), with a zero Result (FR-008).
func TestCreate_KeyCollisionRetriesAreBounded(t *testing.T) {
	outcomes := make([]repoOutcome, maxKeyCollisionRetries+1)
	keys := make([]domain.ShortKey, maxKeyCollisionRetries+1)
	for i := range outcomes {
		outcomes[i] = repoOutcome{err: ErrKeyCollision}
		keys[i] = mustKey(t, fmt.Sprintf("try%04d", i))
	}
	repo := &sequenceRepository{outcomes: outcomes}
	keyGen := &sequenceKeyGenerator{keys: keys}
	uc := NewCreationUseCase(repo, keyGen)

	result, err := uc.Create(context.Background(), mustDestination(t, "https://example.com/retry-exhausted"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable once retries are exhausted, got %v", err)
	}
	if !errors.Is(err, ErrKeyCollision) {
		t.Fatalf("expected the last ErrKeyCollision to remain inspectable via errors.Is, got %v", err)
	}
	if result != (Result{}) {
		t.Fatalf("expected a zero Result, got %+v", result)
	}
	if len(repo.calls) != maxKeyCollisionRetries+1 {
		t.Fatalf("expected exactly %d repository calls (initial + %d retries), got %d", maxKeyCollisionRetries+1, maxKeyCollisionRetries, len(repo.calls))
	}
}
