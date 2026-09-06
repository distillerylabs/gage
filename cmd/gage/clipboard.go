package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"sync"
	"time"

	"github.com/atotto/clipboard"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// clipboardPort is the system clipboard, behind one signature — the same
// shape as memlock.Locker: an injectable seam whose only production
// implementation talks to the OS, so every test can exercise the copy
// and the clear without touching the developer's real clipboard (or
// needing one to exist, which on a headless CI runner it does not).
type clipboardPort interface {
	Write(string) error
	Read() (string, error)
}

// systemClipboard is the production clipboardPort.
//
// github.com/atotto/clipboard is the implementation rather than a
// cgo-backed library, and the M12 plan's implementation note is what
// this answers: on macOS and Linux it drives pbcopy/xclip/wl-copy with
// the text written to the child's *stdin pipe* — never as an argv
// element, where it would be readable by any process that can run `ps`
// — and on Windows it calls the user32 clipboard API directly with no
// child process at all. No cgo, so `make build` stays a plain
// three-platform Go build.
type systemClipboard struct{}

func (systemClipboard) Write(s string) error  { return clipboard.WriteAll(s) }
func (systemClipboard) Read() (string, error) { return clipboard.ReadAll() }

// clipboardTimer is one pending auto-clear, behind a seam for the same
// reason the clipboard itself is: a test needs to fire it on demand
// rather than by sleeping.
type clipboardTimer interface{ Stop() bool }

// newClipboardTimer schedules fn after d. time.AfterFunc in production.
type newClipboardTimer func(d time.Duration, fn func()) clipboardTimer

func realClipboardTimer(d time.Duration, fn func()) clipboardTimer {
	return time.AfterFunc(d, fn)
}

// clipboardKeeper owns whatever gage has put on the clipboard that
// hasn't been cleared yet. There is at most one such thing: the
// clipboard holds one value, so a second `show -c` supersedes the first
// rather than queueing behind it.
//
// What it remembers is a SHA-256 of the copied value, never the value.
// The record outlives the copy by design — it exists to be consulted at
// clear time — and a pending clear must not be a second plaintext copy
// of the secret sitting in this process's memory for the whole timeout.
// A hash is enough for the only question the clear asks: is what's on
// the clipboard still what we put there?
type clipboardKeeper struct {
	cb    clipboardPort
	timer newClipboardTimer

	mu        sync.Mutex
	pending   bool
	digest    [sha256.Size]byte
	scheduled clipboardTimer
}

func newClipboardKeeper(cb clipboardPort, timer newClipboardTimer) *clipboardKeeper {
	if cb == nil {
		cb = systemClipboard{}
	}
	if timer == nil {
		timer = realClipboardTimer
	}
	return &clipboardKeeper{cb: cb, timer: timer}
}

// copy puts value on the clipboard and records what to look for when
// clearing it. Any previously pending clear is cancelled first: its
// value is no longer on the clipboard, so its timer firing later could
// only ever be a no-op or — if the hashes collided — a wrongly-timed
// wipe of this copy.
func (k *clipboardKeeper) copy(value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.scheduled != nil {
		k.scheduled.Stop()
		k.scheduled = nil
	}
	if err := k.cb.Write(value); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: copying to the clipboard: %w", err))
	}
	k.digest = sha256.Sum256([]byte(value))
	k.pending = true
	return nil
}

// scheduleClear arranges for clear to run after d without blocking the
// caller — session mode's half of the clipboard decision, where the
// process is already long-lived and the human wants their prompt back.
func (k *clipboardKeeper) scheduleClear(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.pending {
		return
	}
	k.scheduled = k.timer(d, func() { _ = k.clear() })
}

// clear wipes the clipboard, but only if it still holds what gage put
// there. Clearing unconditionally would throw away whatever the human
// copied in the meantime — the clipboard is shared state gage is a guest
// in, and the timeout's job is to remove gage's own value, not to hold
// the clipboard hostage for 45 seconds.
//
// A clipboard that can't be read is left alone rather than wiped: with
// no way to tell whose value is on it, the safe answer is not to
// destroy a stranger's.
//
// It is idempotent and safe to call when nothing is pending, which is
// what lets a session's exit path call it unconditionally.
func (k *clipboardKeeper) clear() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.scheduled != nil {
		k.scheduled.Stop()
		k.scheduled = nil
	}
	if !k.pending {
		return nil
	}
	// Cleared from gage's books either way. A failure below means the
	// value may still be on the clipboard, which the human is told
	// about; what must not happen is a retry loop that keeps trying to
	// wipe a clipboard someone else has since taken over.
	k.pending = false

	current, err := k.cb.Read()
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: reading the clipboard back: %w", err))
	}
	got := sha256.Sum256([]byte(current))
	// Constant-time not because a timing attack is plausible here, but
	// because comparing secret-derived bytes any other way is the habit
	// that eventually gets used somewhere it does matter.
	if subtle.ConstantTimeCompare(got[:], k.digest[:]) != 1 {
		return nil
	}
	if err := k.cb.Write(""); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: clearing the clipboard: %w", err))
	}
	return nil
}
