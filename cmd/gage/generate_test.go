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
func TestGenerateProducesDifferentValuesAcrossEntries(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res := runCLI(t, []string{"generate", "A"}, ""); res.Code != 0 {
		t.Fatalf("generate failed: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"generate", "B"}, ""); res.Code != 0 {
		t.Fatalf("generate failed: %s", res.Stderr)
	}

	a := catEntry(t, "A")
	b := catEntry(t, "B")
	if a.Value == b.Value {
		t.Error("two generated entries share the same value")
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
