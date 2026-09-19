package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
)

// vaultForSync resolves which vault a sync verb targets, without
// unlocking it.
//
// Sync unlocks lazily (see "Sync model"): a fast-forward, and a merge
// whose two sides touched different entries, decrypt nothing, so asking
// for a passphrase up front would prompt for a key most syncs never use.
// M9's identity and recipient-reading verbs need the same resolution for
// a different reason — they never decrypt at all — so the mechanism
// lives in vaultWithoutUnlocking and this is the sync-side name for it.
func vaultForSync(app *App, use string) (*gage.Vault, error) {
	return vaultWithoutUnlocking(app, use)
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
			"pushes the result. An entry both sides changed is presented with each\n" +
			"version's date and device, to keep, replace, or keep both under separate\n" +
			"ids — gage never guesses which version of a secret to keep, and with\n" +
			"nobody to ask (a script, CI) it stops rather than choosing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultForSync(app, useFlag)
			if err != nil {
				return err
			}
			ctx, cancel := syncContext()
			defer cancel()

			unlock, done := syncUnlocker(app, v, useFlag)
			defer done()

			report, err := v.SyncResolving(ctx, gage.ConflictResolver{
				Prompter: app.Prompter,
				Unlock:   unlock,
			})
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

// syncUnlocker builds the lazy unlock a resolving sync needs, plus the
// cleanup for whatever it opened.
//
// The callback is what makes the unlock lazy rather than up-front: a
// fast-forward and a disjoint merge never call it, so the syncs that
// decrypt nothing still prompt for nothing. When a conflict does force
// it, a session hands back the Identity it already holds and nobody is
// asked twice, while one-shot mode unlocks on the spot and closes again
// when the command ends — which is why only that branch has anything to
// clean up.
func syncUnlocker(app *App, v *gage.Vault, use string) (unlock func() (*gage.Identity, error), done func()) {
	var opened *gage.Identity

	unlock = func() (*gage.Identity, error) {
		if app.Session != nil {
			_, ident, err := app.Session.Vault(use)
			return ident, err
		}
		ident, err := v.Unlock(app.Prompter)
		if err != nil {
			return nil, err
		}
		opened = &ident
		return opened, nil
	}
	return unlock, func() {
		if opened != nil {
			_ = opened.Close()
		}
	}
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
	case r.Resolved > 0 && r.Pushed:
		// Named separately from the plain merge below because it is the
		// one outcome a human actively produced: they answered N
		// questions, and the line should say so rather than describe it
		// as something gage did on its own.
		return fmt.Sprintf("gage: resolved %s in %q, merged origin's changes and pushed",
			pluralEntries(r.Resolved), v.Name)
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

// pluralEntries renders a count of entries as English, for lines people
// read rather than parse.
func pluralEntries(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

// reportConflicts prints the paths a divergence couldn't merge, so a
// human sees *what* is conflicting and not only that something is.
//
// The entry files are named by UUID, which is all gage can say without
// decrypting them — and decrypting to label a conflict is exactly the
// unlock lazy unlocking avoids. `gage sync` resolves entry conflicts
// interactively; what reaches here is what it couldn't, or wasn't asked
// to: a `pull`/`push`, a sync with nobody to ask, one whose questions
// were skipped or aborted, and recipient-file conflicts, which are never
// resolved through that menu.
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
