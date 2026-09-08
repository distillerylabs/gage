package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/vaultconfig"
)

// newVaultCommand groups the vault-generic lifecycle commands that
// don't create a vault (that's init/clone) but list, inspect, forget, or
// re-point one already registered. See "Vault lifecycle" in the design
// doc.
func newVaultCommand(app *App) *cobra.Command {
	parent := &cobra.Command{
		Use:   "vault",
		Short: "Manage registered vaults",
	}
	parent.AddCommand(newVaultListCommand(app))
	parent.AddCommand(newVaultInfoCommand(app))
	parent.AddCommand(newVaultRemoveCommand(app))
	parent.AddCommand(newVaultSetDefaultCommand(app))
	return parent
}

// vaultFromEntry builds the *gage.Vault a command operates on from one
// registration in global config, and is the single place that check
// lives so no command can forget it.
//
// The id comes from global config rather than from the vault's own
// committed file, which is what A20 records it here for: `vault remove`
// has to know which identities directory a vault owns *after* its files
// may be gone. But wherever the vault's own config is readable, the two
// recorded ids are compared and a mismatch is refused — a registration
// and the vault it points at disagreeing is an ordinary thing (a vault
// re-created at the same path, a registration repointed by hand, a
// restored backup), and continuing would look up this device's identity
// under one vault's id while reading the recipient list of another.
func vaultFromEntry(name string, entry config.VaultEntry) (*gage.Vault, error) {
	if err := checkRegistrationPointsAtThisVault(name, entry); err != nil {
		return nil, err
	}
	return &gage.Vault{Name: name, ID: entry.ID, Path: entry.Path}, nil
}

// checkRegistrationPointsAtThisVault compares global config's recorded
// id against the id the vault at that path actually carries.
//
// A vault whose files cannot be read at all is not a mismatch — it is
// the case global config's copy exists to cover, and turning it into a
// failure would break `vault remove` for exactly the vaults it most
// needs to work on. A vault whose config is *readable but unusable* is a
// different thing: an unrecognized format_version or an unparseable id
// is reported as itself, because "re-create this vault" is far better
// advice than the empty-id path failure that would otherwise surface
// several steps later.
func checkRegistrationPointsAtThisVault(name string, entry config.VaultEntry) error {
	vc, err := vaultconfig.Read(filepath.Join(entry.Path, ".gage", "config.toml"))
	if err != nil {
		if errors.Is(err, vaultconfig.ErrUnsupportedFormatVersion) || errors.Is(err, vaultconfig.ErrInvalidVaultID) {
			return exitcode.Wrap(exitcode.Conflict, err)
		}
		return nil
	}
	if vc.Vault.ID == entry.ID {
		return nil
	}
	return exitcode.Newf(exitcode.Conflict,
		"gage: the registration for %q points at a different vault than the one at %s "+
			"(registered id %s, vault id %s); re-register it with `gage vault remove %s` and `gage clone`",
		name, entry.Path, quotedOrNone(entry.ID), vc.Vault.ID, name)
}

// quotedOrNone renders a recorded id for the mismatch message, naming
// the absent case rather than printing an empty pair of quotes: a
// registration written before A20 has no id at all, and "none recorded"
// is what tells its owner why re-registering is the answer.
func quotedOrNone(id string) string {
	if id == "" {
		return "none recorded"
	}
	return id
}

// readVaultConfig reads a vault's own .gage/config.toml and maps its
// typed refusals onto the exit-code taxonomy deliberately.
//
// All three are anticipated, well-understood states a human has to
// resolve — "this vault's format_version isn't the one this build
// speaks", and the two untrusted-input refusals for a committed field
// that becomes a path component: a device name and, since A20, the vault
// id (any git-writer can edit that file; see Q-DEVICE-NAME). So none of
// them belongs in Internal, which the taxonomy reserves for the
// unexpected. A script that gets Conflict can tell "upgrade gage / fix
// this vault" apart from "gage hit a bug", which a blanket Internal
// would flatten. Everything else (a missing file, an I/O error,
// malformed TOML) stays Internal.
func readVaultConfig(vaultPath string) (vaultconfig.File, error) {
	vc, err := vaultconfig.Read(filepath.Join(vaultPath, ".gage", "config.toml"))
	if err != nil {
		if errors.Is(err, vaultconfig.ErrUnsupportedFormatVersion) ||
			errors.Is(err, vaultconfig.ErrInvalidDeviceName) ||
			errors.Is(err, vaultconfig.ErrInvalidVaultID) {
			return vaultconfig.File{}, exitcode.Wrap(exitcode.Conflict, err)
		}
		return vaultconfig.File{}, exitcode.Wrap(exitcode.Internal, err)
	}
	return vc, nil
}

func newVaultListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: commandShort("vault list"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := readGlobalConfig()
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			names := make([]string, 0, len(g.Vaults))
			for name := range g.Vaults {
				names = append(names, name)
			}
			sort.Strings(names)

			lines := make([]string, 0, len(names))
			for _, name := range names {
				marker := "  "
				if name == g.Current {
					marker = "* "
				}
				lines = append(lines, marker+name)
			}
			writeOut(app.Out, lines)
			return nil
		},
	}
}

func newVaultInfoCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "info [name]",
		Short: commandShort("vault info"),
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := readGlobalConfig()
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}

			name := g.Current
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" {
				return exitcode.New(exitcode.Usage, "gage: no vault name given and no current vault is set")
			}

			entry, ok := g.Vaults[name]
			if !ok {
				return exitcode.Newf(exitcode.NotFound, "gage: no such vault %q", name)
			}
			// Resolved like any other command's target, so a
			// registration pointing at a different vault than the one on
			// disk is refused here too rather than being described.
			if _, err := vaultFromEntry(name, entry); err != nil {
				return err
			}

			vc, err := readVaultConfig(entry.Path)
			if err != nil {
				return err
			}

			lines := []string{
				fmt.Sprintf("name: %s", name),
				// The vault's own id, read from its committed config
				// rather than echoed back from the registration —
				// which is what makes this the place to check that the
				// two agree (they are compared in vaultFromEntry, and
				// a mismatch never gets this far).
				fmt.Sprintf("id: %s", vc.Vault.ID),
				fmt.Sprintf("type: %s", vc.Vault.Type),
				fmt.Sprintf("method: %s", vc.Method.Default),
				fmt.Sprintf("recipients: %d", len(vc.Recipients)),
			}

			// Git-type-specific detail — see "Git-specific commands":
			// remote and clean/dirty state have no meaning for a future
			// non-git vault type.
			if vc.Vault.Type == gage.TypeGit {
				remote, err := gitrepo.RemoteURL(entry.Path)
				if err != nil {
					return exitcode.Wrap(exitcode.Internal, err)
				}
				if remote == "" {
					remote = "(none)"
				}
				lines = append(lines, fmt.Sprintf("remote: %s", remote))

				clean, err := gitrepo.IsClean(entry.Path)
				if err != nil {
					return exitcode.Wrap(exitcode.Internal, err)
				}
				status := "dirty"
				if clean {
					status = "clean"
				}
				lines = append(lines, fmt.Sprintf("status: %s", status))
			}

			writeOut(app.Out, lines)
			return nil
		},
	}
}

func newVaultRemoveCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: commandShort("vault remove"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			g, err := readGlobalConfig()
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			entry, ok := g.Vaults[name]
			if !ok {
				return exitcode.Newf(exitcode.NotFound, "gage: no such vault %q", name)
			}

			// Forgets the registration only — the underlying git repo
			// and its files are never touched. See "Vault lifecycle".
			delete(g.Vaults, name)
			if g.Current == name {
				g.Current = ""
			}
			if err := writeGlobalConfig(g); err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}

			// Local memory of the vault goes with the registration.
			// Keeping M10's trust cache would let a later re-add
			// silently inherit an approval for a recipient list nobody
			// looked at in the interval, which is the one thing that
			// cache exists to prevent; dropping it makes a re-add a
			// first use — the documented weaker-but-honest bootstrap.
			// It touches nothing inside the vault, which is what `vault
			// remove` already promises.
			//
			// Keyed by the registration's own recorded id, not by a
			// resolved vault: `vault remove` must keep working when the
			// vault's files are gone, which is the situation global
			// config records the id for. An id it never recorded (a
			// registration older than A20) leaves nothing to remove and
			// is not a reason to fail — the whole command is "forget
			// this registration", and that must always be possible.
			if entry.ID != "" {
				if err := gage.RemoveTrustCache(entry.ID); err != nil {
					return exitcode.Wrap(exitcode.Internal, err)
				}
			}

			removeOrphanedIdentity(app, name, entry)
			return nil
		},
	}
}

// removeOrphanedIdentity offers to delete this device's local identity
// file for a just-removed vault when the vault's own recipient list, read
// fresh from the store still sitting on disk, no longer contains this
// device's public key. A key missing from that list has nothing left the
// identity file could be needed for.
//
// It compares *public keys*, never device names. A name is a label the
// vault happens to store, not proof of which key it refers to, and here
// that distinction gates an irreversible deletion of the only copy of a
// private key. The name comparison this replaced was wrong in both
// directions: it would delete an active recipient's key whenever the
// vault labelled it differently from local config (which `recipient
// approve --device` produces deliberately), and keep a genuinely stale
// key whenever some other device had since taken its old label. See
// Q-ORPHAN-BY-NAME.
//
// And it never deletes without asking. That is the load-bearing half:
// comparing keys makes the *suggestion* accurate, but a record that has
// gone stale — someone swapped the .age file by hand, an enrollment
// request is still awaiting approval — would make it confidently wrong,
// which is worse than uncertain. A prompt turns any wrong answer into a
// wrong suggestion a human reads with the path in front of them, and
// Confirm defaults to no, so a scripted `vault remove` keeps the file by
// construction.
//
// Every other outcome keeps the file and says why. If the recipient list
// can't be read (moved or deleted store, remote-only, filesystem
// trouble), or global config records no public key for this device, or
// the registration turns out to point at a different vault than the one
// on disk, guessing would silently strand access to that vault's
// ciphertext forever. `vault remove` only ever forgets local
// registration; it never touches the store, so those cases are left for
// the human to resolve and are reported rather than acted on.
func removeOrphanedIdentity(app *App, name string, entry config.VaultEntry) {
	if entry.Device == "" || entry.ID == "" {
		return
	}
	has, err := gage.HasIdentity(entry.ID, entry.Device)
	if err != nil || !has {
		return
	}
	idPath, err := gage.IdentityFilePath(entry.ID, entry.Device)
	if err != nil {
		return
	}

	keep := func(why string) {
		writeOut(app.Err, []string{fmt.Sprintf(
			"gage: kept the local identity file for %q (%s) — %s.", name, idPath, why)})
	}

	if entry.Pubkey == "" {
		// Older registration, hand-edited config, or a clone that never
		// ran `identity add` — there is no key to compare, so there is
		// no question this can answer. Say so rather than guessing.
		keep(fmt.Sprintf(
			"gage doesn't know which public key %s holds here, so it can't tell whether the key is still a recipient. "+
				"Delete the file yourself once you're sure it's safe", entry.Device))
		return
	}

	// A registration pointing at a different vault than the one on disk
	// is exactly the shape of the bug A20 exists to kill: the identity
	// would be looked up under one vault's id and judged against the
	// other's recipient list. Refusing to act on it is the whole point.
	if err := checkRegistrationPointsAtThisVault(name, entry); err != nil {
		keep(errorClause(err))
		return
	}

	v := &gage.Vault{Name: name, ID: entry.ID, Path: entry.Path}
	recipients, err := v.Recipients()
	if err != nil {
		keep(fmt.Sprintf("could not confirm %s's key is no longer a recipient (%v)", entry.Device, err))
		return
	}
	for _, r := range recipients {
		if r.Pubkey == entry.Pubkey {
			keep(fmt.Sprintf(
				"the key it holds is still listed as a recipient there (as %q). "+
					"Remove it as a recipient first (from another device, with --reencrypt) if you want the local identity file gone too",
				r.Device))
			return
		}
	}

	ok, err := app.Prompter.Confirm(fmt.Sprintf(
		"%s's key is no longer a recipient of %q. Delete this device's identity file for it?\n"+
			"  %s\n"+
			"This is the only copy of that private key, and deleting it cannot be undone.",
		entry.Device, name, idPath))
	if err != nil || !ok {
		keep("not confirmed")
		return
	}

	if err := gage.RemoveIdentity(entry.ID, entry.Device); err != nil {
		writeOut(app.Err, []string{fmt.Sprintf("gage: could not remove the local identity file for %q: %v", name, err)})
	}
}

func newVaultSetDefaultCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "set-default <name>",
		Short: commandShort("vault set-default"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			g, err := readGlobalConfig()
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			if _, ok := g.Vaults[name]; !ok {
				return exitcode.Newf(exitcode.NotFound, "gage: no such vault %q", name)
			}
			g.Current = name
			if err := writeGlobalConfig(g); err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			return nil
		},
	}
}

// vaultWithoutUnlocking resolves which vault a command targets and
// returns it without asking for a passphrase — the counterpart to
// withUnlockedVault for the commands that genuinely need no key.
//
// Sync uses it because it unlocks lazily; `identity add/list` and
// `recipient list/verify` use it because they operate on plaintext
// control-plane data and must never prompt at all. `recipient verify`'s
// "needs no unlock, safe to run in CI" claim is exactly this: there is
// no Identity in reach to unlock with.
func vaultWithoutUnlocking(app *App, use string) (*gage.Vault, error) {
	if app.Session != nil {
		return app.Session.VaultWithoutUnlocking(use)
	}
	name, entry, err := resolveVaultEntry(use)
	if err != nil {
		return nil, err
	}
	return vaultFromEntry(name, entry)
}
