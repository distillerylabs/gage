package gitrepo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/gittest"
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

// TestIsCleanCountsUntrackedFilesAsDirty pins down the property M1's
// vault tests reason from: "exactly one commit and a clean tree" is only
// evidence that the skeleton files were *committed* if an uncommitted
// file would have made the tree dirty. If go-git ever stopped counting
// untracked files, those tests would keep passing while no longer
// proving anything, so the assumption is asserted here rather than left
// implicit at the call site.
func TestIsCleanCountsUntrackedFilesAsDirty(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, "never-committed.txt"), "stray")

	clean, err := IsClean(dir)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Error("IsClean = true with an untracked file present; \"clean tree\" no longer implies \"everything was committed\"")
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

// TestHasRemoteOnARepoWithNoOriginConfigured is HasRemote's other "no"
// answer: not TestHasRemoteOnADirectoryThatIsNotARepo's "not a repository
// at all", but a real repository that has simply never had SetRemote
// called on it.
func TestHasRemoteOnARepoWithNoOriginConfigured(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	has, err := HasRemote(dir)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("HasRemote = true on a repository with no origin configured")
	}
}

// TestRemoteURLAndHasRemoteWithNoConfiguredURLs covers the other empty
// case both functions share: an "origin" section that exists in
// .git/config but names no url at all — go-git's own CreateRemote
// refuses to construct that in memory ("remote config: empty URL"), but
// a hand-edited or partially-written config file is exactly this shape,
// and both functions read whatever's on disk. Both report the same
// absence as no remote, distinct from TestRemoteURLEmptyWhenUnset's "no
// origin section whatsoever".
func TestRemoteURLAndHasRemoteWithNoConfiguredURLs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, ".git", "config")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("[remote \"origin\"]\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")...)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	url, err := RemoteURL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if url != "" {
		t.Errorf("RemoteURL with a URL-less origin = %q, want empty", url)
	}
	has, err := HasRemote(dir)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("HasRemote = true for an origin with zero configured URLs")
	}
}

// TestRequireCleanWorkTreeTruncatesTheNamedPaths is DirtyPaths' consumer
// side: a refusal names at most maxNamedDirtyPaths paths and folds the
// rest into an "and N more" suffix, since a vault mid-restore can have
// thousands and an error message is a sentence, not a report.
func TestRequireCleanWorkTreeTruncatesTheNamedPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "seed.txt"), "seed")
	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	const dirtyCount = maxNamedDirtyPaths + 1
	for i := range dirtyCount {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), "dirty")
	}

	err := requireCleanWorkTree(dir, "testing")
	if !errors.Is(err, ErrDirtyWorkTree) {
		t.Fatalf("requireCleanWorkTree = %v, want it to wrap ErrDirtyWorkTree", err)
	}
	for i := range maxNamedDirtyPaths {
		name := fmt.Sprintf("f%d.txt", i)
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error = %q, want it to name %q (one of the first %d, sorted)", err, name, maxNamedDirtyPaths)
		}
	}
	if strings.Contains(err.Error(), fmt.Sprintf("f%d.txt", maxNamedDirtyPaths)) {
		t.Errorf("error = %q, named more than maxNamedDirtyPaths paths", err)
	}
	if !strings.Contains(err.Error(), "and 1 more") {
		t.Errorf("error = %q, want it to summarize the one path left out", err)
	}
}
