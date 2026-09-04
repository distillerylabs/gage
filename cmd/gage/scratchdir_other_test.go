//go:build !linux

package main

import (
	"os"
	"testing"
)

// TestScratchDirIsTheOSTempDirOffLinux: on macOS and Windows the plan
// calls for the OS's standard secure temp directory, since neither
// platform offers gage an unprivileged way to verify a memory-backed
// filesystem the way Linux's statfs/tmpfs check does. Asserting the
// exact directory (not merely "somewhere writable") is what keeps this
// from silently drifting to, say, the current working directory.
func TestScratchDirIsTheOSTempDirOffLinux(t *testing.T) {
	if got, want := scratchDir(), os.TempDir(); got != want {
		t.Errorf("scratchDir() = %q, want the OS temp directory %q", got, want)
	}
}
