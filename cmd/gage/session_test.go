package main

import (
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// TestBareGageOnTTYEntersSessionMode asserts the dispatch-level decision
// from Q-ROOT-CMD: a TTY on stdin means bare `gage` goes to the session
// path, not help. A real pty is used only where the terminal itself is
// the thing under test (see prompter_pty_test.go); the dispatch decision
// isn't, so it's asserted at the dispatch level — the pure decision
// function, and the command tree wired up with a forced-true IsTerminal.
//
// Now that M6 has a session to reach, "reached it" is asserted by the
// REPL's own prompt rather than by a stub's message: stdin is empty, so
// the loop starts, prompts once, reads EOF, and exits cleanly.
func TestBareGageOnTTYEntersSessionMode(t *testing.T) {
	isolateXDG(t)

	if got := chooseRootMode(true); got != "session" {
		t.Errorf("chooseRootMode(true) = %q, want %q", got, "session")
	}

	res := runCLIWithTerminal(t, nil, "", true)
	if res.Code != 0 {
		t.Fatalf("bare gage on a TTY exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stdout, "Usage:") {
		t.Errorf("bare gage on a TTY printed help instead of entering the session: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "gage> ") {
		t.Errorf("bare gage on a TTY didn't reach the REPL prompt: %q", res.Stdout)
	}
}

func TestBareGageWithPipedStdinPrintsHelp(t *testing.T) {
	if got := chooseRootMode(false); got != "help" {
		t.Errorf("chooseRootMode(false) = %q, want %q", got, "help")
	}

	res := runCLIWithTerminal(t, nil, "", false)
	if res.Code != 0 {
		t.Fatalf("bare gage with piped stdin exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "Usage:") {
		t.Errorf("bare gage with piped stdin didn't print help: %q", res.Stdout)
	}
}

// TestSessionOnlyCommandsFailCleanlyOneShot: use/lock/status/exit exist
// as real Cobra commands (so registry completeness holds — see
// registry_test.go) but report plainly that they need a session, rather
// than running or looking like an unknown command, when invoked in
// one-shot mode.
func TestSessionOnlyCommandsFailCleanlyOneShot(t *testing.T) {
	for _, name := range []string{"use", "lock", "status", "exit"} {
		t.Run(name, func(t *testing.T) {
			res := runCLI(t, []string{name}, "")
			if res.Code != int(exitcode.Usage) {
				t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
			}
			if !strings.Contains(res.Stderr, "session") {
				t.Errorf("stderr doesn't explain this is session-only: %q", res.Stderr)
			}
		})
	}
}

// TestNoBareUnenumeratedExitCode: every scenario M0 can actually produce
// must map to a defined exitcode.Code, never a bare, unenumerated
// integer status — in particular never a bare 1, since Conflict (which
// is 1) means something specific nothing in M0 produces.
func TestNoBareUnenumeratedExitCode(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown top-level command", []string{"bogus"}},
		{"unknown help topic", []string{"help", "bogus"}},
		{"session-only command one-shot", []string{"use"}},
	}
	valid := map[int]bool{}
	for _, c := range exitcode.All() {
		valid[int(c)] = true
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runCLI(t, tc.args, "")
			if !valid[res.Code] {
				t.Errorf("exit code %d is not in the taxonomy", res.Code)
			}
			if res.Code == 1 {
				t.Errorf("exit code is a bare 1 (Conflict) for %v, which nothing in this scenario should produce", tc.args)
			}
		})
	}
}
