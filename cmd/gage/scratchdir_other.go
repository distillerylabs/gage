//go:build !linux

package main

import "os"

// scratchDir picks editYAML's scratch-file directory on macOS and
// Windows: the OS's standard secure per-user temp directory. Neither
// platform gives gage a portable, unprivileged way to verify or prefer a
// memory-backed filesystem the way Linux's tmpfs does, so this is the
// narrower guarantee the M5 plan calls for rather than a claim of parity
// with the Linux path in scratchdir_linux.go.
func scratchDir() string {
	return os.TempDir()
}
