package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// fakeClipboard stands in for the system clipboard. Every clipboard test
// uses one: a CI runner may have no clipboard at all, and a developer's
// must not be clobbered by `go test`.
type fakeClipboard struct {
	mu       sync.Mutex
	content  string
	writes   []string
	writeErr error
	readErr  error
}

func (c *fakeClipboard) Write(s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return c.writeErr
	}
	c.content = s
	c.writes = append(c.writes, s)
	return nil
}

func (c *fakeClipboard) Read() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return "", c.readErr
	}
	return c.content, nil
}

func (c *fakeClipboard) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content
}

// set simulates someone else copying something after gage did.
func (c *fakeClipboard) set(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.content = s
}

// fakeTimer captures a scheduled clear instead of running it, so a test
// fires the timeout on demand rather than sleeping through it.
type fakeTimer struct {
	mu      sync.Mutex
	d       time.Duration
	fn      func()
	stopped bool
	set     bool
}

func (ft *fakeTimer) schedule(d time.Duration, fn func()) clipboardTimer {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.d, ft.fn, ft.set, ft.stopped = d, fn, true, false
	return ft
}

func (ft *fakeTimer) Stop() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.stopped = true
	return true
}

// fire runs the scheduled clear, as the real timer would at the timeout.
func (ft *fakeTimer) fire(t *testing.T) {
	t.Helper()
	ft.mu.Lock()
	fn, set := ft.fn, ft.set
	ft.mu.Unlock()
	if !set {
		t.Fatal("no clipboard clear was ever scheduled")
	}
	fn()
}

// clipRun runs one CLI invocation with the clipboard seams installed.
// waited records whether the one-shot blocking wait was entered, which
// is how "a one-shot blocks until the timeout" is asserted without a
// test that actually waits 45 seconds.
type clipRun struct {
	cliResult
	cb      *fakeClipboard
	timer   *fakeTimer
	waited  bool
	waitFor time.Duration
}

func runCLIWithClipboard(t *testing.T, args []string) *clipRun {
	t.Helper()
	return runCLIWithClipboardAndStdin(t, args, "", false)
}

func runCLIWithClipboardAndStdin(t *testing.T, args []string, stdin string, isTerminal bool) *clipRun {
	t.Helper()
	run := &clipRun{cb: &fakeClipboard{}, timer: &fakeTimer{}}
	res, _ := runCLIWithApp(t, args, strings.NewReader(stdin), isTerminal,
		&fakePrompter{passphrases: []string{testPassphrase}},
		func(app *App) {
			app.Clipboard = run.cb
			app.ClipboardTimer = run.timer.schedule
			app.ClipboardWait = func(d time.Duration) {
				run.waited = true
				run.waitFor = d
			}
		})
	run.cliResult = res
	return run
}

// TestClipCopiesAndClearsAfterTheTimeout is the core of `-c`: the value
// reaches the clipboard, and it does not stay there.
func TestClipCopiesAndClearsAfterTheTimeout(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	run := runCLIWithClipboard(t, []string{"show", "GitHub", "-c"})
	if run.Code != 0 {
		t.Fatalf("show -c failed: %s", run.Stderr)
	}
	if len(run.cb.writes) == 0 || run.cb.writes[0] != "hunter2" {
		t.Fatalf("clipboard writes = %q, want the value first", run.cb.writes)
	}
	if got := run.cb.get(); got != "" {
		t.Errorf("clipboard still holds %q after the timeout; want it cleared", got)
	}
}

// TestClipBlocksUntilTheTimeoutThenExitsZero: a one-shot `show -c` waits
// in the foreground rather than forking a clearer — the M12 decision
// that keeps the clear inside the process that wrote the clipboard
// (principle 5). The wait is asserted by the seam being entered, and by
// the clear landing after it.
func TestClipBlocksUntilTheTimeoutThenExitsZero(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	run := runCLIWithClipboard(t, []string{"show", "GitHub", "-c"})
	if !run.waited {
		t.Error("one-shot show -c did not block; it must wait out the timeout in the foreground")
	}
	if run.waitFor != defaultClipboardTimeout {
		t.Errorf("waited for %s, want the configured %s", run.waitFor, defaultClipboardTimeout)
	}
	if run.Code != 0 {
		t.Errorf("exit code = %d, want 0", run.Code)
	}
}

// TestClipInterruptDuringTheWaitClearsAndExitsZero: Ctrl-C while the
// clipboard is held is "I've finished pasting", not an abort — the clear
// still runs and the command still succeeds. waitOrInterrupt is what
// makes both outcomes identical, so it is asserted directly.
func TestClipInterruptDuringTheWaitClearsAndExitsZero(t *testing.T) {
	interrupt := make(chan os.Signal, 1)
	interrupt <- os.Interrupt
	done := make(chan struct{})
	go func() {
		defer close(done)
		// A timer channel that never fires: only the interrupt can end
		// this wait.
		waitOrInterrupt(make(chan time.Time), interrupt)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitOrInterrupt did not return on an interrupt")
	}

	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	// The CLI-level half: whatever ends the wait, the clear runs and the
	// exit code is 0.
	run := runCLIWithClipboard(t, []string{"show", "GitHub", "-c"})
	if run.Code != 0 {
		t.Errorf("exit code after an interrupted wait = %d, want 0", run.Code)
	}
	if got := run.cb.get(); got != "" {
		t.Errorf("clipboard holds %q after an interrupted wait; want it cleared", got)
	}
}

// TestClipDoesNotWipeAClipboardSomeoneElseChanged: the clear is
// conditional. gage is a guest on a shared clipboard, and the timeout's
// job is to remove gage's own value, not whatever the human copied in
// the meantime.
func TestClipDoesNotWipeAClipboardSomeoneElseChanged(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	run := &clipRun{cb: &fakeClipboard{}, timer: &fakeTimer{}}
	res, _ := runCLIWithApp(t, []string{"show", "GitHub", "-c"}, strings.NewReader(""), false,
		&fakePrompter{passphrases: []string{testPassphrase}},
		func(app *App) {
			app.Clipboard = run.cb
			app.ClipboardTimer = run.timer.schedule
			// Someone copies something else while gage is waiting out
			// the timeout — the precise window the conditional clear
			// exists for.
			app.ClipboardWait = func(time.Duration) { run.cb.set("someone else's copy") }
		})
	if res.Code != 0 {
		t.Fatalf("show -c failed: %s", res.Stderr)
	}
	if got := run.cb.get(); got != "someone else's copy" {
		t.Errorf("clipboard = %q; gage wiped a value it did not write", got)
	}
}

// TestClipPrintsNoPlaintextAndNoticesOnStderr: -c renders *instead of*
// printing (principle 6), and the notice a human needs goes to stderr so
// a redirected stdout carries nothing at all.
func TestClipPrintsNoPlaintextAndNoticesOnStderr(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	run := runCLIWithClipboard(t, []string{"show", "GitHub", "-c"})
	if strings.Contains(run.Stdout, "hunter2") {
		t.Errorf("show -c printed the value to stdout: %q", run.Stdout)
	}
	if strings.TrimSpace(run.Stdout) != "" {
		t.Errorf("show -c wrote %q to stdout; -c must print nothing there", run.Stdout)
	}
	if !strings.Contains(run.Stderr, "clipboard") {
		t.Errorf("stderr does not tell the human their clipboard is on a clock: %q", run.Stderr)
	}
	if strings.Contains(run.Stderr, "hunter2") {
		t.Errorf("the clipboard notice leaks the value: %q", run.Stderr)
	}
}

// TestClipAndQRAreMutuallyExclusive: two answers to the same question
// (where does this value go instead of the terminal), so asking for both
// is a usage error rather than a run that does neither well.
func TestClipAndQRAreMutuallyExclusive(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}

	run := runCLIWithClipboard(t, []string{"show", "GitHub", "-c", "-q"})
	if run.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", run.Code, exitcode.Usage)
	}
	if len(run.cb.writes) != 0 {
		t.Errorf("a rejected -c -q still touched the clipboard: %q", run.cb.writes)
	}
}

// TestClipboardKeeperClearIsIdempotentAndSkipsWhenNothingPending: the
// session's exit path calls clear unconditionally, so calling it with
// nothing outstanding must be a no-op rather than a wipe of whatever is
// on the clipboard.
func TestClipboardKeeperClearIsIdempotentAndSkipsWhenNothingPending(t *testing.T) {
	cb := &fakeClipboard{content: "someone else's copy"}
	k := newClipboardKeeper(cb, (&fakeTimer{}).schedule)

	if err := k.clear(); err != nil {
		t.Fatalf("clear with nothing pending: %v", err)
	}
	if got := cb.get(); got != "someone else's copy" {
		t.Errorf("clear with nothing pending wiped the clipboard: %q", got)
	}

	if err := k.copy("secret"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := k.clear(); err != nil {
		t.Fatalf("first clear: %v", err)
	}
	cb.set("something new")
	if err := k.clear(); err != nil {
		t.Fatalf("second clear: %v", err)
	}
	if got := cb.get(); got != "something new" {
		t.Errorf("a second clear wiped a value gage did not write: %q", got)
	}
}

// TestClipboardKeeperLeavesAnUnreadableClipboardAlone: with no way to
// tell whose value is on the clipboard, the safe answer is not to
// destroy a stranger's.
func TestClipboardKeeperLeavesAnUnreadableClipboardAlone(t *testing.T) {
	cb := &fakeClipboard{}
	k := newClipboardKeeper(cb, (&fakeTimer{}).schedule)
	if err := k.copy("secret"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	cb.readErr = errors.New("no clipboard here")

	if err := k.clear(); err == nil {
		t.Error("clear over an unreadable clipboard succeeded silently; the human should be told")
	}
	if got := len(cb.writes); got != 1 {
		t.Errorf("clipboard writes = %d, want only the original copy — a blind clear must not happen", got)
	}
}

// TestClipboardTimeoutIsConfigurable: [shell].clipboard_timeout
// overrides the 45s default and reaches the wait.
func TestClipboardTimeoutIsConfigurable(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	setShellClipboardTimeout(t, "5s")

	run := runCLIWithClipboard(t, []string{"show", "GitHub", "-c"})
	if run.Code != 0 {
		t.Fatalf("show -c failed: %s", run.Stderr)
	}
	if run.waitFor != 5*time.Second {
		t.Errorf("waited for %s, want the configured 5s", run.waitFor)
	}
}

// TestClipboardTimeoutBelowTheMinimumIsRejected: under a second the
// clear races the paste it exists to allow, so it's a config error
// rather than a very brisk setting — and the command fails before
// anything reaches the clipboard.
func TestClipboardTimeoutBelowTheMinimumIsRejected(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	if res, _ := runCLIWithValue(t, []string{"insert", "GitHub"}, "hunter2"); res.Code != 0 {
		t.Fatalf("insert failed: %s", res.Stderr)
	}
	setShellClipboardTimeout(t, "200ms")

	run := runCLIWithClipboard(t, []string{"show", "GitHub", "-c"})
	if run.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", run.Code, exitcode.Usage)
	}
	if len(run.cb.writes) != 0 {
		t.Errorf("a rejected timeout still reached the clipboard: %q", run.cb.writes)
	}
	if !strings.Contains(run.Stderr, "clipboard_timeout") {
		t.Errorf("error does not name the setting at fault: %q", run.Stderr)
	}
}

// TestResolveShellSettingsClipboardTimeout covers the parsing directly,
// including the default and a malformed value.
func TestResolveShellSettingsClipboardTimeout(t *testing.T) {
	isolateXDG(t)

	got, err := resolveShellSettings(config.Shell{})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if got.clipboardTimeout != defaultClipboardTimeout {
		t.Errorf("default clipboard timeout = %s, want %s", got.clipboardTimeout, defaultClipboardTimeout)
	}

	got, err = resolveShellSettings(config.Shell{ClipboardTimeout: "90s"})
	if err != nil {
		t.Fatalf("90s: %v", err)
	}
	if got.clipboardTimeout != 90*time.Second {
		t.Errorf("clipboard timeout = %s, want 90s", got.clipboardTimeout)
	}

	if _, err := resolveShellSettings(config.Shell{ClipboardTimeout: "45sec"}); err == nil {
		t.Error("a malformed clipboard_timeout was accepted")
	} else if exitcode.CodeOf(err) != exitcode.Usage {
		t.Errorf("malformed clipboard_timeout exit code = %v, want %v", exitcode.CodeOf(err), exitcode.Usage)
	}
}

// setShellClipboardTimeout writes [shell].clipboard_timeout into the
// isolated global config for the current test.
func setShellClipboardTimeout(t *testing.T, v string) {
	t.Helper()
	g, err := readGlobalConfig()
	if err != nil {
		t.Fatalf("reading global config: %v", err)
	}
	g.Shell.ClipboardTimeout = v
	if err := writeGlobalConfig(g); err != nil {
		t.Fatalf("writing global config: %v", err)
	}
}

// runSessionScriptWithClipboard drives a whole session with the
// clipboard seams installed, so the session half of the -c decision —
// schedule, don't block — can be asserted end to end.
func runSessionScriptWithClipboard(t *testing.T, s string, cb *fakeClipboard, ft *fakeTimer) cliResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	in := strings.NewReader(s)
	app := &App{
		Out:            &stdout,
		Err:            &stderr,
		In:             in,
		Build:          BuildInfo{Version: "v1.2.3", Commit: "abcdef1"},
		IsTerminal:     func() bool { return true },
		Clipboard:      cb,
		ClipboardTimer: ft.schedule,
		// A session must never take this path. If it does, the test
		// fails loudly rather than hanging or quietly passing.
		ClipboardWait: func(time.Duration) {
			t.Error("a session's show -c blocked on the one-shot clipboard wait; it must schedule instead")
		},
	}
	app.Prompter = newTerminalPrompter(in, &stderr)
	code := runApp(app, []string{})
	return cliResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

// TestSessionClipSchedulesRatherThanBlocking: in a session the process
// is already long-lived, so -c hands the prompt straight back and clears
// on a timer. The blocking wait is the one-shot's answer, not this one.
func TestSessionClipSchedulesRatherThanBlocking(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")

	cb, ft := &fakeClipboard{}, &fakeTimer{}
	res := runSessionScriptWithClipboard(t, script(
		"use personal",
		testPassphrase,
		"show GitHub -c",
		"exit",
	), cb, ft)
	if res.Code != 0 {
		t.Fatalf("session exit code = %d: %s", res.Code, res.Stderr)
	}
	if len(cb.writes) == 0 || cb.writes[0] != "hunter2" {
		t.Fatalf("clipboard writes = %q, want the value first", cb.writes)
	}
	if !ft.set {
		t.Error("in-session show -c scheduled no clear")
	}
	if ft.d != defaultClipboardTimeout {
		t.Errorf("scheduled clear at %s, want the configured %s", ft.d, defaultClipboardTimeout)
	}
	// The prompt came back: a further command ran after the copy.
	if !strings.Contains(res.Stderr, "clipboard") {
		t.Errorf("no clipboard notice reached the human: %q", res.Stderr)
	}
	if strings.Contains(res.Stdout, "hunter2") {
		t.Errorf("in-session show -c printed the value: %q", res.Stdout)
	}
}

// TestSessionExitClearsAPendingCopy: `exit` a second after `show -c`
// must not leave the secret on the clipboard for the remaining 44 —
// the session's exit drops the clipboard for the same reason it drops
// key material.
func TestSessionExitClearsAPendingCopy(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "GitHub", "hunter2")

	cb, ft := &fakeClipboard{}, &fakeTimer{}
	res := runSessionScriptWithClipboard(t, script(
		"use personal",
		testPassphrase,
		"show GitHub -c",
		"exit",
	), cb, ft)
	if res.Code != 0 {
		t.Fatalf("session exit code = %d: %s", res.Code, res.Stderr)
	}
	// The timer never fired — the session ended first — and the value is
	// gone anyway.
	if got := cb.get(); got != "" {
		t.Errorf("clipboard still holds %q after the session exited", got)
	}
	if !ft.stopped {
		t.Error("the pending clear's timer was left running after the session ended")
	}
}

// TestSessionClipTimerClearsWhenItFires is the other path: the session
// is still running when the timeout arrives.
func TestSessionClipTimerClearsWhenItFires(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	cb, ft := &fakeClipboard{}, &fakeTimer{}
	k := newClipboardKeeper(cb, ft.schedule)

	if err := k.copy("hunter2"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	k.scheduleClear(defaultClipboardTimeout)
	if got := cb.get(); got != "hunter2" {
		t.Fatalf("clipboard = %q immediately after the copy, want the value", got)
	}
	ft.fire(t)
	if got := cb.get(); got != "" {
		t.Errorf("clipboard = %q after the scheduled clear fired, want it empty", got)
	}
}

// TestSecondCopySupersedesTheFirstsPendingClear: the clipboard holds one
// value, so a second `show -c` replaces the first rather than queueing
// behind it — and the first copy's timer must not later wipe the second.
func TestSecondCopySupersedesTheFirstsPendingClear(t *testing.T) {
	cb := &fakeClipboard{}
	first := &fakeTimer{}
	k := newClipboardKeeper(cb, first.schedule)

	if err := k.copy("one"); err != nil {
		t.Fatalf("first copy: %v", err)
	}
	k.scheduleClear(defaultClipboardTimeout)
	if err := k.copy("two"); err != nil {
		t.Fatalf("second copy: %v", err)
	}
	if !first.stopped {
		t.Error("the first copy's pending clear was not cancelled")
	}
	if got := cb.get(); got != "two" {
		t.Errorf("clipboard = %q, want the second value", got)
	}
	if err := k.clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := cb.get(); got != "" {
		t.Errorf("clipboard = %q after clearing the second copy", got)
	}
}
