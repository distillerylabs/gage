package gitrepo

import (
	"errors"
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

// MergeResult is what a merge found: either one it completed, or the set
// of paths that stopped it.
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

// MergeSide names one of the two versions a merge is reconciling — which
// side's content to read, and which side's version to keep for a path
// both changed.
type MergeSide int

const (
	// LocalSide is this device's version: what HEAD has.
	LocalSide MergeSide = iota
	// RemoteSide is the fetched version: what the remote-tracking ref
	// has.
	RemoteSide
)

// PendingMerge is a three-way merge that has been *computed* but not
// applied.
//
// It exists because resolving a conflict has to happen between those two
// halves. MergeRemote decides and applies in one call, which is exactly
// right when nothing conflicts — but a conflicting entry has to be
// decrypted, shown to a human and answered before anything can be
// written, and the whole sync model depends on nothing being written
// until then. Splitting the two lets a caller read both sides
// (Content), make up its mind, and only then commit (Commit) — with the
// same all-or-nothing guarantee MergeRemote already gives, since a
// caller that decides not to proceed simply never calls Commit and the
// vault is untouched.
type PendingMerge struct {
	dir    string
	repo   *git.Repository
	local  *object.Commit
	remote *object.Commit

	// localFiles is HEAD's tree, kept for rollback: it says which of the
	// paths a failed Commit had written were tracked (restored by the
	// hard reset) and which were brand new (removed by name).
	localFiles map[string]blobVersion

	// conflicts is what both sides changed differently, sorted. Commit
	// requires a choice for every one of them.
	conflicts []string
	// takeRemote is what only the remote touched — applied without
	// asking anyone, since there is nothing to choose between.
	takeRemote []string
}

// Conflicts returns the paths both sides changed differently, sorted. An
// empty result means Commit can proceed with no choices at all.
func (m *PendingMerge) Conflicts() []string { return m.conflicts }

// Content returns one side's version of path, and whether that side has
// it at all — false is a deletion, which is what makes a delete/modify
// conflict presentable as "one side removed this".
func (m *PendingMerge) Content(side MergeSide, path string) ([]byte, bool, error) {
	commit := m.local
	if side == RemoteSide {
		commit = m.remote
	}
	if commit == nil {
		return nil, false, nil
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, false, fmt.Errorf("gitrepo: reading tree: %w", err)
	}
	file, err := tree.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("gitrepo: reading %s: %w", path, err)
	}

	// Reader rather than Contents: entry files are age ciphertext, and
	// Contents would round-trip them through a string.
	reader, err := file.Reader()
	if err != nil {
		return nil, false, fmt.Errorf("gitrepo: reading %s: %w", path, err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, fmt.Errorf("gitrepo: reading %s: %w", path, err)
	}
	return data, true, nil
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
// half-merged tree behind for the next command to trip over. Resolving
// those conflicts instead of reporting them is PrepareMerge's job.
func MergeRemote(dir, message string) (MergeResult, error) {
	merge, err := PrepareMerge(dir)
	if err != nil {
		return MergeResult{}, err
	}
	if len(merge.conflicts) > 0 {
		return MergeResult{Conflicts: merge.conflicts}, nil
	}
	return merge.Commit(message, nil, nil)
}

// PrepareMerge computes the merge MergeRemote would perform and stops
// before writing anything, so a caller can inspect what conflicted and
// decide.
//
// A dirty working tree is refused up front, the same way FastForward
// refuses one. The reasons are mirror images: a fast-forward's hard reset
// would *discard* someone's uncommitted editing, while this merge's
// `git add -A`-equivalent staging would *publish* it, swept into a merge
// commit nobody wrote it for and pushed to every other device. gage
// commits its own writes immediately, so a dirty tree here always means
// something outside gage is mid-edit.
//
// It is checked here rather than in Commit deliberately: a human is
// about to be asked which version of a secret to keep, and discovering
// only afterwards that the answer can't be applied would waste exactly
// the attention this milestone is spending.
func PrepareMerge(dir string) (*PendingMerge, error) {
	if err := requireCleanWorkTree(dir, "merging origin's changes on top of them"); err != nil {
		return nil, err
	}

	repo, err := git.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	local, remote, err := headAndTracking(repo)
	if err != nil {
		return nil, err
	}
	if remote == nil {
		return nil, fmt.Errorf("gitrepo: %s has nothing fetched to merge", dir)
	}

	base, err := mergeBase(local, remote)
	if err != nil {
		return nil, err
	}

	baseFiles, err := treeVersions(base)
	if err != nil {
		return nil, err
	}
	localFiles, err := treeVersions(local)
	if err != nil {
		return nil, err
	}
	remoteFiles, err := treeVersions(remote)
	if err != nil {
		return nil, err
	}

	merge := &PendingMerge{
		dir: dir, repo: repo, local: local, remote: remote,
		localFiles: localFiles,
	}
	for _, path := range unionPaths(baseFiles, localFiles, remoteFiles) {
		b, l, r := baseFiles[path], localFiles[path], remoteFiles[path]
		switch {
		case l == r:
			// Both sides agree — including both having deleted it.
		case l == b:
			// Only the remote touched it: take its version, whether that
			// means new content or a deletion.
			merge.takeRemote = append(merge.takeRemote, path)
		case r == b:
			// Only we touched it; nothing to do.
		default:
			merge.conflicts = append(merge.conflicts, path)
		}
	}
	sort.Strings(merge.conflicts)
	return merge, nil
}

// mergedFileMode is what any file this merge creates is written as —
// one the remote added, or one of Commit's adds.
//
// 0600 rather than the mode git recorded: everything a vault merge
// writes is either an entry's ciphertext or a file describing who can
// read it, and none of it has any business being group- or
// world-readable just because git normalizes blobs to 0644. It matches
// what Vault.WriteEntry gives every entry gage writes directly, so how
// an entry arrived stops deciding who can read its file.
const mergedFileMode = 0o600

// Commit applies this merge and records it as a real two-parent commit.
//
// choices answers every path Conflicts() named — LocalSide leaves this
// device's version in place, RemoteSide replaces it with the fetched one
// (including replacing it with a deletion). A missing answer is refused
// rather than defaulted: silently keeping the local version is the
// last-write-wins outcome the sync model exists to refuse, and it must
// not be reachable by a caller forgetting a path.
//
// adds are brand-new files this merge should also create — how `keep
// both` writes the losing version of an entry under a fresh id without
// gage's git layer having to know what an entry is. They are written,
// staged and rolled back with everything else, so a failure part-way
// through leaves no orphan behind.
func (m *PendingMerge) Commit(message string, choices map[string]MergeSide, adds map[string][]byte) (MergeResult, error) {
	for _, path := range m.conflicts {
		if _, answered := choices[path]; !answered {
			return MergeResult{}, fmt.Errorf(
				"gitrepo: refusing to merge with %s unanswered: every conflicting path needs a choice", path)
		}
	}
	for path := range adds {
		if _, taken := m.localFiles[path]; taken {
			return MergeResult{}, fmt.Errorf("gitrepo: refusing to add %s: it already exists", path)
		}
	}

	// applied grows *before* each write rather than after, so a path that
	// failed halfway through being written is still rolled back.
	applied := make([]string, 0, len(m.takeRemote)+len(m.conflicts)+len(adds))
	take := append([]string{}, m.takeRemote...)
	for _, path := range m.conflicts {
		if choices[path] == RemoteSide {
			take = append(take, path)
		}
	}
	for _, path := range take {
		applied = append(applied, path)
		if err := applyRemoteVersion(m.dir, m.remote, path); err != nil {
			return MergeResult{}, m.rollback(applied, err)
		}
	}

	for _, path := range sortedAddPaths(adds) {
		applied = append(applied, path)
		if err := writeMergedFile(m.dir, path, adds[path]); err != nil {
			return MergeResult{}, m.rollback(applied, err)
		}
	}

	hash, err := commitMerge(m.repo, message, m.local.Hash, m.remote.Hash)
	if err != nil {
		return MergeResult{}, m.rollback(applied, err)
	}
	return MergeResult{Merged: true, Commit: hash}, nil
}

// sortedAddPaths orders new files deterministically, so two runs of the
// same merge write in the same order on every platform.
func sortedAddPaths(adds map[string][]byte) []string {
	paths := make([]string, 0, len(adds))
	for path := range adds {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// writeMergedFile creates one of Commit's adds.
func writeMergedFile(dir, path string, content []byte) error {
	full, err := safeJoin(dir, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return fmt.Errorf("gitrepo: creating directory for %s: %w", path, err)
	}
	if err := os.WriteFile(full, content, mergedFileMode); err != nil {
		return fmt.Errorf("gitrepo: writing %s: %w", path, err)
	}
	return nil
}

// rollback undoes a merge that failed partway through applying itself,
// and returns cause so the caller still sees why it failed.
//
// Without this a failed merge leaves a tree that is neither the old state
// nor the new one — and since every later sync operation now refuses over
// a dirty tree, that state would wedge the vault until a human cleaned it
// up by hand. Discarding is safe here and only here: PrepareMerge
// verified the tree was clean before anything was written, so everything
// being undone is this merge's own work.
//
// The hard reset restores tracked files; paths the merge *created* — a
// file the remote added, or one of Commit's adds — are untracked and
// invisible to it, so they are removed by name first.
func (m *PendingMerge) rollback(applied []string, cause error) error {
	fail := func(err error) error {
		return fmt.Errorf("%w (and rolling the partial merge back failed: %v)", cause, err)
	}

	for _, path := range applied {
		if _, tracked := m.localFiles[path]; tracked {
			continue
		}
		full, err := safeJoin(m.dir, path)
		if err != nil {
			return fail(err)
		}
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return fail(err)
		}
	}

	wt, err := m.repo.Worktree()
	if err != nil {
		return fail(err)
	}
	if err := wt.Reset(&git.ResetOptions{Commit: m.local.Hash, Mode: git.HardReset}); err != nil {
		return fail(err)
	}
	return cause
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

	// mergedFileMode rather than the mode git recorded, which for every
	// blob it has ever stored here is 0644. It only bites on a file this
	// merge *creates* — an overwrite keeps the mode the file already has,
	// O_TRUNC not being a chmod — so before there was anything to create
	// but a fast-forward, nothing noticed. A merge that brings in an
	// entry this device has never seen is exactly that case, and an entry
	// file gage wrote itself is 0600.
	//
	// #nosec G304 -- full is safeJoin's result, which has already been
	// confined to dir.
	out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mergedFileMode)
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
