package gage

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// TestPendingFileNameRoundTrips pins D-ENROLL-EXPIRY-IN-NAME's format:
// <request-id>-<expires-epoch>.age, with the expiry as UTC seconds in the
// clear.
//
// The epoch is deliberately readable, which is the one exception to
// entries/'s "a filename never encodes content" rule — it is what lets
// `recipient pending` show a real expiry and pruning run as a string
// parse rather than a walk through git history.
func TestPendingFileNameRoundTrips(t *testing.T) {
	id := uuid.NewString()
	// Truncated to the second, because that is the resolution the
	// filename carries and the round trip is only claimed to that
	// precision.
	expires := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)

	name := pendingFileName(id, expires)
	if !strings.HasSuffix(name, ".age") {
		t.Errorf("pending file %q does not end in .age", name)
	}
	if !strings.HasPrefix(name, id+"-") {
		t.Errorf("pending file %q does not start with the request id %q", name, id)
	}

	gotID, gotExpires, ok := parsePendingFileName(name)
	if !ok {
		t.Fatalf("parsePendingFileName(%q) rejected a name pendingFileName produced", name)
	}
	if gotID != id {
		t.Errorf("parsed id = %q, want %q", gotID, id)
	}
	if !gotExpires.Equal(expires) {
		t.Errorf("parsed expiry = %s, want %s", gotExpires, expires)
	}
}

// TestPendingFileNameParserSkipsStrays is the posture the whole scheme
// depends on: pending/ is a committed directory any git writer can put
// files into, so a name that is not one gage wrote is skipped rather than
// treated as an error — and, elsewhere, never deleted.
//
// The two halves of the name are validated independently on purpose. A
// name whose UUID half is fine and whose epoch half is garbage is no more
// gage's than one where it is the other way round.
func TestPendingFileNameParserSkipsStrays(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		name string
		file string
	}{
		{"no extension", id + "-1788862320"},
		{"wrong extension", id + "-1788862320.txt"},
		{"no epoch", id + ".age"},
		{"empty epoch", id + "-.age"},
		{"non-numeric epoch", id + "-tomorrow.age"},
		{"negative epoch", id + "--1788862320.age"},
		{"signed epoch", id + "-+1788862320.age"},
		{"epoch with padding", id + "- 1788862320.age"},
		{"not a uuid", "not-a-uuid-1788862320.age"},
		{"uppercase uuid", strings.ToUpper(id) + "-1788862320.age"},
		{"uuid without hyphens", strings.ReplaceAll(id, "-", "") + "-1788862320.age"},
		{"empty", ""},
		{"extension only", ".age"},
		{"an entry-shaped name", id + ".age"},
		{"a directory marker", "README"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if gotID, _, ok := parsePendingFileName(tc.file); ok {
				t.Errorf("parsePendingFileName(%q) accepted a stray, as id %q", tc.file, gotID)
			}
		})
	}
}

// TestSealRejectsTTLsOutsideTheCeiling is D-ENROLL-TTL's "validated at
// the boundary, before anything is generated or written."
//
// The check lives here rather than on a flag because sealing is the only
// function in this milestone that takes a duration, and because a library
// a GUI could call directly cannot rely on cmd/gage having validated
// first. The exit code is Usage: this is a rejection of what was asked
// for, not a state anyone has to resolve.
func TestSealRejectsTTLsOutsideTheCeiling(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")

	for _, tc := range []struct {
		name string
		ttl  time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Hour},
		{"one second past the ceiling", MaxEnrollmentTTL + time.Second},
		{"a year", 365 * 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := v.sealEnrollment(d.name, d.pubkey, MethodPassphrase, tc.ttl)
			if err == nil {
				t.Fatalf("sealEnrollment with a ttl of %s succeeded; want a usage rejection", tc.ttl)
			}
			if got := exitcode.CodeOf(err); got != exitcode.Usage {
				t.Errorf("exit code = %s, want %s — a ttl outside the ceiling is a rejection of what was "+
					"asked for, not a state to resolve", got, exitcode.Usage)
			}
		})
	}

	// And the boundary itself is accepted, so the ceiling is a ceiling
	// rather than an off-by-one below one.
	if _, _, err := v.sealEnrollment(d.name, d.pubkey, MethodPassphrase, MaxEnrollmentTTL); err != nil {
		t.Errorf("sealEnrollment at exactly the ceiling (%s) failed: %v", MaxEnrollmentTTL, err)
	}
}

// TestDefaultTTLIsWithinTheCeiling keeps the two D-ENROLL-TTL numbers
// from drifting apart: a default gage itself could not seal would be
// found by every user and no test.
func TestDefaultTTLIsWithinTheCeiling(t *testing.T) {
	if DefaultEnrollmentTTL <= 0 || DefaultEnrollmentTTL > MaxEnrollmentTTL {
		t.Errorf("DefaultEnrollmentTTL = %s, which is outside the range sealing accepts (0, %s]",
			DefaultEnrollmentTTL, MaxEnrollmentTTL)
	}
	if want := 24 * time.Hour; DefaultEnrollmentTTL != want {
		t.Errorf("DefaultEnrollmentTTL = %s, want %s (D-ENROLL-TTL)", DefaultEnrollmentTTL, want)
	}
	if want := 7 * 24 * time.Hour; MaxEnrollmentTTL != want {
		t.Errorf("MaxEnrollmentTTL = %s, want %s (D-ENROLL-TTL)", MaxEnrollmentTTL, want)
	}
}
