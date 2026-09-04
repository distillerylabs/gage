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

// TestEditYAMLScratchFileLandsInScratchDir: the file editYAML actually
// opens must live in the directory scratchDir() chose — on Linux a
// statfs-verified tmpfs one, elsewhere the OS's standard temp directory.
// Without this, scratchDir() could be correct and unused.
func TestEditYAMLScratchFileLandsInScratchDir(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record.txt")
	setFakeEditor(t, "noop", map[string]string{fakeEditorRecordEnvVar: record})

	if _, _, err := editYAML(sampleSeedEntry()); err != nil {
		t.Fatalf("editYAML: %v", err)
	}

	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.SplitN(string(data), "\n", 2)[0]

	gotDir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatalf("resolving the scratch file's directory: %v", err)
	}
	// EvalSymlinks on both sides: macOS's temp dir is /var/... behind a
	// symlink to /private/var/..., so a raw string compare would fail on
	// a correct implementation.
	wantDir, err := filepath.EvalSymlinks(scratchDir())
	if err != nil {
		t.Fatalf("resolving scratchDir(): %v", err)
	}
	if gotDir != wantDir {
		t.Errorf("scratch file landed in %q, want scratchDir() = %q", gotDir, wantDir)
	}
}

// TestOverwriteFileZeroesContentsInPlace is the "contents overwritten
// first" half of the cleanup bullet. It has to be asserted here rather
// than through editYAML: by the time editYAML returns, the file is
// unlinked and there is nothing left to read back.
func TestOverwriteFileZeroesContentsInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scratch.yaml")
	const secret = "value: correcthorsebatterystaple\n"
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := overwriteFile(path); err != nil {
		t.Fatalf("overwriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(secret) {
		t.Errorf("file length = %d, want the original %d (overwrite in place, not truncate)", len(got), len(secret))
	}
	if strings.Contains(string(got), "correcthorsebatterystaple") {
		t.Errorf("the plaintext survived the overwrite: %q", got)
	}
	for i, b := range got {
		if b != 0 {
			t.Errorf("byte %d = %#x after overwrite, want 0x00", i, b)
			break
		}
	}
}

// TestOverwriteThenRemoveOverwritesBeforeUnlinking pins the ordering the
// bullet actually specifies — overwrite *then* remove — rather than just
// the end state.
//
// Asserting the order needs something that outlives the unlink, so the
// test hard-links a second name onto the same inode first. Unlinking
// `path` then leaves the data reachable through `witness`: zeroed if the
// overwrite really ran before the unlink, still-readable plaintext if it
// didn't. Checking only "the file is gone afterward" would pass against
// an overwriteThenRemove that had been reduced to a bare os.Remove —
// which is exactly the regression this guards.
func TestOverwriteThenRemoveOverwritesBeforeUnlinking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scratch.yaml")
	witness := filepath.Join(dir, "witness.yaml")

	const secret = "value: correcthorsebatterystaple\n"
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, witness); err != nil {
		// Hard links aren't available everywhere (some Windows
		// filesystems, some CI sandboxes). The property is
		// platform-independent, but this way of observing it isn't.
		t.Skipf("hard links unavailable here, so the overwrite can't be observed after the unlink: %v", err)
	}

	if err := overwriteThenRemove(path); err != nil {
		t.Fatalf("overwriteThenRemove: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after overwriteThenRemove: stat err = %v", err)
	}

	got, err := os.ReadFile(witness)
	if err != nil {
		t.Fatalf("reading the surviving hard link: %v", err)
	}
	if strings.Contains(string(got), "correcthorsebatterystaple") {
		t.Errorf("the plaintext survived in the unlinked inode: %q — the contents must be overwritten before the file is removed", got)
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
