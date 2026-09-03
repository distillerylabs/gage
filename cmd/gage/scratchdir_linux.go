//go:build linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// tmpfsMagic is TMPFS_MAGIC from linux/magic.h — statfs's f_type for a
// tmpfs mount.
const tmpfsMagic = 0x01021994

// scratchDir picks editYAML's scratch-file directory on Linux: prefer a
// verified tmpfs-backed directory (never let a plaintext secret get
// memory-pressured onto swap by sitting on a disk-backed temp dir),
// trying $XDG_RUNTIME_DIR first and /dev/shm second, and falling back to
// the OS's standard temp directory only if neither actually verifies as
// tmpfs. "Verified" means statfs, not "the env var is set" — see the M5
// plan's "tmpfs-where-available posture."
func scratchDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" && isTmpfs(dir) {
		return dir
	}
	if isTmpfs("/dev/shm") {
		return "/dev/shm"
	}
	return os.TempDir()
}

// isTmpfs reports whether dir is itself the root of (or resolves onto) a
// tmpfs mount.
func isTmpfs(dir string) bool {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return false
	}
	return int64(stat.Type) == tmpfsMagic
}
