package main

import (
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
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

	// IsErrTerminal reports whether Err is a real terminal, which is a
	// different question from IsTerminal: Err is where gage puts prompts,
	// warnings, and `init`'s one-time recovery key, and `gage init
	// 2>build.log` redirects all of that while leaving stdin a terminal.
	// Only the recovery key consults it, because it is the only output
	// gage produces that is both secret and unrepeatable — see
	// planRecoveryKey.
	//
	// nil means "same as IsTerminal", which is what every test wants and
	// what Run overrides with a real check on the stderr it was handed.
	// Read through errIsTerminal, never directly.
	IsErrTerminal func() bool

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

	// ScriptFile and ScriptStdin are the two spellings of
	// non-interactive session mode: read this session's command lines
	// from a file, or from stdin. They live on App because dispatchRoot
	// consults them, and because the passphrase policy a scripted run
	// gets (see scriptPrompter) depends on which of the two it is.
	ScriptFile  string
	ScriptStdin bool

	// Clipboard, ClipboardTimer and ClipboardWait are --clip's three
	// seams, and they exist for the same reason Locker and RemoteSyncer
	// do: the behavior under test is a copy, a timeout and a clear, none
	// of which a test can drive against a real system clipboard — a CI
	// runner may not have one at all, and a developer's must not be
	// clobbered by `go test`. nil means the real thing in each case.
	Clipboard      clipboardPort
	ClipboardTimer newClipboardTimer
	ClipboardWait  func(time.Duration)

	// keeper is the lazily-built clipboardKeeper. It lives on App rather
	// than being made where it's used because a session's exit path has
	// to be able to clear a copy some earlier command made — see
	// App.clipboard and runSession's deferred clear.
	keeper *clipboardKeeper

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

	// --script/--stdin are persistent so Cobra accepts them before the
	// root's own RunE, but they are only ever meaningful on bare `gage`:
	// they select a whole session's input, not one command's. A command
	// given alongside them is rejected below rather than silently
	// winning.
	root.PersistentFlags().StringVar(&app.ScriptFile, "script", "",
		"run session commands from a file instead of a prompt")
	root.PersistentFlags().BoolVar(&app.ScriptStdin, "stdin", false,
		"run session commands read from stdin instead of a prompt")

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
		if err := checkScriptFlags(app, cmd, root); err != nil {
			return err
		}
		if !app.AssumeYes {
			return nil
		}
		// cmd == root is bare `gage`, which dispatches to a session on a
		// terminal; app.Session != nil is a line typed inside one.
		//
		// A *non-interactive* session is the exception, and it is the
		// case --yes exists for: `gage --script deploy.gage --yes` in CI
		// has no human to show a recipient diff to, which is the whole
		// premise of the flag. What the rule below actually excludes is
		// a session with somebody sitting at it.
		if scriptMode(app) {
			app.Prompter = withAssumeYes(app.Prompter)
			return nil
		}
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
	root.AddCommand(newRecoveryCommand(app))
	root.AddCommand(newGitCommand(app))
	root.AddCommand(newAuthCommand(app))
	root.AddCommand(newSyncCommand(app))
	root.AddCommand(newPullCommand(app))
	root.AddCommand(newPushCommand(app))
	root.AddCommand(newLogCommand(app))
	root.AddCommand(newHistoryCommand(app))
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

// errIsTerminal reports whether there is a human watching app.Err,
// falling back to IsTerminal when nothing more specific was supplied.
func (app *App) errIsTerminal() bool {
	if app.IsErrTerminal != nil {
		return app.IsErrTerminal()
	}
	return app.IsTerminal()
}

// clipboard returns this run's clipboardKeeper, building it on first
// use. One per App, so the pending-clear record a `show -c` leaves is
// the same one a session's exit path consults.
func (app *App) clipboard() *clipboardKeeper {
	if app.keeper == nil {
		app.keeper = newClipboardKeeper(app.Clipboard, app.ClipboardTimer)
		// A session's clear fires on a timer, long after the command
		// that copied has returned, so there is nobody left to hand an
		// error to — it goes to stderr the way the session's own exit
		// path reports the same failure.
		app.keeper.report = func(err error) { writeError(app.Err, err) }
	}
	return app.keeper
}
