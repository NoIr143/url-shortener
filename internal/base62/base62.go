// Package base62 implements the case-sensitive alphabet confirmed in
// AGENTS.md ("0-9", "a-z", "A-Z") used to encode leased numeric IDs into
// short keys, per docs/decisions/DEC-012.md.
package base62

import (
	"errors"
	"strings"
)

const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

const base = int64(len(alphabet))

// ErrNegative is returned when Encode is called with a negative ID.
var ErrNegative = errors.New("base62: cannot encode a negative id")

// ErrInvalidChar is returned when Decode encounters a character outside
// the confirmed alphabet.
var ErrInvalidChar = errors.New("base62: invalid character")

// Encode converts a non-negative numeric ID into its Base62 representation
// using the confirmed alphabet. It does not pad to a fixed width.
func Encode(id int64) (string, error) {
	if id < 0 {
		return "", ErrNegative
	}
	if id == 0 {
		return string(alphabet[0]), nil
	}

	var b strings.Builder
	for id > 0 {
		remainder := id % base
		b.WriteByte(alphabet[remainder])
		id /= base
	}

	// digits were generated least-significant first; reverse them.
	encoded := []byte(b.String())
	for i, j := 0, len(encoded)-1; i < j; i, j = i+1, j-1 {
		encoded[i], encoded[j] = encoded[j], encoded[i]
	}
	return string(encoded), nil
}

// Decode converts a Base62 string back into its numeric ID. It is the
// exact inverse of Encode and is case-sensitive: "aB1" and "ab1" decode
// to different values, matching NFR-COMP-001's case-preservation
// requirement.
func Decode(key string) (int64, error) {
	if key == "" {
		return 0, ErrInvalidChar
	}
	var id int64
	for _, r := range key {
		idx := strings.IndexRune(alphabet, r)
		if idx < 0 {
			return 0, ErrInvalidChar
		}
		id = id*base + int64(idx)
	}
	return id, nil
}
