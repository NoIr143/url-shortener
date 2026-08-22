// Package application implements the creation use case (FR-004 to
// FR-008; ARC-002): obtain a candidate short key, commit it against the
// destination, and report exactly what happened. Deliberately operates
// on already-validated internal/domain values, not raw strings —
// destination validation (FR-001 to FR-003, docs/decisions/DEC-007.md)
// is T6-02's separate concern (internal/domain.NewDestination), already
// satisfied by the time a caller reaches Create here.
//
// Depends only on internal/domain, never a concrete infrastructure
// package (docs/TECH_STACK.md Section 6: "Keep domain rules independent
// of AWS SDK types. Cloud services are adapters behind narrow
// interfaces.") — MappingRepository/KeyGenerator below are the ports;
// a concrete adapter over internal/mapping.Repository and
// internal/keyalloc.Allocator is wired in by whoever composes this use
// case for real (the JSON handler work in T6-04, or this task's own
// integration test), not by this package.
package application

import (
	"context"
	"errors"

	"url-shortener/internal/domain"
)

// ErrUnavailable reports a dependency failure unrelated to the
// request's own validity (key-space exhaustion, repository
// connectivity, ...). FR-008's "no resolvable short key when creation
// fails before commit": Create's zero Result is returned alongside
// this error, never a ShortKey a caller could mistake for committed.
var ErrUnavailable = errors.New("application: dependency unavailable")

// ErrConflict reports a state a MappingRepository adapter could not
// safely resolve on its own — the intended mapping for
// internal/mapping.ErrDigestCollision once a concrete adapter exists.
// Distinct from a successful exact-repeat (FR-004), which is not an
// error at all: Result.Created is false, but err is nil.
var ErrConflict = errors.New("application: could not safely commit")

// MappingRepository is the port this use case depends on.
type MappingRepository interface {
	// Create attempts to atomically commit shortKey -> destination.
	// created=false with a nil error means an exact byte-identical
	// destination already had a mapping (FR-004/BR-005,
	// docs/decisions/DEC-006.md) — committedKey is that existing
	// mapping's key, which may differ from the shortKey argument.
	// A non-nil err wrapping ErrConflict signals a state the adapter
	// could not resolve; any other non-nil err is treated as a generic
	// dependency failure.
	Create(ctx context.Context, shortKey domain.ShortKey, destination domain.Destination) (committedKey domain.ShortKey, created bool, err error)
}

// KeyGenerator is the port for obtaining one candidate short key
// (FR-006) — uniqueness/leasing is the concrete adapter's job
// (internal/keyalloc.Allocator); this use case only asks for the next
// one.
type KeyGenerator interface {
	Next() (domain.ShortKey, error)
}

// Result reports FR-007's contract: the committed short key and
// whether this call created it.
type Result struct {
	ShortKey domain.ShortKey
	Created  bool // false: FR-004 exact-repeat — ShortKey is the pre-existing mapping's key
}

// CreationUseCase implements FR-004 to FR-008.
type CreationUseCase struct {
	repo MappingRepository
	keys KeyGenerator
}

func NewCreationUseCase(repo MappingRepository, keys KeyGenerator) *CreationUseCase {
	return &CreationUseCase{repo: repo, keys: keys}
}

// Create obtains a candidate key and commits destination. Every error
// return carries a zero Result (FR-008) — there is no path where a
// ShortKey is returned alongside a non-nil error.
func (u *CreationUseCase) Create(ctx context.Context, destination domain.Destination) (Result, error) {
	candidate, err := u.keys.Next()
	if err != nil {
		return Result{}, errors.Join(ErrUnavailable, err)
	}

	committed, created, err := u.repo.Create(ctx, candidate, destination)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return Result{}, ErrConflict
		}
		return Result{}, errors.Join(ErrUnavailable, err)
	}
	return Result{ShortKey: committed, Created: created}, nil
}
