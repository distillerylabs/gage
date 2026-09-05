// Package gitrepo wraps the small slice of go-git operations gage's
// vault lifecycle needs: init + first commit, reading/setting the
// "origin" remote, and reporting clean/dirty state. There's deliberately
// no generic passthrough here — see "Git-specific commands" in the
// design doc — every call goes through go-git rather than shelling out
// to a git binary.
package gitrepo

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// commitAuthorName and commitAuthorEmail identify gage's own commits —
// deliberately not read from the user's global git config, which may
// not exist in a fresh environment and would make an init's resulting
// commit non-deterministic across machines.
const (
	commitAuthorName  = "gage"
	commitAuthorEmail = "gage@localhost"
)

// OriginRemoteName is the one remote name gage's git-type vaults use.
const OriginRemoteName = "origin"

// InitAndCommit runs `git init` at dir (already populated with the
// vault's on-disk skeleton) and creates one commit containing every file
// currently in the working tree. It returns the new commit's hash as a
// string.
func InitAndCommit(dir, message string) (string, error) {
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return "", fmt.Errorf("gitrepo: initializing %s: %w", dir, err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("gitrepo: opening worktree: %w", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return "", fmt.Errorf("gitrepo: staging files: %w", err)
	}

	sig := &object.Signature{Name: commitAuthorName, Email: commitAuthorEmail, When: time.Now()}
	hash, err := wt.Commit(message, &git.CommitOptions{Author: sig})
	if err != nil {
		return "", fmt.Errorf("gitrepo: committing: %w", err)
	}
	return hash.String(), nil
}

// CommitAll stages every change in dir's working tree — additions,
// modifications, and deletions alike, equivalent to `git add -A` — and
// commits with message under gage's fixed, anonymous commit identity (see
// commitAuthorName/commitAuthorEmail). It returns the new commit's hash.
//
// This is M4's per-write commit: one CommitAll call per insert/rm, message
// the entry's UUID and nothing else — see the M4 plan's "Decisions made"
// on why the message carries no title, description, or verb.
func CommitAll(dir, message string) (string, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("gitrepo: opening worktree: %w", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return "", fmt.Errorf("gitrepo: staging files: %w", err)
	}

	sig := &object.Signature{Name: commitAuthorName, Email: commitAuthorEmail, When: time.Now()}
	hash, err := wt.Commit(message, &git.CommitOptions{Author: sig})
	if err != nil {
		return "", fmt.Errorf("gitrepo: committing: %w", err)
	}
	return hash.String(), nil
}

// SetRemote sets (or, if one already exists, changes) dir's "origin"
// remote to url — equivalent to `git remote add origin <url>` or
// `git remote set-url origin <url>`, whichever applies.
func SetRemote(dir, url string) error {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}

	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("gitrepo: reading config: %w", err)
	}
	if cfg.Remotes == nil {
		cfg.Remotes = map[string]*gitconfig.RemoteConfig{}
	}
	cfg.Remotes[OriginRemoteName] = &gitconfig.RemoteConfig{Name: OriginRemoteName, URLs: []string{url}}

	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("gitrepo: writing config: %w", err)
	}
	return nil
}

// RemoteURL reports dir's "origin" remote URL, or "" if none is set.
func RemoteURL(dir string) (string, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	remote, err := repo.Remote(OriginRemoteName)
	if err != nil {
		if err == git.ErrRemoteNotFound {
			return "", nil
		}
		return "", fmt.Errorf("gitrepo: reading remote: %w", err)
	}
	cfg := remote.Config()
	if len(cfg.URLs) == 0 {
		return "", nil
	}
	return cfg.URLs[0], nil
}

// HeadCommit returns HEAD's message and author identity (name, email) —
// mainly so tests can assert on what CommitAll/InitAndCommit actually
// wrote without each one re-deriving the same handful of go-git calls.
func HeadCommit(dir string) (message, authorName, authorEmail string, err error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", "", "", fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", "", "", fmt.Errorf("gitrepo: reading HEAD: %w", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return "", "", "", fmt.Errorf("gitrepo: reading HEAD commit: %w", err)
	}
	return commit.Message, commit.Author.Name, commit.Author.Email, nil
}

// HeadHash returns dir's current HEAD commit as a string — how a caller
// (or a test) asks "did this operation move the branch at all" without
// reimplementing the reference lookup.
func HeadHash(dir string) (string, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("gitrepo: reading HEAD: %w", err)
	}
	return head.Hash().String(), nil
}

// IsClean reports whether dir's working tree has no staged or unstaged
// changes.
func IsClean(dir string) (bool, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return false, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("gitrepo: opening worktree: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return false, fmt.Errorf("gitrepo: reading status: %w", err)
	}
	return status.IsClean(), nil
}

// ErrDirtyWorkTree means a vault's working tree has staged or unstaged
// changes, so an operation that would move it declined to run.
//
// gage commits every write immediately, so a dirty tree means something
// *outside* gage is mid-edit. Both operations that move the tree refuse
// over one, for the same reason from two directions: a fast-forward's
// hard reset would discard the work outright, and a merge's commit would
// publish it under a message nobody wrote it for. Neither is gage's call
// to make on someone else's unfinished editing.
var ErrDirtyWorkTree = errors.New("gitrepo: the working tree has uncommitted changes")

// maxNamedDirtyPaths caps how many paths a refusal spells out. A vault
// mid-restore can have thousands, and an error message is a sentence, not
// a report.
const maxNamedDirtyPaths = 5

// DirtyPaths lists the vault-relative paths with staged or unstaged
// changes, sorted. An empty result means the tree is clean.
func DirtyPaths(dir string) ([]string, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("gitrepo: opening worktree: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("gitrepo: reading status: %w", err)
	}

	paths := make([]string, 0, len(status))
	for path, st := range status {
		if st.Staging == git.Unmodified && st.Worktree == git.Unmodified {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

// requireCleanWorkTree is the guard both tree-moving operations open
// with. remedy completes the sentence "..., so gage is not <remedy>".
func requireCleanWorkTree(dir, remedy string) error {
	paths, err := DirtyPaths(dir)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}

	named := paths
	suffix := ""
	if len(named) > maxNamedDirtyPaths {
		named = named[:maxNamedDirtyPaths]
		suffix = fmt.Sprintf(" and %d more", len(paths)-maxNamedDirtyPaths)
	}
	return fmt.Errorf("%w (%s%s), so gage is not %s; commit or discard them first",
		ErrDirtyWorkTree, strings.Join(named, ", "), suffix, remedy)
}

// CommitCount returns the number of commits reachable from HEAD.
func CommitCount(dir string) (int, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return 0, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	head, err := repo.Head()
	if err != nil {
		return 0, fmt.Errorf("gitrepo: reading HEAD: %w", err)
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return 0, fmt.Errorf("gitrepo: reading log: %w", err)
	}
	defer iter.Close()

	count := 0
	err = iter.ForEach(func(*object.Commit) error {
		count++
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("gitrepo: walking log: %w", err)
	}
	return count, nil
}
