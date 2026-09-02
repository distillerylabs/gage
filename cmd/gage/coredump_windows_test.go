//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

// coreDumpSuppressionIsIrrevocable is false on Windows, and that is the
// point rather than an omission: SetErrorMode has no one-way form, so
// anything in the process can call it again and clear the bits gage set.
// POSIX's RLIMIT_CORE hard limit has no Windows counterpart.
//
// This is the same narrower-guarantee the design doc records for Windows
// instead of claiming parity, and it is a constant the test branches on
// so the difference is visible where the assertion is made — not buried
// in a helper that quietly returns "fine" on a platform where the
// property does not exist.
const coreDumpSuppressionIsIrrevocable = false

// undoCoreDumpSuppression clears the process error mode so that
// disableCoreDumps has something to actually do — the Windows analogue of
// raising RLIMIT_CORE. See the unix file for why this matters: without
// it, the test would pass against a disableCoreDumps that did nothing.
func undoCoreDumpSuppression(t *testing.T) bool {
	t.Helper()
	windows.SetErrorMode(0)
	disabled, err := coreDumpsDisabled()
	if err != nil {
		t.Fatalf("reading the process error mode: %v", err)
	}
	return !disabled
}

// hardLimitIsZero has no meaning on Windows — there is no hard limit to
// lower. It is never consulted, because coreDumpSuppressionIsIrrevocable
// gates every caller; it returns false so that if that gate is ever
// flipped by mistake, the test fails loudly rather than reporting a
// guarantee this platform cannot make.
func hardLimitIsZero(t *testing.T) bool { return false }
