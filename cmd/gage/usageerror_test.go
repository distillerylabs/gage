package main

import (
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// TestWrongArgCountShowsCommandUsage is the regression test for the bug
// this file exists to fix: `gage edit` with no query used to print only
// Cobra's bare "gage: accepts 1 arg(s), received 0" and leave an operator
// to guess what was expected. It must now also carry that command's own
// usage block, the same text `gage help edit` renders, folded into the
// one error message writeError prints — see usageError.
func TestWrongArgCountShowsCommandUsage(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"edit"}, "")

	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "accepts 1 arg(s), received 0") {
		t.Errorf("stderr missing Cobra's original complaint:\n%s", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Usage:\n  gage edit <query>") {
		t.Errorf("stderr missing edit's own usage block:\n%s", res.Stderr)
	}
	// The extra help is appended after the error, not printed to stdout —
	// stdout must stay empty so a script piping stdout elsewhere never
	// sees it.
	if res.Stdout != "" {
		t.Errorf("wrong-arg-count error wrote to stdout: %q", res.Stdout)
	}
}

// TestUnknownFlagShowsCommandUsage covers the other Cobra-native
// rejection this fix has to catch, not just the arg-count one: an unknown
// flag on a real subcommand.
func TestUnknownFlagShowsCommandUsage(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"ls", "--no-such-flag"}, "")

	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Usage:\n  gage ls") {
		t.Errorf("stderr missing ls's own usage block:\n%s", res.Stderr)
	}
}

// TestUnknownCommandShowsFullHelpListing is the top-level case: a
// rejection that never resolved to any real subcommand gets the same
// grouped listing gage --help renders, not a lone command's usage (there
// is no single command to show one for).
func TestUnknownCommandShowsFullHelpListing(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"frobnicate"}, "")

	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, `Use "gage help <command>"`) {
		t.Errorf("stderr missing the grouped listing's closing line:\n%s", res.Stderr)
	}
}

// TestCodedApplicationErrorsDoNotGrowUsageText draws the line the fix
// deliberately stops at: a real application failure (an entry that
// doesn't exist) already opted into an exit code and its own message via
// exitcode.New/Wrap, and must not gain a bolted-on usage block the way a
// genuine Cobra parsing rejection does.
func TestCodedApplicationErrorsDoNotGrowUsageText(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("seeding: %s", res.Stderr)
	}

	res := runCLI(t, []string{"show", "nosuchentry"}, "")

	if res.Code == 0 {
		t.Fatal("show of a nonexistent entry unexpectedly succeeded")
	}
	if strings.Contains(res.Stderr, "Usage:") {
		t.Errorf("a coded application error grew a usage block it shouldn't have:\n%s", res.Stderr)
	}
}

// TestSessionWrongArgCountShowsCommandUsage is the session/script-mode
// half of the same fix: a line typed inside a session (or read via
// --stdin) goes through runSessionCommand, a separate dispatch path from
// one-shot's runApp, and it has to fold in the same usage text rather
// than leaving the REPL's bare "accepts 1 arg(s)" the one-shot fix
// doesn't reach.
func TestSessionWrongArgCountShowsCommandUsage(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("seeding: %s", res.Stderr)
	}

	res := runCLI(t, []string{"--stdin"}, "edit\n")

	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "accepts 1 arg(s), received 0") {
		t.Errorf("stderr missing Cobra's original complaint:\n%s", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Usage:\n  gage edit <query>") {
		t.Errorf("stderr missing edit's own usage block:\n%s", res.Stderr)
	}
}
