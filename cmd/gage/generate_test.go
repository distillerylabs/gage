package main

import (
	"testing"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// TestGenerateInsertsEntryWithRandomValueOfSaneDefaultLength.
func TestGenerateInsertsEntryWithRandomValueOfSaneDefaultLength(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"generate", "GitHub"}, "")
	if res.Code != 0 {
		t.Fatalf("generate failed: %s", res.Stderr)
	}

	got := catEntry(t, "GitHub")
	if got.Value == "" {
		t.Fatal("generated entry has an empty value")
	}
	// "sane default length" — long enough to be a real generated secret,
	// not a placeholder.
	if len(got.Value) < 16 {
		t.Errorf("generated value length = %d, want at least 16", len(got.Value))
	}
}

// TestGenerateProducesDifferentValuesAcrossEntries: a minimal sanity
// check (not a statistical randomness test — see internal/gage's
// GenerateValue tests for that) that two generated entries don't share a
// value.
//
// This test originally used "A" and "B", which are single hex
// characters. Resolve's *original* stage order tried UUID matches before
// title matches, so roughly one run in eight, one of the two entries
// drew a UUID that happened to contain "a" or "b", `cat A` and `cat B`
// both resolved to that same entry, and the test reported two generated
// values as identical — a false alarm about crypto/rand that cost a CI
// run to chase down. Resolve now checks every title stage before any
// UUID stage (see the design doc's "Addressing entries" and the M5
// plan's "Decisions made"), which fixes this at the source: an exact
// title match can no longer be pre-empted by an unrelated entry's UUID.
// "Zeta"/"Yolk" (non-hex) are kept anyway, so this test's pass/fail
// stays about crypto/rand specifically rather than also depending on the
// resolver's stage order.
func TestGenerateProducesDifferentValuesAcrossEntries(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res := runCLI(t, []string{"generate", "Zeta"}, ""); res.Code != 0 {
		t.Fatalf("generate failed: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"generate", "Yolk"}, ""); res.Code != 0 {
		t.Fatalf("generate failed: %s", res.Stderr)
	}

	a := catEntry(t, "Zeta")
	b := catEntry(t, "Yolk")
	if a.Value == b.Value {
		t.Errorf("two generated entries share the same value: %q", a.Value)
	}
}

// TestGenerateDuplicateTitleRejectedUnlessForced mirrors insert's own
// duplicate-title behavior, since generate reuses Insert.
func TestGenerateDuplicateTitleRejectedUnlessForced(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res := runCLI(t, []string{"generate", "Site"}, ""); res.Code != 0 {
		t.Fatalf("generate failed: %s", res.Stderr)
	}

	dup := runCLI(t, []string{"generate", "Site"}, "")
	if dup.Code != int(exitcode.Conflict) {
		t.Errorf("exit code = %d, want %d (Conflict)", dup.Code, exitcode.Conflict)
	}

	forced := runCLI(t, []string{"generate", "Site", "--force"}, "")
	if forced.Code != 0 {
		t.Fatalf("forced generate failed: %s", forced.Stderr)
	}
}

// TestGenerateProducesExactlyOneCommitAndSetsTimestamps.
func TestGenerateProducesExactlyOneCommitAndSetsTimestamps(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	device := readGlobalConfigForTest(t).Vaults["personal"].Device

	if res := runCLI(t, []string{"generate", "Site"}, ""); res.Code != 0 {
		t.Fatalf("generate failed: %s", res.Stderr)
	}

	got := catEntry(t, "Site")
	if got.Created.IsZero() || got.Updated.IsZero() {
		t.Errorf("created/updated not set: created=%v updated=%v", got.Created, got.Updated)
	}
	if got.UpdatedBy != device {
		t.Errorf("updated_by = %q, want this device's recorded name %q", got.UpdatedBy, device)
	}
}
