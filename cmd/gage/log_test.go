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

// unknownEntryUUID is a syntactically valid UUID that names no entry —
// what a human pastes from an old `gage log` after the entry, and the
// commit that held it, are long gone. It resolves (the UUID path
// accepts it) and then has nothing to report, which is the case both
// "no commit history" branches exist for.
const unknownEntryUUID = "11111111-1111-4111-8111-111111111111"

// TestLogAndHistoryOnAnEntryWithNoHistorySayNothingIsThere: a resolvable
// id with no commits behind it reports that on stderr and exits 0.
// Zero revisions is an ordinary answer, not a failure — an id gage can
// parse but has never committed is exactly what a stale note or a typo'd
// paste produces.
func TestLogAndHistoryOnAnEntryWithNoHistorySayNothingIsThere(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	for _, args := range [][]string{
		{"log", unknownEntryUUID},
		{"history", "--decrypt", unknownEntryUUID},
	} {
		t.Run(args[0], func(t *testing.T) {
			res := runCLI(t, args, "")
			if res.Code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
			}
			if !strings.Contains(res.Stderr, "no commit history for that entry yet") {
				t.Errorf("stderr doesn't say there's nothing to show:\n%s", res.Stderr)
			}
			if strings.TrimSpace(res.Stdout) != "" {
				t.Errorf("stdout should be empty when there is nothing to report:\n%s", res.Stdout)
			}
		})
	}
}

// TestLogOnAQueryThatIsNeitherATitleNorAUUID: the UUID fallback exists
// for ids of entries that no longer resolve by title
// (TestLogShowsADeletedEntrysEnd covers that). A query that is neither
// must come back as the original not-found rather than being coerced
// into something.
func TestLogOnAQueryThatIsNeitherATitleNorAUUID(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"log", "no-such-entry"}, "")
	if res.Code != int(exitcode.NotFound) {
		t.Errorf("exit code = %d, want %d (NotFound); stderr=%s", res.Code, exitcode.NotFound, res.Stderr)
	}
}

// TestHistoryDecryptShowsADeletedRevision: `history` walks the same
// revisions `log` does, so a deletion has to render there too — as the
// end of the entry rather than as a revision with empty fields.
func TestHistoryDecryptShowsADeletedRevision(t *testing.T) {
	isolateXDG(t)
	path := initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "Temp"}, "gone-now"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	// Captured before the rm, for the same reason
	// TestLogShowsADeletedEntrysEnd captures it: afterwards the title
	// resolves to nothing.
	id := soleEntryID(t, path).String()
	if res := runCLI(t, []string{"rm", "Temp"}, ""); res.Code != 0 {
		t.Fatalf("rm failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"history", "--decrypt", id}, "")
	if res.Code != 0 {
		t.Fatalf("history failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "(entry deleted)") {
		t.Errorf("history of a removed entry doesn't show its deletion:\n%s", res.Stdout)
	}
	// The revision before the deletion is still readable — the point of
	// --decrypt is that the old value is recoverable from history.
	if !strings.Contains(res.Stdout, "gone-now") {
		t.Errorf("history didn't show the value the entry held before it was removed:\n%s", res.Stdout)
	}
}

// TestHistoryDecryptMarksADescriptionChange: description is the one
// field diffEntryLines renders conditionally — it is omitted entirely
// when neither revision has one, so that an entry without a description
// doesn't grow an empty line per commit. When one *is* present, a change
// to it has to be marked like any other.
func TestHistoryDecryptMarksADescriptionChange(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub", "--description", "personal account"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	setFakeEditor(t, "set-description-only", map[string]string{
		"GAGE_TEST_FAKE_EDITOR_DESCRIPTION": "work account",
	})
	if res := runCLI(t, []string{"edit", "GitHub"}, ""); res.Code != 0 {
		t.Fatalf("edit failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"history", "--decrypt", "GitHub"}, "")
	if res.Code != 0 {
		t.Fatalf("history failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "- description: personal account") {
		t.Errorf("the old description isn't marked as replaced:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "+ description: work account") {
		t.Errorf("the new description isn't marked as added:\n%s", res.Stdout)
	}
}

// TestHistoryDecryptShowsAFieldThatWasRemoved: the key set is the union
// of both revisions', so a field that existed and then didn't still
// appears — "it used to have a TOTP seed" is exactly the question
// history answers, and collecting keys from the newer revision alone
// would silently drop it.
func TestHistoryDecryptShowsAFieldThatWasRemoved(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntryWithFields(t, "GitHub", "hunter2", map[string]string{"username": "octocat"})

	// An edit that keeps the value and drops every field: an empty
	// FIELDS list leaves set-value-and-fields with an empty map.
	setFakeEditor(t, "set-value-and-fields", map[string]string{
		"GAGE_TEST_FAKE_EDITOR_VALUE":  "hunter2",
		"GAGE_TEST_FAKE_EDITOR_FIELDS": "",
	})
	if res := runCLI(t, []string{"edit", "GitHub"}, ""); res.Code != 0 {
		t.Fatalf("edit failed: %s", res.Stderr)
	}

	res := runCLI(t, []string{"history", "--decrypt", "GitHub"}, "")
	if res.Code != 0 {
		t.Fatalf("history failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "fields.username") {
		t.Errorf("a field removed in the newest revision vanished from the history:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "octocat") {
		t.Errorf("the removed field's old value isn't recoverable from the history:\n%s", res.Stdout)
	}
}
