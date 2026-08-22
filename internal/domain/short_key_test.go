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

// TestShortKey_ExhaustiveCasePreservation is T8-01's "supported
// edge/client corpus preserves keys" evidence, exhaustive rather than
// the single a/A pair TestShortKey_CaseSensitive already checks: for
// every one of the 26 letters, the lowercase and uppercase single-char
// keys must be distinct ShortKey values. NFR-COMP-001/DEC-010 requires
// this to hold for every supported client/intermediary — proving it
// holds for every letter position in this package's own type is the
// part of that guarantee this repository actually controls; DEC-010's
// remaining browser/proxy/CDN chain is out of this package's reach.
func TestShortKey_ExhaustiveCasePreservation(t *testing.T) {
	for c := 'a'; c <= 'z'; c++ {
		lower, err := NewShortKey(string(c))
		if err != nil {
			t.Fatalf("NewShortKey(%q): unexpected error: %v", string(c), err)
		}
		upperChar := c - 'a' + 'A'
		upper, err := NewShortKey(string(upperChar))
		if err != nil {
			t.Fatalf("NewShortKey(%q): unexpected error: %v", string(upperChar), err)
		}
		if lower == upper {
			t.Errorf("expected %q and %q to be distinct ShortKey values, got equal", lower, upper)
		}
	}
}

// TestNewShortKey_ClientEdgeCaseCorpus covers realistic client-side
// mistakes and edge inputs (accidental whitespace from copy-paste, a
// visually-confusable-but-genuinely-different alphabet mix, embedded
// control characters) that a real supported client or intermediary
// could plausibly produce, beyond the basic invalid-character corpus
// TestNewShortKey_InvalidCases already covers.
func TestNewShortKey_ClientEdgeCaseCorpus(t *testing.T) {
	invalid := []string{
		" abc1234",    // leading whitespace (accidental copy-paste)
		"abc1234 ",    // trailing whitespace
		"   ",         // whitespace only
		"abc\t1234",   // embedded tab
		"abc\n1234",   // embedded newline
		"abc\r\n1234", // embedded CRLF
		"%6b1",        // a percent-encoded-looking string that was never actually decoded
		"abc1234\x00", // embedded NUL
	}
	for _, s := range invalid {
		if _, err := NewShortKey(s); !errors.Is(err, ErrInvalidShortKey) {
			t.Errorf("NewShortKey(%q): expected ErrInvalidShortKey, got %v", s, err)
		}
	}

	// "l1O0I" mixes visually similar-looking characters (lowercase L,
	// digit one, uppercase O, digit zero, uppercase I) that are all
	// distinct, valid alphabet members — DEC-012 accepts this
	// visual-confusability risk rather than normalizing between them;
	// the parser's job is only to accept it as one exact 5-character
	// key, not to second-guess or normalize it.
	if _, err := NewShortKey("l1O0I"); err != nil {
		t.Errorf("NewShortKey(%q): expected success (valid alphabet, just visually confusable), got %v", "l1O0I", err)
	}
}
