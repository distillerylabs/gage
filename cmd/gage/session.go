package main

import (
	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
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
		// The registry carries each meta-verb's argument syntax, so the
		// use line here and in-session help's listing can't drift apart
		// (Q-HELP-SURFACES) — `help use` and the `use <vault>` entry in
		// the top-level listing render the same string from the same
		// field.
		Use:     ci.UsageName(),
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
	// --script/--stdin are checked before the TTY dispatch, and that
	// ordering is the point: they are the *explicit* spellings of
	// "read session commands from somewhere that isn't a prompt", so
	// they must win over whatever stdin happens to be. A --script run
	// from a terminal is still a script.
	if scriptMode(app) {
		return dispatchScript(app)
	}
	if chooseRootMode(app.IsTerminal()) == "session" {
		return runSession(app)
	}
	renderHelp(app.Out, root)
	return nil
}
