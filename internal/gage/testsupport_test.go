package gage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// fakePrompter satisfies Prompter for tests without ever touching a
// terminal, proving the interface is usable from the library side. It
// answers with passphrases from a scripted list — one per attempt — and
// records every request and warning it was handed, which is how the
// unlock tests assert on the exchange itself rather than only its result.
type fakePrompter struct {
	// passphrases is answered in order, one per Unlock call. When it
	// runs out, Unlock returns errPrompterGaveUp — which is how a test
	// Prompter expresses the same "stop retrying" decision cmd/gage's
	// real retry policy makes.
	passphrases []string
	unlockErr   error

	requests []UnlockRequest
	warnings []string
}

// errPrompterGaveUp stands in for cmd/gage's "that's enough attempts."
// The library has no retry policy of its own; it stops when the Prompter
// stops answering, and this is a Prompter that stops.
var errPrompterGaveUp = errors.New("fake prompter: out of scripted answers")

func (f *fakePrompter) Unlock(req UnlockRequest) (UnlockResponse, error) {
	f.requests = append(f.requests, req)
	if f.unlockErr != nil {
		return UnlockResponse{}, f.unlockErr
	}
	i := len(f.requests) - 1
	if i >= len(f.passphrases) {
		return UnlockResponse{}, errPrompterGaveUp
	}
	return UnlockResponse{Kind: KindPassphrase, Passphrase: f.passphrases[i]}, nil
}

func (f *fakePrompter) Confirm(prompt string) (bool, error)       { return true, nil }
func (f *fakePrompter) Choose(list CandidateList) (string, error) { return "", nil }
func (f *fakePrompter) Warn(msg string)                           { f.warnings = append(f.warnings, msg) }

// mismatchedPrompter answers a passphrase request with a different Kind,
// which is what a buggy or mismatched frontend looks like from the
// library's side.
type mismatchedPrompter struct{}

func (mismatchedPrompter) Unlock(req UnlockRequest) (UnlockResponse, error) {
	return UnlockResponse{Kind: "yubikey"}, nil
}
func (mismatchedPrompter) Confirm(prompt string) (bool, error)       { return true, nil }
func (mismatchedPrompter) Choose(list CandidateList) (string, error) { return "", nil }
func (mismatchedPrompter) Warn(msg string)                           {}

// alwaysLocks is a Locker that succeeds without asking the OS for
// anything.
//
// The Close contract — releases the lock, zeroes the key, does neither
// twice — is about Identity's own logic, not about whether this
// particular machine's kernel will honour an mlock. Wiring the real
// memlock into those tests made them fail on any host that refuses to
// lock pages: a restricted `ulimit -l`, a container, a locked-down
// Windows policy. That is exactly the environment the design says gage
// must keep working in ("a weaker guarantee beats an unusable tool"), so
// a red test suite there would be the tests contradicting the product.
//
// The real memlock is still covered: by its own package's round-trip
// test, and by TestUnlockPageLocksOrWarnsButNeverBoth, which drives the
// default locker and asserts the invariant that holds either way.
type alwaysLocks struct{}

func (alwaysLocks) Lock(b []byte) error   { return nil }
func (alwaysLocks) Unlock(b []byte) error { return nil }

// countingLocker wraps the real Locker so a test can assert how many
// times a page was locked and unlocked — the only way to prove Close is
// idempotent in the sense that matters (it doesn't double-unlock a page),
// as opposed to merely not panicking.
type countingLocker struct {
	inner    Locker
	lockErr  error
	locks    int
	unlocks  int
	unlocked [][]byte
}

func (c *countingLocker) Lock(b []byte) error {
	c.locks++
	if c.lockErr != nil {
		return c.lockErr
	}
	return c.inner.Lock(b)
}

func (c *countingLocker) Unlock(b []byte) error {
	c.unlocks++
	c.unlocked = append(c.unlocked, b)
	if c.lockErr != nil {
		// Nothing was ever locked, so there is nothing to release; a
		// real Locker would report an error for this, which is exactly
		// what Close must not reach.
		return errors.New("countingLocker: asked to unlock a page that was never locked")
	}
	return c.inner.Unlock(b)
}

// isolateXDG points every XDG root at a fresh temp directory for the
// duration of one test, so library tests that read global config or write
// identity files never touch the real machine's config/data.
func isolateXDG(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
}

// registerVault writes the global-config record Unlock reads this
// device's name and method out of.
func registerVault(t *testing.T, name, device, method string) {
	t.Helper()
	dir := configDirForTest(t)
	g := config.Global{
		Current: name,
		Vaults: map[string]config.VaultEntry{
			name: {Path: filepath.Join(t.TempDir(), name), Type: TypeGit, Device: device, Method: method},
		},
	}
	if err := config.Write(filepath.Join(dir, "config.toml"), g); err != nil {
		t.Fatal(err)
	}
}

// configDirForTest resolves and creates $GAGE_CONFIG under the isolated
// roots isolateXDG set up.
func configDirForTest(t *testing.T) string {
	t.Helper()
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
