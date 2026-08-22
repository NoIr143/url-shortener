package base62

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// confirmedAlphabet mirrors base62.go's own alphabet constant exactly —
// kept as a separate literal here (not exported from the package) so
// these tests independently re-derive the expected mapping rather than
// trusting the implementation's own constant.
const confirmedAlphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []int64{0, 1, 61, 62, 63, 3844, 1_000_000, 3_521_614_606_207, math.MaxInt64}
	for _, id := range cases {
		encoded, err := Encode(id)
		if err != nil {
			t.Fatalf("Encode(%d) unexpected error: %v", id, err)
		}
		decoded, err := Decode(encoded)
		if err != nil {
			t.Fatalf("Decode(%q) unexpected error: %v", encoded, err)
		}
		if decoded != id {
			t.Errorf("round trip mismatch: id=%d encoded=%q decoded=%d", id, encoded, decoded)
		}
	}
}

func TestCaseSensitivity(t *testing.T) {
	// "a" (index 10) and "A" (index 36) must decode to different values,
	// per NFR-COMP-001's case-preservation requirement.
	lower, err := Decode("a")
	if err != nil {
		t.Fatal(err)
	}
	upper, err := Decode("A")
	if err != nil {
		t.Fatal(err)
	}
	if lower == upper {
		t.Fatalf("expected case-sensitive decode, got equal values: a=%d A=%d", lower, upper)
	}
}

func TestEncodeNegativeRejected(t *testing.T) {
	if _, err := Encode(-1); err != ErrNegative {
		t.Fatalf("expected ErrNegative, got %v", err)
	}
}

func TestDecodeInvalidCharRejected(t *testing.T) {
	if _, err := Decode("has space"); err != ErrInvalidChar {
		t.Fatalf("expected ErrInvalidChar, got %v", err)
	}
	if _, err := Decode("has-dash"); err != ErrInvalidChar {
		t.Fatalf("expected ErrInvalidChar, got %v", err)
	}
}

func TestSevenCharacterCapacity(t *testing.T) {
	// docs/SRS.md: seven Base62 positions provide ~3.52 trillion combinations.
	maxSevenChar := int64(math.Pow(62, 7)) - 1
	encoded, err := Encode(maxSevenChar)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 7 {
		t.Errorf("expected at most 7 characters for id=%d, got %q (%d chars)", maxSevenChar, encoded, len(encoded))
	}
}

// TestAlphabetExhaustive proves the full 62-character alphabet bijection
// (BR-002/DEC-012), not just a handful of sample points: every single
// digit 0..61 encodes to exactly the alphabet character at that index,
// and decoding that character returns exactly that digit back.
func TestAlphabetExhaustive(t *testing.T) {
	if len(confirmedAlphabet) != 62 {
		t.Fatalf("test setup bug: expected 62 confirmedAlphabet characters, got %d", len(confirmedAlphabet))
	}
	for i := 0; i < len(confirmedAlphabet); i++ {
		want := string(confirmedAlphabet[i])

		got, err := Encode(int64(i))
		if err != nil {
			t.Fatalf("Encode(%d): unexpected error: %v", i, err)
		}
		if got != want {
			t.Errorf("Encode(%d) = %q, want %q", i, got, want)
		}

		decoded, err := Decode(want)
		if err != nil {
			t.Fatalf("Decode(%q): unexpected error: %v", want, err)
		}
		if decoded != int64(i) {
			t.Errorf("Decode(%q) = %d, want %d", want, decoded, i)
		}
	}
}

// TestDecode_RejectsEveryByteOutsideAlphabet exhaustively covers the
// negative space too: every one of the 256 possible byte values that is
// not one of the 62 confirmed alphabet characters must be rejected with
// ErrInvalidChar — not just the two illustrative examples (space, dash)
// TestDecodeInvalidCharRejected already checks.
func TestDecode_RejectsEveryByteOutsideAlphabet(t *testing.T) {
	inAlphabet := make(map[byte]bool, len(confirmedAlphabet))
	for i := 0; i < len(confirmedAlphabet); i++ {
		inAlphabet[confirmedAlphabet[i]] = true
	}
	for b := 0; b < 256; b++ {
		if inAlphabet[byte(b)] {
			continue
		}
		s := string([]byte{byte(b)})
		if _, err := Decode(s); !errors.Is(err, ErrInvalidChar) {
			t.Errorf("Decode(%q) [byte 0x%02x]: expected ErrInvalidChar, got %v", s, b, err)
		}
	}
}

// FuzzEncodeDecodeRoundTrip is the "property test" T7-01 names: for any
// non-negative int64 (not just the hand-picked cases in
// TestEncodeDecodeRoundTrip), Encode followed by Decode must return the
// exact original value. Also satisfies docs/TECH_STACK.md Section 6's
// general "fuzz tests" requirement — the first in this repository.
func FuzzEncodeDecodeRoundTrip(f *testing.F) {
	for _, seed := range []int64{0, 1, 61, 62, 63, 3844, 1_000_000, 3_521_614_606_207, math.MaxInt64} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, id int64) {
		if id < 0 {
			t.Skip() // Encode's documented negative-rejection behavior is TestEncodeNegativeRejected's job.
		}
		encoded, err := Encode(id)
		if err != nil {
			t.Fatalf("Encode(%d): unexpected error: %v", id, err)
		}
		for _, r := range encoded {
			if !strings.ContainsRune(confirmedAlphabet, r) {
				t.Fatalf("Encode(%d) = %q contains a character outside the confirmed alphabet: %q", id, encoded, r)
			}
		}
		decoded, err := Decode(encoded)
		if err != nil {
			t.Fatalf("Decode(%q): unexpected error: %v", encoded, err)
		}
		if decoded != id {
			t.Fatalf("round-trip mismatch: id=%d encoded=%q decoded=%d", id, encoded, decoded)
		}
	})
}
