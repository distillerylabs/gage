package gittest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Device is a throwaway clone of a bare remote standing in for "another
// device" — the second half of M8a's sync harness. A test uses one to
// produce the states that only exist because two machines share a vault:
// remote commits to pull, and (paired with a local commit) real
// divergence to detect.
//
// It is deliberately not a gage vault: it never unlocks, encrypts, or
// reads .gage/config.toml. It only needs to be able to move commits
// through the same bare remote a real second device would, which is what
// makes it a stand-in rather than a second implementation of anything.
type Device struct {
	// Dir is the clone's working tree.
	Dir string

	repo *git.Repository
}

// NewDevice clones remote (a NewBareRemote path) into a fresh temp
// directory.
func NewDevice(t testing.TB, remote string) *Device {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "device")
	repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: remote})
	if err != nil {
		t.Fatalf("gittest: cloning %s: %v", remote, err)
	}
	return &Device{Dir: dir, repo: repo}
}

// Write creates or replaces a file in the clone's working tree. path is
// slash-separated and relative to the vault root, like a git path.
func (d *Device) Write(t testing.TB, path, content string) {
	t.Helper()

	full := filepath.Join(d.Dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("gittest: creating directory for %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("gittest: writing %s: %v", path, err)
	}
}

// Remove deletes a file from the clone's working tree.
func (d *Device) Remove(t testing.TB, path string) {
	t.Helper()

	if err := os.Remove(filepath.Join(d.Dir, filepath.FromSlash(path))); err != nil {
		t.Fatalf("gittest: removing %s: %v", path, err)
	}
}

// Read returns a file's contents from the clone's working tree — how a
// test asserts what actually reached the remote.
func (d *Device) Read(t testing.TB, path string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(d.Dir, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("gittest: reading %s: %v", path, err)
	}
	return string(data)
}

// Exists reports whether a path is present in the clone's working tree.
func (d *Device) Exists(t testing.TB, path string) bool {
	t.Helper()

	_, err := os.Stat(filepath.Join(d.Dir, filepath.FromSlash(path)))
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("gittest: stat %s: %v", path, err)
	return false
}

// Commit stages everything in the clone and commits it.
func (d *Device) Commit(t testing.TB, message string) {
	t.Helper()

	wt, err := d.repo.Worktree()
	if err != nil {
		t.Fatalf("gittest: opening worktree: %v", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatalf("gittest: staging: %v", err)
	}
	_, err = wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: "gittest", Email: "gittest@localhost", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("gittest: committing: %v", err)
	}
}

// CommitAt is Commit with the signature's timestamp supplied, for the
// one thing a test cannot otherwise produce: a commit whose committer
// time is not this machine's idea of now.
//
// It exists for E3's clock-skew warning, which reads HEAD's committer
// timestamp and warns when local time is meaningfully behind it. go-git
// copies Author into Committer when no Committer is given, so setting
// one signature sets both.
func (d *Device) CommitAt(t testing.TB, message string, when time.Time) {
	t.Helper()

	wt, err := d.repo.Worktree()
	if err != nil {
		t.Fatalf("gittest: opening worktree: %v", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatalf("gittest: staging: %v", err)
	}
	sig := &object.Signature{Name: "gittest", Email: "gittest@localhost", When: when}
	if _, err := wt.Commit(message, &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatalf("gittest: committing: %v", err)
	}
}

// Push publishes the clone's commits back to the bare remote.
func (d *Device) Push(t testing.TB) {
	t.Helper()

	if err := d.repo.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("gittest: pushing: %v", err)
	}
}

// Pull fast-forwards the clone to whatever is on the remote now, so a
// test can assert what a *second* device would see after the vault under
// test pushed.
func (d *Device) Pull(t testing.TB) {
	t.Helper()

	wt, err := d.repo.Worktree()
	if err != nil {
		t.Fatalf("gittest: opening worktree: %v", err)
	}
	err = wt.Pull(&git.PullOptions{})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		t.Fatalf("gittest: pulling: %v", err)
	}
}

// WriteCommitPush is the whole "another device changed something" gesture
// in one call, which is what most tests want.
func (d *Device) WriteCommitPush(t testing.TB, path, content, message string) {
	t.Helper()

	d.Write(t, path, content)
	d.Commit(t, message)
	d.Push(t)
}
