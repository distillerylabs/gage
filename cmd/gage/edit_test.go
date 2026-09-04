package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// backdateEntry rewrites title's created/updated timestamps to when,
// directly through the library. A subsequent gage edit's fresh
// NewTimestamp(time.Now()) is then guaranteed to land after when,
// regardless of how fast the two commands actually run.
//
// Without this, "updated moved past the original" is a race against the
// wall clock: NewTimestamp truncates to whole seconds, so an insert and
// an edit landing in the same second produce identical timestamps. This
// suite used to win that race by accident — a real scrypt unlock took
// over a second on its own, more than enough separation — but lowering
// the test work factor (see SetScryptWorkFactorForTests) made the two
// commands fast enough to collide for real.
func backdateEntry(t *testing.T, title string, when time.Time) {
	t.Helper()
	app := &App{
		Out:      &bytes.Buffer{},
		Err:      &bytes.Buffer{},
		In:       strings.NewReader(""),
		Prompter: &fakePrompter{passphrases: []string{testPassphrase}},
	}
	err := withUnlockedVault(app, "personal", func(v *gage.Vault, ident *gage.Identity) error {
		id, e, err := v.Resolve(title, ident)
		if err != nil {
			return err
		}
		e.Created = gage.NewTimestamp(when)
		e.Updated = gage.NewTimestamp(when)
		return v.Update(id, e)
	})
	if err != nil {
		t.Fatalf("backdating %q: %v", title, err)
	}
}

// TestEditRestampsUpdatedAndUpdatedByAndCommits: gage edit must re-stamp
// updated/updated_by on save (even when the fake editor changes nothing
// else) and produce exactly one new commit.
func TestEditRestampsUpdatedAndUpdatedByAndCommits(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")
	device := readGlobalConfigForTest(t).Vaults["personal"].Device

	if res, _ := runCLIWithValue(t, []string{"insert", "ProtonMail", "--description", "personal email"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	backdateEntry(t, "ProtonMail", time.Now().Add(-24*time.Hour))
	before := catEntry(t, "ProtonMail")
	beforeCommits, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}

	setFakeEditor(t, "noop", nil)
	edit := runCLI(t, []string{"edit", "ProtonMail"}, "")
	if edit.Code != 0 {
		t.Fatalf("edit failed: %s", edit.Stderr)
	}

	after := catEntry(t, "ProtonMail")
	if !after.Updated.After(before.Updated.Time) {
		t.Errorf("Updated = %v, want it bumped past %v", after.Updated, before.Updated)
	}
	if after.UpdatedBy != device {
		t.Errorf("UpdatedBy = %q, want this device's recorded name %q", after.UpdatedBy, device)
	}
	if after.Created != before.Created {
		t.Errorf("Created changed: got %v, want unchanged %v", after.Created, before.Created)
	}
	if after.Value != before.Value {
		t.Errorf("Value changed on an unedited save: got %q, want unchanged %q", after.Value, before.Value)
	}
	if after.Title != before.Title {
		t.Errorf("Title changed on an unedited save: got %q, want unchanged %q", after.Title, before.Title)
	}
	if after.Description != before.Description {
		t.Errorf("Description changed on an unedited save: got %q, want unchanged %q", after.Description, before.Description)
	}
	if len(after.Fields) != len(before.Fields) {
		t.Errorf("Fields changed on an unedited save: got %v, want unchanged %v", after.Fields, before.Fields)
	}

	afterCommits, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterCommits != beforeCommits+1 {
		t.Errorf("commit count = %d, want %d (exactly one new commit)", afterCommits, beforeCommits+1)
	}
}

// TestEditRestampIsAuthoritativeOverTheEditedFile is the assertion the
// unedited-save test above structurally cannot make: because the editing
// device is the same one that inserted the entry, `updated_by` is
// already correct before the edit, so that test passes whether or not
// gage re-stamps anything.
//
// Here the fake editor rewrites the three fields gage owns to values it
// must overrule — a foreign updated_by and a 1999 created/updated — so
// each guarantee becomes observable: updated_by comes from the unlocked
// identity, updated from the clock, and created from the *original*
// entry rather than from whatever the file came back saying.
func TestEditRestampIsAuthoritativeOverTheEditedFile(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	device := readGlobalConfigForTest(t).Vaults["personal"].Device

	if res, _ := runCLIWithValue(t, []string{"insert", "ProtonMail"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	backdateEntry(t, "ProtonMail", time.Now().Add(-24*time.Hour))
	before := catEntry(t, "ProtonMail")

	setFakeEditor(t, "tamper-stamps", nil)
	if res := runCLI(t, []string{"edit", "ProtonMail"}, ""); res.Code != 0 {
		t.Fatalf("edit failed: %s", res.Stderr)
	}

	after := catEntry(t, "ProtonMail")
	if after.UpdatedBy != device {
		t.Errorf("updated_by = %q, want it re-stamped to this device %q rather than taken from the edited file", after.UpdatedBy, device)
	}
	if after.Updated.Year() == 1999 {
		t.Errorf("updated = %v, want it re-stamped from the clock rather than taken from the edited file", after.Updated)
	}
	if !after.Updated.After(before.Updated.Time) {
		t.Errorf("updated = %v, want it bumped past %v", after.Updated, before.Updated)
	}
	if after.Created != before.Created {
		t.Errorf("created = %v, want the original %v preserved — created is set once and never touched again", after.Created, before.Created)
	}
}

// TestEditChangesValueAndLeavesOtherFieldsUntouched.
func TestEditChangesValueAndLeavesOtherFieldsUntouched(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "ProtonMail", "--description", "personal email"}, "old-value"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	setFakeEditor(t, "set-value", map[string]string{"GAGE_TEST_FAKE_EDITOR_VALUE": "new-value"})
	edit := runCLI(t, []string{"edit", "ProtonMail"}, "")
	if edit.Code != 0 {
		t.Fatalf("edit failed: %s", edit.Stderr)
	}

	got := catEntry(t, "ProtonMail")
	if got.Value != "new-value" {
		t.Errorf("Value = %q, want %q", got.Value, "new-value")
	}
	if got.Description != "personal email" {
		t.Errorf("Description changed: got %q, want unchanged %q", got.Description, "personal email")
	}
	if got.Title != "ProtonMail" {
		t.Errorf("Title changed: got %q, want unchanged %q", got.Title, "ProtonMail")
	}
}

// TestEditUnparseableYAMLAbortsWithNoCommitAndOriginalUnchanged.
func TestEditUnparseableYAMLAbortsWithNoCommitAndOriginalUnchanged(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "ProtonMail"}, "original-value"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	before, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}

	setFakeEditor(t, "invalid-yaml", nil)
	edit := runCLI(t, []string{"edit", "ProtonMail"}, "")
	if edit.Code == 0 {
		t.Fatal("expected edit to fail on unparseable YAML")
	}
	if edit.Stderr == "" {
		t.Error("expected a clear error message on stderr")
	}

	after, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("commit count changed (%d -> %d) on a failed edit", before, after)
	}

	got := catEntry(t, "ProtonMail")
	if got.Value != "original-value" {
		t.Errorf("Value = %q, want the original unchanged %q", got.Value, "original-value")
	}
}

// TestEditAmbiguousQueryListsCandidatesAndFails: gage edit goes through
// the same shared resolver as show/cat/rm.
func TestEditAmbiguousQueryListsCandidatesAndFails(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "AWS IAM backup admin"}, "v2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"edit", "aws"}, "")
	if res.Code != int(exitcode.Ambiguous) {
		t.Errorf("exit code = %d, want %d (Ambiguous)", res.Code, exitcode.Ambiguous)
	}
	if !strings.Contains(res.Stderr, "AWS root account") || !strings.Contains(res.Stderr, "AWS IAM backup admin") {
		t.Errorf("stderr missing candidates: %q", res.Stderr)
	}
}
