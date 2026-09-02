//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

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

// hardLimitIsAlsoZero has no Windows equivalent: SetErrorMode has no
// irrevocable form, which is part of why the design records Windows'
// guarantee as narrower than POSIX's rather than claiming parity. The
// test skips that assertion here rather than asserting a fiction.
func hardLimitIsAlsoZero(t *testing.T) bool { return true }
