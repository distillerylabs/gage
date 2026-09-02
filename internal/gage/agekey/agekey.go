// Package agekey validates the shape of an age public key — an
// "age1..." Bech32 string (BIP-173) — without performing any
// cryptographic operation. M1 has no crypto at all (see the M1 plan's
// "Why no crypto here"): this exists so a malformed --recipient fails at
// the CLI boundary, at init time, rather than surfacing as a confusing
// encrypt failure in M3.
package agekey

import (
	"fmt"
	"strings"
)

// charset is Bech32's 32-character data alphabet.
const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// maxLength is Bech32's own string-length cap (BIP-173).
const maxLength = 90

// checksumConst is the target polymod value for standard Bech32 (as
// opposed to Bech32m, whose constant differs). age recipients use
// standard Bech32.
const checksumConst = 1

// ValidateRecipient reports whether s is a well-formed age recipient
// string: a valid Bech32 encoding (correct charset, single case, correct
// checksum) whose human-readable part starts with "age" — covering both
// a plain X25519 recipient (hrp "age") and a plugin recipient like
// age-plugin-yubikey's (hrp "age1yubikey", per Bech32's own rule that
// the hrp/data split is just "everything before the last '1'"). It
// checks shape only — it never decodes key material, since M1 does no
// crypto at all.
func ValidateRecipient(s string) error {
	if len(s) < 8 || len(s) > maxLength {
		return fmt.Errorf("agekey: %q is not a valid age recipient: wrong length", s)
	}
	if strings.ToLower(s) != s && strings.ToUpper(s) != s {
		return fmt.Errorf("agekey: %q is not a valid age recipient: mixed case", s)
	}
	lower := strings.ToLower(s)

	sep := strings.LastIndexByte(lower, '1')
	if sep < 1 || sep+7 > len(lower) {
		return fmt.Errorf("agekey: %q is not a valid age recipient: missing separator or data too short", s)
	}
	hrp := lower[:sep]
	dataPart := lower[sep+1:]

	if !strings.HasPrefix(hrp, "age") {
		return fmt.Errorf("agekey: %q is not a valid age recipient: human-readable part %q does not start with \"age\"", s, hrp)
	}
	if !isPrintableASCII(hrp) {
		return fmt.Errorf("agekey: %q is not a valid age recipient: human-readable part contains non-printable characters", s)
	}

	values := make([]int, len(dataPart))
	for i, c := range dataPart {
		idx := strings.IndexRune(charset, c)
		if idx < 0 {
			return fmt.Errorf("agekey: %q is not a valid age recipient: character %q is not in the Bech32 charset", s, c)
		}
		values[i] = idx
	}

	if !verifyChecksum(hrp, values) {
		return fmt.Errorf("agekey: %q is not a valid age recipient: checksum mismatch", s)
	}
	return nil
}

func isPrintableASCII(s string) bool {
	for _, r := range s {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

func polymod(values []int) int {
	generator := [5]int{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := 1
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ v
		for i := 0; i < 5; i++ {
			if (top>>i)&1 == 1 {
				chk ^= generator[i]
			}
		}
	}
	return chk
}

func hrpExpand(hrp string) []int {
	ret := make([]int, 0, len(hrp)*2+1)
	for _, c := range hrp {
		ret = append(ret, int(c)>>5)
	}
	ret = append(ret, 0)
	for _, c := range hrp {
		ret = append(ret, int(c)&31)
	}
	return ret
}

func verifyChecksum(hrp string, data []int) bool {
	values := append(hrpExpand(hrp), data...)
	return polymod(values) == checksumConst
}
