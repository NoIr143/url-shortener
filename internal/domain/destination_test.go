package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNewDestination_ValidCases(t *testing.T) {
	cases := []string{
		"https://example.com/very/long/path?query=1",
		"http://example.com/ok",
		"https://example.com/" + strings.Repeat("a", maxDestinationLength-len("https://example.com/")), // exactly at the limit
	}
	for _, s := range cases {
		if _, err := NewDestination(s); err != nil {
			t.Errorf("NewDestination(len=%d): expected success, got %v", len(s), err)
		}
	}
}

// TestNewDestination_LengthBoundary proves the exact limit-1/limit/
// limit+1 boundary T6-02's evidence bar names, all built as otherwise-
// valid URLs so length is the only variable under test.
func TestNewDestination_LengthBoundary(t *testing.T) {
	prefix := "https://example.com/"
	pad := func(total int) string {
		return prefix + strings.Repeat("a", total-len(prefix))
	}

	limitMinus1 := pad(maxDestinationLength - 1)
	limit := pad(maxDestinationLength)
	limitPlus1 := pad(maxDestinationLength + 1)

	if len(limitMinus1) != maxDestinationLength-1 || len(limit) != maxDestinationLength || len(limitPlus1) != maxDestinationLength+1 {
		t.Fatalf("test construction bug: got lengths %d/%d/%d, want %d/%d/%d",
			len(limitMinus1), len(limit), len(limitPlus1),
			maxDestinationLength-1, maxDestinationLength, maxDestinationLength+1)
	}

	if _, err := NewDestination(limitMinus1); err != nil {
		t.Errorf("limit-1 (%d chars): expected success, got %v", len(limitMinus1), err)
	}
	if _, err := NewDestination(limit); err != nil {
		t.Errorf("limit (%d chars): expected success, got %v", len(limit), err)
	}
	if _, err := NewDestination(limitPlus1); !errors.Is(err, ErrDestinationTooLong) {
		t.Errorf("limit+1 (%d chars): expected ErrDestinationTooLong, got %v", len(limitPlus1), err)
	}
}

func TestNewDestination_Empty(t *testing.T) {
	if _, err := NewDestination(""); !errors.Is(err, ErrDestinationEmpty) {
		t.Fatalf("expected ErrDestinationEmpty, got %v", err)
	}
}

func TestNewDestination_TooLong(t *testing.T) {
	tooLong := strings.Repeat("a", maxDestinationLength+1) // one over the limit
	if _, err := NewDestination(tooLong); !errors.Is(err, ErrDestinationTooLong) {
		t.Fatalf("expected ErrDestinationTooLong, got %v", err)
	}
}

// TestNewDestination_MaliciousCorpus proves DEC-007's full validation
// profile: scheme allowlist, absolute-URL requirement, no user-info,
// no control characters — the malicious/boundary cases T6-02 names.
func TestNewDestination_MaliciousCorpus(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"relative path", "/just/a/path", ErrRelativeDestination},
		{"javascript scheme", "javascript:alert(1)", ErrUnsupportedScheme}, // has a scheme (so IsAbs()==true) but not http/https
		{"data scheme", "data:text/html,<script>alert(1)</script>", ErrUnsupportedScheme},
		{"file scheme", "file:///etc/passwd", ErrUnsupportedScheme},
		{"user-info credential leak", "https://user:pass@example.com/", ErrUserInfoPresent},
		{"CRLF header injection attempt", "https://example.com/\r\nSet-Cookie: evil=1", ErrControlCharacters},
		{"embedded NUL byte", "https://example.com/\x00hidden", ErrControlCharacters},
		{"embedded TAB", "https://example.com/\thidden", ErrControlCharacters},
		{"embedded DEL", "https://example.com/\x7fhidden", ErrControlCharacters},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewDestination(c.raw); !errors.Is(err, c.wantErr) {
				t.Errorf("NewDestination(%q): expected %v, got %v", c.raw, c.wantErr, err)
			}
		})
	}
}

// TestDestination_ZeroValueNotConstructible mirrors the ShortKey case:
// the zero value's own String() (empty) is itself rejected by
// NewDestination, so a zero Destination can never be mistaken for a
// validated one.
func TestDestination_ZeroValueNotConstructible(t *testing.T) {
	var zero Destination
	if _, err := NewDestination(zero.String()); !errors.Is(err, ErrDestinationEmpty) {
		t.Fatalf("expected the zero Destination's own String() to be rejected, got err=%v", err)
	}
}
