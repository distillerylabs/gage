package main

import (
	"strings"
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

// TestGenerateRespectsLengthAndNoSymbols: both flags reach the value
// that actually lands in the vault, not just the library call.
func TestGenerateRespectsLengthAndNoSymbols(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res := runCLI(t, []string{"generate", "Long", "-l", "40"}, ""); res.Code != 0 {
		t.Fatalf("generate -l failed: %s", res.Stderr)
	}
	if got := catEntry(t, "Long"); len(got.Value) != 40 {
		t.Errorf("generated value length = %d, want 40", len(got.Value))
	}

	if res := runCLI(t, []string{"generate", "Plain", "--no-symbols", "-l", "32"}, ""); res.Code != 0 {
		t.Fatalf("generate --no-symbols failed: %s", res.Stderr)
	}
	got := catEntry(t, "Plain")
	if len(got.Value) != 32 {
		t.Errorf("generated value length = %d, want 32", len(got.Value))
	}
	for _, r := range got.Value {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isAlnum {
			t.Fatalf("--no-symbols value %q contains %q", got.Value, r)
		}
	}
}

// TestGenerateRejectsAnUnreasonablySmallLength: the entry must not be
// created at all — a weak secret refused after being committed would be
// no refusal.
func TestGenerateRejectsAnUnreasonablySmallLength(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"generate", "Weak", "-l", "4"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if !strings.Contains(res.Stderr, "8") {
		t.Errorf("rejection does not say what the minimum is: %q", res.Stderr)
	}

	ls := runCLI(t, []string{"ls"}, "")
	if strings.Contains(ls.Stdout, "Weak") {
		t.Errorf("a rejected generate still created the entry:\n%s", ls.Stdout)
	}
}

// TestGenerateClipCopiesTheValueAndPrintsNone is why `generate` carries
// -c at all. Plain `generate` prints a confirmation and never the value,
// so without a destination flag the only way to get a freshly generated
// password out is `gage show` — which puts it in the scrollback, exactly
// what principle 6 is about. -c is the spelling that doesn't.
func TestGenerateClipCopiesTheValueAndPrintsNone(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	run := runCLIWithClipboard(t, []string{"generate", "GitHub", "-c"})
	if run.Code != 0 {
		t.Fatalf("generate -c failed: %s", run.Stderr)
	}

	stored := catEntry(t, "GitHub")
	if len(run.cb.writes) == 0 || run.cb.writes[0] != stored.Value {
		t.Fatalf("clipboard writes = %q, want the generated value first", run.cb.writes)
	}
	if strings.Contains(run.Stdout, stored.Value) {
		t.Errorf("generate -c printed the generated value to stdout:\n%s", run.Stdout)
	}
	// The confirmation still lands, and before the blocking wait.
	if !strings.Contains(run.Stdout, "generated") {
		t.Errorf("generate -c printed no confirmation:\n%s", run.Stdout)
	}
	if !run.waited {
		t.Error("one-shot generate -c did not block for the clipboard timeout")
	}
	// And it was cleared on the way out, like any other one-shot copy.
	if got := run.cb.get(); got != "" {
		t.Errorf("clipboard = %q after generate -c returned, want it cleared", got)
	}
}

// TestGenerateQREncodesTheValueWithoutPrintingIt is the same contract
// for -q.
func TestGenerateQRRendersWithoutPrintingTheValue(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"generate", "GitHub", "-q"}, "")
	if res.Code != 0 {
		t.Fatalf("generate -q failed: %s", res.Stderr)
	}
	stored := catEntry(t, "GitHub")
	if strings.Contains(res.Stdout, stored.Value) {
		t.Errorf("generate -q printed the value alongside the code:\n%s", res.Stdout)
	}
	if !gridsEqual(decodeRenderedQR(t, res.Stdout), referenceGrid(t, stored.Value)) {
		t.Error("generate -q did not encode the value it stored")
	}
}

// TestGenerateClipAndQRAreMutuallyExclusive: refused before anything is
// generated or committed, so the vault is untouched.
func TestGenerateClipAndQRAreMutuallyExclusive(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"generate", "Both", "-c", "-q"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	ls := runCLI(t, []string{"ls"}, "")
	if strings.Contains(ls.Stdout, "Both") {
		t.Errorf("a rejected generate still created the entry:\n%s", ls.Stdout)
	}
}
