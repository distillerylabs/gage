package main

import (
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// TestLogShowsTimestampsAndNoDecryptedContent is `log`'s whole contract:
// when an entry changed, and nothing about what it holds.
func TestLogShowsTimestampsAndNoDecryptedContent(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub", "--description", "personal account"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"rename", "GitHub", "GitHub work"}, ""); res.Code != 0 {
		t.Fatalf("rename failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"log", "GitHub work"}, "")
	if res.Code != 0 {
		t.Fatalf("log failed: %s", res.Stderr)
	}
	// Two writes to the entry: the insert and the rename.
	lines := nonEmptyLines(res.Stdout)
	if len(lines) != 2 {
		t.Errorf("log lines = %d, want 2 (insert + rename):\n%s", len(lines), res.Stdout)
	}
	for _, leak := range []string{"hunter2", "personal account", "GitHub"} {
		if strings.Contains(res.Stdout, leak) {
			t.Errorf("log output leaks decrypted content %q:\n%s", leak, res.Stdout)
		}
	}
}

// TestLogRevealsNoTitlesFromCommitMessages: commit messages are entry
// UUIDs and nothing else (M4's decision), so even a log rendered
// straight from them can't disclose what an entry is called.
func TestLogRevealsNoTitlesFromCommitMessages(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	for _, title := range []string{"Chase bank login", "Proton Mail"} {
		if res, _ := runCLIWithValue(t, []string{"insert", title}, "v"); res.Code != 0 {
			t.Fatalf("insert %q failed: %s", title, res.Stderr)
		}
	}

	res := runCLI(t, []string{"log"}, "")
	if res.Code != 0 {
		t.Fatalf("log failed: %s", res.Stderr)
	}
	for _, leak := range []string{"Chase", "Proton", "bank", "Mail"} {
		if strings.Contains(res.Stdout, leak) {
			t.Errorf("vault-wide log leaks the title fragment %q:\n%s", leak, res.Stdout)
		}
	}
	if len(nonEmptyLines(res.Stdout)) != 2 {
		t.Errorf("log over two entries = %d lines, want 2:\n%s", len(nonEmptyLines(res.Stdout)), res.Stdout)
	}
}

// TestLogShowsADeletedEntrysEnd: an rm is a commit too, and a log that
// stopped at the last edit would imply the entry is still there.
func TestLogShowsADeletedEntrysEnd(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "Temp"}, "v"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	// Captured before the rm: afterwards the title resolves to nothing,
	// and the id is the only way left to ask about the entry — which is
	// exactly the situation a log of a deleted entry exists for.
	id := soleEntryID(t, path).String()
	if res := runCLI(t, []string{"rm", "Temp"}, ""); res.Code != 0 {
		t.Fatalf("rm failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"log", id}, "")
	if res.Code != 0 {
		t.Fatalf("log failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "(deleted)") {
		t.Errorf("log of a removed entry does not show its deletion:\n%s", res.Stdout)
	}
}

// TestHistoryRequiresDecrypt: `history` without --decrypt is a usage
// error, not a quieter variant. The flag is one of the three explicit
// acts that stand in for a confirmation prompt.
func TestHistoryRequiresDecrypt(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"history", "GitHub"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if strings.Contains(res.Stdout, "hunter2") {
		t.Error("history without --decrypt still printed a value")
	}
	if !strings.Contains(res.Stderr, "--decrypt") {
		t.Errorf("error does not name the required flag: %q", res.Stderr)
	}
}

// TestHistoryWithNoQueryIsAUsageError: there is no bulk "decrypt the
// history of everything" form, per the M12 decision. A missing query
// must fail, not fan out across the vault.
func TestHistoryWithNoQueryIsAUsageError(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"history", "--decrypt"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("a query-less history --decrypt dumped the vault:\n%s", res.Stdout)
	}
}

// TestHistoryDecryptWalksRevisionsAndDiffs: the command's actual job —
// every past value of one entry, marked as changed across commits.
func TestHistoryDecryptWalksRevisionsAndDiffs(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "first-password"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	setFakeEditor(t, "set-value-and-field", map[string]string{
		"GAGE_TEST_FAKE_EDITOR_VALUE":       "second-password",
		"GAGE_TEST_FAKE_EDITOR_FIELD_KEY":   "username",
		"GAGE_TEST_FAKE_EDITOR_FIELD_VALUE": "me@example.com",
	})
	if res := runCLI(t, []string{"edit", "GitHub"}, ""); res.Code != 0 {
		t.Fatalf("edit failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"history", "--decrypt", "GitHub"}, "")
	if res.Code != 0 {
		t.Fatalf("history --decrypt failed: %s", res.Stderr)
	}
	// Both the rotated-away value and the current one — that is the
	// exposure this command is named for.
	for _, want := range []string{"first-password", "second-password"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("history output missing revision value %q:\n%s", want, res.Stdout)
		}
	}
	// Rendered as a diff: the superseded value is marked as removed and
	// the new one as added.
	if !strings.Contains(res.Stdout, "- value: first-password") {
		t.Errorf("the superseded value is not marked as replaced:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "+ value: second-password") {
		t.Errorf("the new value is not marked as added:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "fields.username") {
		t.Errorf("a field added in the second revision is not shown:\n%s", res.Stdout)
	}
	if strings.Count(res.Stdout, "commit ") != 2 {
		t.Errorf("want two commits in the history, got:\n%s", res.Stdout)
	}
}

// TestLogAndHistoryWorkInASession: both are in the sync family, which
// the design doc says operates against the current session vault.
func TestLogAndHistoryWorkInASession(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"log GitHub",
		"history --decrypt GitHub",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d: %s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("in-session history --decrypt printed no value:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "commit ") {
		t.Errorf("in-session log/history produced no commits:\n%s", res.Stdout)
	}
}

// nonEmptyLines is the log tests' line counter, ignoring the prompt
// noise a session adds and any trailing blank.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
