package main

import (
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
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

	// Now is the clock the session's idle timeout reads, defaulting to
	// time.Now when nil. It exists for the same reason IsTerminal does:
	// a test needs to differ from production on it and cannot fake it
	// any other way. Real elapsed time is not usable here — Windows'
	// time.Now advances in steps of up to ~15ms, so two commands in one
	// scripted session routinely read the identical instant and no
	// timeout, however small, elapses between them. See
	// TestConfiguredIdleTimeoutReachesTheSession.
	Now func() time.Time

	// AssumeYes is the persistent --yes flag: answer M10's
	// recipient-change confirmation with yes, without rendering it. It
	// lives on App rather than in the library because "who answers, and
	// whether they are asked at all" is a frontend policy — the same
	// split that puts maxPassphraseAttempts here and not in Vault.Unlock.
	//
	// It is read once, in NewRootCmd's PersistentPreRunE, to wrap
	// Prompter. Nothing else consults it.
	AssumeYes bool

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

	// --yes is persistent so it reaches insert/edit/generate/rename in a
	// script, not only the design doc's mv/cp. What it answers is one
	// question — see withAssumeYes.
	root.PersistentFlags().BoolVar(&app.AssumeYes, "yes", false,
		"answer the recipient-change confirmation with yes, without showing it (for scripts and CI)")

	// The wrap happens once, here, after flag parsing and before any
	// handler runs — rather than at each point a handler hands a
	// Prompter to the library, which would be the same decision written
	// out a dozen times with a dozen chances to forget it.
	//
	// A session is refused rather than wrapped, and the reason is not
	// tidiness. --yes exists because a scripted run has nobody to show a
	// recipient diff to; a session is the opposite case, a human sitting
	// at a prompt, and there the flag would either do nothing (a --yes
	// typed on one line never reaches the check, which asks through the
	// prompter the session unlocked with) or far too much (`gage --yes`
	// entering a session would silently approve every recipient change
	// for as long as that session lasted). Both are worse than saying
	// so. `-m`/--value-stdin are refused in a session for the same
	// shape of reason.
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if !app.AssumeYes {
			return nil
		}
		// cmd == root is bare `gage`, which dispatches to a session on a
		// terminal; app.Session != nil is a line typed inside one.
		if app.Session != nil || cmd == root {
			return exitcode.New(exitcode.Usage,
				"gage: --yes is for scripted, one-shot runs and isn't available in a session; "+
					"answer the prompt, or run the command from your shell with --yes")
		}
		app.Prompter = withAssumeYes(app.Prompter)
		return nil
	}

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
	root.AddCommand(newCloneCommand(app))
	root.AddCommand(newVaultCommand(app))
	root.AddCommand(newIdentityCommand(app))
	root.AddCommand(newRecipientCommand(app))
	root.AddCommand(newGitCommand(app))
	root.AddCommand(newAuthCommand(app))
	root.AddCommand(newSyncCommand(app))
	root.AddCommand(newPullCommand(app))
	root.AddCommand(newPushCommand(app))
	root.AddCommand(newInsertCommand(app))
	root.AddCommand(newShowCommand(app))
	root.AddCommand(newCatCommand(app))
	root.AddCommand(newEditCommand(app))
	root.AddCommand(newRenameCommand(app))
	root.AddCommand(newGenerateCommand(app))
	root.AddCommand(newRmCommand(app))
	root.AddCommand(newMvCommand(app))
	root.AddCommand(newCpCommand(app))
	root.AddCommand(newLsCommand(app))
	root.AddCommand(newSearchCommand(app))
	root.AddCommand(newReindexCommand(app))

	installHelp(app, root)

	return root
}
