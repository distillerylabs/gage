package gage

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// generateDefaultLength is gage generate's value length until M12 adds
// -l. generateAlphabet is its character set until M12 adds --no-symbols:
// upper/lower letters, digits, and a set of symbols unlikely to need
// escaping wherever the value ends up pasted.
const generateDefaultLength = 24

const generateAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*-_=+"

// GenerateValue returns a fresh secret drawn from crypto/rand:
// generateDefaultLength characters from generateAlphabet. This is the one
// shape `gage generate` produces until M12's -l/--no-symbols land.
func GenerateValue() (string, error) {
	return randomString(rand.Reader, generateDefaultLength, generateAlphabet)
}

// randomString is GenerateValue's pure core: the randomness source and
// output shape are parameters so a test can substitute a deterministic
// reader and assert the draw is structural — every output character
// traceable to bytes the reader actually produced — rather than
// asserting anything statistical about real randomness. rand.Int is what
// keeps the draw unbiased: a naive `b % len(alphabet)` would skew toward
// low indices whenever len(alphabet) doesn't evenly divide 256.
func randomString(rnd io.Reader, length int, alphabet string) (string, error) {
	if length <= 0 {
		return "", exitcode.New(exitcode.Internal, "gage: generate: length must be positive")
	}
	if len(alphabet) == 0 {
		return "", exitcode.New(exitcode.Internal, "gage: generate: empty alphabet")
	}

	max := big.NewInt(int64(len(alphabet)))
	out := make([]byte, length)
	for i := range out {
		n, err := rand.Int(rnd, max)
		if err != nil {
			return "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: generating random value: %w", err))
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}
