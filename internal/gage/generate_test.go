package gage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// TestRandomStringDrawIsStructural substitutes a fully deterministic
// reader for crypto/rand.Reader and checks that the output is actually
// derived from the bytes that reader produced — every character traces
// back to a byte the reader returned — rather than asserting anything
// statistical about real randomness (which the M5 plan calls for: "drawn
// from a cryptographically secure source, asserted structurally rather
// than statistically").
//
// Concretely: two different deterministic byte streams must produce two
// different outputs. If randomString ignored rnd (e.g. always picked
// alphabet[0]), this would fail.
func TestRandomStringDrawIsStructural(t *testing.T) {
	const alphabet = "0123456789abcdef"
	const length = 16

	streamA := bytes.Repeat([]byte{0x00}, 4*length) // plenty of bytes for rand.Int's rejection sampling
	streamB := bytes.Repeat([]byte{0xFF}, 4*length)

	gotA, err := randomString(bytes.NewReader(streamA), length, alphabet)
	if err != nil {
		t.Fatalf("randomString: %v", err)
	}
	gotB, err := randomString(bytes.NewReader(streamB), length, alphabet)
	if err != nil {
		t.Fatalf("randomString: %v", err)
	}

	if len(gotA) != length || len(gotB) != length {
		t.Fatalf("lengths = %d, %d, want both %d", len(gotA), len(gotB), length)
	}
	if gotA == gotB {
		t.Error("two different randomness streams produced identical output — the draw isn't actually using rnd")
	}
	for _, s := range []string{gotA, gotB} {
		for _, r := range s {
			if !strings.ContainsRune(alphabet, r) {
				t.Errorf("output %q contains %q, which isn't in the alphabet %q", s, r, alphabet)
			}
		}
	}
}

// errReader always fails, so randomString must propagate the failure
// rather than silently falling back to something else — this is the
// other half of "structural": the source is actually consulted, not
// merely accepted as a parameter and ignored.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestRandomStringPropagatesReaderFailure(t *testing.T) {
	wantErr := errors.New("boom")
	_, err := randomString(errReader{wantErr}, 8, "ab")
	if err == nil {
		t.Fatal("expected an error when the randomness source fails")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestGenerateValueLengthAndAlphabet exercises the real crypto/rand path
// GenerateValue uses in production.
func TestGenerateValueLengthAndAlphabet(t *testing.T) {
	got, err := GenerateValue(GenerateOptions{})
	if err != nil {
		t.Fatalf("GenerateValue: %v", err)
	}
	if len(got) != GenerateDefaultLength {
		t.Errorf("length = %d, want %d", len(got), GenerateDefaultLength)
	}
	for _, r := range got {
		if !strings.ContainsRune(generateAlphabet, r) {
			t.Errorf("value %q contains %q, which isn't in generateAlphabet", got, r)
		}
	}
}

// TestGenerateValueDiffersAcrossCalls is a minimal sanity check (not a
// statistical randomness test): two real calls should essentially never
// produce the same 24-character value.
func TestGenerateValueDiffersAcrossCalls(t *testing.T) {
	a, err := GenerateValue(GenerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateValue(GenerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two consecutive GenerateValue calls produced the same value")
	}
}

// TestGenerateDrawsFromCryptoRandNotMathRand is the structural half of
// the "cryptographically secure source" bullet, and it reads the source
// file to get it.
//
// That is deliberate. Every behavioral property the other tests here
// assert — exact length, characters drawn from the alphabet, two calls
// differing, an injected reader being consulted — holds just as well for
// a math/rand implementation, so none of them can tell a secure source
// from an insecure one. What actually distinguishes them is which
// package the production path draws from, and this asserts exactly that.
// The same technique guards the Makefile's -X paths in cmd/gage (see
// TestMakefileLdflagsTargetTheRealSymbols) for the same reason: some
// properties are only observable in the source.
//
// math/big is expected and allowed — it's rand.Int's counterpart, not a
// randomness source.
func TestGenerateDrawsFromCryptoRandNotMathRand(t *testing.T) {
	data, err := os.ReadFile("generate.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	if !strings.Contains(src, `"crypto/rand"`) {
		t.Error("generate.go does not import crypto/rand; gage generate must draw from a cryptographically secure source")
	}
	if strings.Contains(src, `"math/rand"`) || strings.Contains(src, `"math/rand/v2"`) {
		t.Error("generate.go imports math/rand; a generated secret must never come from a non-cryptographic PRNG")
	}
	// rand.Int (crypto/rand's rejection-sampling helper) rather than a
	// modulo of raw bytes, which would bias the draw toward the front of
	// the alphabet.
	if !strings.Contains(src, "rand.Int(") {
		t.Error("generate.go does not use rand.Int; a modulo-based draw over a 74-character alphabet is biased")
	}
}

var _ io.Reader = errReader{} // errReader must satisfy io.Reader for randomString's signature

// TestGenerateValueHonoursLength: -l LENGTH reaches the draw. Checked
// across the whole accepted range rather than at one point, since the
// length is the only thing standing between `generate` and a weak
// secret.
func TestGenerateValueHonoursLength(t *testing.T) {
	for _, n := range []int{GenerateMinLength, 12, 24, 64, 200} {
		got, err := GenerateValue(GenerateOptions{Length: n})
		if err != nil {
			t.Fatalf("GenerateValue(length %d): %v", n, err)
		}
		if len(got) != n {
			t.Errorf("GenerateValue(length %d) produced %d characters", n, len(got))
		}
	}
}

// TestGenerateValueZeroLengthUsesTheDefault: a zero Length means "not
// specified", not "zero characters" — the flag's unset state has to be
// distinguishable from a request, and a zero-length secret is exactly
// what must never be produced.
func TestGenerateValueZeroLengthUsesTheDefault(t *testing.T) {
	got, err := GenerateValue(GenerateOptions{})
	if err != nil {
		t.Fatalf("GenerateValue: %v", err)
	}
	if len(got) != GenerateDefaultLength {
		t.Errorf("GenerateValue with no length = %d characters, want the default %d", len(got), GenerateDefaultLength)
	}
}

// TestGenerateValueNoSymbolsExcludesSymbols: --no-symbols restricts the
// alphabet to letters and digits, for the systems that reject
// punctuation in a password.
//
// Drawn repeatedly rather than once: a single 24-character draw from the
// full alphabet omits symbols by chance often enough that a one-shot
// check would pass against a --no-symbols flag that did nothing at all.
func TestGenerateValueNoSymbolsExcludesSymbols(t *testing.T) {
	for i := 0; i < 50; i++ {
		got, err := GenerateValue(GenerateOptions{NoSymbols: true})
		if err != nil {
			t.Fatalf("GenerateValue: %v", err)
		}
		for _, r := range got {
			isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !isAlnum {
				t.Fatalf("--no-symbols value %q contains non-alphanumeric %q", got, r)
			}
		}
	}
}

// TestGenerateValueStillDrawsSymbolsByDefault is the other half of the
// test above: it fails if the alphabet lost its symbols outright rather
// than only under --no-symbols. 50 draws of 24 characters from a
// 74-character alphabet with 14 symbols miss symbols entirely only with
// probability (60/74)^1200, so a flake here is not a real possibility.
func TestGenerateValueStillDrawsSymbolsByDefault(t *testing.T) {
	for i := 0; i < 50; i++ {
		got, err := GenerateValue(GenerateOptions{})
		if err != nil {
			t.Fatalf("GenerateValue: %v", err)
		}
		if strings.ContainsAny(got, generateSymbols) {
			return
		}
	}
	t.Error("50 default generate draws contained no symbol at all; the default alphabet has lost them")
}

// TestGenerateValueRejectsTooShortLength: `generate -l 4` must fail
// rather than produce a trivially weak secret. The floor is a usage
// error — the human asked for something gage won't do — not an internal
// one.
func TestGenerateValueRejectsTooShortLength(t *testing.T) {
	for _, n := range []int{-5, 1, GenerateMinLength - 1} {
		_, err := GenerateValue(GenerateOptions{Length: n})
		if err == nil {
			t.Fatalf("GenerateValue(length %d) succeeded, want a rejection", n)
		}
		if got := exitcode.CodeOf(err); got != exitcode.Usage {
			t.Errorf("GenerateValue(length %d) exit code = %v, want %v", n, got, exitcode.Usage)
		}
		if !strings.Contains(err.Error(), fmt.Sprint(GenerateMinLength)) {
			t.Errorf("rejection %q does not say what the minimum is (%d)", err, GenerateMinLength)
		}
	}
}
