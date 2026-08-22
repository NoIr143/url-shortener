package domain

import "errors"

// Errors returned by NewDestination.
var (
	ErrDestinationEmpty   = errors.New("domain: destination is required")
	ErrDestinationTooLong = errors.New("domain: destination exceeds 2048 characters") // docs/decisions/DEC-007.md
)

const maxDestinationLength = 2048

// Destination is a Mapping's target URL value (DR-002). NewDestination
// enforces only the two invariants DR-002 itself states — non-empty,
// <=2048 characters — deliberately not the fuller scheme/absolute/
// user-info/malicious-input profile DEC-007 defines. That deeper
// validation profile is T6-02's separate, dependent task
// ("destination validation profile and malicious/boundary corpus");
// T6-01 defines the value type's shape, T6-02 extends what populates it.
//
// The zero value is not a valid Destination — only NewDestination
// produces one. There is no setter: BR-004's "an active mapping's
// destination is immutable during normal MVP operation" is enforced by
// the type simply never exposing a way to change it after construction,
// not by a runtime check someone could forget to call.
type Destination struct {
	value string
}

// NewDestination validates raw and returns the corresponding
// Destination, or one of ErrDestinationEmpty/ErrDestinationTooLong.
func NewDestination(raw string) (Destination, error) {
	if raw == "" {
		return Destination{}, ErrDestinationEmpty
	}
	if len(raw) > maxDestinationLength {
		return Destination{}, ErrDestinationTooLong
	}
	return Destination{value: raw}, nil
}

// String returns the exact destination value.
func (d Destination) String() string { return d.value }
