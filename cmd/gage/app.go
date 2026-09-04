package main

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
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

	// Prompter is how internal/gage asks this frontend for a human
	// decision — a passphrase, a yes/no, a pick from a list. It lives on
	// App rather than being constructed where it's needed so that tests
	// drive the whole command tree with an in-memory fake and never
	// touch a terminal. See "Library architecture".
	Prompter gage.Prompter

	// IsTerminal reports whether stdin is a real terminal, for the bare
	// `gage` TTY-vs-piped dispatch (Q-ROOT-CMD). Kept separate from In
	// (which tests set to an in-memory reader) because term.IsTerminal
	// only means anything against a real file descriptor — this is the
	// one seam production code and tests genuinely need to differ on.
	IsTerminal func() bool

	// Session is non-nil only while the REPL is running. It's what makes
	// every entry command work unchanged in both modes: the handlers all
	// go through withUnlockedVault, which unlocks and closes per command
	// when this is nil and borrows the session's already-unlocked
	// Identity when it isn't. No command has a session-only variant.
	Session *gage.Session
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

	// Only the session-only meta-verbs get the generic "you need a
	// session for this" stub (they have no real logic to run outside a
	// session at all — see newStubCommand). Every other registry entry
	// gets a dedicated command tree below, built for real starting in
	// M1.
	//
	// help is the one session-only entry that already exists as a real
	// command: it's registered so in-session help can list it (the
	// design doc keeps it out of gage --help's listing, which is what
	// AvailSessionOnly governs), but `gage help` itself works one-shot
	// and is wired by installHelp below.
	for _, ci := range registry {
		if ci.Availability == AvailSessionOnly && ci.Name != "help" {
			root.AddCommand(newStubCommand(app, ci))
		}
	}

	root.AddCommand(newInitCommand(app))
	root.AddCommand(newVaultCommand(app))
	root.AddCommand(newGitCommand(app))
	root.AddCommand(newInsertCommand(app))
	root.AddCommand(newShowCommand(app))
	root.AddCommand(newCatCommand(app))
	root.AddCommand(newEditCommand(app))
	root.AddCommand(newRenameCommand(app))
	root.AddCommand(newGenerateCommand(app))
	root.AddCommand(newRmCommand(app))
	root.AddCommand(newLsCommand(app))

	installHelp(app, root)

	return root
}
