//go:build windows

package vaultlock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// LockFileEx locks a byte range, not a whole-file description the way
// flock does. Locking a fixed reserved range far past any realistic file
// size — rather than the bytes actually used for holder info — keeps that
// holder-info read/write path (offset 0) untouched by the lock, so a
// contending process can still read who holds it without itself needing
// the lock. Any single-byte range would do; this offset is just
// comfortably out of the way.
const (
	lockOffsetLow  uint32 = 0
	lockOffsetHigh uint32 = 0x7fffffff
	lockLen        uint32 = 1
)

// tryLockFile makes one non-blocking attempt to exclusively lock f's
// reserved byte range.
func tryLockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	ol.Offset = lockOffsetLow
	ol.OffsetHigh = lockOffsetHigh

	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		lockLen,
		0,
		ol,
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errWouldBlock
	}
	return err
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	ol.Offset = lockOffsetLow
	ol.OffsetHigh = lockOffsetHigh
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockLen, 0, ol)
}
