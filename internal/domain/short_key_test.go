package domain

import (
	"errors"
	"testing"
)

func TestNewShortKey_ValidCases(t *testing.T) {
	cases := []string{
		"a",       // 1 char, minimum length
		"1234567", // 7 chars, maximum length, all digits
		"aB1cD2e", // 7 chars, mixed case
		"ZZZZZZZ", // 7 chars, all uppercase
		"0000000", // 7 chars, all zeros
		"kx7fQ2b", // representative example from README.md
	}
	for _, s := range cases {
		if _, err := NewShortKey(s); err != nil {
			t.Errorf("NewShortKey(%q): expected success, got %v", s, err)
		}
	}
}

func TestNewShortKey_InvalidCases(t *testing.T) {
	cases := []string{
		"",         // empty
		"12345678", // 8 chars, one over the limit
		"ab-cd",    // disallowed character
		"a b",      // space
		"kx7fq2!",  // punctuation
		"kx7fq2é",  // non-ASCII
	}
	for _, s := range cases {
		if _, err := NewShortKey(s); !errors.Is(err, ErrInvalidShortKey) {
			t.Errorf("NewShortKey(%q): expected ErrInvalidShortKey, got %v", s, err)
		}
	}
}

// TestShortKey_CaseSensitive proves BR-002's "comparisons preserve
// case" invariant: two keys differing only in case are distinct
// values, not the same key under a different spelling.
func TestShortKey_CaseSensitive(t *testing.T) {
	lower, err := NewShortKey("abc1234")
	if err != nil {
		t.Fatalf("NewShortKey(lower): %v", err)
	}
	upper, err := NewShortKey("ABC1234")
	if err != nil {
		t.Fatalf("NewShortKey(upper): %v", err)
	}
	if lower == upper {
		t.Fatalf("expected %q and %q to be distinct ShortKey values, got equal", lower, upper)
	}
	if lower.String() == upper.String() {
		t.Fatalf("expected distinct String() output, got %q for both", lower.String())
	}
}

// TestShortKey_ZeroValueNotConstructible proves the zero value is never
// a real, validated ShortKey — it can only arise from a failed
// NewShortKey call, and its String() is empty, not a string that could
// ever pass NewShortKey itself (empty fails the length check).
func TestShortKey_ZeroValueNotConstructible(t *testing.T) {
	var zero ShortKey
	if _, err := NewShortKey(zero.String()); !errors.Is(err, ErrInvalidShortKey) {
		t.Fatalf("expected the zero ShortKey's own String() to be rejected by NewShortKey, got err=%v", err)
	}
}
