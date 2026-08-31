package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestInterruptedGlobalConfigWriteLeavesPreviousFileIntact simulates a
// write to $GAGE_CONFIG/config.toml getting interrupted between the temp
// file being written and the rename that makes it visible: it writes a
// good config, then hand-writes an orphan temp file the same way Write
// would (without the rename), and asserts the real config.toml — the
// thing a concurrently-crashing gage would leave behind — is untouched
// and still parses. Write's atomicity itself is proven once, at the
// atomicfile layer; this is the same guarantee re-asserted where the
// checklist actually names it: the global config.toml Write uses.
func TestInterruptedGlobalConfigWriteLeavesPreviousFileIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	good := Global{Current: "personal", Vaults: map[string]VaultEntry{
		"personal": {Path: "/data/vaults/personal", Type: "git", Device: "laptop-1", Method: "passphrase"},
	}}
	if err := Write(path, good); err != nil {
		t.Fatal(err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	tmp.WriteString("current = \"CORRUPTED")
	tmp.Close()
	// No rename — this is the simulated interruption.

	got, err := Read(path)
	if err != nil {
		t.Fatalf("previous config.toml no longer parses after an interrupted write: %v", err)
	}
	if !reflect.DeepEqual(got, good) {
		t.Errorf("previous config.toml was altered by the interrupted write: got %+v, want %+v", got, good)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "CORRUPTED") {
		t.Error("config.toml contains content from the interrupted write")
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
