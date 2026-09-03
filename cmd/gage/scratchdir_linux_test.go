//go:build linux

package main

import "testing"

// TestScratchDirPrefersTmpfsOnLinux: on a Linux host, scratchDir() must
// resolve to a directory that actually verifies as tmpfs — either
// $XDG_RUNTIME_DIR or /dev/shm — whenever one of the two is available,
// rather than silently falling back to the (disk-backed, on most distros)
// standard temp directory. Most CI runners provide /dev/shm.
func TestScratchDirPrefersTmpfsOnLinux(t *testing.T) {
	if !isTmpfs("/dev/shm") && !isTmpfs(t.TempDir()) {
		// Neither /dev/shm nor a fresh temp dir verify as tmpfs on this
		// host (some minimal containers strip /dev/shm entirely) — nothing
		// to assert against tmpfs preference here, so this environment
		// can't exercise the property this test is for.
		t.Skip("no tmpfs-backed directory available on this host to prefer")
	}

	dir := scratchDir()
	if !isTmpfs(dir) {
		t.Errorf("scratchDir() = %q, which does not verify as tmpfs, though a tmpfs-backed directory is available on this host", dir)
	}
}

// TestIsTmpfsRejectsANonTmpfsDirectory: a directory that plainly isn't
// tmpfs-backed (t.TempDir(), which on Linux CI is ordinarily disk-backed)
// must not be reported as tmpfs — otherwise isTmpfs would just be
// returning true unconditionally.
func TestIsTmpfsRejectsANonExistentDirectory(t *testing.T) {
	if isTmpfs("/gage-test-this-path-should-never-exist-anywhere") {
		t.Error("isTmpfs reported true for a path that doesn't exist")
	}
}
