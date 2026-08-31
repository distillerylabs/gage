// Package vaultlock is a cross-platform, per-vault advisory file lock:
// flock(2) on Linux/macOS, LockFileEx on Windows, behind one signature.
// It's a correctness mechanism between cooperating gage processes, not a
// security one — see "Concurrent processes and the vault lock" in the
// design doc. Writes take it across their whole read-modify-commit
// sequence; reads never do. Not used by anything yet in M0 — M4 is the
// first caller.
package vaultlock

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// pollInterval is how often Acquire retries a contended lock while
// waiting. Advisory locks on both platforms have no "wake me when free"
// API this package uses, so waiting is a short poll loop rather than a
// blocking syscall — simple, and fine at gage's contention levels (a
// handful of local processes, not a lock server).
const pollInterval = 20 * time.Millisecond

// HolderInfo is what Acquire records about whoever currently holds a
// lock, so a contended acquisition can report who's holding it rather
// than just "someone." Best-effort: a lock file predating this package,
// or one written by a process that died between opening and recording,
// yields a zero HolderInfo.
type HolderInfo struct {
	PID   int
	Since time.Time
}

func (h HolderInfo) String() string {
	if h.PID == 0 {
		return "another process"
	}
	return fmt.Sprintf("pid %d (since %s)", h.PID, h.Since.Format(time.RFC3339))
}

// ContendedError is returned when Acquire gives up after its timeout
// without obtaining the lock.
type ContendedError struct {
	Path   string
	Holder HolderInfo
}

func (e *ContendedError) Error() string {
	return fmt.Sprintf("vaultlock: %s is locked by %s", e.Path, e.Holder)
}

// errWouldBlock is the platform layer's signal that the lock is currently
// held by someone else — never returned to callers directly, always
// translated into a *ContendedError once the timeout is reached.
var errWouldBlock = errors.New("vaultlock: would block")

// Lock is a held advisory lock. The zero value is not usable; obtain one
// from Acquire.
type Lock struct {
	file *os.File
	path string
}

// Acquire blocks until it holds the advisory lock at path, the timeout
// elapses, or an unrecoverable error occurs.
//
// Per "Contended-lock behavior" in the plan: a contended lock waits
// (polling up to timeout) rather than failing immediately, since whoever
// holds it is almost always about to finish — but it still gives up
// eventually rather than hanging a terminal forever. Pass timeout <= 0 to
// try exactly once and fail fast instead of waiting at all.
//
// The lock is released by the OS the moment every file descriptor
// pointing at it closes — including on the holding process being killed
// — so there is never a stale lock file to clean up by hand.
func Acquire(path string, timeout time.Duration) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("vaultlock: opening %s: %w", path, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		lockErr := tryLockFile(f)
		if lockErr == nil {
			_ = writeHolderInfo(f, HolderInfo{PID: os.Getpid(), Since: time.Now()})
			return &Lock{file: f, path: path}, nil
		}
		if !errors.Is(lockErr, errWouldBlock) {
			f.Close()
			return nil, fmt.Errorf("vaultlock: locking %s: %w", path, lockErr)
		}
		if timeout <= 0 || time.Now().After(deadline) {
			holder := readHolderInfo(f)
			f.Close()
			return nil, &ContendedError{Path: path, Holder: holder}
		}
		time.Sleep(pollInterval)
	}
}

// Release drops the lock and closes its underlying file handle. Safe to
// call on a nil *Lock.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("vaultlock: unlocking %s: %w", l.path, unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("vaultlock: closing %s: %w", l.path, closeErr)
	}
	return nil
}

// writeHolderInfo records who holds the lock into the plain (unlocked)
// byte range of the file, so a contending process can read it back
// without itself needing the lock — see the platform files for why that
// byte range is safe to touch concurrently on both platforms.
func writeHolderInfo(f *os.File, h HolderInfo) error {
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	content := fmt.Sprintf("pid=%d\nsince=%s\n", h.PID, h.Since.UTC().Format(time.RFC3339Nano))
	if err := f.Truncate(int64(len(content))); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(content), 0)
	return err
}

// readHolderInfo is best-effort: a malformed or missing record yields a
// zero HolderInfo rather than an error, since this only ever feeds a
// human-facing message.
func readHolderInfo(f *os.File) HolderInfo {
	buf := make([]byte, 256)
	n, _ := f.ReadAt(buf, 0)
	var h HolderInfo
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "pid":
			if pid, err := strconv.Atoi(value); err == nil {
				h.PID = pid
			}
		case "since":
			if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
				h.Since = t
			}
		}
	}
	return h
}
