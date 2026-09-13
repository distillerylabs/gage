// Package vaultconfig reads and writes a vault's own .gage/config.toml —
// committed, plaintext control-plane data naming the vault's type,
// method default, and recipients. See "On-disk layout" in the design
// doc. Unlike the global config, every field this file carries reaches
// the local filesystem eventually (a recipient's device name becomes a
// path component), so Read validates before returning rather than
// trusting a file any git-writer could have edited — see Q-DEVICE-NAME.
package vaultconfig

import (
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"

	"github.com/denmark/gage/internal/gage/atomicfile"
	"github.com/denmark/gage/internal/gage/devicename"
)

// CurrentFormatVersion is the only format_version this build of gage
// understands. Bumped when .gage/config.toml's shape changes in a way
// old readers can't safely ignore.
//
// 2 added [vault].id (A20). A v1 vault carries no id at all, so there is
// nothing for this build to key that vault's identities directory, trust
// cache, or lock file by — which is why the refusal below is not a
// formality it could parse past.
const CurrentFormatVersion = 2

// ErrUnsupportedFormatVersion is returned by Read when a vault's
// format_version isn't CurrentFormatVersion. gage refuses to
// best-effort parse a vault format it doesn't recognize — see "On-disk
// layout"'s "format_version is enforced, not decorative."
var ErrUnsupportedFormatVersion = errors.New("vaultconfig: unsupported format_version")

// ErrInvalidVaultID is returned by Read when [vault].id is absent or
// isn't a canonical UUID. Like the device names below it, the id becomes
// a filesystem path component and arrives from a committed,
// git-writable file, so it is refused rather than coerced — see A20 and
// Q-DEVICE-NAME.
var ErrInvalidVaultID = errors.New("vaultconfig: invalid vault id")

// ErrInvalidDeviceName is returned by Read when a [[recipients]] entry's
// device name fails the filesystem-safe character allowlist — see
// Q-DEVICE-NAME. The file is committed and git-writable by any
// recipient, so this field is untrusted input.
var ErrInvalidDeviceName = errors.New("vaultconfig: invalid recipient device name")

// VaultMeta is the [vault] table.
type VaultMeta struct {
	Name string `toml:"name"`
	// ID is the vault's own identity: a UUID minted once by `gage init`
	// and never changed. Because it lives in the committed config it
	// clones with the vault, so every clone agrees on it however it was
	// named locally — which is what makes it, rather than the local
	// registration name, the safe key for this device's identities
	// directory, trust cache, and lock file. See A20.
	ID            string `toml:"id"`
	Type          string `toml:"type"`
	FormatVersion int    `toml:"format_version"`
	Created       string `toml:"created"`
}

// Method is the [method] table: a default for devices joining this
// vault, not a vault-wide constraint. See "Decryption methods are
// per-device" and Q-METHOD-SCOPE.
type Method struct {
	Default string `toml:"default"`
	Plugin  string `toml:"plugin,omitempty"`
}

// Recipient is one [[recipients]] entry. Deliberately carries only
// Device and Pubkey — no per-recipient method field, which would tell
// anyone with read access which recipient is the softest target. See
// Q-METHOD-SCOPE.
type Recipient struct {
	Device string `toml:"device"`
	Pubkey string `toml:"pubkey"`
}

// File is the full shape of .gage/config.toml.
type File struct {
	Vault      VaultMeta   `toml:"vault"`
	Method     Method      `toml:"method"`
	Recipients []Recipient `toml:"recipients"`
}

// Read parses a vault's .gage/config.toml. format_version is checked
// first, before any other field is acted on: an unrecognized version
// fails outright rather than being parsed best-effort. Every recipient's
// device name is then validated against the filesystem-safe allowlist,
// since it's committed, git-writable data that eventually reaches the
// filesystem as a path component.
func Read(path string) (File, error) {
	// #nosec G304 -- path is a vault-relative location the caller
	// resolves (<vault>/.gage/config.toml), not attacker input; the
	// untrusted part of this file is its *contents*, validated below.
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := toml.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("vaultconfig: parsing %s: %w", path, err)
	}

	if f.Vault.FormatVersion != CurrentFormatVersion {
		return File{}, formatVersionError(path, f.Vault.FormatVersion)
	}

	if !ValidID(f.Vault.ID) {
		return File{}, fmt.Errorf("vaultconfig: %s: vault id %q is not a valid id: %w", path, f.Vault.ID, ErrInvalidVaultID)
	}

	for _, r := range f.Recipients {
		if !devicename.Valid(r.Device) {
			return File{}, fmt.Errorf("vaultconfig: %s: recipient device name %q is invalid: %w", path, r.Device, ErrInvalidDeviceName)
		}
	}

	return f, nil
}

// formatVersionError says which direction the mismatch runs in, because
// the two directions need opposite advice. A vault written by a *newer*
// gage needs a newer binary. A vault written by an older one cannot be
// upgraded in place — A20's schema change added [vault].id, and there is
// no id to invent for a vault that never had one — so the migration is
// to re-create it, which is only an acceptable answer because nothing
// has shipped (see A20's "this is the cheapest it will ever be").
func formatVersionError(path string, found int) error {
	if found > CurrentFormatVersion {
		return fmt.Errorf("vaultconfig: %s has format_version %d, this gage understands %d — upgrade gage: %w",
			path, found, CurrentFormatVersion, ErrUnsupportedFormatVersion)
	}
	return fmt.Errorf("vaultconfig: %s has format_version %d, this gage understands %d — "+
		"re-create this vault (`make reset-local-state` clears this machine's vaults and identities first): %w",
		path, found, CurrentFormatVersion, ErrUnsupportedFormatVersion)
}

// Write atomically writes f to path through the shared atomic-write
// helper, so an interrupted write never leaves a truncated
// .gage/config.toml behind.
func Write(path string, f File) error {
	data, err := toml.Marshal(f)
	if err != nil {
		return fmt.Errorf("vaultconfig: encoding %s: %w", path, err)
	}
	if err := atomicfile.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("vaultconfig: writing %s: %w", path, err)
	}
	return nil
}
