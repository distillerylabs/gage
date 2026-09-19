package main

import (
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// TestRenameChangesOnlyTitleThroughTheCLI.
func TestRenameChangesOnlyTitleThroughTheCLI(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "ProtonMail", "--description", "personal email"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	rename := runCLI(t, []string{"rename", "ProtonMail", "Proton Mail"}, "")
	if rename.Code != 0 {
		t.Fatalf("rename failed: %s", rename.Stderr)
	}

	got := catEntry(t, "Proton Mail")
	if got.Title != "Proton Mail" {
		t.Errorf("Title = %q, want %q", got.Title, "Proton Mail")
	}
	if got.Value != "hunter2" {
		t.Errorf("Value changed: got %q, want unchanged %q", got.Value, "hunter2")
	}
	if got.Description != "personal email" {
		t.Errorf("Description changed: got %q, want unchanged %q", got.Description, "personal email")
	}

	old := runCLI(t, []string{"cat", "ProtonMail"}, "")
	if old.Code != int(exitcode.NotFound) {
		t.Errorf("old title still resolves after rename: exit code = %d, want %d (NotFound)", old.Code, exitcode.NotFound)
	}
}

// TestRenameToExistingTitleRejectedUnlessForcedThroughTheCLI.
func TestRenameToExistingTitleRejectedUnlessForcedThroughTheCLI(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "Entry A"}, "a"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "Entry B"}, "b"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	rejected := runCLI(t, []string{"rename", "Entry B", "Entry A"}, "")
	if rejected.Code != int(exitcode.Conflict) {
		t.Errorf("exit code = %d, want %d (Conflict)", rejected.Code, exitcode.Conflict)
	}

	forced := runCLI(t, []string{"rename", "Entry B", "Entry A", "--force"}, "")
	if forced.Code != 0 {
		t.Fatalf("forced rename failed: %s", forced.Stderr)
	}

	ls := runCLI(t, []string{"ls"}, "")
	if n := strings.Count(ls.Stdout, "Entry A"); n != 2 {
		t.Errorf("ls shows %d \"Entry A\" entries after a forced rename, want 2:\n%s", n, ls.Stdout)
	}
}

// TestRenameAmbiguousQueryListsCandidatesAndFails.
func TestRenameAmbiguousQueryListsCandidatesAndFails(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "v1"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "AWS IAM backup admin"}, "v2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"rename", "aws", "Whatever"}, "")
	if res.Code != int(exitcode.Ambiguous) {
		t.Errorf("exit code = %d, want %d (Ambiguous)", res.Code, exitcode.Ambiguous)
	}
	if !strings.Contains(res.Stderr, "AWS root account") || !strings.Contains(res.Stderr, "AWS IAM backup admin") {
		t.Errorf("stderr missing candidates: %q", res.Stderr)
	}
}
