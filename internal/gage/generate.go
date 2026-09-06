package gage

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// GenerateDefaultLength is gage generate's value length when -l isn't
// given. Exported so `generate --help` can state the default without
// cmd/gage carrying a second copy of the number to drift from.
const GenerateDefaultLength = 24

// GenerateMinLength is the shortest value `generate -l` will produce.
//
// The floor exists because -l is the one knob on this command that can
// make its output worse, and silently obliging `-l 4` would hand back
// something the tool's whole purpose is to prevent. Eight is the point
// below which even a full-alphabet draw stops being meaningfully more
// than a delay: 8 characters of the 74-character alphabet is ~49 bits,
// and every length below it is within reach of an offline attack that
// 12 or 24 is not. It is a refusal rather than a clamp — silently
// rounding 4 up to 8 would tell the human they got what they asked for.
const GenerateMinLength = 8

// generateLetters/generateDigits/generateSymbols compose the two
// alphabets. The symbol set is deliberately narrow: characters unlikely
// to need escaping wherever the value ends up pasted — a shell, a URL, a
// CSV — rather than every punctuation mark ASCII has.
const (
	generateLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	generateDigits  = "0123456789"
	generateSymbols = "!@#$%^&*-_=+"
)

const (
	generateAlphabet         = generateLetters + generateDigits + generateSymbols
	generateAlphabetNoSymbol = generateLetters + generateDigits
)

// GenerateOptions is what `gage generate`'s -l/--no-symbols become by
// the time they reach the library: a value, not two more parameters on
// every call, so a third knob is additive rather than a signature
// change reaching every caller.
type GenerateOptions struct {
	// Length is the number of characters to draw. Zero means "not
	// specified" and takes GenerateDefaultLength — the flag's unset
	// state has to be distinguishable from a request, and "generate zero
	// characters" is not a thing any caller wants.
	Length int

	// NoSymbols restricts the draw to letters and digits, for the
	// systems that still reject punctuation in a password.
	NoSymbols bool
}

// alphabet picks the character set opts asks for.
func (o GenerateOptions) alphabet() string {
	if o.NoSymbols {
		return generateAlphabetNoSymbol
	}
	return generateAlphabet
}

// length resolves the requested length, filling in the default.
func (o GenerateOptions) length() int {
	if o.Length == 0 {
		return GenerateDefaultLength
	}
	return o.Length
}

// GenerateValue returns a fresh secret drawn from crypto/rand, shaped by
// opts. A length below GenerateMinLength is refused rather than obliged.
func GenerateValue(opts GenerateOptions) (string, error) {
	if opts.Length != 0 && opts.Length < GenerateMinLength {
		return "", exitcode.Newf(exitcode.Usage,
			"gage: generate: length %d is too short; the minimum is %d", opts.Length, GenerateMinLength)
	}
	return randomString(rand.Reader, opts.length(), opts.alphabet())
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
