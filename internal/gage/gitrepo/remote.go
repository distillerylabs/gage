package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/distillerylabs/gage/internal/gage/remoteauth"
	"github.com/distillerylabs/gage/internal/gage/syncerr"
)

// RemoteState describes how a vault's local branch relates to the last
// known state of its remote — the four-way answer every sync decision is
// made from.
type RemoteState int

const (
	// RemoteNone means the vault has no origin, or nothing has ever been
	// fetched from it, so there is no remote-tracking ref to compare
	// against. A local-only vault sits here permanently.
	RemoteNone RemoteState = iota
	// RemoteInSync means both sides are on the same commit.
	RemoteInSync
	// RemoteAhead means the local branch has commits the remote lacks: a
	// push will fast-forward.
	RemoteAhead
	// RemoteBehind means the remote has commits the local branch lacks: a
	// pull will fast-forward.
	RemoteBehind
	// RemoteDiverged means both sides have commits the other lacks.
	RemoteDiverged
)

// String renders the state the way messages and tests refer to it.
func (s RemoteState) String() string {
	switch s {
	case RemoteNone:
		return "no-remote"
	case RemoteInSync:
		return "in-sync"
	case RemoteAhead:
		return "ahead"
	case RemoteBehind:
		return "behind"
	case RemoteDiverged:
		return "diverged"
	default:
		return fmt.Sprintf("state(%d)", int(s))
	}
}

// HasRemote reports whether dir has an "origin" remote to sync with.
//
// A directory that isn't a git repository at all answers false rather
// than failing: this is the question every automatic sync path asks
// first, and "there is nothing here to sync" is the honest answer for a
// vault whose files are missing or damaged. Reporting it as a sync error
// would put a confusing warning in front of a human on the way to the far
// clearer failure their actual command is about to produce.
func HasRemote(dir string) (bool, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return false, nil
		}
		return false, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	remote, err := repo.Remote(OriginRemoteName)
	if err != nil {
		if errors.Is(err, git.ErrRemoteNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("gitrepo: reading remote: %w", err)
	}
	return len(remote.Config().URLs) > 0, nil
}

// Fetch updates dir's remote-tracking refs from origin. It reports
// whether anything new actually arrived, so a caller can tell "caught up"
// from "already current" without diffing refs itself.
//
// Every failure comes back as one of syncerr's three classified
// sentinels; no raw go-git or transport error escapes this package.
func Fetch(ctx context.Context, dir string) (bool, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return false, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	auth, host, err := authFor(repo)
	if err != nil {
		return false, err
	}

	err = repo.FetchContext(ctx, &git.FetchOptions{RemoteName: OriginRemoteName, Auth: auth})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return false, nil
	}
	if err != nil {
		return false, annotateAuth(syncerr.ClassifyFetch(err), host)
	}
	return true, nil
}

// Push publishes the local branch to origin, reporting whether anything
// was actually sent. A remote that has moved on comes back as
// syncerr.ErrDiverged; see syncerr.ClassifyPush for why that's decided by
// elimination rather than by matching go-git's own rejection.
func Push(ctx context.Context, dir string) (bool, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return false, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	auth, host, err := authFor(repo)
	if err != nil {
		return false, err
	}

	err = repo.PushContext(ctx, &git.PushOptions{RemoteName: OriginRemoteName, Auth: auth})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return false, nil
	}
	if err != nil {
		return false, annotateAuth(syncerr.ClassifyPush(err), host)
	}
	return true, nil
}

// Clone clones url into dir — `gage clone`'s git half. The directory must
// not already exist.
//
// The branch is chosen explicitly rather than by following the remote's
// HEAD, because a vault's remote routinely has a HEAD that points
// nowhere. A repository created with modern `git init --bare` sets HEAD
// to refs/heads/main before any commit exists, while go-git — which is
// what gage commits with — creates refs/heads/master. Pushing a vault
// into such a repository leaves it holding a real branch that its own
// HEAD doesn't name, and a HEAD-following clone of it fails with a bare
// "reference not found". Asking the remote what branches it actually has
// costs one extra round trip on a once-per-vault operation and works
// whichever name is in play.
func Clone(ctx context.Context, url, dir string) error {
	auth, err := remoteauth.Method(url)
	if err != nil {
		return err
	}
	host, hostErr := remoteauth.Host(url)
	if hostErr != nil {
		host = ""
	}

	branch, err := remoteBranch(ctx, url, auth)
	if err != nil {
		return annotateAuth(syncerr.ClassifyFetch(err), host)
	}

	opts := &git.CloneOptions{URL: url, Auth: auth, ReferenceName: branch}
	if _, err := git.PlainCloneContext(ctx, dir, false, opts); err != nil {
		return annotateAuth(syncerr.ClassifyFetch(err), host)
	}
	return nil
}

// remoteBranch asks a remote which branch to clone.
//
// A repository with exactly one branch answers itself. With several, the
// two conventional default names win in turn — and anything else is
// refused by name rather than guessed at, since picking the wrong branch
// of a secrets vault would silently produce a vault missing entries.
func remoteBranch(ctx context.Context, url string, auth transport.AuthMethod) (plumbing.ReferenceName, error) {
	remote := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{
		Name: OriginRemoteName,
		URLs: []string{url},
	})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return "", err
	}

	var heads []plumbing.ReferenceName
	for _, ref := range refs {
		if ref.Name().IsBranch() {
			heads = append(heads, ref.Name())
		}
	}

	switch len(heads) {
	case 0:
		return "", fmt.Errorf("gitrepo: %s has no branches; it is an empty repository, not a vault", url)
	case 1:
		return heads[0], nil
	}

	for _, preferred := range []plumbing.ReferenceName{"refs/heads/main", "refs/heads/master"} {
		for _, head := range heads {
			if head == preferred {
				return head, nil
			}
		}
	}
	return "", fmt.Errorf("gitrepo: %s has several branches (%v) and none is main or master; "+
		"gage cannot tell which one holds the vault", url, heads)
}

// Compare reports how dir's current branch relates to its remote-tracking
// ref. It performs no network I/O: it answers from what the last Fetch
// left behind, which is what makes "fetch, then decide" two separable
// steps.
func Compare(dir string) (RemoteState, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return RemoteNone, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	local, remote, err := headAndTracking(repo)
	if err != nil {
		return RemoteNone, err
	}
	if remote == nil {
		return RemoteNone, nil
	}
	if local.Hash == remote.Hash {
		return RemoteInSync, nil
	}

	remoteIsDescendant, err := local.IsAncestor(remote)
	if err != nil {
		return RemoteNone, fmt.Errorf("gitrepo: comparing with the remote: %w", err)
	}
	localIsDescendant, err := remote.IsAncestor(local)
	if err != nil {
		return RemoteNone, fmt.Errorf("gitrepo: comparing with the remote: %w", err)
	}

	switch {
	case remoteIsDescendant:
		return RemoteBehind, nil
	case localIsDescendant:
		return RemoteAhead, nil
	default:
		return RemoteDiverged, nil
	}
}

// FastForward advances the local branch to the remote-tracking ref when
// that is a fast-forward, reporting whether it moved anything.
//
// It is deliberately the only way this package moves HEAD on a pull: a
// state that isn't a clean fast-forward leaves local state completely
// untouched, per "Sync model"'s fast-forward-only rule. A dirty working
// tree also declines to move — gage commits every write immediately, so a
// dirty tree means something outside gage is mid-edit, and clobbering it
// would be the one destructive thing this path could do.
func FastForward(dir string) (bool, error) {
	state, err := Compare(dir)
	if err != nil {
		return false, err
	}
	if state != RemoteBehind {
		return false, nil
	}

	if err := requireCleanWorkTree(dir, "fast-forwarding over them"); err != nil {
		return false, err
	}

	repo, err := git.PlainOpen(dir)
	if err != nil {
		return false, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	_, remote, err := headAndTracking(repo)
	if err != nil {
		return false, err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("gitrepo: opening worktree: %w", err)
	}
	// Hard is right precisely because this is a fast-forward: the local
	// branch is an ancestor of what it's moving to, so there is no local
	// commit for a reset to discard, and the tree check above already
	// ruled out uncommitted work.
	if err := wt.Reset(&git.ResetOptions{Commit: remote.Hash, Mode: git.HardReset}); err != nil {
		return false, fmt.Errorf("gitrepo: fast-forwarding: %w", err)
	}
	return true, nil
}

// AheadCount reports how many local commits the remote doesn't have —
// the "2 local commits pending" half of the divergence message in "Sync
// model". A vault with nothing fetched yet reports 0 rather than its
// whole history, since "pending" has no meaning without a remote to be
// pending against.
func AheadCount(dir string) (int, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return 0, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	local, remote, err := headAndTracking(repo)
	if err != nil {
		return 0, err
	}
	if remote == nil {
		return 0, nil
	}
	// Everything the remote already has. A commit is "pending" exactly
	// when the local head can reach it and this set can't.
	//
	// It has to be the whole reachable set rather than the single
	// merge-base commit, because the two stop being equivalent the moment
	// a merge commit sits between the local head and the base. Walking
	// back from a merge descends into its *other* parent, re-entering the
	// history behind the base without ever passing through the base
	// itself — so a walk that only stopped at that one hash counted the
	// entire shared history as local work, and reported a vault's whole
	// commit count as "N local commits pending". See
	// TestAheadCountIsNotFooledByAMergeCommit.
	have, err := reachableFrom(remote)
	if err != nil {
		return 0, err
	}

	seen := map[plumbing.Hash]bool{}
	queue := []*object.Commit{local}
	count := 0
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if seen[c.Hash] || have[c.Hash] {
			continue
		}
		seen[c.Hash] = true
		count++
		for _, parent := range c.ParentHashes {
			if seen[parent] || have[parent] {
				continue
			}
			pc, err := repo.CommitObject(parent)
			if err != nil {
				return 0, fmt.Errorf("gitrepo: walking local history: %w", err)
			}
			queue = append(queue, pc)
		}
	}
	return count, nil
}

// reachableFrom returns the hashes of every commit reachable from c,
// including c itself.
func reachableFrom(c *object.Commit) (map[plumbing.Hash]bool, error) {
	seen := map[plumbing.Hash]bool{}
	err := object.NewCommitPreorderIter(c, nil, nil).ForEach(func(x *object.Commit) error {
		seen[x.Hash] = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gitrepo: walking the remote's history: %w", err)
	}
	return seen, nil
}

// headAndTracking resolves the local branch's commit and the commit its
// remote-tracking ref points at. A nil remote commit (with no error)
// means there is nothing to compare against yet.
func headAndTracking(repo *git.Repository) (local, remote *object.Commit, err error) {
	head, err := repo.Head()
	if err != nil {
		return nil, nil, fmt.Errorf("gitrepo: reading HEAD: %w", err)
	}
	local, err = repo.CommitObject(head.Hash())
	if err != nil {
		return nil, nil, fmt.Errorf("gitrepo: reading HEAD commit: %w", err)
	}

	ref, err := repo.Reference(trackingRefName(head.Name()), true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return local, nil, nil
		}
		return nil, nil, fmt.Errorf("gitrepo: reading the remote-tracking ref: %w", err)
	}
	remote, err = repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, nil, fmt.Errorf("gitrepo: reading the remote's commit: %w", err)
	}
	return local, remote, nil
}

// trackingRefName maps refs/heads/<branch> to
// refs/remotes/origin/<branch>.
func trackingRefName(branch plumbing.ReferenceName) plumbing.ReferenceName {
	short := strings.TrimPrefix(branch.String(), "refs/heads/")
	return plumbing.ReferenceName("refs/remotes/" + OriginRemoteName + "/" + short)
}

// authFor resolves the auth method for a repo's origin, along with the
// host it belongs to (for error messages). A repo with no origin gets nil
// auth and an empty host rather than an error — the caller's own
// operation is what fails on a missing remote, with a better message than
// this could give.
func authFor(repo *git.Repository) (transport.AuthMethod, string, error) {
	remote, err := repo.Remote(OriginRemoteName)
	if err != nil {
		if errors.Is(err, git.ErrRemoteNotFound) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("gitrepo: reading remote: %w", err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return nil, "", nil
	}

	auth, err := remoteauth.Method(urls[0])
	if err != nil {
		return nil, "", err
	}
	host, err := remoteauth.Host(urls[0])
	if err != nil {
		return nil, "", err
	}
	return auth, host, nil
}

// annotateAuth appends the "which host, and what to run" hint to a
// classified auth failure, so an expired or absent token names
// `gage auth login` rather than surfacing a bare 403 (Q-OAUTH-APP: the
// one real cost of user-supplied tokens). Every other classification
// passes through untouched.
func annotateAuth(err error, host string) error {
	if err == nil || !errors.Is(err, syncerr.ErrAuth) {
		return err
	}
	return fmt.Errorf("%w; %s", err, remoteauth.AuthHint(host))
}
