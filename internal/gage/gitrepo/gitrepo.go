// Package gitrepo wraps the small slice of go-git operations gage's
// vault lifecycle needs: init + first commit, reading/setting the
// "origin" remote, and reporting clean/dirty state. There's deliberately
// no generic passthrough here — see "Git-specific commands" in the
// design doc — every call goes through go-git rather than shelling out
// to a git binary.
package gitrepo

import (
	"fmt"
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
