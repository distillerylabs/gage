package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// newRecipientCommand groups the vault-side access verbs: who this vault
// is encrypted to, and the all-or-nothing re-encryption that changing
// that list can carry with it. See "Recipient / access management".
func newRecipientCommand(app *App) *cobra.Command {
	parent := &cobra.Command{
		Use:   "recipient",
		Short: "Manage who can read this vault",
	}
	parent.AddCommand(newRecipientAddCommand(app))
	parent.AddCommand(newRecipientRemoveCommand(app))
	parent.AddCommand(newRecipientListCommand(app))
	parent.AddCommand(newRecipientVerifyCommand(app))
	parent.AddCommand(newRecipientPendingCommand(app))
	parent.AddCommand(newRecipientApproveCommand(app))
	parent.AddCommand(newRecipientDenyCommand(app))
	return parent
}

// newRecipientAddCommand builds `gage recipient add`, which takes no
// --reencrypt flag: adding a recipient always re-encrypts (A19). The
// flag is not accepted-and-ignored, because a script that passed it was
// asking for a behavior that used to be optional and is now the only
// one — silently accepting it would let the command line and the
// operation quietly drift apart. Cobra rejects the unknown flag as a
// usage error, which is the loud version.
func newRecipientAddCommand(app *App) *cobra.Command {
	var (
		useFlag    string
		deviceFlag string
	)

	cmd := &cobra.Command{
		Use:   "add <pubkey>",
		Short: commandShort("recipient add"),
		Long: commandShort("recipient add") + ".\n\n" +
			"Every entry is decrypted and rewritten to the new list, so the new recipient\n" +
			"can read the vault's whole history — there is no way to add a recipient who\n" +
			"can read only part of it. All of it lands in a single commit or none of it\n" +
			"does. Adding a recipient therefore requires being able to read every entry\n" +
			"yourself; run it from a device that can.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pubkey := args[0]
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				change, err := v.AddRecipient(deviceFlag, pubkey, ident)
				if err != nil {
					return err
				}
				writeOut(app.Out, recipientChangeLines("added", change))
				return nil
			})
		},
	}

	addUseFlag(cmd, &useFlag)
	cmd.Flags().StringVar(&deviceFlag, "device", "", "the device name to label this key with")
	// A bare public key says nothing about whose it is, and the label is
	// what every later `recipient remove`/`list` addresses it by, so
	// there is no sensible default to invent.
	_ = cmd.MarkFlagRequired("device")
	return cmd
}

func newRecipientRemoveCommand(app *App) *cobra.Command {
	var (
		useFlag       string
		reencryptFlag bool
	)

	cmd := &cobra.Command{
		Use:   "remove <pubkey-or-name>",
		Short: commandShort("recipient remove"),
		Long: commandShort("recipient remove") + ".\n\n" +
			"--reencrypt is required: without it the removed key still opens every entry\n" +
			"that already exists, so the revocation would only look like one. Removing\n" +
			"this device's own key is allowed but asks first, and removing the last\n" +
			"recipient is refused outright.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Checked here as well as in the library so the refusal
			// arrives before the passphrase prompt: being asked to
			// unlock a vault and only then told the command line was
			// wrong is the wrong order to fail in.
			if !reencryptFlag {
				return exitcode.Wrap(exitcode.Usage, fmt.Errorf(
					"%w, which rewrites every entry so the removed key stops opening them", gage.ErrReencryptRequired))
			}
			query := args[0]
			return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
				change, err := v.RemoveRecipient(query, reencryptFlag, ident)
				if err != nil {
					return err
				}
				writeOut(app.Out, recipientChangeLines("removed", change))
				return nil
			})
		},
	}

	addUseFlag(cmd, &useFlag)
	cmd.Flags().BoolVar(&reencryptFlag, "reencrypt", false,
		"re-encrypt every entry to the reduced recipient list (required)")
	return cmd
}

// recipientChangeLines renders what a completed add or remove did.
//
// There is no with/without-re-encryption distinction left to render:
// add always re-encrypts (A19) and remove has always required it, so
// the count line is unconditional.
//
// It says the commit is local on purpose. The guarantee re-encryption
// makes is about HEAD — every entry and both recipient files in one
// commit or none — while publishing it is the ordinary post-write push,
// which warns and proceeds when the remote is unreachable. Saying
// "committed locally" is what keeps a failed push legible as a
// publishing problem rather than a half-done migration.
func recipientChangeLines(verb string, change gage.RecipientChange) []string {
	return []string{
		fmt.Sprintf("gage: %s recipient %q (%s)", verb, change.Device, change.Pubkey),
		fmt.Sprintf("gage: re-encrypted %d %s; committed locally as %s",
			change.Reencrypted, plural(change.Reencrypted, "entry", "entries"), shortHash(change.Commit)),
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// shortHash abbreviates a commit for human output, leaving anything that
// isn't a full hash alone.
func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func newRecipientListCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: commandShort("recipient list"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultWithoutUnlocking(app, useFlag)
			if err != nil {
				return err
			}
			list, err := v.Recipients()
			if err != nil {
				return err
			}
			lines := make([]string, 0, len(list))
			for _, r := range list {
				lines = append(lines, fmt.Sprintf("%s\t%s", r.Device, r.Pubkey))
			}
			writeOut(app.Out, lines)
			return nil
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// newRecipientVerifyCommand builds `gage recipient verify`: does
// .age-recipients agree with .gage/config.toml's [[recipients]]?
//
// Both files are plaintext control-plane data, so this needs no identity
// and asks nothing of a human — it is safe to run in CI, and against a
// vault this device has just cloned and cannot yet decrypt. That is
// structural rather than a promise: the vault is resolved without
// unlocking and the library call it makes takes no Prompter.
func newRecipientVerifyCommand(app *App) *cobra.Command {
	var (
		useFlag    string
		repairFlag bool
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: commandShort("recipient verify"),
		Long: commandShort("recipient verify") + ".\n\n" +
			"--repair rewrites .age-recipients from .gage/config.toml's list and commits it,\n" +
			"after naming every key it drops and every key it adds. It is the exit from the\n" +
			"divergence `recipient add`/`remove` refuse to run over. It does not clear the\n" +
			"trust cache — repairing the split between the two files says nothing about\n" +
			"whether the recipient list itself is one you approve, so the next write still\n" +
			"shows you the change — and it takes no --reencrypt, since rewriting a plaintext\n" +
			"key list says nothing about whether existing ciphertext should be rewritten.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultWithoutUnlocking(app, useFlag)
			if err != nil {
				return err
			}
			if repairFlag {
				return runRecipientRepair(app, v)
			}
			got, err := v.VerifyRecipients()
			if err != nil {
				return err
			}
			if got.InSync {
				writeOut(app.Out, []string{fmt.Sprintf(
					"gage: %q's recipient files are in sync", v.Name)})
				return nil
			}

			// Reported as one Conflict error rather than printed output
			// plus a bare exit code, so the exit status and the reason
			// for it can never disagree. Each difference names the file
			// that has the key and the file that doesn't: a key in one
			// and not the other is the tampering signature M10's trust
			// cache keys off, and which direction it points in is the
			// part that says what happened.
			lines := []string{fmt.Sprintf("gage: %q's recipient files disagree:", v.Name)}
			for _, k := range got.OnlyInRecipientsFile {
				lines = append(lines, fmt.Sprintf("  %s is in .age-recipients but not in .gage/config.toml", k))
			}
			for _, k := range got.OnlyInConfig {
				lines = append(lines, fmt.Sprintf("  %s is in .gage/config.toml but not in .age-recipients", k))
			}
			return exitcode.New(exitcode.Conflict, strings.Join(lines, "\n"))
		},
	}
	addUseFlag(cmd, &useFlag)
	cmd.Flags().BoolVar(&repairFlag, "repair", false,
		"rewrite .age-recipients from .gage/config.toml and commit it, after confirming what that drops")
	return cmd
}

// runRecipientRepair is `recipient verify --repair`: the exit the
// refusal in `recipient add`/`remove` ships with.
//
// It says what it dropped rather than only that it succeeded. The keys
// it removes could read everything written while they were listed, so
// "repaired" on its own would be the least useful true thing gage could
// print here.
func runRecipientRepair(app *App, v *gage.Vault) error {
	repair, err := v.RepairRecipients(app.Prompter)
	if err != nil {
		return err
	}
	if repair.Commit == "" {
		writeOut(app.Out, []string{fmt.Sprintf(
			"gage: %q's recipient files are already in sync; nothing to repair", v.Name)})
		return nil
	}

	lines := make([]string, 0, len(repair.Dropped)+len(repair.Added)+1)
	for _, k := range repair.Dropped {
		lines = append(lines, fmt.Sprintf("gage: dropped %s from .age-recipients", k))
	}
	for _, k := range repair.Added {
		lines = append(lines, fmt.Sprintf("gage: added %s to .age-recipients", k))
	}
	lines = append(lines, fmt.Sprintf("gage: rewrote .age-recipients from .gage/config.toml; committed locally as %s",
		shortHash(repair.Commit)))
	writeOut(app.Out, lines)
	return nil
}
