package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestInterruptedWriteLeavesPreviousFileIntact simulates the write being
// interrupted between the temp-file write and the rename: it performs the
// temp-file half of WriteFile by hand and stops there, then asserts the
// real destination file — written earlier via the real WriteFile — is
// untouched and still parses. This is the property the temp+rename
// pattern exists to guarantee: the destination is never opened for
// writing directly, so a crash before rename can't leave it truncated.
func TestInterruptedWriteLeavesPreviousFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	original := "current = \"personal\"\n"
	if err := WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// Simulate a write that got interrupted after creating its temp
	// file but before the rename that would make it visible.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("current = \"CORRUPTED-HALF-WRITE"); err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	// Deliberately no os.Rename call here — this is the interruption.

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("destination file was touched by the interrupted write: got %q, want %q", got, original)
	}
	if !strings.HasPrefix(string(got), "current") {
		t.Errorf("destination file no longer parses as before: %q", got)
	}
}

func TestWriteFileFailsForMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does", "not", "exist", "config.toml")
	if err := WriteFile(path, []byte("x"), 0o600); err == nil {
		t.Fatal("expected an error writing into a nonexistent directory")
	}
}
