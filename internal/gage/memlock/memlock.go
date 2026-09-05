// Package memlock page-locks byte slices holding key material so the OS
// can't write them to swap, and releases those locks again — mlock(2)/
// munlock(2) on Linux and macOS, VirtualLock/VirtualUnlock on Windows,
// behind one signature callers use unchanged regardless of platform. See
// "Session model" in the design doc.
//
// Locking is best-effort by design, not by accident. It fails routinely
// under a restrictive RLIMIT_MEMLOCK (`ulimit -l`), in containers, and
// under locked-down Windows policy, and the design's answer there is to
// warn once and proceed rather than refuse to unlock: "a weaker
// guarantee beats an unusable tool." This package therefore reports
// failures faithfully and leaves the warn-or-fail decision to its
// caller — it never downgrades a failure to silence of its own accord.
package memlock

import (
	"os"
	"unsafe"
)

// Lock pins b's pages in physical memory. A zero-length slice is a no-op
// success: there is nothing to pin, and the platform calls disagree about
// whether a zero-length range is an error.
func Lock(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return lock(b)
}

// Unlock releases a lock taken by Lock. Like Lock, a zero-length slice is
// a no-op success. Unlocking a slice that was never locked is the
// caller's error to avoid — this package doesn't track lock state,
// because the thing that owns that state (an Identity, and its
// idempotent Close) is where it can actually be reasoned about.
func Unlock(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return unlock(b)
}

// Alloc returns an n-byte slice that occupies pages no other allocation
// shares, for callers that intend to Lock it.
//
// This exists because neither mlock nor VirtualLock is reference-counted
// and both operate on whole pages. Locking a 74-byte key pins the entire
// page it sits on, and unlocking it releases that page for everything
// else living there — so two keys that share a page share a single lock,
// and the first Unlock drops the protection out from under the other.
// Go's allocator puts two small allocations on the same page as a matter
// of course (measured at 20 out of 20 for key-sized buffers), so "they
// probably won't collide" is not a guarantee available to us.
//
// The failure mode differs by platform, which is what made this worth a
// dedicated allocator rather than a comment: munlock(2) unlocks a page
// it no longer should and reports success, losing the second key's
// protection in silence, while VirtualUnlock fails the second call with
// ERROR_NOT_LOCKED. Only Windows says anything, and what it says is a
// confusing error on the innocent caller rather than the real problem.
//
// The returned slice's capacity is capped at n so an append can't spill
// into the surrounding pages, which are allocated but never locked.
func Alloc(n int) []byte {
	if n <= 0 {
		return nil
	}
	pg := os.Getpagesize()
	// Two pages of slack: up to one page is lost to aligning the start,
	// and up to one more rounds the end out to a whole page. Both bounds
	// are worst-case, so this needs no arithmetic a reader has to
	// re-derive before trusting it.
	raw := make([]byte, n+2*pg)

	// #nosec G103 -- the address is converted straight to an offset
	// within raw and used as a slice index; no pointer is reconstructed
	// from it, so this is arithmetic on a number rather than an unsafe
	// pointer that could outlive its object.
	base := uintptr(unsafe.Pointer(&raw[0]))
	off := int((base+uintptr(pg)-1)&^(uintptr(pg)-1) - base)
	return raw[off : off+n : off+n]
}
