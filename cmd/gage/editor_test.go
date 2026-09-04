package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage"
)

func sampleSeedEntry() gage.Entry {
	now := gage.NewTimestamp(time.Now())
	return gage.Entry{
		Title:       "ProtonMail",
		Description: "personal email",
		Created:     now,
		Updated:     now,
		UpdatedBy:   "laptop-1",
		Value:       "",
	}
}

// TestEditYAMLUnchangedReportsUnchangedAndParsesBackIdentically is the
// sanity check for the whole fake-editor mechanism: a "noop" editor
// leaves the file untouched, so editYAML must report unchanged == true
// and hand back an Entry equal to what was seeded.
func TestEditYAMLUnchangedReportsUnchangedAndParsesBackIdentically(t *testing.T) {
	setFakeEditor(t, "noop", nil)

	seed := sampleSeedEntry()
	got, unchanged, err := editYAML(seed)
	if err != nil {
		t.Fatalf("editYAML: %v", err)
	}
	if !unchanged {
		t.Error("unchanged = false, want true for a no-op edit")
	}
	if got.Title != seed.Title || got.Description != seed.Description {
		t.Errorf("got = %+v, want it to match the seed", got)
	}
}

// TestEditYAMLSetValueReportsChanged proves the other direction: an
// editor that actually changes the file reports unchanged == false and
// the change is visible in the parsed result.
func TestEditYAMLSetValueReportsChanged(t *testing.T) {
	setFakeEditor(t, "set-value", map[string]string{"GAGE_TEST_FAKE_EDITOR_VALUE": "new-secret"})

	got, unchanged, err := editYAML(sampleSeedEntry())
	if err != nil {
		t.Fatalf("editYAML: %v", err)
	}
	if unchanged {
		t.Error("unchanged = true, want false: the fake editor changed the value")
	}
	if got.Value != "new-secret" {
		t.Errorf("Value = %q, want %q", got.Value, "new-secret")
	}
}

// TestEditYAMLInvalidYAMLFailsToParse: an editor that leaves unparseable
// content behind must surface that as an error, not silently succeed.
func TestEditYAMLInvalidYAMLFailsToParse(t *testing.T) {
	setFakeEditor(t, "invalid-yaml", nil)

	_, _, err := editYAML(sampleSeedEntry())
	if err == nil {
		t.Fatal("expected an error for unparseable edited content")
	}
}

// TestEditYAMLNonZeroExitFails: $EDITOR exiting nonzero must fail
// editYAML rather than reading back whatever's on disk.
func TestEditYAMLNonZeroExitFails(t *testing.T) {
	setFakeEditor(t, "fail", nil)

	_, _, err := editYAML(sampleSeedEntry())
	if err == nil {
		t.Fatal("expected an error when $EDITOR exits non-zero")
	}
}

// TestEditYAMLScratchFileRemovedOnEveryExitPath drives editYAML through
// each exit path (success, non-zero exit, unparseable content) and
// checks, via the fake editor's side-channel record, that the scratch
// file no longer exists afterward.
func TestEditYAMLScratchFileRemovedOnEveryExitPath(t *testing.T) {
	for _, mode := range []string{"noop", "set-value", "fail", "invalid-yaml"} {
		t.Run(mode, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "record.txt")
			extra := map[string]string{fakeEditorRecordEnvVar: record}
			if mode == "set-value" {
				extra["GAGE_TEST_FAKE_EDITOR_VALUE"] = "x"
			}
			setFakeEditor(t, mode, extra)

			_, _, _ = editYAML(sampleSeedEntry())

			data, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("fake editor never recorded the scratch path: %v", err)
			}
			lines := strings.SplitN(string(data), "\n", 2)
			path := lines[0]
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("scratch file %q still exists after editYAML (mode %s): stat err = %v", path, mode, err)
			}
		})
	}
}

// TestEditYAMLScratchFileCreated0600 checks the scratch file's
// permissions at the moment the fake editor sees it — after editYAML
// returns, the file is already gone.
func TestEditYAMLScratchFileCreated0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits don't apply on Windows")
	}
	record := filepath.Join(t.TempDir(), "record.txt")
	setFakeEditor(t, "noop", map[string]string{fakeEditorRecordEnvVar: record})

	if _, _, err := editYAML(sampleSeedEntry()); err != nil {
		t.Fatalf("editYAML: %v", err)
	}

	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(data), "\n", 2)
	if len(lines) < 2 {
		t.Fatalf("record file malformed: %q", data)
	}
	perm := strings.TrimSpace(lines[1])
	if perm != "-rw-------" {
		t.Errorf("scratch file permissions = %q, want %q (0600)", perm, "-rw-------")
	}
}

// TestEditYAMLUnsetEditorFailsBeforeAnyScratchFile: an unset $EDITOR
// must fail before a scratch file is ever created — checked by pointing
// the record var at a path and confirming nothing was ever written
// there.
func TestEditYAMLUnsetEditorFailsBeforeAnyScratchFile(t *testing.T) {
	t.Setenv("EDITOR", "")
	record := filepath.Join(t.TempDir(), "record.txt")
	t.Setenv(fakeEditorRecordEnvVar, record)

	_, _, err := editYAML(sampleSeedEntry())
	if err == nil {
		t.Fatal("expected an error for an unset $EDITOR")
	}
	if _, statErr := os.Stat(record); !os.IsNotExist(statErr) {
		t.Error("a scratch file was recorded despite $EDITOR being unset — $EDITOR must be validated before any file is written")
	}
}

// TestEditYAMLUnusableEditorFailsBeforeAnyScratchFile mirrors the above
// for an $EDITOR naming something that doesn't exist.
func TestEditYAMLUnusableEditorFailsBeforeAnyScratchFile(t *testing.T) {
	t.Setenv("EDITOR", "gage-test-definitely-not-a-real-editor-"+strconv.Itoa(os.Getpid()))
	record := filepath.Join(t.TempDir(), "record.txt")
	t.Setenv(fakeEditorRecordEnvVar, record)

	_, _, err := editYAML(sampleSeedEntry())
	if err == nil {
		t.Fatal("expected an error for an unusable $EDITOR")
	}
	if _, statErr := os.Stat(record); !os.IsNotExist(statErr) {
		t.Error("a scratch file was recorded despite $EDITOR being unusable")
	}
}
