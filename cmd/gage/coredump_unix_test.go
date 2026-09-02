//go:build unix

package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

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

// hardLimitIsAlsoZero reports whether the *hard* limit was lowered too,
// which is what stops anything in the process from simply raising the
// soft limit again. disableCoreDumps claims to do this, so it gets
// asserted rather than assumed.
func hardLimitIsAlsoZero(t *testing.T) bool {
	t.Helper()
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &lim); err != nil {
		t.Fatalf("reading RLIMIT_CORE: %v", err)
	}
	return lim.Max == 0
}
