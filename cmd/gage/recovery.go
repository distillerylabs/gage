package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// newRecoveryCommand groups the verbs for a vault's offline recovery key.
// Today that is one: proving a stored copy still works. It is its own group
// rather than a `recipient` subcommand because `recipient verify` already
// means something else — that the two recipient files agree — and two
// verifies under one parent would be a trap.
func newRecoveryCommand(app *App) *cobra.Command {
	parent := &cobra.Command{
		Use:   "recovery",
		Short: "Work with a vault's offline recovery key",
	}
	parent.AddCommand(newRecoveryVerifyCommand(app))
	return parent
}

// newRecoveryVerifyCommand builds `gage recovery verify`, which checks a
// pasted key against the vault's recipient list.
//
// It needs no unlock, and that is the point: a recovery key is what you
// reach for when this device's identity file is gone, so the check cannot
// be one that depends on it. It reads only the plaintext recipient list.
func newRecoveryVerifyCommand(app *App) *cobra.Command {
	var useFlag string

	cmd := &cobra.Command{
		Use:   "verify",
		Short: commandShort("recovery verify"),
		Long: commandShort("recovery verify") + ".\n\n" +
			"Paste the key you stored offline (it is read without echo, and neither shown\n" +
			"nor kept) and gage checks that it is one of this vault's recipients. That\n" +
			"proves the copy you hold is intact and belongs to this vault. It does not\n" +
			"decrypt anything, so it cannot tell you the vault's entries are undamaged.\n\n" +
			"It needs no passphrase: it never touches this device's identity file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultWithoutUnlocking(app, useFlag)
			if err != nil {
				return err
			}

			pasted, err := app.Prompter.Value("Paste the recovery key to check: ")
			if err != nil {
				return exitcode.Wrap(exitcode.LockedOrAuth, err)
			}
			// The string the prompter returned is a copy Go will not let us
			// erase; this one is ours, and is zeroed as soon as the check is
			// done. The same accepted gap init documents.
			secret := []byte(pasted)
			defer zeroBytes(secret)

			if err := v.VerifyRecoveryKey(secret); err != nil {
				return err
			}
			writeOut(app.Out, []string{fmt.Sprintf(
				"gage: that key is a recipient of vault %q", v.Name)})
			return nil
		},
	}

	addUseFlag(cmd, &useFlag)
	return cmd
}
