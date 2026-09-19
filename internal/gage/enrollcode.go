package gage

// The enrollment code: generation, and the normalization every typed
// spelling is put through before it is used as a passphrase. See
// D-ENROLL-CODE-FORMAT.

import (
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// crockfordAlphabet is Crockford's base32 encoding alphabet: the digits
// and the uppercase letters, minus I, L, O and U.
//
// The first three are excluded because they are what gets confused with
// 1, 1 and 0 in handwriting and over a phone; U is excluded so the
// generator cannot spell an unfortunate word by accident. Decoding maps
// the first three back rather than rejecting them (see
// normalizeEnrollmentCode) — U has nothing to map to and stays invalid.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// enrollmentCodeLength is how many Crockford characters a code carries.
// Sixteen of them is 80 bits, which is the number D-ENROLL-SEAL-COST's
// low seal work factor is licensed by — the two move together or not at
// all.
const enrollmentCodeLength = 16

// enrollmentCodeGroup is how many characters appear between hyphens in
// the displayed form. Purely cosmetic: normalization strips the hyphens
// before anything looks at the value.
const enrollmentCodeGroup = 4

// enrollmentCodePrefix is the cosmetic marker the display form carries.
// It earns its place by making the string recognizable when it turns up
// somewhere it shouldn't — a chat log, a screenshot, a terminal
// recording — which is a reminder that it does not belong there.
const enrollmentCodePrefix = "GAGE-"

// newEnrollmentCode generates one enrollment code in its display form,
// GAGE-XXXX-XXXX-XXXX-XXXX.
//
// The entropy is the security parameter here, not the KDF behind it (see
// "The reframe"): the sealed blob sits in a git repository every reader
// of the vault can fetch, which makes it an offline, unlimited-guess
// target. So the code is generated rather than chosen, from crypto/rand,
// and it is 80 bits.
//
// Ten random bytes are consumed for sixteen five-bit groups, which is
// exactly 80 bits with nothing discarded — a uniform draw over the
// alphabet without rejection sampling, since 32 divides a five-bit group
// exactly.
func newEnrollmentCode() (string, error) {
	const bits = enrollmentCodeLength * 5
	raw := make([]byte, bits/8)
	if _, err := rand.Read(raw); err != nil {
		return "", exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: generating an enrollment code: %w", err))
	}

	var b strings.Builder
	b.WriteString(enrollmentCodePrefix)
	for i := 0; i < enrollmentCodeLength; i++ {
		if i > 0 && i%enrollmentCodeGroup == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(crockfordAlphabet[fiveBitsAt(raw, i)])
	}
	return b.String(), nil
}

// fiveBitsAt returns the i'th five-bit group of raw, most significant
// bits first, as a value in [0, 32).
func fiveBitsAt(raw []byte, i int) int {
	start := i * 5
	var v int
	for bit := 0; bit < 5; bit++ {
		pos := start + bit
		set := raw[pos/8]&(1<<(7-pos%8)) != 0
		v <<= 1
		if set {
			v |= 1
		}
	}
	return v
}

// normalizeEnrollmentCode turns a typed code into the canonical 16
// characters the seal was written with, reporting false for anything
// that cannot be one.
//
// It absorbs how people actually retype a code — lowercase, spaces where
// the hyphens were, a dropped GAGE- prefix — and it *maps* the three
// ambiguous letters (I and L to 1, O to 0) rather than rejecting them,
// because a code containing one of them is a transcription of a code
// gage generated rather than a different code.
//
// Its second job is to make a typo cost nothing. A code failing length or
// alphabet validation is refused here, before any decryption is
// attempted, so a mistyped character does not buy N scrypt runs across N
// pending requests. What that buys is cost, not a different answer: the
// caller reports the same ErrEnrollmentCodeWrong either way, deliberately
// (see its comment).
func normalizeEnrollmentCode(typed string) (string, bool) {
	var b strings.Builder
	b.Grow(len(typed))
	for _, r := range strings.ToUpper(strings.TrimSpace(typed)) {
		switch r {
		case '-', ' ':
			// Separators, in either the form gage prints or the form a
			// phone keyboard produces. Dropped rather than treated as
			// positional: a code retyped with the groups in the wrong
			// places is still the same code.
			continue
		case 'I', 'L':
			b.WriteByte('1')
		case 'O':
			b.WriteByte('0')
		default:
			if !strings.ContainsRune(crockfordAlphabet, r) {
				return "", false
			}
			b.WriteRune(r)
		}
	}

	// The prefix is stripped after the separators rather than before,
	// because it arrives attached by a hyphen, by a space, or by nothing
	// at all depending on how the code was retyped. Length is what makes
	// that unambiguous: every letter of "GAGE" is also a legal code
	// character, so a leading "GAGE" is only a prefix when the string is
	// four characters too long without it.
	out := b.String()
	if len(out) == enrollmentCodeLength+len("GAGE") {
		out = strings.TrimPrefix(out, "GAGE")
	}
	if len(out) != enrollmentCodeLength {
		return "", false
	}
	return out, true
}
