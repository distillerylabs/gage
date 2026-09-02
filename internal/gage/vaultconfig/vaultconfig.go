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
const CurrentFormatVersion = 1

// ErrUnsupportedFormatVersion is returned by Read when a vault's
// format_version isn't CurrentFormatVersion. gage refuses to
// best-effort parse a vault format it doesn't recognize — see "On-disk
// layout"'s "format_version is enforced, not decorative."
var ErrUnsupportedFormatVersion = errors.New("vaultconfig: unsupported format_version")

// ErrInvalidDeviceName is returned by Read when a [[recipients]] entry's
// device name fails the filesystem-safe character allowlist — see
// Q-DEVICE-NAME. The file is committed and git-writable by any
// recipient, so this field is untrusted input.
var ErrInvalidDeviceName = errors.New("vaultconfig: invalid recipient device name")

// VaultMeta is the [vault] table.
type VaultMeta struct {
	Name          string `toml:"name"`
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
		return File{}, fmt.Errorf("vaultconfig: %s has format_version %d, this gage understands %d — upgrade gage: %w",
			path, f.Vault.FormatVersion, CurrentFormatVersion, ErrUnsupportedFormatVersion)
	}

	for _, r := range f.Recipients {
		if !devicename.Valid(r.Device) {
			return File{}, fmt.Errorf("vaultconfig: %s: recipient device name %q is invalid: %w", path, r.Device, ErrInvalidDeviceName)
		}
	}

	return f, nil
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
