//go:build unix

package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

// coreDumpSuppressionIsIrrevocable says whether this platform lets a
// process disable core dumps in a way it cannot then undo. POSIX does:
// lowering RLIMIT_CORE's *hard* limit is one-way for the life of the
// process. Windows does not, which is why this is a per-platform
// constant the test branches on rather than an assertion every platform
// is assumed to satisfy.
const coreDumpSuppressionIsIrrevocable = true

// undoCoreDumpSuppression raises RLIMIT_CORE back above zero so that
// disableCoreDumps has something to actually do. Without it the core-dump
// test is vacuous on any machine whose shell already sets `ulimit -c 0` —
// which is the default on macOS and on many Linux distributions, so the
// test would have passed even against a disableCoreDumps that did
// nothing at all.
//
// Returns false when the limit can't be raised, which is the legitimate
// case where an outer process has already lowered the *hard* limit.
func undoCoreDumpSuppression(t *testing.T) bool {
	t.Helper()
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &lim); err != nil {
		t.Fatalf("reading RLIMIT_CORE: %v", err)
	}
	if lim.Max == 0 {
		return false
	}
	raised := unix.Rlimit{Cur: 1, Max: lim.Max}
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &raised); err != nil {
		return false
	}
	return true
}

// hardLimitIsZero reports whether the hard limit was lowered too, which
// is what stops anything in the process from raising the soft limit
// again. disableCoreDumps claims to do this, so the claim gets asserted.
func hardLimitIsZero(t *testing.T) bool {
	t.Helper()
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &lim); err != nil {
		t.Fatalf("reading RLIMIT_CORE: %v", err)
	}
	return lim.Max == 0
}
