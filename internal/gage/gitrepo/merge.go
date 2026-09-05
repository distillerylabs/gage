package gitrepo

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// MergeResult is what MergeRemote found: either a merge it completed, or
// the set of paths that stopped it.
type MergeResult struct {
	// Merged is true when a merge commit was created. When it's false,
	// Conflicts says why, and nothing was changed.
	Merged bool
	// Commit is the new merge commit's hash, when Merged.
	Commit string
	// Conflicts lists the paths both sides changed differently, sorted.
	// Empty when Merged.
	Conflicts []string
}

// MergeRemote performs gage's three-way merge of the local branch and its
// remote-tracking ref, and commits the result as a real two-parent merge
// commit.
//
// go-git has no merge of its own, so this is gage's: a *file-level*
// three-way merge, which is the only kind that makes sense here anyway.
// Entries are age ciphertext — opaque binary blobs with no meaningful
// line structure — so two edits to the same entry can never be reconciled
// by a text merge, and pretending otherwise is precisely the
// silently-lose-a-secret failure the design rules out. A path is taken
// from whichever side changed it; a path both sides changed differently
// is a conflict, full stop.
//
// That same coarseness is what enforces .gitattributes' `-merge` on the
// two recipient-defining files without gage ever interpreting
// .gitattributes: two devices adding a different recipient each produce a
// conflict here rather than a union neither of them wrote. The committed
// .gitattributes still matters — it binds the real `git` a human might
// run inside the vault — but this merge does not depend on it.
//
// Nothing is written when the merge conflicts: the caller gets the list
// and the vault is left exactly as it was, so detection never leaves a
// half-merged tree behind for the next command to trip over.
func MergeRemote(dir, message string) (MergeResult, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return MergeResult{}, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	local, remote, err := headAndTracking(repo)
	if err != nil {
		return MergeResult{}, err
	}
	if remote == nil {
		return MergeResult{}, fmt.Errorf("gitrepo: %s has nothing fetched to merge", dir)
	}

	base, err := mergeBase(local, remote)
	if err != nil {
		return MergeResult{}, err
	}

	baseFiles, err := treeVersions(base)
	if err != nil {
		return MergeResult{}, err
	}
	localFiles, err := treeVersions(local)
	if err != nil {
		return MergeResult{}, err
	}
	remoteFiles, err := treeVersions(remote)
	if err != nil {
		return MergeResult{}, err
	}

	var (
		conflicts  []string
		takeRemote []string
	)
	for _, path := range unionPaths(baseFiles, localFiles, remoteFiles) {
		b, l, r := baseFiles[path], localFiles[path], remoteFiles[path]
		switch {
		case l == r:
			// Both sides agree — including both having deleted it.
		case l == b:
			// Only the remote touched it: take its version, whether that
			// means new content or a deletion.
			takeRemote = append(takeRemote, path)
		case r == b:
			// Only we touched it; nothing to do.
		default:
			conflicts = append(conflicts, path)
		}
	}

	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return MergeResult{Conflicts: conflicts}, nil
	}

	for _, path := range takeRemote {
		if err := applyRemoteVersion(dir, remote, path); err != nil {
			return MergeResult{}, err
		}
	}

	hash, err := commitMerge(repo, message, local.Hash, remote.Hash)
	if err != nil {
		return MergeResult{}, err
	}
	return MergeResult{Merged: true, Commit: hash}, nil
}

// blobVersion identifies one path's content at one commit. Mode is part
// of the identity so a chmod counts as a change, the same way git treats
// it.
type blobVersion struct {
	hash plumbing.Hash
	mode filemode.FileMode
}

// mergeBase finds the common ancestor of two commits. Histories with no
// common ancestor at all (two vaults that were never clones of each
// other) get an empty base, which makes every path present on both sides
// an add/add pair — and therefore a conflict unless the two happen to be
// byte-identical. That is the right answer: gage has no basis for
// preferring either side's file.
func mergeBase(local, remote *object.Commit) (*object.Commit, error) {
	bases, err := local.MergeBase(remote)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: finding the merge base: %w", err)
	}
	if len(bases) == 0 {
		return nil, nil
	}
	return bases[0], nil
}

// treeVersions maps every file in a commit's tree to its content
// identity. A nil commit is an empty tree.
func treeVersions(c *object.Commit) (map[string]blobVersion, error) {
	out := map[string]blobVersion{}
	if c == nil {
		return out, nil
	}
	tree, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("gitrepo: reading tree: %w", err)
	}
	err = tree.Files().ForEach(func(f *object.File) error {
		out[f.Name] = blobVersion{hash: f.Hash, mode: f.Mode}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gitrepo: walking tree: %w", err)
	}
	return out, nil
}

// unionPaths returns every path named by any of the three trees, sorted
// so a merge visits paths in a deterministic order on every platform.
func unionPaths(maps ...map[string]blobVersion) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		for path := range m {
			seen[path] = true
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// applyRemoteVersion writes (or deletes) one path in the working tree to
// match the remote's version of it.
func applyRemoteVersion(dir string, remote *object.Commit, path string) error {
	full, err := safeJoin(dir, path)
	if err != nil {
		return err
	}

	tree, err := remote.Tree()
	if err != nil {
		return fmt.Errorf("gitrepo: reading the remote's tree: %w", err)
	}
	file, err := tree.File(path)
	if err != nil {
		// Not in the remote's tree: the remote deleted it, and we hadn't
		// touched it, so the merged result is the deletion.
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("gitrepo: removing %s: %w", path, err)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return fmt.Errorf("gitrepo: creating directory for %s: %w", path, err)
	}
	reader, err := file.Reader()
	if err != nil {
		return fmt.Errorf("gitrepo: reading %s from the remote: %w", path, err)
	}
	defer func() { _ = reader.Close() }()

	mode, err := file.Mode.ToOSFileMode()
	if err != nil {
		return fmt.Errorf("gitrepo: reading the mode of %s: %w", path, err)
	}
	// #nosec G304 -- full is safeJoin's result, which has already been
	// confined to dir.
	out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return fmt.Errorf("gitrepo: writing %s: %w", path, err)
	}
	if _, err := io.Copy(out, reader); err != nil {
		_ = out.Close()
		return fmt.Errorf("gitrepo: writing %s: %w", path, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("gitrepo: writing %s: %w", path, err)
	}
	return nil
}

// safeJoin resolves a path from a git tree against the vault directory,
// refusing anything that would escape it.
//
// Paths here come from a *remote* — whoever can push to the vault decides
// what they say — so "entries/../../../../etc/cron.d/x" must be refused
// rather than helpfully resolved. Real git rejects such trees too; this
// does not depend on that.
func safeJoin(dir, path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("gitrepo: refusing a remote path %q", path)
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("gitrepo: refusing a remote path that escapes the vault: %q", path)
	}
	return filepath.Join(dir, clean), nil
}

// commitMerge stages the merged working tree and records it as a commit
// with both sides as parents — a real merge commit, so local commit
// granularity survives and both devices' histories stay walkable.
func commitMerge(repo *git.Repository, message string, local, remote plumbing.Hash) (string, error) {
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("gitrepo: opening worktree: %w", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return "", fmt.Errorf("gitrepo: staging the merge: %w", err)
	}

	sig := &object.Signature{Name: commitAuthorName, Email: commitAuthorEmail, When: time.Now()}
	hash, err := wt.Commit(message, &git.CommitOptions{
		Author:  sig,
		Parents: []plumbing.Hash{local, remote},
		// A merge whose result matches the local tree exactly is still a
		// merge worth recording — it is what marks the remote's history
		// as incorporated, without which the next push would be rejected
		// all over again.
		AllowEmptyCommits: true,
	})
	if err != nil {
		return "", fmt.Errorf("gitrepo: committing the merge: %w", err)
	}
	return hash.String(), nil
}
