package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/distillerylabs/gage/internal/gage/gittest"
	"github.com/distillerylabs/gage/internal/gage/syncerr"
)

// newPublishedRepo creates a working repo with one commit, wired to a
// fresh bare remote and already pushed — the state both sides of every
// sync test start from.
func newPublishedRepo(t *testing.T) (dir, remote string) {
	t.Helper()

	dir = t.TempDir()
	writeFile(t, filepath.Join(dir, "seed.txt"), "seed")
	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	remote = gittest.NewBareRemote(t)
	if err := SetRemote(dir, remote); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(context.Background(), dir); err != nil {
		t.Fatalf("publishing: %v", err)
	}
	return dir, remote
}

// commitFile writes and commits one file in dir.
func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()

	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, full, content)
	if _, err := CommitAll(dir, "commit "+name); err != nil {
		t.Fatal(err)
	}
}

func TestHasRemoteOnADirectoryThatIsNotARepo(t *testing.T) {
	// A vault whose files are missing must not turn every unlock into a
	// sync warning — the command the human actually ran will fail with a
	// far clearer message.
	has, err := HasRemote(t.TempDir())
	if err != nil {
		t.Fatalf("HasRemote on a non-repo = %v, want no error", err)
	}
	if has {
		t.Error("HasRemote = true on a directory that isn't a git repository")
	}
}

func TestCompareReportsTheFourStates(t *testing.T) {
	dir, remote := newPublishedRepo(t)

	state, err := Compare(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state != RemoteInSync {
		t.Errorf("Compare right after publishing = %v, want in-sync", state)
	}

	// Local moves ahead.
	commitFile(t, dir, "local.txt", "local")
	if state, err = Compare(dir); err != nil || state != RemoteAhead {
		t.Errorf("Compare after a local commit = %v (err %v), want ahead", state, err)
	}
	if n, err := AheadCount(dir); err != nil || n != 1 {
		t.Errorf("AheadCount = %d (err %v), want 1", n, err)
	}

	// The other device publishes something too: now both sides have moved.
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "remote.txt", "remote", "the other device's commit")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if state, err = Compare(dir); err != nil || state != RemoteDiverged {
		t.Errorf("Compare after both sides moved = %v (err %v), want diverged", state, err)
	}
}

func TestFastForwardOnlyMovesWhenItIsAFastForward(t *testing.T) {
	dir, remote := newPublishedRepo(t)

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "remote.txt", "remote", "the other device's commit")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	moved, err := FastForward(dir)
	if err != nil {
		t.Fatalf("FastForward: %v", err)
	}
	if !moved {
		t.Fatal("FastForward reported no move with the remote ahead")
	}
	if _, err := os.Stat(filepath.Join(dir, "remote.txt")); err != nil {
		t.Errorf("the fast-forwarded file is not in the working tree: %v", err)
	}

	// With both sides moved, a fast-forward must decline rather than
	// merge or reset: ff-only means it either catches up cleanly or does
	// nothing at all.
	commitFile(t, dir, "local.txt", "local")
	other.Pull(t)
	other.WriteCommitPush(t, "remote2.txt", "remote2", "another remote commit")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	before, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	moved, err = FastForward(dir)
	if err != nil {
		t.Fatalf("FastForward on a divergence = %v, want a quiet no-op", err)
	}
	if moved {
		t.Error("FastForward moved HEAD on a divergence")
	}
	after, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("HEAD moved (%s -> %s) on a declined fast-forward", before, after)
	}
}

func TestFastForwardRefusesOverUncommittedChanges(t *testing.T) {
	dir, remote := newPublishedRepo(t)

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "remote.txt", "remote", "the other device's commit")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	// Something outside gage is mid-edit. A hard reset would silently
	// destroy it, which is the one destructive thing this path could do.
	writeFile(t, filepath.Join(dir, "seed.txt"), "edited outside gage")

	if _, err := FastForward(dir); err == nil {
		t.Fatal("FastForward proceeded over uncommitted changes, want a refusal")
	}
	if got := readFile(t, filepath.Join(dir, "seed.txt")); got != "edited outside gage" {
		t.Errorf("the uncommitted edit was clobbered: %q", got)
	}
}

// TestMergeRemoteMergesDisjointChanges is the case git settles alone:
// different files on each side, so there is nothing to ask anyone.
func TestMergeRemoteMergesDisjointChanges(t *testing.T) {
	dir, remote := newPublishedRepo(t)

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "theirs.txt", "theirs", "their commit")
	commitFile(t, dir, "mine.txt", "mine")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	result, err := MergeRemote(dir, "gage: merge origin")
	if err != nil {
		t.Fatalf("MergeRemote: %v", err)
	}
	if !result.Merged {
		t.Fatalf("MergeRemote did not merge disjoint changes: %+v", result)
	}

	if got := readFile(t, filepath.Join(dir, "theirs.txt")); got != "theirs" {
		t.Errorf("their file after the merge = %q, want %q", got, "theirs")
	}
	if got := readFile(t, filepath.Join(dir, "mine.txt")); got != "mine" {
		t.Errorf("my file after the merge = %q, want %q", got, "mine")
	}

	// A real merge commit, with both sides as parents — local commit
	// granularity survives, and both histories stay walkable.
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.ParentHashes) != 2 {
		t.Errorf("merge commit has %d parents, want 2", len(commit.ParentHashes))
	}
	if strings.Contains(commit.Message, "mine.txt") || strings.Contains(commit.Message, "theirs.txt") {
		t.Errorf("merge commit message names files: %q", commit.Message)
	}
}

// TestMergeRemoteConflictsAndChangesNothing is M8a's contract at the git
// layer: a conflict is reported, and the repository is left exactly as it
// was for `gage sync` to work from.
func TestMergeRemoteConflictsAndChangesNothing(t *testing.T) {
	dir, remote := newPublishedRepo(t)

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "seed.txt", "their version", "their edit")
	commitFile(t, dir, "seed.txt", "my version")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	before, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}

	result, err := MergeRemote(dir, "gage: merge origin")
	if err != nil {
		t.Fatalf("MergeRemote: %v", err)
	}
	if result.Merged {
		t.Fatal("MergeRemote merged two edits to the same file, want a conflict")
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0] != "seed.txt" {
		t.Errorf("Conflicts = %v, want [seed.txt]", result.Conflicts)
	}

	after, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("HEAD moved (%s -> %s) on a conflicting merge", before, after)
	}
	if got := readFile(t, filepath.Join(dir, "seed.txt")); got != "my version" {
		t.Errorf("the working tree was modified by a conflicting merge: %q", got)
	}
}

// TestMergeRemoteTakesADeletionAndConflictsOnDeleteModify covers the two
// deletion shapes from "What \"diverged\" actually means".
func TestMergeRemoteTakesADeletionAndConflictsOnDeleteModify(t *testing.T) {
	t.Run("clean deletion is taken", func(t *testing.T) {
		dir, remote := newPublishedRepo(t)

		other := gittest.NewDevice(t, remote)
		other.Remove(t, "seed.txt")
		other.Commit(t, "their deletion")
		other.Push(t)

		commitFile(t, dir, "mine.txt", "mine")
		if _, err := Fetch(context.Background(), dir); err != nil {
			t.Fatal(err)
		}

		result, err := MergeRemote(dir, "gage: merge origin")
		if err != nil {
			t.Fatal(err)
		}
		if !result.Merged {
			t.Fatalf("a deletion we hadn't touched conflicted: %+v", result)
		}
		if _, err := os.Stat(filepath.Join(dir, "seed.txt")); !os.IsNotExist(err) {
			t.Error("the remote's deletion was not applied to the working tree")
		}
	})

	t.Run("delete/modify conflicts", func(t *testing.T) {
		dir, remote := newPublishedRepo(t)

		other := gittest.NewDevice(t, remote)
		other.Remove(t, "seed.txt")
		other.Commit(t, "their deletion")
		other.Push(t)

		commitFile(t, dir, "seed.txt", "my edit of the file they deleted")
		if _, err := Fetch(context.Background(), dir); err != nil {
			t.Fatal(err)
		}

		result, err := MergeRemote(dir, "gage: merge origin")
		if err != nil {
			t.Fatal(err)
		}
		if result.Merged {
			t.Fatal("a delete/modify pair merged silently, want a conflict")
		}
		if len(result.Conflicts) != 1 || result.Conflicts[0] != "seed.txt" {
			t.Errorf("Conflicts = %v, want [seed.txt]", result.Conflicts)
		}
	})
}

func TestCloneProducesAWorkingCopy(t *testing.T) {
	_, remote := newPublishedRepo(t)

	dest := filepath.Join(t.TempDir(), "clone")
	if err := Clone(context.Background(), remote, dest); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if got := readFile(t, filepath.Join(dest, "seed.txt")); got != "seed" {
		t.Errorf("cloned file = %q, want %q", got, "seed")
	}
	url, err := RemoteURL(dest)
	if err != nil {
		t.Fatal(err)
	}
	if url != remote {
		t.Errorf("the clone's origin = %q, want %q", url, remote)
	}
}

// TestCloneWorksWhenTheRemoteHeadPointsNowhere is the regression test for
// a failure that only appears against a *real* git bare repository.
//
// `git init --bare` has set HEAD to refs/heads/main since git 2.28, while
// go-git — which gage commits with — creates refs/heads/master. Pushing a
// vault into such a repository leaves it holding a branch its own HEAD
// doesn't name, and a HEAD-following clone dies with "reference not
// found". This was caught by driving the real binary, not by the tests
// above: gittest's bare remotes are go-git's own, so their HEAD agrees
// with what gage pushes and the mismatch never arises.
func TestCloneWorksWhenTheRemoteHeadPointsNowhere(t *testing.T) {
	dir, _ := newPublishedRepo(t)

	// A bare remote whose HEAD names a branch that will never exist,
	// exactly as `git init --bare` leaves one.
	bare := filepath.Join(t.TempDir(), "real-git-style.git")
	repo, err := git.PlainInit(bare, true)
	if err != nil {
		t.Fatal(err)
	}
	head := plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")
	if err := repo.Storer.SetReference(head); err != nil {
		t.Fatal(err)
	}

	if err := SetRemote(dir, bare); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(context.Background(), dir); err != nil {
		t.Fatalf("publishing into a main-headed bare repo: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "clone")
	if err := Clone(context.Background(), bare, dest); err != nil {
		t.Fatalf("Clone from a repo whose HEAD points at a missing branch: %v", err)
	}
	if got := readFile(t, filepath.Join(dest, "seed.txt")); got != "seed" {
		t.Errorf("cloned file = %q, want %q", got, "seed")
	}
}

// TestAuthFailuresNameTheHostAndTheFix is Q-OAUTH-APP's cost made
// legible: a token that expired or was revoked has to come back as
// something naming the host and `gage auth login`, not as a bare 403 from
// the transport layer.
func TestAuthFailuresNameTheHostAndTheFix(t *testing.T) {
	refused := syncerr.ClassifyPush(fmt.Errorf("unexpected: %w", transport.ErrAuthorizationFailed))

	annotated := annotateAuth(refused, "github.com")
	if !errors.Is(annotated, syncerr.ErrAuth) {
		t.Fatalf("annotateAuth changed the classification: %v", annotated)
	}
	for _, want := range []string{"github.com", "gage auth login"} {
		if !strings.Contains(annotated.Error(), want) {
			t.Errorf("annotated auth error = %q, want it to mention %q", annotated, want)
		}
	}

	// Every other classification passes through untouched — a divergence
	// must never acquire a "check your token" suggestion.
	diverged := syncerr.ClassifyPush(errors.New("non-fast-forward update: refs/heads/master"))
	if got := annotateAuth(diverged, "github.com"); got.Error() != diverged.Error() {
		t.Errorf("annotateAuth rewrote a non-auth error: %q", got)
	}
}

// TestSafeJoinRefusesPathsThatEscapeTheVault covers the traversal case: a
// tree path comes from whoever can push to the remote, so it must never
// resolve outside the vault directory.
func TestSafeJoinRefusesPathsThatEscapeTheVault(t *testing.T) {
	dir := t.TempDir()

	for _, path := range []string{
		"../outside.txt",
		"entries/../../outside.txt",
		"..",
		"",
	} {
		if _, err := safeJoin(dir, path); err == nil {
			t.Errorf("safeJoin(%q) was accepted, want it refused", path)
		}
	}

	got, err := safeJoin(dir, "entries/abc.age")
	if err != nil {
		t.Fatalf("safeJoin on an ordinary path: %v", err)
	}
	if want := filepath.Join(dir, "entries", "abc.age"); got != want {
		t.Errorf("safeJoin = %q, want %q", got, want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestRemoteStateStringCoversAllStatesAndUnknown pins the exact words
// sync messages build on, over every named state plus an out-of-range
// value — the same "state(%d)" fallback pattern exitcode.Code.String
// uses for a code nothing registered.
func TestRemoteStateStringCoversAllStatesAndUnknown(t *testing.T) {
	cases := []struct {
		state RemoteState
		want  string
	}{
		{RemoteNone, "no-remote"},
		{RemoteInSync, "in-sync"},
		{RemoteAhead, "ahead"},
		{RemoteBehind, "behind"},
		{RemoteDiverged, "diverged"},
		{RemoteState(99), "state(99)"},
	}
	for _, c := range cases {
		if got := c.state.String(); got != c.want {
			t.Errorf("RemoteState(%d).String() = %q, want %q", c.state, got, c.want)
		}
	}
}

// pushBranches creates one working repository with a commit on each
// named branch and pushes exactly those branches (and no others — the
// repo's own implicit default branch is never pushed unless "master" is
// explicitly named) to remote, for remoteBranch's branch-selection
// tests. It needs no gage vault: remoteBranch only cares about which
// branch refs a remote advertises.
func pushBranches(t *testing.T, remote string, names ...string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "src")
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "gittest", Email: "gittest@localhost"}

	// The root commit lands on go-git's own default branch name
	// ("master"): a checkout-with-Create against a wholly unborn repo
	// fails, so every other requested branch forks from here.
	writeFile(t, filepath.Join(dir, "f.txt"), "root")
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("root", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("root commit: %v", err)
	}

	for _, name := range names {
		if name == "master" {
			continue // already the root commit's branch.
		}
		if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name), Create: true}); err != nil {
			t.Fatalf("checkout %s: %v", name, err)
		}
		writeFile(t, filepath.Join(dir, "f.txt"), name)
		if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Commit("commit on "+name, &git.CommitOptions{Author: sig}); err != nil {
			t.Fatalf("commit %s: %v", name, err)
		}
	}

	if err := SetRemote(dir, remote); err != nil {
		t.Fatal(err)
	}
	specs := make([]gitconfig.RefSpec, 0, len(names))
	for _, name := range names {
		ref := plumbing.NewBranchReferenceName(name).String()
		specs = append(specs, gitconfig.RefSpec(ref+":"+ref))
	}
	if err := repo.Push(&git.PushOptions{RemoteName: OriginRemoteName, RefSpecs: specs}); err != nil {
		t.Fatalf("pushing %v: %v", names, err)
	}
}

// TestRemoteBranchPicksMainOrMasterAndRefusesAmbiguity is Clone's branch
// selection (remoteBranch), covering the cases newPublishedRepo's
// single-branch remotes never reach: two conventional default names
// competing with an unrelated branch, and two unrelated branches with
// neither default present.
func TestRemoteBranchPicksMainOrMasterAndRefusesAmbiguity(t *testing.T) {
	t.Run("main preferred over an unrelated branch", func(t *testing.T) {
		remote := gittest.NewBareRemote(t)
		pushBranches(t, remote, "main", "feature")

		branch, err := remoteBranch(context.Background(), remote, nil)
		if err != nil {
			t.Fatal(err)
		}
		if branch != "refs/heads/main" {
			t.Errorf("remoteBranch = %q, want refs/heads/main", branch)
		}
	})

	t.Run("master preferred over an unrelated branch", func(t *testing.T) {
		remote := gittest.NewBareRemote(t)
		pushBranches(t, remote, "master", "feature")

		branch, err := remoteBranch(context.Background(), remote, nil)
		if err != nil {
			t.Fatal(err)
		}
		if branch != "refs/heads/master" {
			t.Errorf("remoteBranch = %q, want refs/heads/master", branch)
		}
	})

	t.Run("neither default present is refused by name", func(t *testing.T) {
		remote := gittest.NewBareRemote(t)
		pushBranches(t, remote, "feature-a", "feature-b")

		_, err := remoteBranch(context.Background(), remote, nil)
		if err == nil {
			t.Fatal("remoteBranch picked a branch with no main or master present, want a refusal")
		}
		if !strings.Contains(err.Error(), "several branches") {
			t.Errorf("error = %v, want it to say several branches", err)
		}
	})

	t.Run("no branches at all is refused as not a vault", func(t *testing.T) {
		// A remote that has commits but not on any branch (only a tag,
		// say) rather than one that is simply empty: an empty bare
		// remote fails inside go-git's own transport before reaching
		// remoteBranch's own branch count at all.
		dir := filepath.Join(t.TempDir(), "src")
		repo, err := git.PlainInit(dir, false)
		if err != nil {
			t.Fatal(err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "f.txt"), "x")
		if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			t.Fatal(err)
		}
		sig := &object.Signature{Name: "gittest", Email: "gittest@localhost"}
		hash, err := wt.Commit("c", &git.CommitOptions{Author: sig})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.CreateTag("v1", hash, nil); err != nil {
			t.Fatal(err)
		}

		remote := gittest.NewBareRemote(t)
		if err := SetRemote(dir, remote); err != nil {
			t.Fatal(err)
		}
		if err := repo.Push(&git.PushOptions{RemoteName: OriginRemoteName,
			RefSpecs: []gitconfig.RefSpec{"refs/tags/*:refs/tags/*"}}); err != nil {
			t.Fatalf("pushing the tag: %v", err)
		}

		_, err = remoteBranch(context.Background(), remote, nil)
		if err == nil {
			t.Fatal("remoteBranch succeeded against a remote with no branches, want a refusal")
		}
		if !strings.Contains(err.Error(), "empty repository, not a vault") {
			t.Errorf("error = %v, want it to say this is not a vault", err)
		}
	})
}

// TestCompareWithNoRemoteTrackingRefIsRemoteNone is Compare's baseline —
// a repository with a commit but nothing ever fetched, so there is no
// refs/remotes/origin/<branch> to compare against at all. This is the
// same code path (headAndTracking's ErrReferenceNotFound branch) whether
// or not "origin" itself is configured, since neither state has ever run
// a fetch.
func TestCompareWithNoRemoteTrackingRefIsRemoteNone(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "seed.txt"), "seed")
	if _, err := InitAndCommit(dir, "initial commit"); err != nil {
		t.Fatal(err)
	}

	state, err := Compare(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state != RemoteNone {
		t.Errorf("Compare with nothing ever fetched = %v, want RemoteNone", state)
	}
}

// TestCompareReportsRemoteBehind fills the one state
// TestCompareReportsTheFourStates doesn't reach (it only ever moves the
// local side ahead): the remote alone advances, so a pull would
// fast-forward.
func TestCompareReportsRemoteBehind(t *testing.T) {
	dir, remote := newPublishedRepo(t)

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "remote.txt", "remote", "the other device's commit")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	state, err := Compare(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state != RemoteBehind {
		t.Errorf("Compare after only the remote moved = %v, want RemoteBehind", state)
	}
}
