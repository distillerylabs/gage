package main

import (
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// catEntryIn is catEntry with an explicit --use, for tests that juggle
// more than one vault.
func catEntryIn(t *testing.T, vault, query string) cliResult {
	t.Helper()
	return runCLI(t, []string{"cat", query, "--use", vault}, "")
}

// parseCatOutput parses a successful `cat`'s stdout back into an Entry.
func parseCatOutput(t *testing.T, stdout string) (gage.Entry, error) {
	t.Helper()
	return gage.UnmarshalEntry([]byte(stdout))
}

// TestMvThroughTheCLIMovesEntryBetweenVaults is M11's headline behavior
// end to end: `gage mv --to-vault` removes the entry from the source and
// it becomes readable in the destination, under the destination's own
// (different) recipients — and the destination is never unlocked to get
// there, since runCLI here answers exactly one passphrase prompt total.
func TestMvThroughTheCLIMovesEntryBetweenVaults(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "shared-family")

	if res, _ := runCLIWithValue(t, []string{"insert", "family-wifi", "--use", "personal"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	mv := runCLI(t, []string{"mv", "family-wifi", "--to-vault", "shared-family", "--use", "personal"}, "")
	if mv.Code != 0 {
		t.Fatalf("mv failed: %s", mv.Stderr)
	}
	if !strings.Contains(mv.Stdout, "shared-family") {
		t.Errorf("mv output doesn't name the destination vault: %q", mv.Stdout)
	}

	gone := runCLI(t, []string{"cat", "family-wifi", "--use", "personal"}, "")
	if gone.Code != int(exitcode.NotFound) {
		t.Errorf("source still resolves after mv: exit code = %d, want %d (NotFound)", gone.Code, exitcode.NotFound)
	}

	moved := catEntryIn(t, "shared-family", "family-wifi")
	if moved.Code != 0 {
		t.Fatalf("cat at destination failed: %s", moved.Stderr)
	}
	e, err := parseCatOutput(t, moved.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if e.Title != "family-wifi" || e.Value != "hunter2" {
		t.Errorf("destination entry = %+v, want the moved family-wifi/hunter2", e)
	}
}

// TestCpThroughTheCLILeavesOriginalAndAddsCopy is `cp`'s own half: the
// source is untouched, and a decryptable copy lands in the destination.
func TestCpThroughTheCLILeavesOriginalAndAddsCopy(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "shared-family")

	if res, _ := runCLIWithValue(t, []string{"insert", "family-wifi", "--use", "personal"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	cp := runCLI(t, []string{"cp", "family-wifi", "--to-vault", "shared-family", "--use", "personal"}, "")
	if cp.Code != 0 {
		t.Fatalf("cp failed: %s", cp.Stderr)
	}

	original := catEntryIn(t, "personal", "family-wifi")
	if original.Code != 0 {
		t.Fatalf("original gone from source after cp: %s", original.Stderr)
	}

	copied := catEntryIn(t, "shared-family", "family-wifi")
	if copied.Code != 0 {
		t.Fatalf("cat at destination failed: %s", copied.Stderr)
	}
	e, err := parseCatOutput(t, copied.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if e.Value != "hunter2" {
		t.Errorf("destination copy value = %q, want %q", e.Value, "hunter2")
	}
}

// TestMvToUnregisteredVaultFailsCleanly is the "fails before decrypting
// anything" bullet: an unknown --to-vault name is rejected as NotFound
// and leaves the source entry exactly where it was.
func TestMvToUnregisteredVaultFailsCleanly(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "family-wifi", "--use", "personal"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"mv", "family-wifi", "--to-vault", "does-not-exist", "--use", "personal"}, "")
	if res.Code != int(exitcode.NotFound) {
		t.Errorf("exit code = %d, want %d (NotFound); stderr=%s", res.Code, exitcode.NotFound, res.Stderr)
	}

	still := catEntryIn(t, "personal", "family-wifi")
	if still.Code != 0 {
		t.Fatalf("source entry gone after a rejected --to-vault: %s", still.Stderr)
	}
}

// TestMvToItsOwnSourceVaultIsRejected is the "--to-vault naming the
// source vault itself is rejected" bullet.
func TestMvToItsOwnSourceVaultIsRejected(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "family-wifi", "--use", "personal"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"mv", "family-wifi", "--to-vault", "personal", "--use", "personal"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}

	still := catEntryIn(t, "personal", "family-wifi")
	if still.Code != 0 {
		t.Fatalf("source entry gone after a rejected self-move: %s", still.Stderr)
	}
}

// TestMvRequiresToVaultFlag: --to-vault is mandatory, not defaulted from
// the current vault.
func TestMvRequiresToVaultFlag(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "family-wifi", "--use", "personal"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"mv", "family-wifi", "--use", "personal"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage) for a missing --to-vault; stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
}

// TestMvDecliningTheTrustCheckAbortsThroughTheCLI is the M10 integration
// bullet: an unreviewed recipient change on the *destination* blocks the
// write, and declining it leaves the source untouched and nothing
// written to the destination.
func TestMvDecliningTheTrustCheckAbortsThroughTheCLI(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "shared-family")

	if res, _ := runCLIWithValue(t, []string{"insert", "family-wifi", "--use", "personal"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	// Bootstrap the destination's trust cache so the change below is a
	// genuine, reviewable diff rather than a first use.
	if res := runCLI(t, []string{"ls", "--use", "shared-family"}, ""); res.Code != 0 {
		t.Fatalf("bootstrapping shared-family's trust cache failed: %s", res.Stderr)
	}

	destPath := readGlobalConfigForTest(t).Vaults["shared-family"].Path
	commitRoutineRecipientChangeCLI(t, destPath, "phone-1", testRecipient2)

	res, p := runCLIWithPrompter(t, []string{"mv", "family-wifi", "--to-vault", "shared-family", "--use", "personal"}, "", false, newDecliningPrompter())
	if res.Code != int(exitcode.Conflict) {
		t.Errorf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	dp := p.(*decliningPrompter)
	if len(dp.recipientChanges) != 1 {
		t.Fatalf("recipient-change question asked %d times, want exactly 1", len(dp.recipientChanges))
	}
	if dp.recipientChanges[0].Vault != "shared-family" {
		t.Errorf("question named vault %q, want the destination %q", dp.recipientChanges[0].Vault, "shared-family")
	}

	still := catEntryIn(t, "personal", "family-wifi")
	if still.Code != 0 {
		t.Fatalf("source entry gone after a declined mv: %s", still.Stderr)
	}
	gone := catEntryIn(t, "shared-family", "family-wifi")
	if gone.Code != int(exitcode.NotFound) {
		t.Errorf("destination has the entry after a declined mv: exit code = %d, want %d (NotFound)", gone.Code, exitcode.NotFound)
	}
}

// TestMvInASessionNeverUnlocksTheDestination drives `mv` through the
// REPL: one passphrase for the source, none for the destination. The
// destination's own content is checked afterwards, through a fresh
// one-shot `cat` — deliberately outside the scripted session, since
// asking a session to *list* the destination would unlock it itself and
// prove nothing about mv.
func TestMvInASessionNeverUnlocksTheDestination(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "shared-family")
	insertEntry(t, "personal", "family-wifi", "hunter2")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"mv family-wifi --to-vault shared-family",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if got := countPassphrasePrompts(res.Stdout + res.Stderr); got != 1 {
		t.Errorf("asked for a passphrase %d times, want exactly 1 (the destination must not be unlocked):\n%s", got, res.Stdout+res.Stderr)
	}
	if !strings.Contains(res.Stdout, "shared-family") {
		t.Errorf("mv's own output doesn't name the destination vault:\n%s", res.Stdout)
	}

	moved := catEntryIn(t, "shared-family", "family-wifi")
	if moved.Code != 0 {
		t.Fatalf("cat at destination failed: %s", moved.Stderr)
	}
	e, err := parseCatOutput(t, moved.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if e.Value != "hunter2" {
		t.Errorf("destination entry value = %q, want %q", e.Value, "hunter2")
	}
}
