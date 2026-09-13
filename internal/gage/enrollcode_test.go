package gage

import (
	"strings"
	"testing"
)

// generateCode is newEnrollmentCode with the error handling out of the
// way — a failure here is crypto/rand failing, which is not a case any
// test is about.
func generateCode(t *testing.T) string {
	t.Helper()
	code, err := newEnrollmentCode()
	if err != nil {
		t.Fatalf("generating an enrollment code: %v", err)
	}
	return code
}

// TestGeneratedCodeHasTheDocumentedShape pins D-ENROLL-CODE-FORMAT's
// rendering: a cosmetic GAGE- prefix over four hyphenated groups of four
// Crockford characters.
//
// The shape is worth a test of its own because it is what a human copies
// off one screen and types into another; the entropy behind it is
// asserted separately below.
func TestGeneratedCodeHasTheDocumentedShape(t *testing.T) {
	code := generateCode(t)

	rest, ok := strings.CutPrefix(code, "GAGE-")
	if !ok {
		t.Fatalf("generated code %q does not start with the GAGE- prefix", code)
	}
	groups := strings.Split(rest, "-")
	if len(groups) != 4 {
		t.Fatalf("generated code %q has %d groups, want 4", code, len(groups))
	}
	for _, g := range groups {
		if len(g) != 4 {
			t.Errorf("generated code %q has a group of %d characters, want 4", code, len(g))
		}
	}
	if got := len(rest) - 3; got != enrollmentCodeLength {
		t.Errorf("generated code %q carries %d characters, want %d (%d bits)",
			code, got, enrollmentCodeLength, enrollmentCodeLength*5)
	}
}

// TestGeneratedCodesAvoidTheAmbiguousLetters is the alphabet half of
// D-ENROLL-CODE-FORMAT. Crockford excludes I, L, O and U so that the
// characters most often confused in handwriting or dictated over a phone
// never appear at all — and excluding U additionally keeps the generator
// from spelling unfortunate words by accident.
//
// Asserted over many generations rather than one: a single code
// containing none of them proves nothing, since any given code probably
// contains none of them by chance.
func TestGeneratedCodesAvoidTheAmbiguousLetters(t *testing.T) {
	const generations = 500
	for i := 0; i < generations; i++ {
		code := generateCode(t)
		if strings.ContainsAny(code, "ILOU") {
			t.Fatalf("generated code %q contains one of the excluded letters I, L, O, U", code)
		}
		if strings.ToUpper(code) != code {
			t.Fatalf("generated code %q is not uppercase", code)
		}
	}
}

// TestSuccessiveCodesDiffer is the entropy smoke test: a generator wired
// to a fixed seed, a counter, or the clock's seconds would satisfy every
// other assertion in this file.
//
// It samples rather than comparing two, so that a generator with a very
// short period is caught as well as one with none. At 80 bits a genuine
// collision here is not a thing that happens.
func TestSuccessiveCodesDiffer(t *testing.T) {
	const generations = 200
	seen := make(map[string]bool, generations)
	for i := 0; i < generations; i++ {
		code := generateCode(t)
		if seen[code] {
			t.Fatalf("generated the same code twice in %d attempts (%q): the source is not crypto/rand",
				generations, code)
		}
		seen[code] = true
	}
}

// TestCodeNormalizationAcceptsHowPeopleRetypeCodes covers the
// transcription slips D-ENROLL-CODE-FORMAT says normalization must
// absorb: lowercase, spaces where the hyphens were, and a dropped GAGE-
// prefix all have to reach the same 16 characters the seal was written
// with.
//
// It asserts against the canonical form rather than against "it opened
// something", so a failure points at the normalizer rather than at the
// seal. That the same spellings really do open a sealed request is
// asserted end-to-end in the seal tests.
func TestCodeNormalizationAcceptsHowPeopleRetypeCodes(t *testing.T) {
	const code = "GAGE-7K4M-9QX2-P3RH-8WVN"
	const canonical = "7K4M9QX2P3RH8WVN"

	for _, tc := range []struct {
		name  string
		typed string
	}{
		{"as displayed", code},
		{"lowercase", "gage-7k4m-9qx2-p3rh-8wvn"},
		{"spaces for hyphens", "GAGE 7K4M 9QX2 P3RH 8WVN"},
		{"no prefix", "7K4M-9QX2-P3RH-8WVN"},
		{"no prefix, lowercase, spaces", "7k4m 9qx2 p3rh 8wvn"},
		{"no separators at all", "7K4M9QX2P3RH8WVN"},
		{"surrounding whitespace", "  GAGE-7K4M-9QX2-P3RH-8WVN\n"},
		{"lowercase prefix only", "gage-7K4M9QX2P3RH8WVN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeEnrollmentCode(tc.typed)
			if !ok {
				t.Fatalf("normalizeEnrollmentCode(%q) rejected a code that should normalize", tc.typed)
			}
			if got != canonical {
				t.Errorf("normalizeEnrollmentCode(%q) = %q, want %q", tc.typed, got, canonical)
			}
		})
	}
}

// TestCodeNormalizationMapsCrockfordSubstitutions is the half of
// D-ENROLL-CODE-FORMAT that corrects rather than rejects. The alphabet
// excludes I, L and O so a generated code never contains them — but a
// human reading one back off paper types them anyway, for 1 and 0. The
// decoder maps them rather than refusing, so the ambiguity that survives
// a bad transcription is fixed instead of costing another round trip.
func TestCodeNormalizationMapsCrockfordSubstitutions(t *testing.T) {
	for _, tc := range []struct {
		typed string
		want  string
	}{
		{"1234567890ABCDEF", "1234567890ABCDEF"},
		{"I234567890ABCDEF", "1234567890ABCDEF"},
		{"L234567890ABCDEF", "1234567890ABCDEF"},
		{"i234567890abcdef", "1234567890ABCDEF"},
		{"l234567890abcdef", "1234567890ABCDEF"},
		{"123456789OABCDEF", "1234567890ABCDEF"},
		{"123456789oabcdef", "1234567890ABCDEF"},
		{"GAGE-IL34-5678-90AB-CDEF", "1134567890ABCDEF"},
	} {
		got, ok := normalizeEnrollmentCode(tc.typed)
		if !ok {
			t.Errorf("normalizeEnrollmentCode(%q) rejected a code Crockford decoding accepts", tc.typed)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeEnrollmentCode(%q) = %q, want %q", tc.typed, got, tc.want)
		}
	}
}

// TestCodeValidationRejectsWrongLengthAndAlphabet is what makes a typo
// cost nothing: a code that cannot possibly be one of gage's is refused
// on its own, before any scrypt run against any pending request.
//
// U is in the list deliberately. It is the one excluded letter Crockford
// does *not* map to anything, so a code containing it is a code gage
// never generated and cannot repair.
func TestCodeValidationRejectsWrongLengthAndAlphabet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typed string
	}{
		{"empty", ""},
		{"prefix only", "GAGE-"},
		{"too short", "7K4M9QX2P3RH8WV"},
		{"too long", "7K4M9QX2P3RH8WVNX"},
		{"U is not mapped", "7K4M9QX2P3RH8WVU"},
		{"punctuation", "7K4M9QX2P3RH8WV!"},
		{"non-ascii", "7K4M9QX2P3RH8WVé"},
		{"a dropped character, spaces notwithstanding", "7K4M 9QX2 P3RH 8W N"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := normalizeEnrollmentCode(tc.typed); ok {
				t.Errorf("normalizeEnrollmentCode(%q) = %q, ok — want rejected", tc.typed, got)
			}
		})
	}
}

// TestGeneratedCodesNormalizeToThemselves closes the loop between the
// two halves: a code this build generates must be one this build accepts,
// with the prefix and hyphens the display form carries.
func TestGeneratedCodesNormalizeToThemselves(t *testing.T) {
	for i := 0; i < 50; i++ {
		code := generateCode(t)
		got, ok := normalizeEnrollmentCode(code)
		if !ok {
			t.Fatalf("generated code %q does not pass its own validation", code)
		}
		if want := strings.ReplaceAll(strings.TrimPrefix(code, "GAGE-"), "-", ""); got != want {
			t.Fatalf("normalizeEnrollmentCode(%q) = %q, want %q", code, got, want)
		}
	}
}
