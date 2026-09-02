//go:build darwin

package main

import "golang.org/x/sys/unix"

// ioctlReadTermios — see the Linux file. macOS (and the BSDs) spell the
// same ioctl TIOCGETA.
const ioctlReadTermios = unix.TIOCGETA
