package gitrepo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Revision is one commit in which a given path changed, with the file's
// content at that commit. Content is nil when the commit deleted the
// path, which is what lets a caller render a removal rather than
// silently skipping it.
//
// The blob is carried as bytes rather than a reader because everything
// gage stores at these paths is one small .age file that the caller is
// about to decrypt in memory anyway.
type Revision struct {
	Hash    string
	When    time.Time
	Message string
	Content []byte
}

// Log returns the commits in which path changed, newest first.
//
// It walks HEAD's ancestry and keeps a commit whose blob at path differs
// from that of *every* one of its parents — the same "commits touching
// this file" set `git log -- <path>` reports, computed here rather than
// shelled out, per the design doc's no-git-binary rule.
//
// Comparing against real parents, rather than against the previous
// commit in the walk, is what makes this correct once a vault has ever
// synced. The walk flattens a branching history into a list, so the
// commit printed before another one is frequently not its parent at all
// — it is the tip of the other side of a merge. Diffing those neighbours
// reports every commit on each side as having changed a path only the
// other side touched, and reports a path as *deleted* by any commit that
// merely predates its creation on the other branch. Both are entries
// this vault never had, in a log whose whole job is to say truthfully
// when a secret changed.
//
// A merge is therefore uninteresting for a path whenever it matches any
// parent: it carried that side's version through rather than changing
// anything. Only a merge that resolved a real conflict — a blob equal to
// neither side — is a change of its own, which is exactly what it is.
//
// A path that has never existed yields no revisions and no error: an
// entry with no history is a normal state (it was inserted and the
// commit is still unpushed, or the caller resolved an id that git has
// never seen), not a failure.
func Log(dir, path string) ([]Revision, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("gitrepo: opening %s: %w", dir, err)
	}
	head, err := repo.Head()
	if err != nil {
		// A repository with no commits yet has no history to report,
		// which is not an error any more than an empty log is.
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("gitrepo: reading HEAD: %w", err)
	}

	// Committer-time order, which is git log's own default. The walk no
	// longer depends on adjacency for correctness — each commit is
	// judged against its parents — so ordering is purely presentational,
	// and this is the order a human reading a log expects.
	iter, err := repo.Log(&git.LogOptions{From: head.Hash(), Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, fmt.Errorf("gitrepo: walking history: %w", err)
	}
	defer iter.Close()

	// blobAt is called for every commit and again for it as somebody's
	// parent, so results are memoised per commit. A merge-heavy vault
	// would otherwise re-read the same tree once per child.
	cache := map[plumbing.Hash]blob{}
	at := func(c *object.Commit) (blob, error) {
		if b, ok := cache[c.Hash]; ok {
			return b, nil
		}
		content, present, err := blobAt(c, path)
		if err != nil {
			return blob{}, err
		}
		b := blob{content: content, present: present}
		cache[c.Hash] = b
		return b, nil
	}

	var out []Revision
	err = iter.ForEach(func(c *object.Commit) error {
		mine, err := at(c)
		if err != nil {
			return err
		}
		// A root commit has nothing to differ from, so it is a change
		// exactly when the path is there at all.
		changed := mine.present
		for i := 0; i < c.NumParents(); i++ {
			p, err := c.Parent(i)
			if err != nil {
				return err
			}
			theirs, err := at(p)
			if err != nil {
				return err
			}
			if mine.equal(theirs) {
				// Unchanged on at least one line of descent, so this
				// commit did not touch the path.
				changed = false
				break
			}
			changed = true
		}
		if changed {
			out = append(out, Revision{
				Hash:    c.Hash.String(),
				When:    c.Author.When,
				Message: c.Message,
				Content: mine.content,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gitrepo: reading %s across history: %w", path, err)
	}
	return out, nil
}

// blob is path's state in one commit: its bytes, and whether it was
// there at all. The two are kept together because "absent" and "empty"
// are different answers and comparing only the bytes would merge them.
type blob struct {
	content []byte
	present bool
}

func (b blob) equal(other blob) bool {
	if b.present != other.present {
		return false
	}
	return !b.present || bytes.Equal(b.content, other.content)
}

// blobAt returns the content of path in c's tree, and whether it exists
// there at all.
func blobAt(c *object.Commit, path string) ([]byte, bool, error) {
	f, err := c.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	r, err := f.Reader()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}
