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
	"golang.org/x/sys/unix"

	"github.com/distillerylabs/gage/internal/gage"
)

// TestRealPrompterReadsAPassphraseWithoutEchoing is one of the few tests
// that must use a real terminal. Everything else about the prompter can
// be driven through an ordinary io.Reader, but "the passphrase is not
// echoed" is a property of the tty's line discipline, not of gage's code
// — an in-memory reader has no echo to suppress, so a test against one
// would pass whether or not x/term were wired up at all.
//
// Restricted to Linux and macOS: a pty is a POSIX object, and those are
// the two POSIX platforms gage's CI runs. The Windows prompter has no
// equivalent to exercise here.
func TestRealPrompterReadsAPassphraseWithoutEchoing(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()

	// Everything the terminal emits — the prompt, and any echo of what's
	// typed — arrives on the primary side.
	var mu sync.Mutex
	var emitted bytes.Buffer
	go func() {
		buf := make([]byte, 256)
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

	// The real production wiring: the terminal is both where the prompt
	// is written and where the answer is read from.
	p := newTerminalPrompter(tty, tty)

	type result struct {
		resp gage.UnlockResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := p.Unlock(gage.UnlockRequest{
			Kind:    gage.KindPassphrase,
			Purpose: gage.PurposeUnlock,
			Vault:   "personal",
			Device:  "laptop-1",
			Attempt: 1,
		})
		done <- result{resp, err}
	}()

	// Wait for x/term to actually turn echo off before typing, rather
	// than sleeping and hoping. Writing while echo is still on would
	// fail the test for a reason that has nothing to do with gage.
	waitForEchoDisabled(t, tty.Fd())

	const passphrase = "hunter2"
	if _, err := io.WriteString(ptmx, passphrase+"\n"); err != nil {
		t.Fatalf("typing the passphrase: %v", err)
	}

	var got result
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the prompter never returned")
	}
	if got.err != nil {
		t.Fatalf("Unlock: %v", got.err)
	}
	if got.resp.Passphrase != passphrase {
		t.Errorf("passphrase = %q, want %q", got.resp.Passphrase, passphrase)
	}
	if got.resp.Kind != gage.KindPassphrase {
		t.Errorf("response Kind = %q, want %q", got.resp.Kind, gage.KindPassphrase)
	}

	// Any echo would have been emitted at the moment of the write; this
	// settle only guards against reading the buffer before the copier
	// goroutine got to it.
	time.Sleep(100 * time.Millisecond)
	out := seen()
	if !strings.Contains(out, "Enter passphrase") {
		t.Errorf("the terminal never showed a prompt:\n%q", out)
	}
	if strings.Contains(out, passphrase) {
		t.Errorf("the passphrase was echoed to the terminal:\n%q", out)
	}
	if strings.Contains(out, "personal") == false {
		t.Errorf("the prompt doesn't name the vault being unlocked:\n%q", out)
	}
}

// waitForEchoDisabled blocks until the terminal's ECHO flag is cleared,
// which is what x/term.ReadPassword does before it reads.
func waitForEchoDisabled(t *testing.T, fd uintptr) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		termios, err := unix.IoctlGetTermios(int(fd), ioctlReadTermios)
		if err != nil {
			t.Fatalf("reading terminal settings: %v", err)
		}
		if termios.Lflag&unix.ECHO == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the prompter never disabled terminal echo")
}
