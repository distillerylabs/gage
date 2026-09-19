//go:build linux || darwin

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// TestTabCompletionAtThePromptExpandsAPartialCommand is the M13 plan's
// one required real-terminal test: a literal Tab keypress at the prompt
// triggers completion end to end, through the actual chzyer/readline
// wiring — mirroring M6's minimal pty coverage for masked passphrase
// input. Everything about *which* candidates are offered is already
// covered in-process, against completionCandidates (complete_test.go);
// this only proves the keystroke reaches that logic at all.
//
// It also re-asserts M6's "only a submitted line is recorded" history
// invariant against this new code path: the Tab keystroke that expanded
// "u" into "use" must never itself show up as a history entry, only the
// two full lines actually submitted with Enter.
func TestTabCompletionAtThePromptExpandsAPartialCommand(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()

	var mu sync.Mutex
	var emitted bytes.Buffer
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				emitted.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	seen := func() string {
		mu.Lock()
		defer mu.Unlock()
		return emitted.String()
	}

	app := &App{
		Out:        tty,
		Err:        tty,
		In:         tty,
		Build:      BuildInfo{Version: "v1.2.3", Commit: "abcdef1"},
		IsTerminal: func() bool { return true },
	}
	app.Prompter = newTerminalPrompter(tty, tty)

	done := make(chan int, 1)
	go func() { done <- runApp(app, []string{}) }()

	typeRaw := func(s string) {
		if _, err := io.WriteString(ptmx, s); err != nil {
			t.Errorf("typing %q: %v", s, err)
		}
	}

	waitFor(t, seen, "gage> ", "the session never drew a prompt")
	// "u" is an unambiguous prefix of exactly one session-visible
	// command ("use"), so a single Tab completes it directly rather than
	// rendering a candidate list (see chzyer/readline's OnComplete).
	typeRaw("u\t")
	waitFor(t, seen, "use", "tab completion never expanded \"u\" to \"use\"")
	typeRaw(" personal\r")
	waitFor(t, seen, "Enter passphrase", "`use personal` (after completion) never asked for a passphrase")
	typeRaw(testPassphrase + "\r")
	waitFor(t, seen, unlockedGlyph, "the prompt never showed the vault as unlocked")
	typeRaw("exit\r")

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("session exit code = %d, want 0:\n%s", code, seen())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the session never exited:\n%s", seen())
	}

	dir, err := xdgpaths.StateDir()
	if err != nil {
		t.Fatalf("resolving state dir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "history"))
	if err != nil {
		t.Fatalf("reading history file: %v", err)
	}
	got := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	want := []string{"use personal", "exit"}
	if len(got) != len(want) {
		t.Fatalf("history file has %d lines %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("history line %d = %q, want %q (no Tab keystroke recorded)", i, got[i], want[i])
		}
	}
}
