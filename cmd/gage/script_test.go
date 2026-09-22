package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// writeScript drops a .gage script into the test's temp dir and returns
// its path.
func writeScript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "commands.gage")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return path
}

// runScript runs `gage --script FILE` with a non-terminal stdin — the CI
// shape — and whatever GAGE_PASSPHRASE the test has set.
func runScript(t *testing.T, lines ...string) cliResult {
	t.Helper()
	res, _ := runCLIWithPrompter(t, []string{"--script", writeScript(t, lines...)}, "", false,
		&fakePrompter{passphrases: []string{testPassphrase}})
	return res
}

// runStdinScript pipes the same commands in through --stdin.
func runStdinScript(t *testing.T, lines ...string) cliResult {
	t.Helper()
	res, _ := runCLIWithPrompter(t, []string{"--stdin"}, strings.Join(lines, "\n")+"\n", false,
		&fakePrompter{passphrases: []string{testPassphrase}})
	return res
}

// countingPrompter records how many unlock requests it was handed, which
// is how "unlocking each vault at most once" is asserted.
type countingPrompter struct {
	gage.Prompter
	unlocks map[string]int
}

func (p *countingPrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	if p.unlocks == nil {
		p.unlocks = map[string]int{}
	}
	p.unlocks[req.Vault]++
	return p.Prompter.Unlock(req)
}

// TestScriptRunsCommandsNonInteractively is the mode's basic contract:
// a list of lines runs against one Session, with no prompt anywhere.
func TestScriptRunsCommandsNonInteractively(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runScript(t,
		"use personal",
		"show GitHub",
		"ls",
	)
	if res.Code != 0 {
		t.Fatalf("script exit code = %d: %s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("script did not run `show`:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "GitHub") {
		t.Errorf("script did not run `ls`:\n%s", res.Stdout)
	}
	// No prompt was rendered: a scripted run has nobody to answer one.
	if strings.Contains(res.Stdout, "gage> ") {
		t.Errorf("a scripted session rendered an interactive prompt:\n%s", res.Stdout)
	}
}

// TestStdinRunsCommandsNonInteractively: the same, piped.
func TestStdinRunsCommandsNonInteractively(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runStdinScript(t, "use personal", "show GitHub")
	if res.Code != 0 {
		t.Fatalf("--stdin exit code = %d: %s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("--stdin did not run `show`:\n%s", res.Stdout)
	}
}

// TestScriptUnlocksEachVaultAtMostOnce is the property that makes this
// mode worth having over a shell loop of one-shot invocations: the key
// is held across the whole run, not re-derived per command.
func TestScriptUnlocksEachVaultAtMostOnce(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	insertEntry(t, "personal", "Proton", "hunter3")
	t.Setenv(envPassphraseVar, testPassphrase)

	counter := &countingPrompter{Prompter: &fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t,
		[]string{"--script", writeScript(t, "use personal", "show GitHub", "show Proton", "ls")},
		"", false, counter)
	if res.Code != 0 {
		t.Fatalf("script exit code = %d: %s", res.Code, res.Stderr)
	}
	// GAGE_PASSPHRASE answers the unlock, so the wrapped prompter should
	// see no requests at all — and certainly not one per command.
	if n := counter.unlocks["personal"]; n > 1 {
		t.Errorf("vault unlocked %d times across one script, want at most 1", n)
	}
}

// TestScriptAbortsOnTheFirstFailingCommand: a script has nobody to
// notice a line that failed halfway down, so it stops rather than
// running the rest against a state its author never anticipated.
func TestScriptAbortsOnTheFirstFailingCommand(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runScript(t,
		"use personal",
		"show NoSuchEntry",
		"show GitHub",
	)
	if res.Code != int(exitcode.NotFound) {
		t.Errorf("exit code = %d, want %d (NotFound, from the failing line)", res.Code, exitcode.NotFound)
	}
	if strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("the script continued past a failing line:\n%s", res.Stdout)
	}
	// The failure names where it happened — there is nobody watching the
	// run to have seen which line it was.
	if !strings.Contains(res.Stderr, "commands.gage:2") {
		t.Errorf("the failure does not locate itself in the script: %q", res.Stderr)
	}
}

// TestScriptWritesNothingToTheHistoryFile: the history file records what
// a human typed at a prompt. A script's lines were never typed, and
// mixing a machine's command list into a human's recall buffer would be
// wrong even though none of it is plaintext.
func TestScriptWritesNothingToTheHistoryFile(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, testPassphrase)

	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		t.Skip("no isolated state dir to inspect")
	}
	historyPath := filepath.Join(stateDir, "gage", "history")

	for _, run := range []func() cliResult{
		func() cliResult { return runScript(t, "use personal", "show GitHub") },
		func() cliResult { return runStdinScript(t, "use personal", "show GitHub") },
	} {
		res := run()
		if res.Code != 0 {
			t.Fatalf("script failed: %s", res.Stderr)
		}
		data, err := os.ReadFile(historyPath)
		if err == nil && len(data) > 0 {
			t.Errorf("a non-interactive session wrote to the history file:\n%s", data)
		} else if err != nil && !os.IsNotExist(err) {
			t.Fatalf("reading history file: %v", err)
		}
	}
}

// TestScriptSkipsBlankLinesAndComments so a checked-in .gage file can be
// commented like any other.
func TestScriptSkipsBlankLinesAndComments(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runScript(t,
		"# unlock the vault",
		"",
		"use personal",
		"   ",
		"# and read one secret",
		"show GitHub",
	)
	if res.Code != 0 {
		t.Fatalf("script exit code = %d: %s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("comments or blanks broke the script:\n%s", res.Stdout)
	}
}

// TestGagePassphraseUnlocksWithoutPromptingOrReadingTheCommandStream:
// under --stdin the command stream *is* stdin, so a prompt there would
// eat the next command. The env var is what makes the mode usable at
// all.
func TestGagePassphraseUnlocksWithoutPromptingOrReadingTheCommandStream(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, testPassphrase)

	// The script carries no passphrase line at all: if anything prompted,
	// it would consume `show GitHub` as the answer and the run would
	// fail.
	res := runStdinScript(t, "use personal", "show GitHub")
	if res.Code != 0 {
		t.Fatalf("--stdin with GAGE_PASSPHRASE failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("a prompt appears to have eaten a command line:\n%s\n%s", res.Stdout, res.Stderr)
	}
	if strings.Contains(res.Stderr, "assphrase:") {
		t.Errorf("a passphrase prompt was rendered in a scripted run: %q", res.Stderr)
	}
}

// TestWrongGagePassphraseFailsAfterOneAttempt: the variable gives the
// same answer every time, so retrying it three times would only produce
// the same failure three times more slowly.
func TestWrongGagePassphraseFailsAfterOneAttempt(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, "not the right passphrase")

	counter := &countingPrompter{Prompter: &fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t,
		[]string{"--script", writeScript(t, "use personal", "show GitHub")},
		"", false, counter)

	if res.Code != int(exitcode.LockedOrAuth) {
		t.Errorf("exit code = %d, want %d (LockedOrAuth)", res.Code, exitcode.LockedOrAuth)
	}
	if strings.Contains(res.Stdout, "hunter2") {
		t.Error("a wrong GAGE_PASSPHRASE still decrypted the entry")
	}
	if !strings.Contains(res.Stderr, envPassphraseVar) {
		t.Errorf("the failure does not name the variable at fault: %q", res.Stderr)
	}
}

// TestEnvPrompterAnswersOnceThenRefuses covers the retry policy at the
// unit it lives in, since a CLI run can't distinguish "asked once" from
// "asked three times and gave up".
func TestEnvPrompterAnswersOnceThenRefuses(t *testing.T) {
	p := &envPrompter{Prompter: &fakePrompter{passphrases: []string{testPassphrase}}, passphrase: "from-env"}

	got, err := p.Unlock(gage.UnlockRequest{Purpose: gage.PurposeUnlock, Vault: "personal", Attempt: 1})
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if got.Passphrase != "from-env" {
		t.Errorf("first attempt passphrase = %q, want the env value", got.Passphrase)
	}

	if _, err := p.Unlock(gage.UnlockRequest{Purpose: gage.PurposeUnlock, Vault: "personal", Attempt: 2}); err == nil {
		t.Error("the second attempt was answered; the env var can only give one answer")
	} else if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("second attempt exit code = %v, want %v", exitcode.CodeOf(err), exitcode.LockedOrAuth)
	}
}

// TestGagePassphraseIsIgnoredForANewIdentity: a create-purpose answer
// can't be checked against anything, so a typo in the variable would be
// unrecoverable — and nobody would find out until the next unlock.
func TestGagePassphraseIsIgnoredForANewIdentity(t *testing.T) {
	inner := &fakePrompter{passphrases: []string{testPassphrase}}
	// create is where a PurposeCreate request is passed through to; on a
	// terminal that is the ordinary prompter. See scriptPrompter.
	p := &envPrompter{Prompter: inner, create: inner, passphrase: "from-env"}

	got, err := p.Unlock(gage.UnlockRequest{Purpose: gage.PurposeCreate, Vault: "personal", Attempt: 1})
	if err != nil {
		t.Fatalf("create-purpose unlock: %v", err)
	}
	if got.Passphrase == "from-env" {
		t.Error("GAGE_PASSPHRASE answered a create-purpose request; a new identity must be typed by a human")
	}
	if inner.calls != 1 {
		t.Errorf("the wrapped prompter saw %d create-purpose requests, want 1", inner.calls)
	}
}

// TestScriptWithNoPassphraseSourceFailsFast: no env var and no terminal
// means there is nowhere a passphrase could come from. Saying so beats
// blocking on a read that can't be answered, or reporting an empty
// passphrase as a wrong one.
func TestScriptWithNoPassphraseSourceFailsFast(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	unsetPassphraseEnv(t)

	for name, res := range map[string]cliResult{
		"--stdin":  runStdinScript(t, "use personal", "show GitHub"),
		"--script": runScript(t, "use personal", "show GitHub"),
	} {
		if res.Code != int(exitcode.LockedOrAuth) {
			t.Errorf("%s exit code = %d, want %d (LockedOrAuth)", name, res.Code, exitcode.LockedOrAuth)
		}
		if !strings.Contains(res.Stderr, envPassphraseVar) {
			t.Errorf("%s failure does not name %s: %q", name, envPassphraseVar, res.Stderr)
		}
		if strings.Contains(res.Stdout, "hunter2") {
			t.Errorf("%s decrypted something with no passphrase source", name)
		}
	}
}

// TestScriptOnATerminalStillPrompts: `--script FILE` leaves stdin free,
// which is exactly why a human can run one and answer the prompt. Only
// --stdin can never prompt.
func TestScriptOnATerminalStillPrompts(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	unsetPassphraseEnv(t)

	counter := &countingPrompter{Prompter: &fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t,
		[]string{"--script", writeScript(t, "use personal", "show GitHub")},
		"", true, counter)
	if res.Code != 0 {
		t.Fatalf("--script on a terminal failed: %s", res.Stderr)
	}
	if counter.unlocks["personal"] != 1 {
		t.Errorf("prompted %d times, want exactly 1 — the ordinary prompter should have been used",
			counter.unlocks["personal"])
	}
	if !strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("--script on a terminal did not run:\n%s", res.Stdout)
	}
}

// TestStdinNeverPromptsEvenOnATerminal: stdin is already the command
// stream, so prompting on it would race the script for the same bytes.
func TestStdinNeverPromptsEvenOnATerminal(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	unsetPassphraseEnv(t)

	counter := &countingPrompter{Prompter: &fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t, []string{"--stdin"},
		"use personal\nshow GitHub\n", true, counter)

	if res.Code != int(exitcode.LockedOrAuth) {
		t.Errorf("exit code = %d, want %d (LockedOrAuth)", res.Code, exitcode.LockedOrAuth)
	}
	if counter.unlocks["personal"] != 0 {
		t.Errorf("--stdin prompted %d times; it must never prompt", counter.unlocks["personal"])
	}
}

// TestScriptAndStdinAreMutuallyExclusive: two command streams have no
// meaning together.
func TestScriptAndStdinAreMutuallyExclusive(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"--script", writeScript(t, "ls"), "--stdin"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
}

// TestScriptFlagsRejectASubcommand: they choose where a whole session's
// lines come from, so combining one with a one-shot command asks for two
// different things at once.
func TestScriptFlagsRejectASubcommand(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	for _, args := range [][]string{
		{"--stdin", "ls"},
		{"--script", writeScript(t, "ls"), "show", "GitHub"},
	} {
		res := runCLI(t, args, "")
		if res.Code != int(exitcode.Usage) {
			t.Errorf("%v exit code = %d, want %d (Usage)", args, res.Code, exitcode.Usage)
		}
	}
}

// TestMissingScriptFileIsAUsageError rather than an internal one: the
// path came off the command line.
func TestMissingScriptFileIsAUsageError(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"--script", filepath.Join(t.TempDir(), "nope.gage")}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
}

// TestScriptAcceptsAssumeYes: a non-interactive session is the case
// --yes exists for — CI, with nobody to show a recipient diff to. The
// rule that refuses --yes in a session is about a session with a human
// sitting at it.
func TestScriptAcceptsAssumeYes(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, testPassphrase)

	res, _ := runCLIWithPrompter(t,
		[]string{"--script", writeScript(t, "use personal", "show GitHub"), "--yes"},
		"", false, &fakePrompter{passphrases: []string{testPassphrase}})
	if res.Code != 0 {
		t.Fatalf("--script --yes exit code = %d: %s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("--script --yes did not run the script:\n%s", res.Stdout)
	}
}

// TestScriptExitEndsTheRunEarly: `exit` in a script stops it, with the
// remaining lines unrun and a success status.
func TestScriptExitEndsTheRunEarly(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runScript(t, "use personal", "exit", "show GitHub")
	if res.Code != 0 {
		t.Fatalf("script exit code = %d: %s", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("the script continued past `exit`:\n%s", res.Stdout)
	}
}

// unsetPassphraseEnv removes GAGE_PASSPHRASE for the duration of a test,
// restoring whatever the environment had afterwards. t.Setenv first is
// what registers the cleanup — Unsetenv alone would leak across tests on
// a machine where the variable is genuinely set.
func unsetPassphraseEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envPassphraseVar, "")
	if err := os.Unsetenv(envPassphraseVar); err != nil {
		t.Fatalf("unsetting %s: %v", envPassphraseVar, err)
	}
}

// runStdinScriptWithRealPrompter mirrors run.go: one reader is both the
// command stream and the prompter's input. Every other helper here
// injects a fakePrompter, which answers without reading anything — so
// only this shape can show what a prompt actually does to a script.
func runStdinScriptWithRealPrompter(t *testing.T, lines ...string) cliResult {
	t.Helper()

	var stdout, stderr bytes.Buffer
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	app := &App{
		Out: &stdout, Err: &stderr, In: in,
		IsTerminal: func() bool { return false },
	}
	app.Prompter = newTerminalPrompter(in, &stderr)

	code := runApp(app, []string{"--stdin"})
	return cliResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

// TestStdinRefusesToCreateAnIdentityWithNoHuman.
//
// GAGE_PASSPHRASE deliberately doesn't answer for a new identity, but
// under --stdin the human it defers to doesn't exist: the ordinary
// prompter's stdin *is* the command stream. Falling through to it made
// `identity add` print "Choose a passphrase:" into the output and then
// read the script — reporting the EOF as a failed read, or, once the
// script outgrew the scanner's read-ahead, silently taking a command
// line as the passphrase for a new device key.
//
// The refusal must be explicit, must name the reason, and must not
// consume the command stream.
func TestStdinRefusesToCreateAnIdentityWithNoHuman(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runStdinScriptWithRealPrompter(t,
		"use personal",
		"identity add --device other-box",
	)

	// LockedOrAuth, the same code as the unlock-side refusal: both mean
	// there was nowhere to get a passphrase from.
	if res.Code != int(exitcode.LockedOrAuth) {
		t.Errorf("exit code = %d, want %d (LockedOrAuth)", res.Code, exitcode.LockedOrAuth)
	}
	if !strings.Contains(res.Stderr, "human") {
		t.Errorf("refusal does not say a human is needed: %q", res.Stderr)
	}
	// Never the half-asked question, and never an EOF dressed up as a
	// failed read.
	if strings.Contains(res.Stdout+res.Stderr, "Choose a passphrase") {
		t.Errorf("a passphrase prompt was rendered with nobody to answer it: %q", res.Stderr)
	}
	if strings.Contains(res.Stderr, "reading input") {
		t.Errorf("the unanswerable read still happened: %q", res.Stderr)
	}
}

// TestStdinIdentityAddDoesNotEatTheCommandStream is the other half of
// the same failure: whatever happens, the lines after `identity add`
// are commands, not passphrase answers.
func TestStdinIdentityAddDoesNotEatTheCommandStream(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")
	t.Setenv(envPassphraseVar, testPassphrase)

	res := runStdinScriptWithRealPrompter(t,
		"use personal",
		"identity add --device other-box",
		"show GitHub",
	)

	// The run stops at the refused line (a script aborts on the first
	// failure), so `show` never runs — but it must fail as a *refusal*,
	// with the following line untouched rather than swallowed as an
	// answer.
	if strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("the script continued past a failed command:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stderr, "show GitHub") &&
		strings.Contains(res.Stderr, "unknown command") {
		t.Errorf("a command line was consumed as passphrase input: %q", res.Stderr)
	}
}

// TestScriptOnATerminalStillCreatesIdentitiesInteractively: the refusal
// is scoped to "there is no human", not to scripts. `--script FILE`
// leaves stdin free, so a human running one can still answer.
func TestScriptOnATerminalStillCreatesIdentitiesInteractively(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	t.Setenv(envPassphraseVar, testPassphrase)

	counter := &countingPrompter{Prompter: &fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithPrompter(t,
		[]string{"--script", writeScript(t, "use personal", "identity add --device other-box")},
		"", true, counter)

	if res.Code != 0 {
		t.Fatalf("--script on a terminal could not create an identity: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "other-box") {
		t.Errorf("the identity was not registered:\n%s", res.Stdout)
	}
}

// recordingConflictPrompter is a Prompter that can also resolve
// conflicts, and records that it was asked. It stands in for the real
// terminal prompter in the --yes tests below, where the question is
// which prompter ends up answering rather than what it answers.
type recordingConflictPrompter struct {
	fakePrompter
	asked      int
	resolution gage.Resolution
}

func (p *recordingConflictPrompter) ResolveConflict(c gage.EntryConflict) (gage.Resolution, error) {
	p.asked++
	return p.resolution, nil
}

// TestAssumeYesAnswersTheTrustQuestionAndNothingElse is what `--yes` is
// allowed to be. It answers M10's recipient-change question without
// asking, and it must not quietly acquire an answer to a *conflict*,
// which is a question about which version of a secret to destroy.
func TestAssumeYesAnswersTheTrustQuestionAndNothingElse(t *testing.T) {
	inner := &recordingConflictPrompter{
		fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		resolution:   gage.KeepRemote,
	}

	wrapped := withAssumeYes(inner)

	// The trust question is answered by the wrapper, without reaching
	// the prompter underneath.
	ok, err := wrapped.ConfirmRecipientChange(gage.RecipientChangeWarning{Vault: "personal"})
	if err != nil || !ok {
		t.Fatalf("ConfirmRecipientChange through --yes = (%v, %v), want (true, nil)", ok, err)
	}

	// The conflict question still goes to the real asker.
	asker, isAsker := wrapped.(gage.ConflictPrompter)
	if !isAsker {
		t.Fatal("--yes over a conflict-capable prompter stopped being conflict-capable; " +
			"`gage sync --yes` on a terminal would refuse to resolve anything")
	}
	got, err := asker.ResolveConflict(gage.EntryConflict{})
	if err != nil {
		t.Fatal(err)
	}
	if inner.asked != 1 {
		t.Errorf("the wrapped prompter was asked %d times, want 1 — --yes must not answer a conflict itself", inner.asked)
	}
	if got != gage.KeepRemote {
		t.Errorf("resolution = %v, want the inner prompter's answer (%v)", got, gage.KeepRemote)
	}
}

// TestAssumeYesOverANonConflictPrompterStaysNonConflict is the other
// half of that shape: wrapping must not make a frontend that cannot
// resolve conflicts look like one that can, or every non-interactive run
// would appear answerable the moment --yes was passed.
func TestAssumeYesOverANonConflictPrompterStaysNonConflict(t *testing.T) {
	wrapped := withAssumeYes(&fakePrompter{passphrases: []string{testPassphrase}})
	if _, ok := wrapped.(gage.ConflictPrompter); ok {
		t.Error("--yes over a prompter that cannot resolve conflicts made it look like it could")
	}
}

// TestTerminalPrompterOfWalksDecoratorChains: unwrapping is a property
// of the decorator interface, not a list of concrete types — so a
// terminal prompter behind two wrappers is still found, which is what
// lets `gage --yes` in a session hand its reads to the line editor.
func TestTerminalPrompterOfWalksDecoratorChains(t *testing.T) {
	var out bytes.Buffer
	real := newTerminalPrompter(strings.NewReader(""), &out)

	// Two decorators deep: --yes over the script layer over the terminal.
	chain := withAssumeYes(noCreatePrompter{Prompter: real})

	got, ok := terminalPrompterOf(chain)
	if !ok {
		t.Fatal("the terminal prompter was not found behind two decorators")
	}
	if got != real {
		t.Error("terminalPrompterOf returned a different prompter than the one wrapped")
	}

	// And something that isn't a terminal prompter at all is reported as
	// such rather than mis-identified.
	if _, ok := terminalPrompterOf(&fakePrompter{}); ok {
		t.Error("terminalPrompterOf claimed a fake prompter was a terminal one")
	}
}

// TestNoCreatePrompterPassesOrdinaryUnlocksThrough: it refuses only
// PurposeCreate. An ordinary unlock has a passphrase that can be checked
// against something, so it goes to the prompter underneath — refusing
// those too would break every scripted run.
func TestNoCreatePrompterPassesOrdinaryUnlocksThrough(t *testing.T) {
	inner := &fakePrompter{passphrases: []string{testPassphrase}}
	p := noCreatePrompter{Prompter: inner}

	resp, err := p.Unlock(gage.UnlockRequest{
		Kind: gage.KindPassphrase, Purpose: gage.PurposeUnlock,
		Vault: "personal", Device: "laptop-1", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("an ordinary unlock was refused: %v", err)
	}
	if resp.Passphrase != testPassphrase {
		t.Errorf("passphrase = %q, want the inner prompter's answer", resp.Passphrase)
	}

	// And the one it does refuse, for contrast.
	if _, err := p.Unlock(gage.UnlockRequest{
		Kind: gage.KindPassphrase, Purpose: gage.PurposeCreate,
		Vault: "personal", Device: "laptop-1", Attempt: 1,
	}); err == nil {
		t.Error("creating an identity was allowed with no human to type a passphrase")
	}
}

// TestScriptFlagsAreRefusedInsideASession is checkScriptFlags' third
// refusal: --script/--stdin *start* a session, so asking for one from
// inside a session is a category error.
//
// It is driven directly rather than through the REPL because the session
// dispatcher never gets far enough to ask. A line of nothing but flags
// is refused earlier, as an unknown command ("--stdin"), and a line like
// `--stdin ls` takes the `cmd != root` arm instead
// (TestScriptFlagsRejectASubcommand covers that one). So this guard is
// currently unreachable from the prompt — which is a reason to pin the
// contract, not to leave it untested: the wrong answer here would be a
// session quietly trying to take over its own command stream.
func TestScriptFlagsAreRefusedInsideASession(t *testing.T) {
	app := testApp()
	root := NewRootCmd(app)
	session := gage.NewSession(gage.SessionConfig{
		Open:     func(name string) (*gage.Vault, error) { return nil, nil },
		Prompter: &fakePrompter{},
	})
	t.Cleanup(func() { _ = session.Close() })
	app.Session = session

	for _, name := range []string{"--stdin", "--script"} {
		t.Run(name, func(t *testing.T) {
			app.ScriptFile, app.ScriptStdin = "", false
			if name == "--stdin" {
				app.ScriptStdin = true
			} else {
				app.ScriptFile = filepath.Join(t.TempDir(), "session.gage")
			}

			err := checkScriptFlags(app, root, root)
			if err == nil {
				t.Fatalf("%s was accepted inside a session", name)
			}
			if exitcode.CodeOf(err) != exitcode.Usage {
				t.Errorf("CodeOf(err) = %v, want Usage", exitcode.CodeOf(err))
			}
			if !strings.Contains(err.Error(), "aren't available inside one") {
				t.Errorf("error = %v, want it to explain that a session is already running", err)
			}
		})
	}

	// Outside a session the same flags are fine, so the guard above is
	// about the session and not about the flags.
	app.Session = nil
	app.ScriptFile, app.ScriptStdin = "", true
	if err := checkScriptFlags(app, root, root); err != nil {
		t.Errorf("--stdin outside a session = %v, want it accepted", err)
	}
}
