//go:build linux || darwin

package main

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// TestInsertMultilineCapturesFromARealTerminalUntilEOF is the M4 test
// list's PTY-driven bullet for `-m`/`--multiline`: unlike --value-stdin
// (any io.Reader), -m is documented as reading "straight from the
// terminal until EOF, no external process" — so this drives it through an
// actual pty, typed passphrase and all, rather than an in-memory reader.
//
// The passphrase and the multiline value share the one pty fd (app.In),
// exactly as a real interactive session would: the passphrase line is
// consumed by x/term's raw-mode read, which restores the terminal
// afterward, and only then is the multiline body typed and terminated
// with Ctrl-D (EOT) — the terminal's own EOF signal on an empty line.
func TestInsertMultilineCapturesFromARealTerminalUntilEOF(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--recipient", testRecipient1, "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()

	app := &App{
		Out:        tty,
		Err:        tty,
		In:         tty,
		Build:      BuildInfo{Version: "v1.2.3", Commit: "abcdef1"},
		IsTerminal: func() bool { return true },
	}
	app.Prompter = newTerminalPrompter(tty, tty)

	done := make(chan int, 1)
	go func() {
		done <- runApp(app, []string{"insert", "Recovery codes", "-m"})
	}()

	r := bufio.NewReader(ptmx)
	waitForOnPTY(t, r, "Enter passphrase")
	// Wait for x/term to actually put the tty into raw mode before typing,
	// the same guard TestRealPrompterReadsAPassphraseWithoutEchoing uses —
	// writing while the prompt text is visible but the mode switch hasn't
	// landed yet is a race that has nothing to do with gage.
	waitForEchoDisabled(t, tty.Fd())
	if _, err := ptmx.Write([]byte(testPassphrase + "\n")); err != nil {
		t.Fatalf("typing the passphrase: %v", err)
	}

	// x/term.ReadPassword put the tty into raw mode for the passphrase
	// and restores it on return; waiting for ECHO to come back is this
	// process's only signal that the passphrase read is over and it's
	// safe to type the multiline body without it being consumed by the
	// wrong read.
	waitForEchoEnabled(t, tty.Fd())

	const value = "aaaa-bbbb\ncccc-dddd\neeee-ffff\n"
	if _, err := ptmx.Write([]byte(value)); err != nil {
		t.Fatalf("typing the multiline value: %v", err)
	}
	// Ctrl-D (EOT) on an empty line is the terminal's EOF signal — no
	// external process, per the design doc's note on -m.
	if _, err := ptmx.Write([]byte{0x04}); err != nil {
		t.Fatalf("sending EOF (Ctrl-D): %v", err)
	}

	var code int
	select {
	case code = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("gage insert -m never returned")
	}
	if code != 0 {
		t.Fatalf("gage insert -m exited %d", code)
	}

	// gage show, not cat: cat's display format prints a multi-line
	// value's block-scalar content flush left (no added indentation),
	// which is no longer valid YAML at that indentation and so no longer
	// round-trips through gage.UnmarshalEntry — show prints the raw
	// value with no reformatting, so it is what actually proves the
	// stored value is byte-identical to what was typed.
	show := runCLI(t, []string{"show", "Recovery codes"}, "")
	if show.Code != 0 {
		t.Fatalf("show failed: %s", show.Stderr)
	}
	if show.Stdout != value {
		t.Errorf("round-tripped value = %q, want byte-identical to %q", show.Stdout, value)
	}
}

// waitForOnPTY blocks until substr has appeared in what the pty's primary
// side has emitted, so a test never races writing an answer before the
// prompt that asks for it has actually been rendered.
func waitForOnPTY(t *testing.T, r *bufio.Reader, substr string) {
	t.Helper()
	var seen strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := r.ReadByte()
		if err != nil {
			t.Fatalf("reading from pty while waiting for %q (seen so far: %q): %v", substr, seen.String(), err)
		}
		seen.WriteByte(b)
		if strings.Contains(seen.String(), substr) {
			return
		}
	}
	t.Fatalf("timed out waiting for %q on the pty; saw: %q", substr, seen.String())
}

// waitForEchoEnabled blocks until the terminal's ECHO flag is set —
// x/term.ReadPassword's Restore, undoing the raw mode it set to read the
// passphrase without echoing it.
func waitForEchoEnabled(t *testing.T, fd uintptr) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		termios, err := unix.IoctlGetTermios(int(fd), ioctlReadTermios)
		if err != nil {
			t.Fatalf("reading terminal settings: %v", err)
		}
		if termios.Lflag&unix.ECHO != 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the prompter never re-enabled terminal echo")
}
