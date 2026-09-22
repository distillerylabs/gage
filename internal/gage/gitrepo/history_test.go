package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"

	"github.com/distillerylabs/gage/internal/gage/gittest"
)

// contentsOf renders a log as the sequence of blob states it reports,
// oldest last — enough to assert both which commits were kept and what
// each of them held, without hardcoding hashes a test can't predict. A
// deleted revision reads as "(deleted)".
func contentsOf(revs []Revision) []string {
	out := make([]string, 0, len(revs))
	for _, r := range revs {
		if r.Content == nil {
			out = append(out, "(deleted)")
			continue
		}
		out = append(out, string(r.Content))
	}
	return out
}

// removeAndCommit deletes a path from the working tree and commits it,
// so a test can produce the deleting commit Log has to report.
func removeAndCommit(t *testing.T, dir, name string) {
	t.Helper()

	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitAll(dir, "remove "+name); err != nil {
		t.Fatal(err)
	}
}

func assertLog(t *testing.T, dir, path string, want ...string) {
	t.Helper()

	revs, err := Log(dir, path)
	if err != nil {
		t.Fatalf("Log(%s): %v", path, err)
	}
	got := contentsOf(revs)
	if len(got) != len(want) {
		t.Fatalf("Log(%s) returned %d revisions %v, want %d %v", path, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Log(%s) = %v, want %v", path, got, want)
		}
	}
}

// TestLogReportsEachRealChangeOnce is the linear baseline: one revision
// per commit that actually touched the path, newest first, and nothing
// for the commits that touched something else.
func TestLogReportsEachRealChangeOnce(t *testing.T) {
	dir, _ := newPublishedRepo(t)

	commitFile(t, dir, "entries/x.age", "x-v1")
	commitFile(t, dir, "entries/y.age", "y-v1") // unrelated
	commitFile(t, dir, "entries/x.age", "x-v2")

	assertLog(t, dir, "entries/x.age", "x-v2", "x-v1")
}

// TestLogAcrossAMergeOfUnrelatedEdits is the case a flattened walk gets
// wrong, and the reason Log compares against real parents.
//
// Only the local side ever touches entries/x.age. The remote side edits
// a different entry, and the two are merged. x's history is still just
// the two commits that wrote it — the merge carried it through
// unchanged, and the remote's commits never had anything to do with it.
//
// Comparing each commit with its neighbour in the flattened walk instead
// reported four revisions here, one of them a deletion of an entry that
// was never deleted: the remote's commit predates x on the *other*
// branch, so the walk saw x "disappear" as it crossed from one side to
// the other.
func TestLogAcrossAMergeOfUnrelatedEdits(t *testing.T) {
	dir, remote := newPublishedRepo(t)

	// A shared base both sides descend from.
	commitFile(t, dir, "entries/x.age", "x-v1")
	if _, err := Push(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	// The remote device changes an unrelated entry.
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "entries/y.age", "y-v2", "their unrelated edit")

	// This device changes x, then merges their work in.
	commitFile(t, dir, "entries/x.age", "x-v2")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	res, err := MergeRemote(dir, "merge")
	if err != nil {
		t.Fatalf("MergeRemote: %v", err)
	}
	if !res.Merged {
		t.Fatalf("expected a clean merge, got conflicts %v", res.Conflicts)
	}

	assertLog(t, dir, "entries/x.age", "x-v2", "x-v1")
	// The other side's entry is symmetric: the one commit that created
	// it, and no phantom revision from this side's commits.
	assertLog(t, dir, "entries/y.age", "y-v2")
}

// TestLogAcrossAConflictResolvedByTakingASide is the other half of the
// parent rule. Resolving a conflict by choosing one side leaves the
// merge commit holding that side's blob, so it changed nothing of its
// own and is not a revision — but both of the concurrent edits it
// reconciled are, and so is the base they diverged from.
//
// The two edits are concurrent, on different branches and typically
// within the same second, so their order relative to each other is
// genuinely arbitrary and is not asserted. What the log must not do is
// gain or lose a revision.
func TestLogAcrossAConflictResolvedByTakingASide(t *testing.T) {
	dir, remote := newPublishedRepo(t)

	commitFile(t, dir, "entries/x.age", "x-v1")
	if _, err := Push(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	// Both sides change the same entry differently.
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "entries/x.age", "x-theirs", "their edit")
	commitFile(t, dir, "entries/x.age", "x-mine")

	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	merge, err := PrepareMerge(dir)
	if err != nil {
		t.Fatalf("PrepareMerge: %v", err)
	}
	if len(merge.Conflicts()) != 1 {
		t.Fatalf("conflicts = %v, want exactly entries/x.age", merge.Conflicts())
	}
	if _, err := merge.Commit("merge", map[string]MergeSide{"entries/x.age": RemoteSide}, nil); err != nil {
		t.Fatalf("resolving merge: %v", err)
	}

	// The merge took the remote's blob, so it equals that parent and is
	// not a change of its own. What remains is each side's real edit
	// plus the shared base.
	revs, err := Log(dir, "entries/x.age")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	got := contentsOf(revs)
	if len(got) != 3 {
		t.Fatalf("Log = %v (%d revisions), want 3: both edits and the base, with the merge excluded", got, len(got))
	}
	if got[2] != "x-v1" {
		t.Errorf("oldest revision = %q, want the shared base %q", got[2], "x-v1")
	}
	mine, theirs := got[0], got[1]
	if mine > theirs {
		mine, theirs = theirs, mine
	}
	if mine != "x-mine" || theirs != "x-theirs" {
		t.Errorf("Log = %v, want both concurrent edits (in either order) above the base", got)
	}
}

// TestLogReportsADeletionOnce: a removed entry's last revision is the
// commit that removed it, and it is reported once rather than once per
// later commit.
func TestLogReportsADeletionOnce(t *testing.T) {
	dir, _ := newPublishedRepo(t)

	commitFile(t, dir, "entries/x.age", "x-v1")
	removeAndCommit(t, dir, "entries/x.age")
	commitFile(t, dir, "entries/y.age", "y-v1") // unrelated, after the delete

	assertLog(t, dir, "entries/x.age", "(deleted)", "x-v1")
}

// TestLogOnAPathGitHasNeverSeenIsEmpty: an id with no commits is a
// normal state, not a failure.
func TestLogOnAPathGitHasNeverSeenIsEmpty(t *testing.T) {
	dir, _ := newPublishedRepo(t)

	revs, err := Log(dir, "entries/never.age")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(revs) != 0 {
		t.Errorf("Log on an unknown path = %v, want none", contentsOf(revs))
	}
}

// TestLogAndHeadCommitterTimeOnARepoWithNoCommitsYet is the other empty
// state Log and HeadCommitterTime both have to answer honestly: not an
// unknown path in a repo with history (TestLogOnAPathGitHasNeverSeenIsEmpty
// above), but a repository with no commits at all — HEAD itself doesn't
// resolve. Both report their zero value rather than an error, the same
// "nothing to report yet" posture ResetHard and DirtyPaths take.
func TestLogAndHeadCommitterTimeOnARepoWithNoCommitsYet(t *testing.T) {
	dir := t.TempDir()
	if _, err := git.PlainInit(dir, false); err != nil {
		t.Fatal(err)
	}

	revs, err := Log(dir, "entries/whatever.age")
	if err != nil {
		t.Fatalf("Log on a commit-less repo: %v", err)
	}
	if revs != nil {
		t.Errorf("Log on a commit-less repo = %v, want nil", revs)
	}

	when, err := HeadCommitterTime(dir)
	if err != nil {
		t.Fatalf("HeadCommitterTime on a commit-less repo: %v", err)
	}
	if !when.IsZero() {
		t.Errorf("HeadCommitterTime on a commit-less repo = %v, want the zero value", when)
	}
}
