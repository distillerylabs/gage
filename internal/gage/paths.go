package gage

import (
	"errors"
	"path/filepath"

	"github.com/denmark/gage/internal/gage/devicename"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/vaultconfig"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// ErrUnsafePathComponent is returned when a device name or a vault id
// that would become a path component fails validation. Both values reach
// this package from a vault's committed config, which any git-writer can
// edit, and a device name reading "../../../../etc/cron.d/x" must be
// rejected rather than helpfully resolved — see Q-DEVICE-NAME and
// "Device names — and the vault id — are validated before they're ever
// used as a path".
//
// The vault *name* is deliberately absent from that list now. After A20
// nothing under $GAGE_DATA or $GAGE_STATE is addressed by it, so it is a
// label for humans and never a path component.
var ErrUnsafePathComponent = errors.New("gage: unsafe path component")

// VaultsDir is where actual vaults (git repos, today) live: a subdirectory
// of $GAGE_DATA.
func VaultsDir() (string, error) {
	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "vaults"), nil
}

// IdentitiesDir returns $GAGE_DATA/identities/<vault-id>, the 0700
// directory holding this device's wrapped identity file for one vault. It
// is a sibling of $GAGE_DATA/vaults/, never a child of one — see "Local
// identity storage" in the design doc for why nesting it inside a vault's
// own git tree would be a much weaker guarantee.
//
// Keyed by the vault's id, never by its local registration name: a local
// name is chosen at clone time and tied to the vault by nothing, so two
// unrelated vaults registered under one name in sequence would share this
// directory — and `vault remove` would then delete a key belonging to a
// vault it is not removing. See A20.
func IdentitiesDir(vaultID string) (string, error) {
	if err := checkVaultID(vaultID); err != nil {
		return "", err
	}
	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "identities", vaultID), nil
}

// IdentityFilePath returns the path to a device's wrapped identity file
// for a vault: $GAGE_DATA/identities/<vault-id>/<device>.age.
//
// It validates both components itself rather than documenting that
// callers must. That inversion is the point of Q-DEVICE-NAME's rule: "no
// path is constructed from an unvalidated device name" is only true if
// the construction site enforces it, since a caller that forgets is
// exactly the bug the rule exists to prevent. A20 puts the vault id
// under the same rule, and for the same reason: it too arrives from a
// committed file any git-writer can edit.
func IdentityFilePath(vaultID, device string) (string, error) {
	if !devicename.Valid(device) {
		return "", exitcode.Newf(exitcode.LockedOrAuth,
			"gage: device name %q is invalid: %w", device, ErrUnsafePathComponent)
	}
	dir, err := IdentitiesDir(vaultID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, device+".age"), nil
}

// LockFilePath returns a vault's advisory write-lock file:
// $GAGE_STATE/locks/<vault-id>.lock — see vaultlock and "Concurrent
// processes and the vault lock" in the design doc. It lives under the
// state root, not the vault's own git working tree or $GAGE_DATA: a lock
// file is ephemeral, machine-local coordination, not something to commit,
// sync, or back up.
//
// Keyed by the id, like the other two, because the lock protects a
// *repository* and the id is the thing that identifies one. Keying it by
// the local name would mean two registrations of a single repository —
// a state this milestone deliberately supports — took two different
// locks, so both could write the same working tree at once and the
// advisory lock would stop being mutual exclusion at precisely the
// moment it is needed.
func LockFilePath(vaultID string) (string, error) {
	if err := checkVaultID(vaultID); err != nil {
		return "", err
	}
	stateDir, err := xdgpaths.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "locks", vaultID+".lock"), nil
}

// checkVaultID refuses an id that isn't the canonical UUID form gage
// mints, before any path is built from it.
//
// It can afford to be far stricter than the name-shaped check it
// replaced: an id is gage's own assigned value with exactly one legal
// spelling, so there is no user-chosen label to accommodate. Everything
// it accepts is 36 characters of hex and hyphens, which cannot traverse,
// cannot contain a separator on any platform gage builds for, and cannot
// be empty.
func checkVaultID(id string) error {
	if vaultconfig.ValidID(id) {
		return nil
	}
	return exitcode.Newf(exitcode.LockedOrAuth,
		"gage: vault id %q is not a valid id: %w", id, ErrUnsafePathComponent)
}
