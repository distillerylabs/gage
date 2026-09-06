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
// It walks HEAD's ancestry and keeps a commit whenever the blob at path
// differs from what the previous (i.e. parent-side) commit had — which
// is the same "commits touching this file" set `git log -- <path>`
// reports, computed here rather than shelled out, per the design doc's
// no-git-binary rule.
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

	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return nil, fmt.Errorf("gitrepo: walking history: %w", err)
	}
	defer iter.Close()

	var (
		out  []Revision
		prev []byte
		have bool
	)
	// go-git walks newest-first; the comparison wants each commit
	// against its parent, so the list is built newest-first and the
	// "previous" content is the *older* one. Collect every commit's
	// content first, then diff adjacent pairs.
	type snapshot struct {
		commit  *object.Commit
		content []byte
		present bool
	}
	var snaps []snapshot
	err = iter.ForEach(func(c *object.Commit) error {
		content, present, err := blobAt(c, path)
		if err != nil {
			return err
		}
		snaps = append(snaps, snapshot{commit: c, content: content, present: present})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gitrepo: reading %s across history: %w", path, err)
	}

	// Walk oldest-first so "changed relative to the previous commit" is
	// a forward comparison, then reverse at the end.
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		changed := s.present != have || (s.present && !bytes.Equal(s.content, prev))
		if changed {
			out = append(out, Revision{
				Hash:    s.commit.Hash.String(),
				When:    s.commit.Author.When,
				Message: s.commit.Message,
				Content: s.content,
			})
		}
		prev, have = s.content, s.present
	}

	// Reverse into newest-first, the order a log is read in.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
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
