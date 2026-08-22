package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNewDestination_ValidCases(t *testing.T) {
	cases := []string{
		"https://example.com/very/long/path?query=1",
		strings.Repeat("a", maxDestinationLength), // exactly at the limit
	}
	for _, s := range cases {
		if _, err := NewDestination(s); err != nil {
			t.Errorf("NewDestination(len=%d): expected success, got %v", len(s), err)
		}
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
