// Package gittest provides test-only git fixtures: ephemeral local bare
// repositories that stand in for a real remote, so sync-related tests
// exercise go-git's real code paths without a socket or network access.
// First used by M1's `git set-remote` tests; extended into the full
// sync-testing harness in M8a.
package gittest

import (
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
)

// NewBareRemote creates an ephemeral local bare git repository under
// t.TempDir() and returns its path. Two calls — even within the same
// test — produce independent, non-colliding repos, since each gets its
// own temp subdirectory.
func NewBareRemote(t testing.TB) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(path, true); err != nil {
		t.Fatalf("gittest: initializing bare remote at %s: %v", path, err)
	}
	return path
}
