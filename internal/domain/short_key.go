package domain

import (
	"errors"
	"regexp"
)

// ErrInvalidShortKey is returned for a syntactically invalid key
// (BR-002/DR-001).
var ErrInvalidShortKey = errors.New("domain: short key must be 1-7 characters from 0-9, a-z, A-Z")

// shortKeyPattern is the confirmed case-sensitive alphabet
// (AGENTS.md/DEC-012): digits, lowercase, uppercase, 1-7 characters
// within the planned horizon (docs/SRS.md's seven-position derivation).
//
// This duplicates internal/api.shortKeyPattern for now — deliberately,
// not by oversight. T6-01's scope is the domain value type itself;
// rewiring internal/api to depend on this type instead of its own copy
// is T6-03's job ("creation application use case and repository port"),
// which is what actually restructures the transport/application layers
// around domain types. Until then this is the authoritative home for
// new domain-layer consumers, and internal/api keeps its own working,
// already-tested copy rather than being touched here.
var shortKeyPattern = regexp.MustCompile(`^[0-9a-zA-Z]{1,7}$`)

// ShortKey is a validated short-key value (BR-002/DR-001). The zero
// value is not a valid ShortKey — the only way to obtain one is
// NewShortKey, so an invalid or out-of-alphabet key cannot exist as a
// ShortKey value at all, not merely be checked for at some call site.
type ShortKey struct {
	value string
}

// NewShortKey validates s against the confirmed alphabet and length
// bound and returns the corresponding ShortKey, or ErrInvalidShortKey.
func NewShortKey(s string) (ShortKey, error) {
	if !shortKeyPattern.MatchString(s) {
		return ShortKey{}, ErrInvalidShortKey
	}
	return ShortKey{value: s}, nil
}

// String returns the key's exact, case-preserved form (BR-002:
// "comparisons preserve case").
func (k ShortKey) String() string { return k.value }
