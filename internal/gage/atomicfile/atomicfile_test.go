package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// statFile returns path's identity via an open handle rather than
// os.Stat(path) alone. On Windows, os.Stat defers fetching the file-index
// fields SameFile compares until SameFile is first called on the result,
// and resolves them by reopening the *path* at that time — so two
// os.Stat results for the same path, taken before and after a
// rename-replace, both resolve to whatever now lives at that path and
// compare equal regardless of whether the underlying file object
// changed. An already-open handle's Stat resolves the file index
// immediately, at open time, avoiding that.
func statFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestWriteFileCreatesAndReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := WriteFile(path, []byte("current = \"personal\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "current = \"personal\"\n" {
		t.Errorf("content = %q", got)
	}

	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteFileOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := WriteFile(path, []byte("version 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("version 2"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "version 2" {
		t.Errorf("content = %q, want %q", got, "version 2")
	}

	// No leftover temp files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (leftover temp file?): %v", len(entries), entries)
	}
}

// TestWriteFileReplacesRatherThanTruncatesInPlace is the test that
// actually discriminates an atomic implementation from a naive one, and
// it's the reason the crash-safety claim is credible at all.
//
// "Simulate the interruption" is the tempting way to write this and it
// is worthless: hand-rolling a temp file that never gets renamed, then
// asserting the destination is unchanged, passes just as happily against
// a plain os.WriteFile — nothing in that arrangement ever exercises
// WriteFile's own interrupted path.
//
// What separates the two implementations is observable without any fault
// injection: temp+rename makes the destination path point at a *new*
// filesystem object, so the pre-write object is never truncated and a
// crash at any instant leaves one whole version or the other. An
// in-place write reuses the same object — which is precisely the state
// in which a crash yields a truncated config. So: stat before, stat
// after, and require they are not the same file.
func TestWriteFileReplacesRatherThanTruncatesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	original := "current = \"personal\"\n"
	if err := WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	before := statFile(t, path)

	if err := WriteFile(path, []byte("current = \"work\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := statFile(t, path)

	if os.SameFile(before, after) {
		t.Error("WriteFile wrote the destination in place (same file object before and after); " +
			"a crash mid-write would leave a truncated file, which is exactly what temp+rename exists to prevent")
	}
}

// TestFailedWriteLeavesPreviousFileIntact exercises a real WriteFile
// failure — not a simulated one — and asserts the previous contents
// survive it. The failure is induced by making the containing directory
// unwritable, so WriteFile's own os.CreateTemp fails and it returns
// before touching the destination.
func TestFailedWriteLeavesPreviousFileIntact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits don't restrict writes the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits are not enforced")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	original := "current = \"personal\"\n"
	if err := WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := WriteFile(path, []byte("current = \"CORRUPTED"), 0o600); err == nil {
		t.Fatal("expected WriteFile to fail in an unwritable directory")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("previous file unreadable after a failed write: %v", err)
	}
	if string(got) != original {
		t.Errorf("previous file altered by a failed write: got %q, want %q", got, original)
	}
	if strings.Contains(string(got), "CORRUPTED") {
		t.Error("previous file contains content from the failed write")
	}
}

func TestWriteFileFailsForMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does", "not", "exist", "config.toml")
	if err := WriteFile(path, []byte("x"), 0o600); err == nil {
		t.Fatal("expected an error writing into a nonexistent directory")
	}
}
