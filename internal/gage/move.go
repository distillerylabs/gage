package gage

// M11 — cross-vault sharing: `mv`/`cp --to-vault`.
//
// With one flat recipient list per vault, there's nothing to move an
// entry *between* within a vault, so moving it to another vault is
// literally what "share this credential with someone" means in this
// design — see doc/implementation/00_gage-cli/plans/gage-cli-design/m11-cross-vault-sharing.md and the
// design doc's "mv/cp are now cross-vault, and that's the sharing
// mechanism."

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
)

// ErrSameVault is Move/Copy refusing a destination that names the source
// vault itself. There is nothing to share with yourself, and running the
// shared two-lock machinery against one vault twice would try to acquire
// its own advisory lock a second time rather than sharing an entry.
//
// "The same vault" means the same *id*, not the same local name. Since
// A20 one repository can legitimately be registered twice under two
// names, and those two registrations are one vault however they are
// labelled: they share a working tree and — see LockFilePath — a single
// lock file, so a move between them would deadlock against itself rather
// than share anything.
var ErrSameVault = errors.New("gage: --to-vault must name a different vault than the source")

// MoveResult is what Move/Copy hand back: enough for a caller to update a
// session's metadata index on both sides (M7) without decrypting either
// vault again. Entry is the copy exactly as written to the destination —
// same title/description/value/fields/extra as the source, with
// id/created/updated/updated_by refreshed; see the M11 plan's "Decisions
// to make first".
type MoveResult struct {
	// SourceID is the id Remove drops from the source vault — Copy leaves
	// it in place, so this is set on both calls, but only Move actually
	// deletes anything.
	SourceID uuid.UUID
	// DestID is the copy's fresh id in the destination vault.
	DestID uuid.UUID
	// Entry is the copy as written to the destination.
	Entry Entry
}

// Move resolves query in the source vault, decrypts it, runs the
// destination's M10 trust-cache check, encrypts a fresh copy for the
// destination's recipients and commits it there, then removes and commits
// the original in the source. See the M11 plan for the crash-safety and
// lock-ordering properties this holds.
//
// dest does not need to be unlocked: encrypting to a set of public keys
// never requires holding any of the matching private keys. ident is the
// source's — the only identity this call ever holds — and its device name
// is what the copy's updated_by records in the destination too, since
// this device is the one actually performing the share.
func (src *Vault) Move(query string, dest *Vault, ident *Identity) (MoveResult, error) {
	return moveOrCopy(src, dest, query, ident, true)
}

// Copy is Move without the source-side removal: the original stays in
// src, and a decryptable copy lands in dest.
func (src *Vault) Copy(query string, dest *Vault, ident *Identity) (MoveResult, error) {
	return moveOrCopy(src, dest, query, ident, false)
}

func moveOrCopy(src, dest *Vault, query string, ident *Identity, remove bool) (MoveResult, error) {
	if src.ID == dest.ID {
		return MoveResult{}, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("%w: %q", ErrSameVault, dest.Name))
	}

	var res MoveResult
	err := withTwoVaultLocks(src, dest, func() error {
		if err := src.resetDirtyWorkTree(ident.warnTo()); err != nil {
			return err
		}
		if err := dest.resetDirtyWorkTree(ident.warnTo()); err != nil {
			return err
		}

		srcID, e, err := src.Resolve(query, ident)
		if err != nil {
			return err
		}
		res.SourceID = srcID

		// The blocking half of M10's trust cache, run against the
		// *destination*'s cache — the vault the entry is about to be
		// encrypted for — before anything is written. A decline leaves
		// the source entry untouched and nothing written to the
		// destination, exactly like withEncryptingWrite's single-vault
		// version.
		if err := dest.confirmRecipientTrust(ident.frontend()); err != nil {
			return err
		}

		now := NewTimestamp(time.Now())
		copyEntry := e
		copyEntry.Created = now
		copyEntry.Updated = now
		copyEntry.UpdatedBy = ident.Device()

		destID := NewEntryID()
		if err := dest.WriteEntry(destID, copyEntry); err != nil {
			return err
		}
		if _, err := gitrepo.CommitAll(dest.Path, destID.String()); err != nil {
			return exitcode.Wrap(exitcode.Internal,
				fmt.Errorf("gage: committing %s into %q: %w", destID, dest.Name, err))
		}
		dest.pushAfterWrite(ident.warnTo())
		res.DestID = destID
		res.Entry = copyEntry

		if src.onMoveDestCommitted != nil {
			src.onMoveDestCommitted()
		}

		if !remove {
			return nil
		}

		// Only reached once the destination's write-and-commit sequence
		// has already returned without error — see the M11 plan's
		// "verify" decision. A crash before this point leaves the source
		// untouched; a crash after the destination commit but before this
		// removal's own commit leaves a recoverable duplicate, never a
		// loss.
		if err := os.Remove(src.entryPath(srcID)); err != nil {
			return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: removing entry %s: %w", srcID, err))
		}
		if _, err := gitrepo.CommitAll(src.Path, srcID.String()); err != nil {
			return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: committing removal of %s: %w", srcID, err))
		}
		src.pushAfterWrite(ident.warnTo())
		return nil
	})
	if err != nil {
		return MoveResult{}, err
	}
	return res, nil
}

// withTwoVaultLocks acquires both a and b's advisory write locks for the
// duration of fn, in a fixed order: sorted by vault *id*, the same
// identifier LockFilePath keys each lock file by, rather than by local
// name or filesystem path — see the M11 plan's "Lock ordering across two
// vaults". mv/cp are its only callers; every other mutating method still
// goes through the single-vault withWriteLock/withEncryptingWrite.
//
// The ordering key follows the lock file, which A20 re-keyed from the
// name to the id: ordering by a name that no longer identifies the lock
// would let two processes request the same pair of locks in opposite
// sequences, which is the deadlock this fixed order exists to rule out.
//
// Both locks are held for fn's entire duration, not acquired and released
// one at a time — that's what makes moveOrCopy's crash-safety property
// hold: nothing else can write to either vault while a share is
// mid-flight. Because the order is fixed by name rather than by argument
// order, two concurrent mv's between the same pair of vaults, run in
// opposite directions, always request the two locks in the same
// sequence, so neither can ever hold one while waiting on the other.
func withTwoVaultLocks(a, b *Vault, fn func() error) error {
	first, second := a, b
	if second.ID < first.ID {
		first, second = second, first
	}

	firstLock, err := acquireVaultLock(first.ID, vaultLockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = firstLock.Release() }()

	secondLock, err := acquireVaultLock(second.ID, vaultLockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = secondLock.Release() }()

	return fn()
}
