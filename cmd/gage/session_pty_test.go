//go:build linux || darwin

package main

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestSessionOverARealTerminalDrivesTheLineEditor is the one M6 test
// that needs an actual terminal. Everything else about the REPL is
// asserted through an in-memory reader (see runSessionScript), which
// deliberately takes the plain-reader path — so without this, the
// chzyer/readline half of newSessionLineReader would ship untested: raw
// mode, its background stdin reader, and the prompter reading *through*
// it rather than racing it for the same bytes.
//
// Restricted to Linux and macOS: a pty is a POSIX object. The line
// editor is cross-platform (that's why it was chosen), but exercising it
// on Windows would need a different harness than this one.
func TestSessionOverARealTerminalDrivesTheLineEditor(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "ProtonMail", "correcthorsebatterystaple")

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

	// The real production wiring: one terminal is stdin, stdout, and the
	// prompter's own output, and IsTerminal says so — which is what sends
	// newSessionLineReader down the readline path.
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

	// Raw mode means the terminal does no CR/LF translation of its own,
	// so a typed Enter is a carriage return.
	typeLine := func(s string) {
		if _, err := io.WriteString(ptmx, s+"\r"); err != nil {
			t.Errorf("typing %q: %v", s, err)
		}
	}

	waitFor(t, seen, "gage> ", "the session never drew a prompt")
	typeLine("use personal")
	waitFor(t, seen, "Enter passphrase", "the session never asked for a passphrase")
	typeLine(testPassphrase)
	waitFor(t, seen, unlockedGlyph, "the prompt never showed the vault as unlocked")
	typeLine("show ProtonMail")
	waitFor(t, seen, "correcthorsebatterystaple", "the session never printed the entry's value")
	typeLine("exit")

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("session exit code = %d, want 0:\n%s", code, seen())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the session never exited:\n%s", seen())
	}

	// The passphrase is typed at a masked prompt, so it must not appear
	// in what the terminal drew — the pty equivalent of the history
	// file's plaintext rule.
	if strings.Contains(strings.ReplaceAll(seen(), "\r", ""), testPassphrase) {
		t.Errorf("the passphrase was echoed to the terminal:\n%q", seen())
	}
}

// waitFor blocks until want shows up in the terminal's output, so the
// test types the next line only once the session is ready for it rather
// than sleeping and hoping.
func waitFor(t *testing.T, seen func() string, want, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(seen(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s (waiting for %q):\n%q", msg, want, seen())
}
