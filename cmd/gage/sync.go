package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// vaultForSync resolves which vault a sync verb targets, without
// unlocking it.
//
// Sync unlocks lazily (see "Sync model"): a fast-forward, and a merge
// whose two sides touched different entries, decrypt nothing, so asking
// for a passphrase up front would prompt for a key most syncs never use.
// This is the one family of commands that deliberately doesn't go through
// withUnlockedVault.
func vaultForSync(app *App, use string) (*gage.Vault, error) {
	if app.Session != nil {
		return app.Session.VaultWithoutUnlocking(use)
	}
	name, entry, err := resolveVaultEntry(use)
	if err != nil {
		return nil, err
	}
	return &gage.Vault{Name: name, Path: entry.Path}, nil
}

// newPullCommand builds `gage pull`: fetch and fast-forward, never merge.
func newPullCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "pull",
		Short: commandShort("pull"),
		Long: commandShort("pull") + ".\n\n" +
			"Fast-forward only: if the remote has moved in a way this vault can't simply\n" +
			"catch up to, nothing local is touched and the divergence is reported.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultForSync(app, useFlag)
			if err != nil {
				return err
			}
			ctx, cancel := syncContext()
			defer cancel()

			report, err := v.Pull(ctx)
			if err != nil {
				return err
			}
			if report.Diverged {
				return exitcode.Newf(exitcode.Conflict, "gage: %s", report.Summary())
			}
			writeOut(app.Out, []string{syncLine(v, report)})
			return nil
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// newPushCommand builds `gage push`: publish local commits, merging a
// divergence when the two sides touched different files.
func newPushCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "push",
		Short: commandShort("push"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultForSync(app, useFlag)
			if err != nil {
				return err
			}
			ctx, cancel := syncContext()
			defer cancel()

			report, err := v.Push(ctx)
			if err != nil {
				// Same reporting as `sync`: a push is where a divergence
				// is discovered, so it is where the paths have to be
				// named. Only the summary differing between the two verbs
				// would leave `push` the less informative way to find out
				// about the identical situation.
				reportConflicts(app, v, report)
				return err
			}
			writeOut(app.Out, []string{syncLine(v, report)})
			return nil
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// newSyncCommand builds `gage sync`: catch up, then publish.
func newSyncCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: commandShort("sync"),
		Long: commandShort("sync") + ".\n\n" +
			"Pulls, merges a divergence whose two sides touched different entries, and\n" +
			"pushes the result. An entry both sides changed is reported rather than\n" +
			"resolved: gage never guesses which version of a secret to keep.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultForSync(app, useFlag)
			if err != nil {
				return err
			}
			ctx, cancel := syncContext()
			defer cancel()

			report, err := v.Sync(ctx)
			if err != nil {
				reportConflicts(app, v, report)
				return err
			}
			writeOut(app.Out, []string{syncLine(v, report)})
			return nil
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// syncLine renders what a successful sync actually did, in one line.
//
// The origin lookup separates the two ways of having nothing to do: a
// vault that matches its origin, and a local-only vault that has no
// origin to match. Telling someone with no remote that they are "in sync
// with origin" names a thing that doesn't exist. A lookup that fails is
// not worth failing a successful sync over, so it falls through to the
// ordinary wording.
func syncLine(v *gage.Vault, r gage.SyncReport) string {
	if remote, err := gitrepo.RemoteURL(v.Path); err == nil && remote == "" {
		return fmt.Sprintf("gage: %q is local-only; there is no remote to sync with", v.Name)
	}
	switch {
	case r.Merged && r.Pushed:
		return fmt.Sprintf("gage: merged origin's changes into %q and pushed", v.Name)
	case r.Pulled && r.Pushed:
		return fmt.Sprintf("gage: caught %q up with origin and pushed", v.Name)
	case r.Pushed:
		return fmt.Sprintf("gage: pushed %q to origin", v.Name)
	case r.Pulled:
		return fmt.Sprintf("gage: caught %q up with origin", v.Name)
	default:
		return fmt.Sprintf("gage: %q is already in sync with origin", v.Name)
	}
}

// reportConflicts prints the paths a divergence couldn't merge, so a
// human sees *what* is conflicting and not only that something is.
//
// The entry files are named by UUID, which is all gage can say without
// decrypting them — and decrypting to label a conflict is exactly the
// unlock M8a's detection avoids. Resolving these interactively is
// `gage sync`'s conflict resolution, which is not in this build yet; the
// vault is an ordinary git repository in the meantime.
func reportConflicts(app *App, v *gage.Vault, r gage.SyncReport) {
	if len(r.Conflicts) == 0 {
		return
	}
	lines := []string{fmt.Sprintf("gage: %d path(s) changed on both sides:", len(r.Conflicts))}
	for _, path := range r.Conflicts {
		lines = append(lines, "  "+path)
	}
	if r.ConflictKind() == gage.ConflictRecipients {
		lines = append(lines,
			"gage: these define who can read this vault, so gage will never merge them for you.")
	}
	lines = append(lines, fmt.Sprintf("gage: the vault is an ordinary git repository at %s", v.Path))
	writeOut(app.Err, lines)
}

// syncContext bounds a manually invoked sync the same way the automatic
// ones are bounded, so a hung remote doesn't hang a terminal.
func syncContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), gage.RemoteOpTimeout)
}
