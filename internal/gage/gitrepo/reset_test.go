package gitrepo

import (
	"os"
	"path/filepath"
	"testing"
)

// seededRepo is a local repository with one commit and no remote — all
// ResetHard cares about, since it never looks past HEAD.
func seededRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeWorktreeFile(t, dir, "entries/tracked.age", "committed")
	// A vault is not only entries/: the two files that define who can
	// read it sit at the root, and an interrupted recipient write is the
	// one case that dirties them. A repo without them can't tell a reset
	// that covers the working tree from one that covers entries/.
	writeWorktreeFile(t, dir, ".age-recipients", "age1committed\n")
	if _, err := InitAndCommit(dir, "seed"); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustBeClean(t *testing.T, dir string) {
	t.Helper()
	clean, err := IsClean(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		paths, _ := DirtyPaths(dir)
		t.Errorf("the tree is still dirty after ResetHard: %v", paths)
	}
}

// TestResetHardDiscardsModifiedAndUntrackedFiles pins the behavior M9's
// dirty-tree precondition depends on, in both shapes an interrupted
// write actually leaves: an entry file overwritten partway through
// --reencrypt (tracked, modified) and an entry file an interrupted
// insert created but never committed (untracked).
//
// The untracked half is the one worth pinning. The pinned go-git happens
// to clear untracked files during a hard reset, but `git reset --hard`
// itself does not, so ResetHard discards them explicitly instead of
// depending on that — see its doc comment. This test asserts the outcome
// either way, which is what makes a go-git upgrade that changes its mind
// a red test rather than a vault that quietly folds an interrupted
// insert into the next write.
func TestResetHardDiscardsModifiedAndUntrackedFiles(t *testing.T) {
	dir := seededRepo(t)

	writeWorktreeFile(t, dir, "entries/tracked.age", "half-written ciphertext")
	writeWorktreeFile(t, dir, "entries/orphan.age", "an insert that never committed")

	discarded, err := ResetHard(dir)
	if err != nil {
		t.Fatalf("ResetHard: %v", err)
	}

	want := []string{"entries/orphan.age", "entries/tracked.age"}
	if len(discarded) != len(want) {
		t.Fatalf("ResetHard reported %v, want %v", discarded, want)
	}
	for i, path := range want {
		if discarded[i] != path {
			t.Errorf("discarded[%d] = %q, want %q (sorted)", i, discarded[i], path)
		}
	}

	got, err := os.ReadFile(filepath.Join(dir, "entries", "tracked.age"))
	if err != nil {
		t.Fatalf("the tracked file is gone rather than restored: %v", err)
	}
	if string(got) != "committed" {
		t.Errorf("tracked.age = %q, want its committed content back", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "entries", "orphan.age")); !os.IsNotExist(err) {
		t.Errorf("the untracked file survived the reset (stat err = %v); an interrupted insert must not "+
			"be folded into the next write's commit", err)
	}
	mustBeClean(t, dir)
}

// TestResetHardOnACleanTreeDiscardsNothing is what keeps the reset
// silent on every ordinary write: the caller warns only when this
// returns paths, so "nothing to discard" has to mean an empty list
// rather than a list of unchanged files.
func TestResetHardOnACleanTreeDiscardsNothing(t *testing.T) {
	dir := seededRepo(t)

	discarded, err := ResetHard(dir)
	if err != nil {
		t.Fatalf("ResetHard: %v", err)
	}
	if len(discarded) != 0 {
		t.Errorf("ResetHard on a clean tree reported %v, want nothing", discarded)
	}
	mustBeClean(t, dir)
}

// TestResetHardLeavesHEADWhereItWas: the reset discards uncommitted
// work, never a commit. gage's writes commit immediately, so anything
// already in history is by definition finished — an interrupted
// --reencrypt is exactly the case where nothing was committed at all.
func TestResetHardLeavesHEADWhereItWas(t *testing.T) {
	dir := seededRepo(t)
	before, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}

	writeWorktreeFile(t, dir, "entries/tracked.age", "half-written")
	if _, err := ResetHard(dir); err != nil {
		t.Fatal(err)
	}

	after, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("HEAD = %s after ResetHard, want %s", after, before)
	}
	if n, err := CommitCount(dir); err != nil || n != 1 {
		t.Errorf("commit count = %d (err=%v), want 1 — the reset must not commit", n, err)
	}
}

// TestResetHardDiscardsChangesOutsideEntries pins the coverage M9's
// dirty-tree precondition actually needs: the whole working tree, not
// just entries/.
//
// An interrupted `recipient add` is the shape that makes this
// load-bearing rather than pedantic. It writes .age-recipients and
// .gage/config.toml and commits — it never touches an entry — so a crash
// in that window leaves the recipient pair as the *only* dirty thing in
// the tree. A reset that keys off entries/ finds nothing to do, returns
// no paths, warns about nothing, and lets the next write commit the
// abandoned recipient list as if someone had meant it: a vault whose
// recipient files name a key no entry is encrypted to, which is the
// half-migrated state M9 exists to rule out.
func TestResetHardDiscardsChangesOutsideEntries(t *testing.T) {
	dir := seededRepo(t)

	writeWorktreeFile(t, dir, ".age-recipients", "age1committed\nage1abandoned\n")

	discarded, err := ResetHard(dir)
	if err != nil {
		t.Fatalf("ResetHard: %v", err)
	}
	if len(discarded) != 1 || discarded[0] != ".age-recipients" {
		t.Fatalf("ResetHard reported %v, want [.age-recipients] — a dirty file outside entries/ must be "+
			"discarded and named, or the reset is silent about the one window that dirties nothing else", discarded)
	}

	got, err := os.ReadFile(filepath.Join(dir, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "age1committed\n" {
		t.Errorf(".age-recipients = %q, want its committed content back", got)
	}
	mustBeClean(t, dir)
}
