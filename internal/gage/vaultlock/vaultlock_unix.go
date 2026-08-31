//go:build unix

package vaultlock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile makes one non-blocking attempt to exclusively flock f.
// flock locks the whole open file description rather than a byte range,
// so — unlike the Windows implementation — there's no need to reserve a
// separate byte range for holder-info bytes to stay readable: flock never
// blocks plain read()/write() syscalls at all, only other flock() calls.
func tryLockFile(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return errWouldBlock
	}
	return err
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
