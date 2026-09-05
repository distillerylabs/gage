package main

import (
	"strings"
	"testing"
)

// TestSearchOneShotMatchesTitleDescriptionAndBody drives `gage search`
// through the real one-shot CLI: a title match, a description match, and
// a body (value) match, none of which print the matched value itself —
// only the same "title, short id" row `ls` renders. `gage grep` is
// checked as an alias for the identical command.
func TestSearchOneShotMatchesTitleDescriptionAndBody(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res, _ := runCLIWithValue(t, []string{"insert", "AWS root account"}, "x"); res.Code != 0 {
		t.Fatalf("insert: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "--description", "shared aws credentials", "Cloud creds"}, "y"); res.Code != 0 {
		t.Fatalf("insert: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "Unrelated"}, "the aws secret key is here"); res.Code != 0 {
		t.Fatalf("insert: %s", res.Stderr)
	}
	if res, _ := runCLIWithValue(t, []string{"insert", "Nothing"}, "irrelevant"); res.Code != 0 {
		t.Fatalf("insert: %s", res.Stderr)
	}

	for _, cmd := range []string{"search", "grep"} {
		res := runCLI(t, []string{cmd, "aws"}, "")
		if res.Code != 0 {
			t.Fatalf("%s: %s", cmd, res.Stderr)
		}
		for _, want := range []string{"AWS root account", "Cloud creds", "Unrelated"} {
			if !strings.Contains(res.Stdout, want) {
				t.Errorf("%s output missing %q:\n%s", cmd, want, res.Stdout)
			}
		}
		if strings.Contains(res.Stdout, "Nothing") {
			t.Errorf("%s matched an unrelated entry:\n%s", cmd, res.Stdout)
		}
		if strings.Contains(res.Stdout, "the aws secret key is here") || strings.Contains(res.Stdout, "shared aws credentials") {
			t.Errorf("%s leaked matched text into its output:\n%s", cmd, res.Stdout)
		}
	}
}

// TestSessionLsSearchReindexSeeInsertRenameAndRm is the session-mode
// round trip for M7's whole point: ls/search stay accurate as the vault
// changes underneath them, entirely through the real CLI/REPL (no
// library shortcuts), and reindex still works even though there's
// nothing stale to fix.
func TestSessionLsSearchReindexSeeInsertRenameAndRm(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"insert AWS",
		"correcthorsebatterystaple",
		"ls",
		"search AWS",
		"reindex",
		"ls",
		"rename AWS Amazon",
		"ls",
		"search Amazon",
		"rm Amazon",
		"ls",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d; stderr=%s\nstdout=%s", res.Code, res.Stderr, res.Stdout)
	}
	if got := countPassphrasePrompts(res.Stdout + res.Stderr); got != 1 {
		t.Errorf("asked for a passphrase %d times in one session, want exactly 1", got)
	}

	out := res.Stdout
	if strings.Count(out, "AWS") == 0 {
		t.Errorf("ls/search never showed the inserted entry as AWS:\n%s", out)
	}
	if !strings.Contains(out, "Amazon") {
		t.Errorf("ls/search never showed the renamed entry as Amazon:\n%s", out)
	}
	if !strings.Contains(out, "index rebuilt") {
		t.Errorf("reindex didn't report rebuilding:\n%s", out)
	}

	// The final ls, after `rm Amazon`, must show nothing — find it by
	// taking everything after the last "removed" line cmd/gage prints.
	if i := strings.LastIndex(out, "gage: removed"); i >= 0 {
		tail := out[i:]
		if strings.Contains(tail, "Amazon") {
			t.Errorf("ls after rm still lists the removed entry:\n%s", tail)
		}
	} else {
		t.Errorf("never saw rm's confirmation line:\n%s", out)
	}
}

// TestSessionEditUpdatesIndexWithoutReindex is edit's half of
// "insert/edit/rm update the in-memory index incrementally": a session
// that already listed a vault sees a $EDITOR-driven title change in its
// very next `ls`, with no `reindex` in between.
func TestSessionEditUpdatesIndexWithoutReindex(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "ProtonMail", "correcthorsebatterystaple")

	setFakeEditor(t, "set-title", map[string]string{"GAGE_TEST_FAKE_EDITOR_TITLE": "Proton Mail"})

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"ls",
		"edit ProtonMail",
		"ls",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d; stderr=%s\nstdout=%s", res.Code, res.Stderr, res.Stdout)
	}

	if i := strings.LastIndex(res.Stdout, "gage: updated"); i >= 0 {
		tail := res.Stdout[i:]
		if !strings.Contains(tail, "Proton Mail") {
			t.Errorf("ls after edit doesn't show the renamed title:\n%s", tail)
		}
		if strings.Contains(tail, "ProtonMail\n") || strings.Contains(tail, "ProtonMail ") {
			t.Errorf("ls after edit still shows the old title:\n%s", tail)
		}
	} else {
		t.Errorf("never saw edit's confirmation line:\n%s", res.Stdout)
	}
}

// TestReindexOneShotHasNothingToRebuild: one-shot mode never caches, so
// `gage reindex` there just confirms the vault resolves rather than
// silently doing nothing or failing as an unknown command.
func TestReindexOneShotHasNothingToRebuild(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runCLI(t, []string{"reindex"}, "")
	if res.Code != 0 {
		t.Fatalf("reindex: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "nothing to reindex") {
		t.Errorf("one-shot reindex output = %q, want it to say there's nothing to reindex", res.Stdout)
	}

	// A never-registered vault still fails cleanly rather than silently
	// succeeding.
	bad := runCLI(t, []string{"reindex", "--use", "no-such-vault"}, "")
	if bad.Code == 0 {
		t.Error("reindex --use <unregistered vault> succeeded, want a failure")
	}
}
