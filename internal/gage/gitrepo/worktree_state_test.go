package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/gittest"
)

// publishedRepo is a working repository whose one commit is already on a
// fresh bare remote, with the remote-tracking ref populated — the state
// every sync operation actually starts from.
func publishedRepo(t *testing.T) (dir, remote string) {
	t.Helper()

	remote = gittest.NewBareRemote(t)
	dir = filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeWorktreeFile(t, dir, "seed.txt", "seed")
	if _, err := InitAndCommit(dir, "seed"); err != nil {
		t.Fatal(err)
	}
	if err := SetRemote(dir, remote); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	return dir, remote
}

func writeWorktreeFile(t *testing.T, dir, rel, content string) {
	t.Helper()

	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// divergeFrom produces a real divergence: the other device publishes
// theirFile, this side commits ourFile, and the remote-tracking ref is
// refreshed so a merge has something to merge.
func divergeFrom(t *testing.T, dir, remote, theirFile, ourFile string) {
	t.Helper()

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, theirFile, "theirs", "their commit")

	writeWorktreeFile(t, dir, ourFile, "ours")
	if _, err := CommitAll(dir, "our commit"); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}

// TestMergeRemoteRefusesADirtyWorkTree is the counterpart to
// TestFastForwardRefusesOverUncommittedChanges, and it exists because the
// two used to disagree.
//
// A merge stages the whole working tree, so without this guard a human's
// half-finished editing was swept into an automatic "gage: merge origin"
// commit and pushed to every other device — published rather than
// discarded, but just as much not theirs to publish.
func TestMergeRemoteRefusesADirtyWorkTree(t *testing.T) {
	dir, remote := publishedRepo(t)
	divergeFrom(t, dir, remote, "theirs.txt", "ours.txt")

	// Something outside gage is mid-edit.
	writeWorktreeFile(t, dir, "human-scratch.txt", "half-typed note")
	writeWorktreeFile(t, dir, "seed.txt", "edited by hand")

	_, err := MergeRemote(dir, "gage: merge origin")
	if err == nil {
		t.Fatal("MergeRemote ran over a dirty working tree, want it refused")
	}
	if !errors.Is(err, ErrDirtyWorkTree) {
		t.Errorf("MergeRemote error = %v, want ErrDirtyWorkTree", err)
	}
	for _, want := range []string{"human-scratch.txt", "seed.txt"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name the uncommitted path %q", err, want)
		}
	}

	// The refusal changed nothing: the edits are still uncommitted and
	// the remote's file was not brought in.
	if got := readWorktreeFile(t, dir, "seed.txt"); got != "edited by hand" {
		t.Errorf("seed.txt = %q, want the human's edit left alone", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "theirs.txt")); !os.IsNotExist(err) {
		t.Errorf("a refused merge still applied the remote's file: %v", err)
	}
}

// TestFastForwardAndMergeShareOneDirtyTreeSentinel keeps the two guards
// matchable by one errors.Is at the layer above, which is what lets
// Vault report either as a conflict rather than an internal fault.
func TestFastForwardAndMergeShareOneDirtyTreeSentinel(t *testing.T) {
	dir, remote := publishedRepo(t)

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "theirs.txt", "theirs", "their commit")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	writeWorktreeFile(t, dir, "seed.txt", "edited by hand")

	_, err := FastForward(dir)
	if !errors.Is(err, ErrDirtyWorkTree) {
		t.Errorf("FastForward error = %v, want ErrDirtyWorkTree", err)
	}
}

// TestAheadCountIsNotFooledByAMergeCommit is the regression test for a
// count that reported a vault's entire history as "pending".
//
// Walking back from the local head and stopping only at the merge-base
// *commit* is wrong once a merge sits in between: the walk descends into
// the merge's other parent and re-enters the shared history behind the
// base without ever passing through it. The fix is to stop at everything
// the remote can reach, not at one hash.
func TestAheadCountIsNotFooledByAMergeCommit(t *testing.T) {
	dir, remote := publishedRepo(t)

	// Some shared history for a broken count to over-report.
	for _, n := range []string{"a", "b", "c", "d"} {
		d := gittest.NewDevice(t, remote)
		d.WriteCommitPush(t, n+".txt", n, "commit "+n)
	}
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := FastForward(dir); err != nil {
		t.Fatal(err)
	}

	// Diverge and merge locally, without publishing the merge.
	divergeFrom(t, dir, remote, "theirs.txt", "ours.txt")
	if _, err := MergeRemote(dir, "gage: merge origin"); err != nil {
		t.Fatal(err)
	}

	// Two commits are genuinely unpublished: our own, and the merge.
	got, err := AheadCount(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("AheadCount = %d, want 2 (our commit + the merge commit); "+
			"a larger number means the walk crossed the merge into shared history", got)
	}

	// And the other device moving again — the state a human actually sees
	// the count in, since that is what makes it a reported divergence.
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "later.txt", "later", "their later commit")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if state, err := Compare(dir); err != nil || state != RemoteDiverged {
		t.Fatalf("Compare = %v (err %v), want diverged", state, err)
	}
	got, err = AheadCount(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("AheadCount on a reported divergence = %d, want 2", got)
	}
}

// TestAheadCountCountsAPlainRunOfCommits is the ordinary case the merge
// case must not break.
func TestAheadCountCountsAPlainRunOfCommits(t *testing.T) {
	dir, _ := publishedRepo(t)

	if got, err := AheadCount(dir); err != nil || got != 0 {
		t.Fatalf("AheadCount on a published vault = %d (err %v), want 0", got, err)
	}
	for i, name := range []string{"one.txt", "two.txt", "three.txt"} {
		writeWorktreeFile(t, dir, name, name)
		if _, err := CommitAll(dir, name); err != nil {
			t.Fatal(err)
		}
		got, err := AheadCount(dir)
		if err != nil {
			t.Fatal(err)
		}
		if want := i + 1; got != want {
			t.Errorf("AheadCount after %d local commits = %d, want %d", want, got, want)
		}
	}
}

func readWorktreeFile(t *testing.T, dir, rel string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
