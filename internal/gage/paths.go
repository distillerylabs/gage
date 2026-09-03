package gage

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/denmark/gage/internal/gage/devicename"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// ErrUnsafePathComponent is returned when a vault or device name that
// would become a path component fails validation. Both values reach this
// package from files a git-writer (for a vault's committed config) or a
// local editor (for global config) can edit, and a device name reading
// "../../../../etc/cron.d/x" must be rejected rather than helpfully
// resolved — see Q-DEVICE-NAME and "Device names are validated before
// they're ever used as a path".
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

// IdentitiesDir returns $GAGE_DATA/identities/<vault>, the 0700 directory
// holding this device's wrapped identity file for one vault. It is a
// sibling of $GAGE_DATA/vaults/, never a child of one — see "Local
// identity storage" in the design doc for why nesting it inside a vault's
// own git tree would be a much weaker guarantee.
func IdentitiesDir(vault string) (string, error) {
	if err := checkPathComponent("vault name", vault); err != nil {
		return "", err
	}
	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "identities", vault), nil
}

// IdentityFilePath returns the path to a device's wrapped identity file
// for a vault: $GAGE_DATA/identities/<vault>/<device>.age.
//
// It validates both components itself rather than documenting that
// callers must. That inversion is the point of Q-DEVICE-NAME's rule: "no
// path is constructed from an unvalidated device name" is only true if
// the construction site enforces it, since a caller that forgets is
// exactly the bug the rule exists to prevent.
func IdentityFilePath(vault, device string) (string, error) {
	if !devicename.Valid(device) {
		return "", exitcode.Newf(exitcode.LockedOrAuth,
			"gage: device name %q is invalid: %w", device, ErrUnsafePathComponent)
	}
	dir, err := IdentitiesDir(vault)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, device+".age"), nil
}

// LockFilePath returns a vault's advisory write-lock file:
// $GAGE_STATE/locks/<vault>.lock — see vaultlock and "Concurrent
// processes and the vault lock" in the design doc. It lives under the
// state root, not the vault's own git working tree or $GAGE_DATA: a lock
// file is ephemeral, machine-local coordination, not something to commit,
// sync, or back up.
func LockFilePath(vault string) (string, error) {
	if err := checkPathComponent("vault name", vault); err != nil {
		return "", err
	}
	stateDir, err := xdgpaths.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "locks", vault+".lock"), nil
}

// checkPathComponent rejects anything that wouldn't stay a single path
// component. It is deliberately more permissive than devicename.Valid:
// device names are gage's own normalized creation and can be held to a
// strict allowlist, while a vault name is a label the user chose and is
// not otherwise constrained (a vault called "Work Stuff" is legal). What
// matters for path safety is narrower than an allowlist — that the name
// can't traverse, can't be empty, and can't be a separator on any
// platform gage builds for.
func checkPathComponent(what, name string) error {
	bad := ""
	switch {
	case name == "":
		bad = "it is empty"
	case name == "." || name == "..":
		bad = "it is a relative path element"
	case strings.ContainsAny(name, `/\`):
		bad = "it contains a path separator"
	// Rejected on every platform, not just Windows: an identity file
	// written on macOS with a colon in its name would be unreadable on
	// the Windows machine syncing the same vault, and a name that only
	// works on some of a user's devices is a worse outcome than a name
	// refused everywhere.
	case strings.ContainsRune(name, ':'):
		bad = "it contains a drive or stream separator"
	case strings.ContainsRune(name, 0):
		bad = "it contains a NUL byte"
	// Catches everything shaped like a path that the cases above don't
	// name individually — "./x", "x/.", a trailing separator — by
	// requiring the name to already be its own clean form.
	case name != filepath.Clean(name):
		bad = "it is not a single, already-clean path component"
	}
	if bad == "" {
		return nil
	}
	return exitcode.Newf(exitcode.LockedOrAuth,
		"gage: %s %q cannot be used as a path component: %s: %w", what, name, bad, ErrUnsafePathComponent)
}
