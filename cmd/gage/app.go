package main

import (
	"io"

	"github.com/spf13/cobra"
)

// App holds everything cmd/gage's Cobra layer needs that isn't itself
// part of internal/gage: where output goes, and how to tell whether
// stdin is a real terminal. Tests construct their own App with in-memory
// IO and a fake IsTerminal so the whole command tree runs in-process —
// see run_test.go.
type App struct {
	Out io.Writer
	Err io.Writer
	In  io.Reader

	Build BuildInfo

	// IsTerminal reports whether stdin is a real terminal, for the bare
	// `gage` TTY-vs-piped dispatch (Q-ROOT-CMD). Kept separate from In
	// (which tests set to an in-memory reader) because term.IsTerminal
	// only means anything against a real file descriptor — this is the
	// one seam production code and tests genuinely need to differ on.
	IsTerminal func() bool
}

// NewRootCmd builds gage's full one-shot-mode command tree: every
// registry entry as a real Cobra command, custom help rendering (see
// help.go), --version, and the bare-`gage` TTY dispatch (see session.go).
func NewRootCmd(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "gage",
		Short:         "gage — a git-backed, age-encrypted secret & notes manager",
		Version:       app.Build.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatchRoot(app, cmd)
		},
	}
	root.SetOut(app.Out)
	root.SetErr(app.Err)
	root.SetIn(app.In)
	root.SetVersionTemplate("{{.Version}}\n")
	root.CompletionOptions.DisableDefaultCmd = true

	// Only the session-only meta-verbs get the generic "not implemented
	// yet" stub (they have no real logic to run outside a session at
	// all — see newStubCommand). Every other registry entry gets a
	// dedicated command tree below, built for real starting in M1.
	for _, ci := range registry {
		if ci.Availability == AvailSessionOnly {
			root.AddCommand(newStubCommand(app, ci))
		}
	}

	root.AddCommand(newInitCommand(app))
	root.AddCommand(newVaultCommand(app))
	root.AddCommand(newGitCommand(app))

	installHelp(app, root)

	return root
}
