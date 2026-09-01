package gitrepo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/denmark/gage/internal/gage/gittest"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInitAndCommitCreatesExactlyOneCommit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")

	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	count, err := CommitCount(dir)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("CommitCount = %d, want 1", count)
	}

	clean, err := IsClean(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("IsClean = false immediately after InitAndCommit, want true")
	}
}

func TestIsCleanDetectsDirtyWorkingTree(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, "a.txt"), "changed")

	clean, err := IsClean(dir)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Error("IsClean = true with an uncommitted modification, want false")
	}
}

func TestRemoteURLEmptyWhenUnset(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	url, err := RemoteURL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if url != "" {
		t.Errorf("RemoteURL = %q, want empty", url)
	}
}

func TestSetRemoteThenChangeIt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	first := gittest.NewBareRemote(t)
	if err := SetRemote(dir, first); err != nil {
		t.Fatal(err)
	}
	got, err := RemoteURL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Errorf("RemoteURL = %q, want %q", got, first)
	}

	second := gittest.NewBareRemote(t)
	if err := SetRemote(dir, second); err != nil {
		t.Fatal(err)
	}
	got, err = RemoteURL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != second {
		t.Errorf("RemoteURL after re-set = %q, want %q", got, second)
	}
}
