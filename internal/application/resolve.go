// resolve.go implements the redirect resolution use case (T8-02;
// FR-009 to FR-016/021): a repository-only resolver, deliberately
// without any cache layer — Valkey status-aware cache-aside is T9-06's
// separate, later task, added on top of this one, not built here.
//
// "Repository-only" and "safe status outcomes" together mean this use
// case reports exactly one of a closed set of outcomes for every
// input, with no path that could expose a suspended mapping's
// destination or redirect on an ambiguous/unrecognized state
// (BR-007/NFR-SAFE-001).
package application

import (
	"context"
	"errors"
	"fmt"

	"url-shortener/internal/domain"
)

// ResolveStatus is the closed set of outcomes Resolve can report. The
// zero value is never a valid ResolveStatus — every constant starts at
// 1, so a zero-value ResolveResult is detectably not any of them
// (mirrors internal/domain's "unrepresentable invalid state" pattern).
type ResolveStatus int

const (
	ResolveStatusActive ResolveStatus = iota + 1
	ResolveStatusUnknown
	ResolveStatusSuspended
)

// ResolveResult is Resolve's success return. Destination is only
// meaningful when Status is ResolveStatusActive — for
// ResolveStatusSuspended in particular, this is how "no destination
// leaked" (FR-012's acceptance criterion) is enforced structurally:
// the caller has no way to reach a Suspended mapping's Destination,
// because Resolve never puts one there for that outcome.
type ResolveResult struct {
	Status      ResolveStatus
	Destination domain.Destination
}

// MappingReader is the repository-only port this resolver depends on
// (dependency inversion, matching CreationUseCase's MappingRepository).
// found=false with a nil error means no mapping exists for shortKey —
// not an error at all, exactly like MappingRepository.Create's
// exact-repeat outcome is not an error.
type MappingReader interface {
	Get(ctx context.Context, shortKey domain.ShortKey) (destination domain.Destination, status domain.Status, found bool, err error)
}

// ResolveUseCase implements FR-009 to FR-016/021.
type ResolveUseCase struct {
	repo MappingReader
}

func NewResolveUseCase(repo MappingReader) *ResolveUseCase {
	return &ResolveUseCase{repo: repo}
}

// Resolve looks up shortKey and reports a safe outcome. Every error
// return carries a zero ResolveResult — there is no path where a
// Destination is returned alongside a non-nil error, and no path where
// an unrecognized/corrupt stored status is silently treated as Active.
func (u *ResolveUseCase) Resolve(ctx context.Context, key domain.ShortKey) (ResolveResult, error) {
	destination, status, found, err := u.repo.Get(ctx, key)
	if err != nil {
		return ResolveResult{}, errors.Join(ErrUnavailable, err)
	}
	if !found {
		return ResolveResult{Status: ResolveStatusUnknown}, nil
	}
	switch status {
	case domain.StatusActive:
		return ResolveResult{Status: ResolveStatusActive, Destination: destination}, nil
	case domain.StatusSuspended:
		return ResolveResult{Status: ResolveStatusSuspended}, nil
	default:
		// An unrecognized or corrupt stored status (anything that is
		// neither of the two domain.Status constants) must never be
		// treated as Active — fail closed (NFR-SAFE-001: no redirect
		// from an ambiguous/corrupt/unverifiable state), not guess.
		return ResolveResult{}, errors.Join(ErrUnavailable, fmt.Errorf("unrecognized mapping status %q for key %q", status, key))
	}
}
