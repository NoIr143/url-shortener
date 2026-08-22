package domain

import (
	"errors"
	"net/url"
)

// Errors returned by NewDestination — deliberately distinct so callers
// can map each to a specific response without re-parsing.
var (
	ErrDestinationEmpty    = errors.New("domain: destination is required")
	ErrDestinationTooLong  = errors.New("domain: destination exceeds 2048 characters") // docs/decisions/DEC-007.md
	ErrControlCharacters   = errors.New("domain: destination contains a disallowed control character")
	ErrRelativeDestination = errors.New("domain: destination must be an absolute URL")
	ErrUnsupportedScheme   = errors.New("domain: destination must be http or https")
	ErrUserInfoPresent     = errors.New("domain: destination must not contain user-info (user:pass@host)") // DEC-007
)

const maxDestinationLength = 2048

// Destination is a Mapping's target URL value (DR-002, extended here to
// DEC-007's full validation profile per T6-02: http/https only,
// absolute, <=2048 chars, no user-info, and no control characters —
// defense in depth against header-injection attempts via a crafted
// destination value that later flows into a Location header).
//
// The zero value is not a valid Destination — only NewDestination
// produces one. There is no setter: BR-004's "an active mapping's
// destination is immutable during normal MVP operation" is enforced by
// the type simply never exposing a way to change it after construction,
// not by a runtime check someone could forget to call.
type Destination struct {
	value string
}

// NewDestination validates raw against DEC-007's profile and returns
// the corresponding Destination, or one of the Err* values above.
func NewDestination(raw string) (Destination, error) {
	if raw == "" {
		return Destination{}, ErrDestinationEmpty
	}
	if len(raw) > maxDestinationLength {
		return Destination{}, ErrDestinationTooLong
	}
	for _, r := range raw {
		// Reject any ASCII control character, including CR/LF, TAB, and
		// NUL — these have no legitimate place in a destination URL and
		// are the building blocks of header-injection/response-splitting
		// attempts if a destination is later echoed into an HTTP header.
		if r < 0x20 || r == 0x7f {
			return Destination{}, ErrControlCharacters
		}
	}

	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return Destination{}, ErrRelativeDestination
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Destination{}, ErrUnsupportedScheme
	}
	if u.User != nil {
		return Destination{}, ErrUserInfoPresent
	}

	return Destination{value: raw}, nil
}

// String returns the exact destination value.
func (d Destination) String() string { return d.value }
