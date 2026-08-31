package gage

import (
	"path/filepath"

	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// VaultsDir is where actual vaults (git repos, today) live: a subdirectory
// of $GAGE_DATA.
func VaultsDir() (string, error) {
	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "vaults"), nil
}

// IdentityFilePath returns the path to a device's wrapped identity file for
// a vault: $GAGE_DATA/identities/<vault>/<device>.age. This directory is a
// sibling of $GAGE_DATA/vaults/, never a child of one — see "Local identity
// storage" in the design doc for why nesting it inside a vault's own git
// tree would be a much weaker guarantee.
//
// This is a pure path helper; it does not validate vault or device against
// the filesystem-safe character allowlist. Callers that source either value
// from a vault's own (git-writable) config must validate first — see
// Q-DEVICE-NAME.
func IdentityFilePath(vault, device string) (string, error) {
	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "identities", vault, device+".age"), nil
}
