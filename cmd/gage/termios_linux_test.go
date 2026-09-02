//go:build linux

package main

import "golang.org/x/sys/unix"

// ioctlReadTermios is the ioctl that reads a terminal's line settings.
// Its name differs between Linux and the BSDs, which is the only
// per-platform detail the pty echo test needs.
const ioctlReadTermios = unix.TCGETS
