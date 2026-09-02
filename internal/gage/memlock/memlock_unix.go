//go:build unix

package memlock

import (
	"errors"

	"golang.org/x/sys/unix"
)

// lock/unlock are mlock(2)/munlock(2). Both take the slice directly:
// x/sys/unix computes the address and length from it, and the kernel
// rounds the range out to whole pages itself, so a 32-byte key pins the
// page it happens to sit on rather than requiring page-aligned
// allocation here.
func lock(b []byte) error {
	return unix.Mlock(b)
}

func unlock(b []byte) error {
	return unix.Munlock(b)
}

// resourceLimited reports whether err is the platform's "you are not
// allowed to lock this much memory" answer rather than a genuine bug.
// ENOMEM is the RLIMIT_MEMLOCK ceiling (the common case in containers and
// under a low `ulimit -l`); EPERM is the same refusal on kernels that
// report it that way for an unprivileged process. Used only by this
// package's own test, to tell "the platform said no" apart from "our
// call was wrong" instead of skipping on any error at all.
func resourceLimited(err error) bool {
	return errors.Is(err, unix.ENOMEM) || errors.Is(err, unix.EPERM)
}
