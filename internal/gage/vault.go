package gage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
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
	lock, err := acquireVaultLock(v.ID, timeout)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	return fn()
}

// acquireVaultLock opens (creating if needed) and locks a vault's
// advisory write-lock file, wrapping every failure with the exit code a
// caller expects: exitcode.Conflict for a contended lock, exitcode.Internal
// for anything else. It is the one place that translates a bare
// vaultlock.Acquire into gage's own error shape, shared by
// withWriteLockTimeout (one vault) and withTwoVaultLocks (M11's mv/cp,
// two).
//
// It takes the vault's id rather than its name, so two registrations of
// one repository contend for a single lock instead of holding one each
// — see LockFilePath.
func acquireVaultLock(vaultID string, timeout time.Duration) (*vaultlock.Lock, error) {
	path, err := LockFilePath(vaultID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: creating lock directory: %w", err))
	}

	lock, err := vaultlock.Acquire(path, timeout)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Conflict, fmt.Errorf("gage: %w", err))
	}
	return lock, nil
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

	// ID is the vault's own id — its committed [vault].id, copied into
	// global config at registration so this machine can reach the vault's
	// local state without reading the vault itself.
	//
	// Every piece of per-vault local state is keyed by it: the identities
	// directory, the trust cache, and the advisory write lock. Name is
	// deliberately none of those things any more; after A20 it is a label
	// for humans. Every construction site must set this — a Vault built
	// without it yields the empty string, which every path builder
	// refuses (see checkVaultID), so the failure is loud rather than a
	// directory named "".
	ID string

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

	// onReencryptEntry, if set, is called after each entry has been
	// re-encrypted into the working tree during --reencrypt, with the
	// count written so far. Test-only; nil everywhere else.
	//
	// It exists because --reencrypt's central claim is about what an
	// *interruption* leaves behind, and there is no honest way to
	// interrupt a pass from outside it: a test panics from inside this
	// hook to stop the run with no unwinding, or re-acquires the vault
	// lock from it to prove the lock is still held at every point in the
	// pass. Nothing on the reencrypt path may recover() — a recover
	// would turn the simulated crash into ordinary cleanup, and the
	// crash test would silently start proving something else.
	onReencryptEntry func(done int)

	// onEnrollmentDecrypt, if set, is called once per attempted scrypt
	// run on the enrollment open path — one call per (code, request)
	// pair actually tried. Test-only; nil everywhere else.
	//
	// It exists because several of enrollment's claims are about work
	// *not* done: a code failing length or alphabet validation costs no
	// decryption, an oversized file is never opened, the 32-request bound
	// refuses before any KDF run, and an ID-scoped run costs one attempt
	// per code however full the directory is. None of those is observable
	// from a return value — a correct answer arrived at expensively looks
	// exactly like a correct answer — so the count is the only honest
	// assertion available. It is the same technique M7's index tests use
	// against onDecrypt.
	onEnrollmentDecrypt func()

	// onEnrollPulled, if set, is called from inside Enroll's own write
	// lock, immediately after the catch-up pull and before anything is
	// generated. Test-only; nil everywhere else.
	//
	// It exists because D-ENROLL-REMOTE's "the lock wraps the pull, not
	// just the commit" is a claim about a window that has no observable
	// return value: an implementation that pulled outside the lock and
	// took it afterwards produces the same request. A test re-acquires
	// the vault lock from this hook and asserts contention, which is the
	// same technique the --reencrypt tests use against
	// onReencryptEntry.
	onEnrollPulled func()

	// onMoveDestCommitted, if set on the *source* vault Move/Copy was
	// called on, is called once the destination's write-and-commit
	// sequence has succeeded, before the source-side removal (Move) or
	// return (Copy) runs. Test-only; nil everywhere else.
	//
	// It exists for the same reason onReencryptEntry does: M11's
	// crash-safety claim is about what an interruption between the two
	// vaults' writes leaves behind, and a test panics from inside this
	// hook — never recovers — to produce that interruption honestly
	// rather than simulating its aftermath by hand.
	onMoveDestCommitted func()
}

// Unlock lives in unlock.go.

// withVaultWrite is every mutating method's preamble: take the write
// lock, discard an unexpectedly dirty working tree (warning through p
// first), then run the write itself.
//
// The reset lives here rather than in each verb so that "no write ever
// folds a previous write's leftovers into its own commit" is structural,
// exactly as withWriteLock makes "a write holds the lock across its whole
// sequence" structural. It runs *under* the lock, which is what keeps it
// from racing another process's in-flight write — see the M9 plan's
// "This runs under the vault lock".
//
// The sync paths deliberately do not go through this: a fast-forward or
// a merge refuses over a dirty tree rather than discarding it (see
// gitrepo.ErrDirtyWorkTree), because there the uncommitted work could be
// something outside gage is mid-edit. Here the caller is gage itself,
// about to commit, and a leftover can only have come from a gage write
// that died.
func (v *Vault) withVaultWrite(p Prompter, fn func() error) error {
	return v.withWriteLock(func() error {
		if err := v.resetDirtyWorkTree(p); err != nil {
			return err
		}
		return fn()
	})
}

// withEncryptingWrite is withVaultWrite for the methods that produce
// ciphertext: the same lock and dirty-tree reset, plus M10's blocking
// trust-cache check, run before fn writes anything.
//
// The check sits here rather than in each verb so that "no entry is ever
// encrypted to a recipient list this device never reviewed" is
// structural, the same way withVaultWrite makes the reset structural.
// Ordering is the point of it: the confirmation runs under the vault
// lock and before the first byte is written, so a declined answer leaves
// nothing to undo — nothing written, nothing committed, and the cache
// exactly where it was.
//
// Vault.Remove deliberately does not go through this. Deleting an entry
// encrypts nothing, so there is no list to approve, and asking anyway
// would make the prompt a tax on every write rather than a question
// about the one thing it is asking about. M11's mv/cp reuse this against
// the *destination* vault, which is the one whose recipients the moved
// entry gets encrypted to.
func (v *Vault) withEncryptingWrite(ident *Identity, fn func() error) error {
	return v.withVaultWrite(ident.warnTo(), func() error {
		if err := v.confirmRecipientTrust(ident.frontend()); err != nil {
			return err
		}
		return fn()
	})
}

// resetDirtyWorkTree discards uncommitted changes in the vault's working
// tree and warns once, naming what it discarded. A clean tree is silent:
// a warning on every ordinary write would train people to ignore the one
// that matters.
//
// The warning is emitted whatever the cause. "Only an interrupted
// --reencrypt could have done this" is likely but not provable, and the
// M9 plan is explicit that the reset is automatic but never silent for
// exactly that reason.
//
// The whole working tree is covered, not only entries/: a crash in the
// narrow window after --reencrypt writes the recipient files but before
// it commits dirties those two as well, and a reset that skipped them
// would leave the half-migrated state this milestone exists to rule out.
func (v *Vault) resetDirtyWorkTree(p Prompter) error {
	discarded, err := gitrepo.ResetHard(v.Path)
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: %w", err))
	}
	if len(discarded) == 0 {
		return nil
	}
	warn(p, "gage: discarded uncommitted changes left by an interrupted write: %s", strings.Join(discarded, ", "))
	return nil
}
