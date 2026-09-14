package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// emitSecret is the one place a decrypted value leaves gage, and the
// three flags that can redirect it meet here rather than each branching
// inside `show`. Default prints it; -q renders it as a QR code
// *instead*; -c copies it to the clipboard *instead*. Nothing prints the
// value alongside either alternative — that would defeat the point of
// asking for them (principle 6: "plaintext should be surprising to
// produce").
func emitSecret(app *App, value string, clip, qr bool) error {
	if err := checkSecretOutput(clip, qr); err != nil {
		return err
	}
	switch {
	case qr:
		return renderQR(app.Out, value)
	case clip:
		return copySecret(app, value)
	default:
		out := value
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if _, err := fmt.Fprint(app.Out, out); err != nil {
			return exitcode.Wrap(exitcode.Internal, err)
		}
		return nil
	}
}

// checkSecretOutput rejects asking for two destinations at once.
//
// Refused rather than doing both. They are two answers to the same
// question — where does this value go instead of the terminal — and a
// run that did both would block for the clipboard timeout with a QR code
// on screen, which is neither thing anyone asked for. Allowing it later
// is additive.
//
// It is separate from emitSecret so `generate` can ask before it writes
// anything: emitSecret runs after the entry is already inserted and
// committed, and a usage error at that point would leave a new secret in
// the vault behind a message saying the command failed.
func checkSecretOutput(clip, qr bool) error {
	if clip && qr {
		return exitcode.New(exitcode.Usage, "gage: -c/--clip and -q/--qr are mutually exclusive")
	}
	return nil
}

// copySecret is `-c`: put the value on the clipboard and make sure it
// doesn't stay there.
//
// The two modes differ in who does the waiting, and that split is the
// M12 decision rather than an optimization. The clear always happens
// inside the process that wrote the clipboard — never a forked child,
// which would be exactly the surviving background process principle 5
// refuses — so a one-shot has no choice but to block until the timeout,
// while a session is already long-lived and can hand the prompt straight
// back and clear on a timer.
//
// The notice goes to app.Err, not app.Out: `-c` prints no plaintext, and
// a human still needs to be told their clipboard is on a clock.
func copySecret(app *App, value string) error {
	timeout, err := clipboardTimeout()
	if err != nil {
		return err
	}

	k := app.clipboard()
	if err := k.copy(value); err != nil {
		return err
	}

	if app.Session != nil {
		k.scheduleClear(timeout)
		writeOut(app.Err, []string{fmt.Sprintf("gage: copied to clipboard; clears in %s.", timeout)})
		return nil
	}

	writeOut(app.Err, []string{fmt.Sprintf(
		"gage: copied to clipboard; clears in %s — Ctrl-C to clear now.", timeout)})
	app.waitForClipboard(timeout)
	return k.clear()
}

// waitOrInterrupt blocks until the timeout elapses or the human
// interrupts, whichever comes first. Both outcomes mean the same thing —
// clear the clipboard now — which is why it reports nothing: Ctrl-C here
// is "I've finished pasting", not an abort, and a one-shot `show -c`
// still exits 0 either way.
//
// The two channels are parameters so this is testable without a real
// clock or a real signal.
func waitOrInterrupt(after <-chan time.Time, interrupt <-chan os.Signal) {
	select {
	case <-after:
	case <-interrupt:
	}
}

// waitForClipboard is waitOrInterrupt wired to the real clock and a real
// SIGINT, installed only for the duration of the wait — gage does not
// otherwise handle signals, and leaving a handler installed would change
// how every later part of the process dies.
func (app *App) waitForClipboard(d time.Duration) {
	if app.ClipboardWait != nil {
		app.ClipboardWait(d)
		return
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	t := time.NewTimer(d)
	defer t.Stop()
	waitOrInterrupt(t.C, sig)
}
