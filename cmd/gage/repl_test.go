package main

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chzyer/readline"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/config"
)

// runSessionScript drives a whole session the way a human would: bare
// `gage` with a TTY on stdin (so dispatch reaches the REPL) but an
// in-memory reader for the lines themselves, which is what keeps the
// REPL testable without a pty — see newSessionLineReader.
//
// The prompter is the real terminalPrompter, over the same script, so
// passphrases and candidate-picker answers are typed lines exactly as
// they are for a human. A script line is consumed by whatever asks
// next: after `use personal` that's the passphrase prompt.
func runSessionScript(t *testing.T, script string) cliResult {
	t.Helper()
	return runSessionScriptWithClock(t, script, nil)
}

// runSessionScriptWithClock is runSessionScript with the session's idle
// clock injected. A nil now means the real one. See stepClock for why a
// scripted session can't use real elapsed time.
func runSessionScriptWithClock(t *testing.T, script string, now func() time.Time) cliResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	in := strings.NewReader(script)
	app := &App{
		Out:        &stdout,
		Err:        &stderr,
		In:         in,
		Build:      BuildInfo{Version: "v1.2.3", Commit: "abcdef1"},
		IsTerminal: func() bool { return true },
		Now:        now,
	}
	app.Prompter = newTerminalPrompter(in, &stderr)
	code := runApp(app, []string{})
	return cliResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

// script joins lines into stdin content, so tests read as transcripts.
func script(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// insertEntry adds one entry to a vault through the real one-shot CLI,
// so session tests start from a vault with something in it.
func insertEntry(t *testing.T, vault, title, value string) {
	t.Helper()
	res, _ := runCLIWithValue(t, []string{"insert", "--use", vault, title}, value)
	if res.Code != 0 {
		t.Fatalf("insert %q: %s", title, res.Stderr)
	}
}

// countPassphrasePrompts counts how many times a transcript asked for a
// passphrase — the number every "unlocked once" assertion here is really
// about.
func countPassphrasePrompts(out string) int {
	return strings.Count(out, "Enter passphrase for vault")
}

// TestSessionUnlocksOnceForManyCommands is the milestone's headline
// behavior end to end through the REPL: one passphrase, several
// commands.
func TestSessionUnlocksOnceForManyCommands(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "ProtonMail", "correcthorsebatterystaple")
	insertEntry(t, "personal", "Chase", "hunter2")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"show ProtonMail",
		"show Chase",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "correcthorsebatterystaple") || !strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("session didn't print both values:\n%s", res.Stdout)
	}
	if got := countPassphrasePrompts(res.Stdout + res.Stderr); got != 1 {
		t.Errorf("asked for a passphrase %d times in one session, want exactly 1:\n%s", got, res.Stdout)
	}
}

// TestSessionLockThenUseRePrompts covers the REPL side of `lock`: the
// key is gone, the session isn't, and the next `use` asks again.
func TestSessionLockThenUseRePrompts(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "ProtonMail", "correcthorsebatterystaple")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"lock personal",
		"use personal",
		testPassphrase,
		"show ProtonMail",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "personal locked.") {
		t.Errorf("lock printed no confirmation:\n%s", res.Stdout)
	}
	if got := countPassphrasePrompts(res.Stdout + res.Stderr); got != 2 {
		t.Errorf("asked for a passphrase %d times across lock/use, want 2:\n%s", got, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "correcthorsebatterystaple") {
		t.Errorf("the re-unlocked session couldn't read its entry:\n%s", res.Stdout)
	}
}

// TestSessionStatusListsTouchedVaults is `status`'s rendering over a
// two-vault session with one of them locked.
func TestSessionStatusListsTouchedVaults(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"use work",
		testPassphrase,
		"lock personal",
		"status",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	var personal, work string
	for _, line := range strings.Split(res.Stdout, "\n") {
		switch {
		case strings.Contains(line, "personal") && strings.Contains(line, "lock"):
			personal = line
		case strings.Contains(line, "work") && strings.Contains(line, "lock"):
			work = line
		}
	}
	if !strings.Contains(personal, "locked") || strings.Contains(personal, "unlocked") {
		t.Errorf("status line for personal = %q, want it locked", personal)
	}
	if !strings.Contains(work, "unlocked") {
		t.Errorf("status line for work = %q, want it unlocked", work)
	}
	if !strings.HasPrefix(strings.TrimRight(work, "\r"), "* ") {
		t.Errorf("status didn't mark work as the current vault: %q", work)
	}
}

// TestSessionAdHocUseFlagDoesNotChangeCurrentVault is the `-u|--use
// NAME` path inside a session, end to end: it unlocks the named vault,
// operates against it, and leaves the current vault alone.
func TestSessionAdHocUseFlagDoesNotChangeCurrentVault(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")
	insertEntry(t, "personal", "Personal Entry", "personal-value")
	insertEntry(t, "work", "Work Entry", "work-value")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"show --use work \"Work Entry\"",
		testPassphrase,
		"show \"Personal Entry\"",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "work-value") {
		t.Errorf("--use work didn't reach the work vault:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "personal-value") {
		t.Errorf("the next bare command didn't go back to personal:\n%s", res.Stdout)
	}
	if got := countPassphrasePrompts(res.Stdout + res.Stderr); got != 2 {
		t.Errorf("asked for a passphrase %d times, want 2 (one per vault):\n%s", got, res.Stdout)
	}
}

// TestSessionAmbiguousQueryPrompts is the session half of M5's
// ambiguity handling, driven through the real prompter: the candidate
// list is rendered as the design doc's numbered picker and the typed
// number selects an entry, where one-shot mode fails with the same list.
func TestSessionAmbiguousQueryPrompts(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "AWS root account", "root-secret")
	insertEntry(t, "personal", "AWS IAM backup admin", "iam-secret")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"show AWS",
		"2",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "Multiple entries match") {
		t.Errorf("no candidate list was rendered:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "Which one? [1-2]:") {
		t.Errorf("no numbered picker prompt was rendered:\n%s", res.Stdout)
	}
	// Candidates are listed sorted by title, so 2 is "AWS root account".
	if !strings.Contains(res.Stdout, "root-secret") {
		t.Errorf("the picked entry's value wasn't printed:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, "iam-secret") {
		t.Errorf("an entry that wasn't picked was printed:\n%s", res.Stdout)
	}
}

// TestSessionRmResolvesAmbiguityThroughThePicker: rm resolves its query
// inside the library rather than through resolveQuery, so it needs its
// own proof that session mode still prompts rather than failing — the
// asymmetry mutationQuery exists to close.
func TestSessionRmResolvesAmbiguityThroughThePicker(t *testing.T) {
	isolateXDG(t)
	vaultPath := initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "AWS root account", "root-secret")
	insertEntry(t, "personal", "AWS IAM backup admin", "iam-secret")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"rm AWS",
		"1",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "Which one? [1-2]:") {
		t.Errorf("rm didn't offer the picker on an ambiguous query:\n%s\n%s", res.Stdout, res.Stderr)
	}

	des, err := os.ReadDir(filepath.Join(vaultPath, "entries"))
	if err != nil {
		t.Fatal(err)
	}
	if len(des) != 1 {
		t.Errorf("entries/ holds %d files after rm, want 1", len(des))
	}
}

// TestREPLClosesEveryHeldIdentityOnExit drives the loop directly over a
// Session the test itself holds, which is the only way to look at the
// Identity afterwards. `exit` must leave nothing unlocked.
func TestREPLClosesEveryHeldIdentityOnExit(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	var out bytes.Buffer
	app := &App{
		Out:        &out,
		Err:        &out,
		In:         strings.NewReader(""),
		IsTerminal: func() bool { return true },
		Prompter:   &fakePrompter{passphrases: []string{testPassphrase}},
	}
	sess := gage.NewSession(gage.SessionConfig{
		Open:     openSessionVault,
		Prompter: app.Prompter,
	})
	app.Session = sess

	_, ident, err := sess.Vault("personal")
	if err != nil {
		t.Fatalf("unlocking for the test: %v", err)
	}
	if ident.Closed() {
		t.Fatal("identity reports itself closed before the session ran")
	}

	r := &repl{
		app:      app,
		sess:     sess,
		lr:       newPlainLineReader(strings.NewReader(script("exit")), &out),
		hist:     &history{},
		settings: shellSettings{prompt: defaultPromptTemplate},
	}
	if err := r.run(); err != nil {
		t.Fatalf("repl.run: %v", err)
	}
	if !ident.Closed() {
		t.Error("the REPL exited while still holding an unlocked identity")
	}
}

// TestREPLEOFTerminatesLikeExit: Ctrl-D (here, end of input) is the same
// clean exit `exit` is, keys dropped the same way.
func TestREPLEOFTerminatesLikeExit(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	var out bytes.Buffer
	app := &App{
		Out:        &out,
		Err:        &out,
		In:         strings.NewReader(""),
		IsTerminal: func() bool { return true },
		Prompter:   &fakePrompter{passphrases: []string{testPassphrase}},
	}
	sess := gage.NewSession(gage.SessionConfig{Open: openSessionVault, Prompter: app.Prompter})
	app.Session = sess
	_, ident, err := sess.Vault("personal")
	if err != nil {
		t.Fatal(err)
	}

	r := &repl{
		app:      app,
		sess:     sess,
		lr:       newPlainLineReader(strings.NewReader(""), &out),
		hist:     &history{},
		settings: shellSettings{prompt: defaultPromptTemplate},
	}
	if err := r.run(); err != nil {
		t.Fatalf("repl.run on immediate EOF: %v", err)
	}
	if !ident.Closed() {
		t.Error("EOF ended the REPL while still holding an unlocked identity")
	}
}

// TestSessionHistoryRecordsCommandsNeverValues is the design doc's
// "Command history must never contain plaintext": the typed query is
// fine (you typed it), the decrypted value never is.
func TestSessionHistoryRecordsCommandsNeverValues(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "ProtonMail", "correcthorsebatterystaple")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"show ProtonMail",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "correcthorsebatterystaple") {
		t.Fatalf("the session never printed the value, so this test would pass vacuously:\n%s", res.Stdout)
	}

	data := readHistoryForTest(t)
	if !strings.Contains(data, "show ProtonMail") {
		t.Errorf("history is missing the typed command line:\n%s", data)
	}
	if !strings.Contains(data, "use personal") {
		t.Errorf("history is missing the typed `use` line:\n%s", data)
	}
	if strings.Contains(data, "correcthorsebatterystaple") {
		t.Errorf("history contains a decrypted value:\n%s", data)
	}
	if strings.Contains(data, testPassphrase) {
		t.Errorf("history contains the passphrase typed at the unlock prompt:\n%s", data)
	}
}

// TestSessionHistoryFileIsCreated0600: the file records what a human
// typed at a gage prompt and is nobody else's business on the machine.
func TestSessionHistoryFileIsCreated0600(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	if res := runSessionScript(t, script("status", "exit")); res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	path := historyPathForTest(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("history file was never created: %v", err)
	}
	// Windows has no POSIX mode bits to assert; the rest of this suite
	// takes the same shape (see atomicfile's own mode test).
	if runtime.GOOS == "windows" {
		return
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("history file mode = %v, want 0600", got)
	}
}

func historyPathForTest(t *testing.T) string {
	t.Helper()
	g, err := readGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := resolveShellSettings(g.Shell)
	if err != nil {
		t.Fatal(err)
	}
	return settings.historyFile
}

func readHistoryForTest(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(historyPathForTest(t))
	if err != nil {
		t.Fatalf("reading history: %v", err)
	}
	return string(data)
}

// setShellConfig rewrites just the [shell] table of the global config a
// test has already built with `gage init`.
func setShellConfig(t *testing.T, sh config.Shell) {
	t.Helper()
	g, err := readGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	g.Shell = sh
	if err := writeGlobalConfig(g); err != nil {
		t.Fatal(err)
	}
}

// TestSessionPromptRendersTokensAndTracksLockState: the prompt is
// rendered from [shell].prompt, and its lock indicator follows the
// session's actual state rather than being drawn once at startup.
func TestSessionPromptRendersTokensAndTracksLockState(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	setShellConfig(t, config.Shell{Prompt: "[{vault}{lock}{dirty}] gage> "})

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"lock personal",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	// The vault is `current` in global config from init, so it renders in
	// the prompt from the first line — locked, then unlocked, then
	// locked again.
	wantOrder := []string{
		"[personal" + lockedGlyph + "] gage> ",
		"[personal" + unlockedGlyph + "] gage> ",
		"[personal" + lockedGlyph + "] gage> ",
	}
	rest := res.Stdout
	for _, want := range wantOrder {
		i := strings.Index(rest, want)
		if i < 0 {
			t.Fatalf("prompt %q never appeared (in order) in:\n%s", want, res.Stdout)
		}
		rest = rest[i+len(want):]
	}
}

// TestRenderPromptTokens covers the token substitution itself, including
// the states a scripted session can't easily reach.
func TestRenderPromptTokens(t *testing.T) {
	cases := []struct {
		name     string
		template string
		state    promptState
		want     string
	}{
		{
			name:     "no vault collapses the default template to a bare prompt",
			template: defaultPromptTemplate,
			state:    promptState{},
			want:     "gage> ",
		},
		{
			name:     "unlocked vault",
			template: defaultPromptTemplate,
			state:    promptState{vault: "personal", unlocked: true},
			want:     "personal" + unlockedGlyph + " gage> ",
		},
		{
			name:     "locked vault",
			template: "[{vault}{lock}] gage> ",
			state:    promptState{vault: "personal"},
			want:     "[personal" + lockedGlyph + "] gage> ",
		},
		{
			name:     "dirty marker",
			template: "{vault}{dirty}> ",
			state:    promptState{vault: "personal", unlocked: true, dirty: true},
			want:     "personal*> ",
		},
		{
			name:     "clean vault renders no dirty marker",
			template: "{vault}{dirty}> ",
			state:    promptState{vault: "personal", unlocked: true},
			want:     "personal> ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderPrompt(tc.template, tc.state); got != tc.want {
				t.Errorf("renderPrompt = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUnknownSessionCommandReportsAndContinues: a typo must not end a
// session, and must point at the surface that lists what's available.
func TestUnknownSessionCommandReportsAndContinues(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runSessionScript(t, script("nope", "status", "exit"))
	if res.Code != 0 {
		t.Fatalf("an unknown command ended the session with code %d; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "unknown command") {
		t.Errorf("stderr didn't report the unknown command: %q", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "help") {
		t.Errorf("stderr didn't point at `help`: %q", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "no vaults have been unlocked") {
		t.Errorf("the session didn't survive to run the next command:\n%s", res.Stdout)
	}
}

// stepClock is a clock that advances a fixed step every time it is
// read, which is how a scripted session gets simulated idle time without
// a hook between its commands.
//
// Reading advances it because there is nowhere else to put the advance:
// the session is driven end to end through runApp, so a test has no
// moment between two typed lines at which to move a clock by hand. Any
// two commands therefore see at least one step of elapsed time, which is
// all a wiring test needs. The library's own idle-timeout tests
// (internal/gage) drive a clock they move explicitly, and that is where
// the boundary behavior — just inside the window versus just past it —
// is pinned.
type stepClock struct {
	t    time.Time
	step time.Duration
}

func (c *stepClock) now() time.Time {
	c.t = c.t.Add(c.step)
	return c.t
}

// TestConfiguredIdleTimeoutReachesTheSession proves the [shell] setting
// is actually wired to the Session's re-lock, rather than being parsed
// and dropped.
//
// The clock is injected rather than real. An earlier version of this
// test configured "1ns" on the theory that any real elapsed time would
// exceed it, and it failed on Windows: time.Now there advances in steps
// of up to ~15ms, so `use personal` and the command after it read the
// identical instant and nothing ever aged out. Injecting the clock also
// lets the configured value be a realistic 10m rather than a duration
// chosen to be beneath the clock's resolution — so this now checks that
// the configured timeout reaches the session, where "1ns" would have
// passed against any hardcoded tiny value.
func TestConfiguredIdleTimeoutReachesTheSession(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "ProtonMail", "correcthorsebatterystaple")
	setShellConfig(t, config.Shell{IdleTimeout: "10m"})

	clock := &stepClock{t: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), step: time.Hour}
	res := runSessionScriptWithClock(t, script(
		"use personal",
		testPassphrase,
		"status",
		"show ProtonMail",
		testPassphrase,
		"exit",
	), clock.now)
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "locked") || strings.Contains(res.Stdout, "unlocked") {
		t.Errorf("status didn't report the vault as re-locked by the idle timeout:\n%s", res.Stdout)
	}
	if got := countPassphrasePrompts(res.Stdout + res.Stderr); got != 2 {
		t.Errorf("asked for a passphrase %d times, want 2 (the idle re-lock forces a second):\n%s", got, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "correcthorsebatterystaple") {
		t.Errorf("the re-unlocked command didn't complete:\n%s", res.Stdout)
	}
}

// TestConfiguredIdleTimeoutIsNotAppliedTooEagerly is the other half, and
// the reason the test above can't stand alone: with a step well under
// the configured timeout, nothing ages out and one passphrase covers the
// whole session. Without this, a build that re-locked on every command
// regardless of the timeout would satisfy the test above.
func TestConfiguredIdleTimeoutIsNotAppliedTooEagerly(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "ProtonMail", "correcthorsebatterystaple")
	setShellConfig(t, config.Shell{IdleTimeout: "10m"})

	clock := &stepClock{t: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), step: time.Second}
	res := runSessionScriptWithClock(t, script(
		"use personal",
		testPassphrase,
		"status",
		"show ProtonMail",
		"exit",
	), clock.now)
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "unlocked") {
		t.Errorf("the vault re-locked well inside its idle timeout:\n%s", res.Stdout)
	}
	if got := countPassphrasePrompts(res.Stdout + res.Stderr); got != 1 {
		t.Errorf("asked for a passphrase %d times inside the idle timeout, want 1:\n%s", got, res.Stdout)
	}
}

// TestStdinReadingInsertModesAreRefusedInASession: -m and --value-stdin
// read to EOF, and in a session that stream is the session's own input.
// Refusing beats "the entry was inserted and your session vanished".
func TestStdinReadingInsertModesAreRefusedInASession(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	for _, flag := range []string{"-m", "--value-stdin"} {
		t.Run(flag, func(t *testing.T) {
			res := runSessionScript(t, script(
				"use personal",
				testPassphrase,
				"insert "+flag+" Notes",
				"status",
				"exit",
			))
			if res.Code != 0 {
				t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
			}
			if !strings.Contains(res.Stderr, "session's own input") {
				t.Errorf("%s wasn't refused with an explanation: %q", flag, res.Stderr)
			}
			if !strings.Contains(res.Stdout, "unlocked") {
				t.Errorf("the session didn't survive to run the next command:\n%s", res.Stdout)
			}
		})
	}
}

// TestREPLIsAThinWiringLayer is the one wiring test the M6 plan calls
// for: a typed line is parsed into the corresponding Session call, and
// the result is rendered. Session's own behavior is tested in
// internal/gage; what's asserted here is only that the line reaches it.
func TestREPLIsAThinWiringLayer(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")

	var out bytes.Buffer
	app := &App{
		Out:        &out,
		Err:        &out,
		In:         strings.NewReader(""),
		IsTerminal: func() bool { return true },
		Prompter:   &fakePrompter{passphrases: []string{testPassphrase}},
	}
	sess := gage.NewSession(gage.SessionConfig{Open: openSessionVault, Prompter: app.Prompter})
	app.Session = sess

	// `use work` -> Session.Use("work")
	if quit, err := execSessionLine(app, "use work"); err != nil {
		t.Fatalf("`use work`: %v", err)
	} else if quit {
		t.Fatal("`use` ended the session")
	}
	if got := sess.Current(); got != "work" {
		t.Errorf("after `use work`, Session.Current() = %q, want work", got)
	}

	// `lock work` -> Session.Lock("work"), and the result is rendered.
	if quit, err := execSessionLine(app, "lock work"); err != nil {
		t.Fatalf("`lock work`: %v", err)
	} else if quit {
		t.Fatal("`lock` ended the session")
	}
	if st := sess.Status(); len(st) != 1 || st[0].Unlocked {
		t.Errorf("after `lock work`, Status() = %+v, want work locked", st)
	}
	if !strings.Contains(out.String(), "work locked.") {
		t.Errorf("`lock work` rendered nothing:\n%s", out.String())
	}

	// `exit` -> quit, without having been dispatched anywhere else.
	if quit, err := execSessionLine(app, "exit"); err != nil || !quit {
		t.Errorf("`exit` didn't end the session (quit=%v err=%v)", quit, err)
	}
	if quit, err := execSessionLine(app, "quit"); err != nil || !quit {
		t.Errorf("`quit`, exit's alias, didn't end the session (quit=%v err=%v)", quit, err)
	}
}

// TestSplitLineHandlesQuotesAndBackslashes: entry titles have spaces in
// them, and Windows paths have backslashes in them. Both are typed at
// this prompt.
func TestSplitLineHandlesQuotesAndBackslashes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`show ProtonMail`, []string{"show", "ProtonMail"}},
		{`  show   ProtonMail  `, []string{"show", "ProtonMail"}},
		{`show "AWS root account"`, []string{"show", "AWS root account"}},
		{`show 'AWS root account'`, []string{"show", "AWS root account"}},
		{`rename "old name" "new name"`, []string{"rename", "old name", "new name"}},
		{`insert C:\keys\prod`, []string{"insert", `C:\keys\prod`}},
		{`show --use work Entry`, []string{"show", "--use", "work", "Entry"}},
		{``, nil},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := splitLine(tc.in)
			if err != nil {
				t.Fatalf("splitLine(%q): %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("splitLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("splitLine(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestSplitLineRejectsAnUnbalancedQuote(t *testing.T) {
	if _, err := splitLine(`show "unterminated`); err == nil {
		t.Error("an unbalanced quote was accepted")
	}
}

// interruptOnceReader is a lineReader whose first read is a Ctrl-C.
type interruptOnceReader struct {
	inner     lineReader
	delivered bool
}

func (r *interruptOnceReader) readLine(prompt string) (string, error) {
	if !r.delivered {
		r.delivered = true
		return "", errInterrupted
	}
	return r.inner.readLine(prompt)
}

func (r *interruptOnceReader) readSecret(p string) (string, error) { return r.inner.readSecret(p) }
func (r *interruptOnceReader) readPlain(p string) (string, error)  { return r.inner.readPlain(p) }
func (r *interruptOnceReader) close() error                        { return r.inner.close() }

// TestCtrlCAbandonsTheLineNotTheSession: Ctrl-C is how a shell's user
// says "forget what I was typing", and it must not drop their unlocked
// vaults with it. Ctrl-D (EOF) is the one that ends a session.
func TestCtrlCAbandonsTheLineNotTheSession(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	var out bytes.Buffer
	app := &App{
		Out:        &out,
		Err:        &out,
		In:         strings.NewReader(""),
		IsTerminal: func() bool { return true },
		Prompter:   &fakePrompter{passphrases: []string{testPassphrase}},
	}
	sess := gage.NewSession(gage.SessionConfig{Open: openSessionVault, Prompter: app.Prompter})
	app.Session = sess

	r := &repl{
		app:      app,
		sess:     sess,
		lr:       &interruptOnceReader{inner: newPlainLineReader(strings.NewReader(script("status", "exit")), &out)},
		hist:     &history{},
		settings: shellSettings{prompt: defaultPromptTemplate},
	}
	if err := r.run(); err != nil {
		t.Fatalf("repl.run: %v", err)
	}
	if !strings.Contains(out.String(), "no vaults have been unlocked") {
		t.Errorf("the session didn't survive Ctrl-C to run the next command:\n%s", out.String())
	}
}

// TestOneShotOnlyCommandsReportPlainlyInSession: init and clone create a
// vault rather than operating on one, so they're one-shot only — and say
// so rather than failing as unknown commands.
func TestOneShotOnlyCommandsReportPlainlyInSession(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runSessionScript(t, script("init another", "status", "exit"))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "one-shot command") {
		t.Errorf("init in a session didn't report itself as one-shot only: %q", res.Stderr)
	}
	if strings.Contains(res.Stderr, "unknown command") {
		t.Errorf("init in a session failed as an unknown command: %q", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "no vaults have been unlocked") {
		t.Errorf("the session didn't survive the rejected command:\n%s", res.Stdout)
	}
}

// TestSessionLockWithNothingUnlockedSaysSo: a bare `lock` over a session
// that is holding no keys reports that rather than claiming to have
// locked something. The distinction is the whole reason sessionLock
// counts what was actually unlocked instead of echoing its arguments.
func TestSessionLockWithNothingUnlockedSaysSo(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runSessionScript(t, script(
		"lock",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "no vaults are unlocked in this session") {
		t.Errorf("a bare lock over an empty session said nothing useful:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, "locked. run `use") {
		t.Errorf("lock claimed to have locked a vault it never held:\n%s", res.Stdout)
	}
}

// TestSessionCommandArgumentCountsAreChecked: each of the three session
// verbs that takes a fixed number of arguments reports a usage error
// rather than misinterpreting the extras. The session keeps running
// afterwards — a typo is not a reason to drop the keys — which is what
// the trailing `status` line checks.
func TestSessionCommandArgumentCountsAreChecked(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	cases := []struct {
		name string
		line string
		want string
	}{
		{"use with no argument", "use", "usage: use <vault>"},
		{"use with two arguments", "use personal work", "usage: use <vault>"},
		{"lock with two arguments", "lock personal work", "usage: lock [vault]"},
		{"status with an argument", "status extra", "usage: status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runSessionScript(t, script(tc.line, "status", "exit"))

			// The session itself still exits cleanly: a bad command line
			// inside a session is reported, not fatal.
			if res.Code != 0 {
				t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
			}
			if !strings.Contains(res.Stderr, tc.want) {
				t.Errorf("stderr doesn't carry %q:\n%s", tc.want, res.Stderr)
			}
			// And it kept going rather than dropping out at the error.
			if !strings.Contains(res.Stdout, "no vaults have been unlocked") {
				t.Errorf("the session stopped at the usage error instead of continuing:\n%s", res.Stdout)
			}
		})
	}
}

// TestSessionSkipsBlankLines: pressing Enter at the prompt, or typing
// only spaces, is not a command — it must not reach the dispatcher and
// report "unknown command".
func TestSessionSkipsBlankLines(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	res := runSessionScript(t, script(
		"",
		"   ",
		"\t",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stderr, "unknown command") {
		t.Errorf("a blank line was dispatched as a command:\n%s", res.Stderr)
	}
}

// TestSessionBareLockLocksEverythingItHolds is the other half of
// TestSessionLockWithNothingUnlockedSaysSo: with keys actually held, a
// bare `lock` names each vault it locked. It reports what was holding a
// key rather than echoing its arguments, which is what lets it be
// accurate about a session where some vaults are already locked.
func TestSessionBareLockLocksEverythingItHolds(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")

	res := runSessionScript(t, script(
		"use personal",
		testPassphrase,
		"use work",
		testPassphrase,
		"lock",
		"status",
		"exit",
	))
	if res.Code != 0 {
		t.Fatalf("session exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	for _, name := range []string{"personal", "work"} {
		if !strings.Contains(res.Stdout, name+" locked.") {
			t.Errorf("a bare lock didn't report locking %q:\n%s", name, res.Stdout)
		}
	}
	// And a second bare lock now has nothing left to lock.
	if strings.Count(res.Stdout, "locked. run `use") != 2 {
		t.Errorf("a bare lock reported a number of vaults other than the two it held:\n%s", res.Stdout)
	}
}

// TestHistoryIgnoresBlankLinesAndUnreadableFiles: history is a
// convenience, so neither a blank line nor a file it can't parse is
// allowed to become an error in the middle of a working session.
func TestHistoryIgnoresBlankLinesAndUnreadableFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	h, err := openHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.close() })

	// A blank line — Enter at the prompt — is not a command and is not
	// recorded.
	for _, blank := range []string{"", "   ", "\t"} {
		if err := h.add(blank); err != nil {
			t.Errorf("history.add(%q) = %v, want nil", blank, err)
		}
	}
	if got := h.lines(); len(got) != 0 {
		t.Errorf("history recorded %v, want nothing for blank input", got)
	}

	// A real line is recorded, so the check above isn't passing because
	// nothing is ever written.
	if err := h.add("ls"); err != nil {
		t.Fatal(err)
	}
	if got := h.lines(); len(got) != 1 || got[0] != "ls" {
		t.Errorf("history = %v, want exactly [ls]", got)
	}

	// A line past what bufio.Scanner will buffer makes the read fail;
	// lines() answers "no history" rather than propagating it, since a
	// session must start either way.
	huge := append(bytes.Repeat([]byte("a"), bufio.MaxScanTokenSize+1), '\n')
	if err := os.WriteFile(path, huge, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := h.lines(); got != nil {
		t.Errorf("history.lines() on an unreadable file = %v, want nil", got)
	}
}

// TestMapReadlineErrTranslatesCtrlC: a Ctrl-C inside a prompt is the
// same "abandon this, keep the session" signal it is at the command
// prompt, and every other error has to pass through unchanged so a real
// failure isn't silently read as a cancellation.
func TestMapReadlineErrTranslatesCtrlC(t *testing.T) {
	if got := mapReadlineErr(readline.ErrInterrupt); !errors.Is(got, errInterrupted) {
		t.Errorf("mapReadlineErr(ErrInterrupt) = %v, want errInterrupted", got)
	}
	other := errors.New("the terminal went away")
	if got := mapReadlineErr(other); !errors.Is(got, other) {
		t.Errorf("mapReadlineErr(%v) = %v, want it unchanged", other, got)
	}
	if got := mapReadlineErr(nil); got != nil {
		t.Errorf("mapReadlineErr(nil) = %v, want nil", got)
	}
}
