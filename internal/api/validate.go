// Package api implements the public creation/resolution HTTP contract
// finalized in docs/ACCEPTANCE_CRITERIA.md (T2-02/T2-03) and validated here
// as POC-004 evidence: destination validation (docs/decisions/DEC-007.md),
// short-key syntax (BR-002/BR-003), and per-IP rate limiting
// (docs/decisions/DEC-005.md). Not a production-hardened service (no
// distributed rate-limit store, no structured logging).
package api

import (
	"errors"
	"net/url"
	"regexp"
)

// Errors returned by ValidateDestination — deliberately distinct so callers
// can map each to the confirmed 400 response without re-parsing.
var (
	ErrDestinationEmpty    = errors.New("destination is required")
	ErrDestinationTooLong  = errors.New("destination exceeds 2048 characters") // docs/decisions/DEC-007.md
	ErrUnsupportedScheme   = errors.New("destination must be http or https")
	ErrRelativeDestination = errors.New("destination must be an absolute URL")
	ErrUserInfoPresent     = errors.New("destination must not contain user-info (user:pass@host)") // DEC-007
	ErrControlCharacters   = errors.New("destination contains a disallowed control character")
)

const maxDestinationLength = 2048

// ValidateDestination enforces docs/decisions/DEC-007.md's confirmed
// profile: http/https only, absolute, <=2048 chars, no user-info, and no
// control characters (defense in depth against header-injection attempts
// via a crafted destination value that later flows into a Location
// header).
func ValidateDestination(raw string) error {
	if raw == "" {
		return ErrDestinationEmpty
	}
	if len(raw) > maxDestinationLength {
		return ErrDestinationTooLong
	}
	for _, r := range raw {
		// Reject any ASCII control character, including CR/LF, TAB, and
		// NUL — these have no legitimate place in a destination URL and
		// are the building blocks of header-injection/response-splitting
		// attempts if a destination is later echoed into an HTTP header.
		if r < 0x20 || r == 0x7f {
			return ErrControlCharacters
		}
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ErrRelativeDestination
	}
	if !u.IsAbs() {
		return ErrRelativeDestination
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrUnsupportedScheme
	}
	if u.User != nil {
		return ErrUserInfoPresent
	}
	return nil
}

// shortKeyPattern is the confirmed case-sensitive alphabet from
// AGENTS.md/DEC-012: digits, lowercase, uppercase, 1-7 characters within
// the planned horizon (docs/SRS.md's seven-position derivation).
var shortKeyPattern = regexp.MustCompile(`^[0-9a-zA-Z]{1,7}$`)

// ErrInvalidShortKey is returned for a syntactically invalid key (FR-013).
var ErrInvalidShortKey = errors.New("short key is not syntactically valid")

// ValidateShortKey enforces the confirmed alphabet and length bound.
func ValidateShortKey(key string) error {
	if !shortKeyPattern.MatchString(key) {
		return ErrInvalidShortKey
	}
	return nil
}
