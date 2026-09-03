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
	path, err := LockFilePath(v.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: creating lock directory: %w", err))
	}

	lock, err := vaultlock.Acquire(path, vaultLockTimeout)
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
}

// Unlock lives in unlock.go.
