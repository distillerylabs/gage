package main

import (
	"strings"
	"testing"
)

// sessionHelpOutput runs in-session `help` through the real REPL and
// returns what it printed — asserted on the rendered surface rather than
// on renderSessionHelp directly, since "what a session actually shows"
// is the thing Q-HELP-SURFACES is about.
func sessionHelpOutput(t *testing.T, args ...string) cliResult {
	t.Helper()
	isolateXDG(t)
	return runSessionScript(t, script(strings.TrimSpace("help "+strings.Join(args, " ")), "exit"))
}

// sessionEntryNames is entryNames (help_test.go) with the argument
// syntax dropped, so the "use <vault>" line matches the command name
// "use" while a nested name like "vault list" stays intact.
func sessionEntryNames(line string) []string {
	var out []string
	for _, n := range entryNames(line) {
		var kept []string
		for _, f := range strings.Fields(n) {
			if strings.HasPrefix(f, "<") || strings.HasPrefix(f, "[") {
				continue
			}
			kept = append(kept, f)
		}
		if len(kept) > 0 {
			out = append(out, strings.Join(kept, " "))
		}
	}
	return out
}

func listsSessionEntry(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		for _, n := range sessionEntryNames(line) {
			if n == name {
				return true
			}
		}
	}
	return false
}

// TestSessionHelpMatchesRegistryExactly is the both-directions check the
// M6 test list asks for: every session-available command (meta-verbs and
// vault-domain commands alike) is listed, and nothing that isn't
// available in a session leaks in.
func TestSessionHelpMatchesRegistryExactly(t *testing.T) {
	res := sessionHelpOutput(t)
	if res.Code != 0 {
		t.Fatalf("in-session help exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	for _, ci := range registry {
		for _, n := range append([]string{ci.Name}, ci.Aliases...) {
			listed := listsSessionEntry(res.Stdout, n)
			if ci.Availability.SessionVisible() && !listed {
				t.Errorf("session-available command %q is missing from in-session help:\n%s", n, res.Stdout)
			}
			if !ci.Availability.SessionVisible() && listed {
				t.Errorf("one-shot-only command %q appears in in-session help:\n%s", n, res.Stdout)
			}
		}
	}
}

// TestSessionHelpListsTheMetaVerbs pins the specific list the design doc
// names, rather than trusting the registry-derived check above to be
// non-vacuous.
func TestSessionHelpListsTheMetaVerbs(t *testing.T) {
	res := sessionHelpOutput(t)
	for _, name := range []string{"use", "lock", "status", "whoami", "exit", "quit", "help"} {
		if !listsSessionEntry(res.Stdout, name) {
			t.Errorf("in-session help doesn't list the session command %q:\n%s", name, res.Stdout)
		}
	}
}

// TestInitIsNotOfferedInSession: init and clone create a vault rather
// than operating on one, so a session neither lists nor accepts them.
// (clone lands in M8a; init is the one that exists today.)
func TestInitIsNotOfferedInSession(t *testing.T) {
	res := sessionHelpOutput(t)
	if listsSessionEntry(res.Stdout, "init") {
		t.Errorf("in-session help lists init, which is one-shot only:\n%s", res.Stdout)
	}
}

// TestBothHelpSurfacesRenderTheSameRegistry compares the two rendered
// sets rather than two hand-written lists: a command available in both
// modes must appear in both surfaces, with the same description.
func TestBothHelpSurfacesRenderTheSameRegistry(t *testing.T) {
	isolateXDG(t)
	oneShot := runCLI(t, []string{"--help"}, "")
	session := runSessionScript(t, script("help", "exit"))

	both := 0
	for _, ci := range registry {
		if ci.Availability != AvailBoth {
			continue
		}
		both++
		if !listsCommandEntry(oneShot.Stdout, ci.Name) {
			t.Errorf("%q is available in both modes but missing from gage --help:\n%s", ci.Name, oneShot.Stdout)
		}
		if !listsSessionEntry(session.Stdout, ci.Name) {
			t.Errorf("%q is available in both modes but missing from in-session help:\n%s", ci.Name, session.Stdout)
		}
		if !strings.Contains(oneShot.Stdout, ci.Short) {
			t.Errorf("gage --help doesn't carry %q's registry description %q", ci.Name, ci.Short)
		}
		if !strings.Contains(session.Stdout, ci.Short) {
			t.Errorf("in-session help doesn't carry %q's registry description %q", ci.Name, ci.Short)
		}
	}
	if both == 0 {
		t.Fatal("no AvailBoth commands in the registry; this test would pass vacuously")
	}
}

// TestSessionOnlyCommandsNeverLeakIntoOneShotHelp is the other
// direction, checked against the same rendered surfaces.
func TestSessionOnlyCommandsNeverLeakIntoOneShotHelp(t *testing.T) {
	isolateXDG(t)
	oneShot := runCLI(t, []string{"--help"}, "")

	for _, ci := range registry {
		if ci.Availability != AvailSessionOnly {
			continue
		}
		for _, n := range append([]string{ci.Name}, ci.Aliases...) {
			if listsCommandEntry(oneShot.Stdout, n) || listsSessionEntry(oneShot.Stdout, n) {
				t.Errorf("session-only command %q is listed in gage --help:\n%s", n, oneShot.Stdout)
			}
		}
	}
}

// TestSessionHelpLeadsWithBareUse: top-level session help presents vault
// selection as `use <vault>`, not as the -u|--use flag, which belongs in
// per-command help. See Q-HELP-SURFACES.
func TestSessionHelpLeadsWithBareUse(t *testing.T) {
	res := sessionHelpOutput(t)
	if !strings.Contains(res.Stdout, "use <vault>") {
		t.Errorf("in-session help doesn't present vault selection as `use <vault>`:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, "--use") {
		t.Errorf("top-level in-session help mentions the -u|--use flag, which belongs in per-command help:\n%s", res.Stdout)
	}
}

// TestSessionPerCommandHelpListsTheUseFlag is the same rule's other
// half: --use does still work ad hoc in a session, so `help show`
// documents it.
func TestSessionPerCommandHelpListsTheUseFlag(t *testing.T) {
	res := sessionHelpOutput(t, "show")
	if res.Code != 0 {
		t.Fatalf("`help show` exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "--use") {
		t.Errorf("`help show` doesn't document the --use flag:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "-u") {
		t.Errorf("`help show` doesn't document --use's -u shorthand:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "show <query>") {
		t.Errorf("`help show` doesn't print show's usage line:\n%s", res.Stdout)
	}
}

// TestSessionHelpForAMetaVerbPrintsItsUsage: `help use` mirrors `gage
// help <subcommand>`, rendered from the same registry-sourced use line
// the top-level listing shows.
func TestSessionHelpForAMetaVerbPrintsItsUsage(t *testing.T) {
	res := sessionHelpOutput(t, "use")
	if res.Code != 0 {
		t.Fatalf("`help use` exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "use <vault>") {
		t.Errorf("`help use` doesn't print use's usage line:\n%s", res.Stdout)
	}
}

// TestSessionHelpUnknownTopicDoesNotEndTheSession: an unknown topic is a
// usage report, not a reason to drop the human's unlocked session.
func TestSessionHelpUnknownTopicDoesNotEndTheSession(t *testing.T) {
	isolateXDG(t)
	res := runSessionScript(t, script("help not-a-real-command", "status", "exit"))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "unknown help topic") {
		t.Errorf("stderr didn't report the unknown help topic: %q", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "no vaults have been unlocked") {
		t.Errorf("the session didn't survive to run the next command:\n%s", res.Stdout)
	}
}
