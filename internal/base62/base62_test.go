package base62

import (
	"math"
	"testing"
)

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
