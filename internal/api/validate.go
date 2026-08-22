// Package api implements the public creation/resolution HTTP contract
// finalized in docs/ACCEPTANCE_CRITERIA.md (T2-02/T2-03) and tested here
// against an attack/boundary corpus: destination validation
// (docs/decisions/DEC-007.md), short-key syntax (BR-002/BR-003), and
// per-IP rate limiting (docs/decisions/DEC-005.md). Not a
// production-hardened service (no distributed rate-limit store, no
// structured logging).
package api

import (
	"errors"

	"url-shortener/internal/domain"
)

// Errors returned by ValidateDestination — deliberately distinct so callers
// can map each to the confirmed 400 response without re-parsing. Kept as
// this package's own sentinel values (internal/webui switches on their
// exact identity to render field-level UI copy — docs/UI_UX_DESIGN.md)
// even though the validation itself now lives in internal/domain
// (T6-02): ValidateDestination delegates to domain.NewDestination and
// translates its error back to the matching value below, so this
// package's public error identity never changes underneath its callers.
var (
	ErrDestinationEmpty    = errors.New("destination is required")
	ErrDestinationTooLong  = errors.New("destination exceeds 2048 characters") // docs/decisions/DEC-007.md
	ErrUnsupportedScheme   = errors.New("destination must be http or https")
	ErrRelativeDestination = errors.New("destination must be an absolute URL")
	ErrUserInfoPresent     = errors.New("destination must not contain user-info (user:pass@host)") // DEC-007
	ErrControlCharacters   = errors.New("destination contains a disallowed control character")
)

// ValidateDestination enforces docs/decisions/DEC-007.md's confirmed
// profile — delegated to internal/domain.NewDestination (T6-02), the
// single authoritative implementation, and translated back to this
// package's own error values so existing callers (internal/webui's
// error-message switch) keep working unchanged.
func ValidateDestination(raw string) error {
	_, err := domain.NewDestination(raw)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrDestinationEmpty):
		return ErrDestinationEmpty
	case errors.Is(err, domain.ErrDestinationTooLong):
		return ErrDestinationTooLong
	case errors.Is(err, domain.ErrUnsupportedScheme):
		return ErrUnsupportedScheme
	case errors.Is(err, domain.ErrRelativeDestination):
		return ErrRelativeDestination
	case errors.Is(err, domain.ErrUserInfoPresent):
		return ErrUserInfoPresent
	case errors.Is(err, domain.ErrControlCharacters):
		return ErrControlCharacters
	default:
		return err
	}
}

// ErrInvalidShortKey is returned for a syntactically invalid key (FR-013).
// Kept as this package's own sentinel for the same reason as the
// destination errors above, even though nothing outside this package
// currently branches on its specific identity (unlike the destination
// errors, ResolveHandler only ever checks err != nil) — kept for
// consistency and so a future caller could start relying on it without
// this package's public error taxonomy changing shape again.
var ErrInvalidShortKey = errors.New("short key is not syntactically valid")

// ValidateShortKey enforces the confirmed alphabet and length bound —
// delegated to internal/domain.NewShortKey (T6-02), the single
// authoritative implementation.
func ValidateShortKey(key string) error {
	if _, err := domain.NewShortKey(key); err != nil {
		return ErrInvalidShortKey
	}
	return nil
}
