package main

import (
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// TestEditRestampsUpdatedAndUpdatedByAndCommits: gage edit must re-stamp
// updated/updated_by on save (even when the fake editor changes nothing
// else) and produce exactly one new commit.
func TestEditRestampsUpdatedAndUpdatedByAndCommits(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")
	device := readGlobalConfigForTest(t).Vaults["personal"].Device

	if res, _ := runCLIWithValue(t, []string{"insert", "ProtonMail"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
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

	afterCommits, err := gitrepo.CommitCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterCommits != beforeCommits+1 {
		t.Errorf("commit count = %d, want %d (exactly one new commit)", afterCommits, beforeCommits+1)
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
