package main

import (
	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
)

// newGitCommand groups the commands that only exist because the current
// (and only) vault type is git — see "Git-specific commands" in the
// design doc. There is deliberately no generic passthrough: every
// operation here goes through go-git via internal/gage/gitrepo, never a
// shelled-out git binary.
func newGitCommand(app *App) *cobra.Command {
	parent := &cobra.Command{
		Use:   "git",
		Short: "Git-specific vault operations",
	}
	parent.AddCommand(newGitSetRemoteCommand(app))
	return parent
}

func newGitSetRemoteCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "set-remote <name> <url>",
		Short: commandShort("git set-remote"),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, url := args[0], args[1]

			g, err := readGlobalConfig()
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			entry, ok := g.Vaults[name]
			if !ok {
				return exitcode.Newf(exitcode.NotFound, "gage: no such vault %q", name)
			}

			// Sets the actual git repo's origin *and* updates global
			// config in the same call, so the two never drift apart —
			// see "Git-specific commands".
			if err := gitrepo.SetRemote(entry.Path, url); err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			entry.Git.Origin = url
			g.Vaults[name] = entry
			if err := writeGlobalConfig(g); err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			return nil
		},
	}
}
