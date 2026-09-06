package gage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/devicename"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// ErrDeviceNameTaken is AddIdentity refusing a device name the vault
// already lists as a recipient.
//
// The check is against the *vault's* list rather than against local
// files, and that is what makes the case the design actually worries
// about fail: two machines whose normalized hostnames collide. The
// second machine holds no identity file of its own, so a local-only
// check would let it register a name the vault already means someone
// else by — and the first machine's next `recipient add` would then be
// ambiguous about which device it was authorizing.
var ErrDeviceNameTaken = errors.New("gage: this vault already has a recipient with that device name")

// LocalIdentity is one wrapped identity file this machine holds for one
// vault. It says nothing about whether the vault still lists that key as
// a recipient — that is what `recipient list` answers — only that the
// key is here and openable.
type LocalIdentity struct {
	Device string
	Path   string
	// Current reports whether global config's [vaults.<name>].device
	// points at this identity: the one an unlock on this machine would
	// use right now.
	Current bool
}

// AddIdentity registers an additional device for this vault: a fresh
// keypair whose private half is wrapped with a passphrase obtained
// through p and written to $GAGE_DATA/identities/<vault>/<device>.age.
// It returns the public half — the only part that ever leaves this
// machine, and the string someone pastes into `gage recipient add` from
// a device that can already read the vault.
//
// It lives on Vault because the collision check is vault-side: the name
// must not already label a [[recipients]] entry in .gage/config.toml.
// The local half — recording device/method in global config — stays in
// cmd/gage, exactly the split `gage init` already uses.
//
// Nothing here touches the vault's recipient list. Publishing the new
// key is `recipient add`'s job, run from a device that already holds
// access, and keeping the two apart is what makes the lost-identity
// recovery story work at all: the device with the lost key has no way to
// authorize itself. See "`identity` vs `recipient` stay separate".
func (v *Vault) AddIdentity(device string, p Prompter) (string, error) {
	if !devicename.Valid(device) {
		return "", exitcode.Newf(exitcode.Usage, "gage: device name %q is invalid", device)
	}

	current, err := v.Recipients()
	if err != nil {
		return "", err
	}
	for _, r := range current {
		if r.Device == device {
			return "", exitcode.Wrap(exitcode.Conflict,
				fmt.Errorf("%w: %q already names a recipient of vault %q", ErrDeviceNameTaken, device, v.Name))
		}
	}

	// CreateIdentity refuses to overwrite an existing file on its own,
	// which is the second half of the same guarantee: the collision
	// check above catches a name the vault knows, and that refusal
	// catches a name only this machine knows. Either way an existing
	// private key is never replaced — it is the only copy there is.
	return CreateIdentity(v.Name, device, p)
}

// ListIdentities reports the wrapped identities this machine holds for a
// vault, sorted by device name.
//
// It reads the local identities directory rather than the vault's
// recipient list, so it answers "what can this machine open" rather than
// "who does this vault trust". A device holding nothing for the vault
// reports nothing rather than failing — that is the normal state right
// after a clone, before `gage identity add`.
func ListIdentities(vault string) ([]LocalIdentity, error) {
	dir, err := IdentitiesDir(vault)
	if err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, exitcode.Wrap(exitcode.Internal, err)
	}

	registered := registeredDevice(vault)

	out := make([]LocalIdentity, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		device, ok := strings.CutSuffix(de.Name(), ".age")
		if !ok {
			continue
		}
		// A name that isn't a valid device name can't have been written
		// by gage, and IdentityFilePath would refuse to reconstruct its
		// path anyway. Skipped rather than reported, the same way
		// EntryIDs skips a stray file in entries/.
		if !devicename.Valid(device) {
			continue
		}
		out = append(out, LocalIdentity{
			Device:  device,
			Path:    filepath.Join(dir, de.Name()),
			Current: device == registered,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out, nil
}

// registeredDevice reports which device global config currently points
// at for a vault, or "" if it points at none.
//
// Every failure reading global config comes back as "": a listing of
// what is on disk should not fail because the registry is missing or
// unreadable, and the only thing lost is the "current" marker.
func registeredDevice(vault string) string {
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		return ""
	}
	g, err := config.Read(filepath.Join(dir, "config.toml"))
	if err != nil {
		return ""
	}
	return g.Vaults[vault].Device
}
