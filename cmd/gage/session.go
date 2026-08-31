package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// newStubCommand turns one registry entry into a real Cobra command, so
// the registry-completeness test (every registry entry has a
// corresponding Cobra command, and vice versa) holds even for the
// session-only meta-verbs that don't do anything until M6. Session-only
// commands invoked here — in one-shot mode, since no session exists yet
// outside one — report that plainly rather than running or looking like
// an unknown command, the mirror image of how init/clone report
// "one-shot only" when invoked inside a session (see the design doc's
// "Session-only commands").
func newStubCommand(app *App, ci CommandInfo) *cobra.Command {
	cmd := &cobra.Command{
		Use:     ci.Name,
		Aliases: ci.Aliases,
		Short:   ci.Short,
		// Session-only commands never belong in gage --help's listing —
		// see the M0 test list's "Neither help spelling lists the
		// session-only commands ... as top-level subcommands." Our
		// custom help renderer already filters on Availability
		// regardless, but Hidden keeps Cobra's own machinery (error
		// suggestions, shell completion) consistent with that too.
		Hidden:        ci.Availability == AvailSessionOnly,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ci.Availability == AvailSessionOnly {
				return exitcode.Newf(exitcode.Usage,
					"%s is only available in a session; run `gage` with no arguments to start one", ci.Name)
			}
			return exitcode.Newf(exitcode.Internal, "%s is not implemented yet", ci.Name)
		},
	}
	return cmd
}

// chooseRootMode is the pure decision behind bare `gage`'s dispatch
// (Q-ROOT-CMD): a real terminal on stdin drops into the session; stdin
// piped or redirected prints help instead, since --stdin is already the
// explicit spelling for "read session commands from stdin" and letting
// bare `gage` silently mean the same thing would give one behavior two
// spellings. Kept as a pure function so both branches are unit-testable
// without a real pty — the session implementation itself is M6's.
func chooseRootMode(stdinIsTTY bool) string {
	if stdinIsTTY {
		return "session"
	}
	return "help"
}

func dispatchRoot(app *App, root *cobra.Command) error {
	if chooseRootMode(app.IsTerminal()) == "session" {
		return runSessionStub(app)
	}
	renderHelp(app.Out, root)
	return nil
}

// runSessionStub is bare `gage`'s session-mode landing point until M6
// builds the real REPL. cmd/gage is the CLI's I/O layer, so printing
// here is fine — this is not internal/gage.
func runSessionStub(app *App) error {
	fmt.Fprintln(app.Out, "gage: interactive session mode isn't implemented yet (arrives in M6).")
	fmt.Fprintln(app.Out, `Run "gage --help" for the commands available today.`)
	return nil
}
