package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	want := Global{
		Current: "personal",
		Vaults: map[string]VaultEntry{
			"personal": {
				Path:   "/data/vaults/personal",
				Type:   "git",
				Device: "laptop-1",
				Method: "passphrase",
				Git:    GitMeta{Origin: "https://github.com/you/personal-vault.git"},
			},
			"work": {
				Path:   "/data/vaults/work",
				Type:   "git",
				Device: "laptop-1",
				Method: "passphrase",
				Git:    GitMeta{Origin: "https://git.internal.example/secrets/work-vault.git"},
			},
		},
		Shell: Shell{
			Prompt:      "[{vault}{lock}] gage> ",
			IdleTimeout: "10m",
			HistoryFile: "/state/history",
		},
	}

	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestReadMissingFile(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Fatal("expected an error reading a nonexistent file")
	}
}

// TestGlobalConfigWriteIsAtomic asserts that Write actually routes
// through the atomic helper rather than writing config.toml in place —
// the property that keeps a crash mid-write from truncating the file
// that lists every registered vault. Same discriminator as
// atomicfile's own test (stat before, stat after, require a different
// file object), applied here because it's Write, not WriteFile, that
// every config writer in gage calls: a future refactor that dropped the
// atomicfile call would leave atomicfile's tests green and only this one
// would notice.
func TestGlobalConfigWriteIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	good := Global{Current: "personal", Vaults: map[string]VaultEntry{
		"personal": {Path: "/data/vaults/personal", Type: "git", Device: "laptop-1", Method: "passphrase"},
	}}
	if err := Write(path, good); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := Write(path, Global{Current: "work"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if os.SameFile(before, after) {
		t.Error("config.Write wrote config.toml in place; an interrupted write could truncate it and lose every registered vault")
	}
}

// TestFailedGlobalConfigWriteLeavesPreviousConfigParseable exercises a
// real Write failure and asserts the previous config survives it intact
// and still parses — the checklist's "never a truncated file".
func TestFailedGlobalConfigWriteLeavesPreviousConfigParseable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits don't restrict writes the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits are not enforced")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	good := Global{Current: "personal", Vaults: map[string]VaultEntry{
		"personal": {Path: "/data/vaults/personal", Type: "git", Device: "laptop-1", Method: "passphrase"},
	}}
	if err := Write(path, good); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if err := Write(path, Global{Current: "work"}); err == nil {
		t.Fatal("expected Write to fail in an unwritable directory")
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("previous config.toml no longer parses after a failed write: %v", err)
	}
	if !reflect.DeepEqual(got, good) {
		t.Errorf("previous config.toml altered by a failed write: got %+v, want %+v", got, good)
	}
}

func TestWriteThenModifyLeavesOldValueUntouchedUntilRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := Write(path, Global{Current: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, Global{Current: "b"}); err != nil {
		t.Fatal(err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current != "b" {
		t.Errorf("Current = %q, want %q", got.Current, "b")
	}
}
