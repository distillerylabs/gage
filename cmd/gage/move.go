package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// newMvCommand builds `gage mv <query> --to-vault <name>`: M11's sharing
// mechanism. See "mv/cp are now cross-vault, and that's the sharing
// mechanism" in the design doc.
func newMvCommand(app *App) *cobra.Command {
	var useFlag, toVaultFlag string
	cmd := &cobra.Command{
		Use:   "mv <query> --to-vault <name>",
		Short: commandShort("mv"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMoveOrCopy(app, useFlag, toVaultFlag, args[0], true)
		},
	}
	addUseFlag(cmd, &useFlag)
	addToVaultFlag(cmd, &toVaultFlag)
	return cmd
}

// newCpCommand builds `gage cp <query> --to-vault <name>`: like `mv`, but
// leaves the original in place — see Vault.Copy.
func newCpCommand(app *App) *cobra.Command {
	var useFlag, toVaultFlag string
	cmd := &cobra.Command{
		Use:   "cp <query> --to-vault <name>",
		Short: commandShort("cp"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMoveOrCopy(app, useFlag, toVaultFlag, args[0], false)
		},
	}
	addUseFlag(cmd, &useFlag)
	addToVaultFlag(cmd, &toVaultFlag)
	return cmd
}

// addToVaultFlag wires the --to-vault flag both mv and cp require.
func addToVaultFlag(cmd *cobra.Command, toVault *string) {
	cmd.Flags().StringVar(toVault, "to-vault", "", "destination vault to share the entry into (required)")
	_ = cmd.MarkFlagRequired("to-vault")
}

// runMoveOrCopy is mv/cp's shared handler. It resolves --to-vault and
// checks it against the source *before* the source is ever unlocked —
// which is what makes an unregistered --to-vault name, or one naming the
// source itself, fail without decrypting anything (see the M11 plan's
// test list). The destination itself is never unlocked at all: Vault.Move
// and Vault.Copy encrypt to its recipients, which needs no private key —
// see resolveVaultWithoutUnlocking.
func runMoveOrCopy(app *App, useFlag, toVaultFlag, query string, move bool) error {
	sourceName, err := resolveSourceVaultName(app, useFlag)
	if err != nil {
		return err
	}
	if sourceName == toVaultFlag {
		return exitcode.Wrap(exitcode.Usage, fmt.Errorf("%w: %q", gage.ErrSameVault, toVaultFlag))
	}

	dest, err := resolveVaultWithoutUnlocking(app, toVaultFlag)
	if err != nil {
		return err
	}

	return withUnlockedVault(app, useFlag, func(v *gage.Vault, ident *gage.Identity) error {
		mQuery, _, err := mutationQuery(app, v, query, ident)
		if err != nil {
			return err
		}

		var res gage.MoveResult
		if move {
			res, err = v.Move(mQuery, dest, ident)
		} else {
			res, err = v.Copy(mQuery, dest, ident)
		}
		if err != nil {
			reportAmbiguous(app, err)
			return err
		}

		if move {
			forgetIndexEntry(app, v, res.SourceID)
		}
		if app.Session != nil {
			app.Session.NoteEntry(dest.Name, res.DestID, res.Entry)
		}

		verb := "moved"
		if !move {
			verb = "copied"
		}
		writeOut(app.Out, []string{fmt.Sprintf("gage: %s %q into %q (%s)",
			verb, res.Entry.Title, dest.Name, shortEntryID(res.DestID.String()))})
		return nil
	})
}

// resolveSourceVaultName reports which vault name mv/cp's source side
// will resolve to — --use if given, else the current vault in whichever
// mode is running — without unlocking anything. It exists so the
// "--to-vault can't name the source itself" check runs before any
// unlock, the same way resolveVaultWithoutUnlocking lets a bad
// --to-vault name fail before one too.
func resolveSourceVaultName(app *App, useFlag string) (string, error) {
	if app.Session != nil {
		if useFlag != "" {
			return useFlag, nil
		}
		if app.Session.Current() == "" {
			return "", exitcode.Wrap(exitcode.Usage,
				fmt.Errorf("%w: run `use <vault>` first, or pass -u/--use", gage.ErrNoCurrentVault))
		}
		return app.Session.Current(), nil
	}
	name, _, err := resolveVaultEntry(useFlag)
	return name, err
}

// resolveVaultWithoutUnlocking resolves name to a *gage.Vault without
// unlocking it: the session path borrows Session.VaultWithoutUnlocking
// (registering it with the session so a later pull still invalidates its
// index), and the one-shot path builds the same bare struct
// withUnlockedVault's own one-shot branch does, minus the Unlock call.
func resolveVaultWithoutUnlocking(app *App, name string) (*gage.Vault, error) {
	if app.Session != nil {
		return app.Session.VaultWithoutUnlocking(name)
	}
	name, entry, err := resolveVaultEntry(name)
	if err != nil {
		return nil, err
	}
	return vaultFromEntry(name, entry)
}
