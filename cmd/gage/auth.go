package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/remoteauth"
)

// newAuthCommand groups the token commands. They sit under the git group
// rather than at the top level because a token for a git host has no
// meaning under a different backing store — a future vault type would
// authenticate its own way, or not at all. See "Git-specific commands".
func newAuthCommand(app *App) *cobra.Command {
	parent := &cobra.Command{
		Use:   "auth",
		Short: "Manage tokens for git hosts",
	}
	parent.AddCommand(newAuthLoginCommand(app))
	parent.AddCommand(newAuthStatusCommand(app))
	parent.AddCommand(newAuthLogoutCommand(app))
	return parent
}

// addHostFlag wires the --host flag all three auth verbs share.
func addHostFlag(cmd *cobra.Command, host *string) {
	cmd.Flags().StringVar(host, "host", "",
		"git host the token belongs to (default: the host of the current vault's origin)")
}

// resolveHost applies --host if given, and otherwise derives the host
// from the current vault's origin — per "Git-specific commands", tokens
// are per-host and `--host` defaults to the host of the current vault's
// remote.
func resolveHost(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	name, entry, err := resolveVaultEntry("")
	if err != nil {
		return "", exitcode.New(exitcode.Usage,
			"gage: no --host given and no current vault to take one from")
	}

	origin := entry.Git.Origin
	if origin == "" {
		// Global config can lag a remote set outside gage; the repository
		// itself is the authority on what origin actually is.
		origin, err = gitrepo.RemoteURL(entry.Path)
		if err != nil {
			return "", exitcode.Wrap(exitcode.Internal, err)
		}
	}
	if origin == "" {
		return "", exitcode.Newf(exitcode.Usage,
			"gage: vault %q has no remote, so there is no host to authenticate to; pass --host", name)
	}

	host, err := remoteauth.Host(origin)
	if err != nil {
		return "", err
	}
	if host == "" {
		return "", exitcode.Newf(exitcode.Usage,
			"gage: vault %q's remote (%s) is a local path, which needs no token; pass --host to name one", name, origin)
	}
	return host, nil
}

// newAuthLoginCommand builds `gage auth login`: store a token the user
// issued themselves.
//
// It never brokers one. There's no OAuth device flow and no client ID in
// the binary — per Q-OAUTH-APP that's a deliberate refusal, not a missing
// feature: on GitHub a device-flow grant is scope-based and would acquire
// access to *every* private repository you own, which is strictly broader
// than the fine-grained, single-repository token this prompt asks for.
func newAuthLoginCommand(app *App) *cobra.Command {
	var hostFlag string
	cmd := &cobra.Command{
		Use:   "login",
		Short: commandShort("auth login"),
		Long: commandShort("auth login") + ".\n\n" +
			"gage never issues a token for you: create one on the host and paste it here.\n" +
			"Scope it as narrowly as the host allows — on GitHub, a fine-grained personal\n" +
			"access token limited to the vault's own repository, not a classic repo-scoped\n" +
			"one that reaches every repository you own.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := resolveHost(hostFlag)
			if err != nil {
				return err
			}

			// Prompted, never taken as an argument: a token passed on a
			// command line would land in shell history and in the
			// session's own history file. The Prompter masks it the same
			// way it masks a passphrase.
			token, err := app.Prompter.Value(fmt.Sprintf("Token for %s: ", host))
			if err != nil {
				return exitcode.Wrap(exitcode.LockedOrAuth, err)
			}
			if err := remoteauth.Store(host, token); err != nil {
				return err
			}

			writeOut(app.Out, []string{fmt.Sprintf("gage: stored a token for %s", host)})
			return nil
		},
	}
	addHostFlag(cmd, &hostFlag)
	return cmd
}

// newAuthStatusCommand builds `gage auth status`: which hosts have a
// token. It deliberately makes no network call — "does this token still
// work" is answered by the next fetch or push, which says so in terms of
// the operation the human actually wanted.
func newAuthStatusCommand(app *App) *cobra.Command {
	var hostFlag string
	cmd := &cobra.Command{
		Use:   "status",
		Short: commandShort("auth status"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if hostFlag != "" {
				return reportHostStatus(app, hostFlag)
			}

			hosts, err := remoteauth.Hosts()
			if err != nil {
				return err
			}
			if len(hosts) == 0 {
				// Falling back to the current vault's host makes the
				// no-token case actionable instead of blank.
				if host, err := resolveHost(""); err == nil {
					return reportHostStatus(app, host)
				}
				writeOut(app.Out, []string{"gage: no tokens are stored on this machine"})
				return nil
			}

			lines := make([]string, 0, len(hosts))
			for _, host := range hosts {
				lines = append(lines, fmt.Sprintf("%s  configured", host))
			}
			writeOut(app.Out, lines)
			return nil
		},
	}
	addHostFlag(cmd, &hostFlag)
	return cmd
}

// reportHostStatus prints one host's token state. The token itself is
// never printed, only whether there is one.
func reportHostStatus(app *App, host string) error {
	if _, err := remoteauth.Load(host); err != nil {
		writeOut(app.Out, []string{fmt.Sprintf("%s  no token (run `gage auth login --host %s`)", host, host)})
		return nil
	}
	writeOut(app.Out, []string{fmt.Sprintf("%s  configured", host)})
	return nil
}

// newAuthLogoutCommand builds `gage auth logout`.
func newAuthLogoutCommand(app *App) *cobra.Command {
	var hostFlag string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: commandShort("auth logout"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := resolveHost(hostFlag)
			if err != nil {
				return err
			}
			if err := remoteauth.Remove(host); err != nil {
				return err
			}
			writeOut(app.Out, []string{fmt.Sprintf("gage: forgot the token for %s", host)})
			return nil
		},
	}
	addHostFlag(cmd, &hostFlag)
	return cmd
}
