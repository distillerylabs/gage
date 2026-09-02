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
