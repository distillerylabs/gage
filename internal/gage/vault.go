package gage

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/vaultlock"
)

// vaultLockTimeout bounds how long a write waits for a contended vault
// lock before giving up — long enough that "another gage process is
// about to finish" (the common case per "Concurrent processes and the
// vault lock") succeeds without a human noticing, short enough that a
// wedged holder doesn't hang a terminal indefinitely.
const vaultLockTimeout = 10 * time.Second

// withWriteLock runs fn — a complete read-modify-commit sequence — under
// this vault's advisory write lock, released on every exit path including
// fn's own error return. Every Vault method that writes to entries/ and
// commits goes through this, so "a write holds the lock across its whole
// sequence" is structural rather than a convention each method has to
// remember. Reads never call this — see "Concurrent processes and the
// vault lock" in the design doc.
func (v *Vault) withWriteLock(fn func() error) error {
	return v.withWriteLockTimeout(vaultLockTimeout, fn)
}

// withWriteLockTimeout is withWriteLock with the wait spelled out.
//
// A timeout of 0 tries exactly once and gives up — what the *automatic*
// sync on unlock wants. Nobody asked for that sync, so it must not make a
// read queue behind an unrelated write; see Vault.syncOnUnlock. Callers
// distinguish "someone else holds it" from a real failure with
// errors.As against *vaultlock.ContendedError.
func (v *Vault) withWriteLockTimeout(timeout time.Duration, fn func() error) error {
	path, err := LockFilePath(v.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: creating lock directory: %w", err))
	}

	lock, err := vaultlock.Acquire(path, timeout)
	if err != nil {
		return exitcode.Wrap(exitcode.Conflict, fmt.Errorf("gage: %w", err))
	}
	defer func() { _ = lock.Release() }()

	return fn()
}

// Vault is a single vault's on-disk state: config, recipients, entries. It
// stays a stateless, identity-agnostic operator over ciphertext in both
// invocation modes — Session, not Vault, owns any "how long does this stay
// unlocked" bookkeeping. See "Library architecture" in the design doc.
//
// The on-disk structure arrived in M1 and real crypto in M2; what M0
// fixed is the shape everything after it builds against.
type Vault struct {
	// Name and Path are the vault's registry name and its on-disk
	// location. Set by Create, or by whatever M1+ adds for opening an
	// already-registered vault.
	Name string
	Path string

	// locker is the page-locking seam Unlock and Identity.Close go
	// through. nil means the real memlock-backed implementation, so
	// nothing outside this package's own tests ever sets it — see
	// Locker.
	locker Locker

	// onDecrypt, if set, is called once per successful ReadEntry — a
	// test-only seam for counting how many times this vault's
	// ciphertext was actually decrypted. It exists for M7's index
	// tests ("the first ls/show/search triggers exactly one full-decrypt
	// pass, later ones don't"); nil everywhere else.
	onDecrypt func()

	// onPull, if set, is called whenever a sync moves entries/ underneath
	// a caller — a fast-forward that advanced HEAD, or a merge that
	// brought another device's writes in. Session sets it to discard that
	// vault's metadata index, which is the whole of M7's
	// "a successful pull invalidates the index" requirement; one-shot
	// mode caches nothing and leaves it nil.
	//
	// It hangs off Vault rather than being returned to each caller so
	// that the invalidation is wired once, where the pull happens, rather
	// than at every call site that might cause one.
	onPull func()

	// remoteSyncer overrides how this vault reaches its remote. nil means
	// the real go-git-backed syncer. Only this package's own tests set
	// it — see RemoteSyncer for why that seam exists at all.
	remoteSyncer RemoteSyncer
}

// Unlock lives in unlock.go.
