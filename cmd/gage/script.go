package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// envPassphraseVar is where a non-interactive session gets its
// passphrase. See "Non-interactive session mode" and the M12 plan's
// decision on it.
const envPassphraseVar = "GAGE_PASSPHRASE"

// runScriptSession is `--script FILE` and `--stdin`: one Session driven
// by a list of commands instead of by a human at a prompt.
//
// It is the same Session, holding keys the same way and dropping them at
// the same point — "unlock once, do several things, then gone" without a
// TTY. What differs from the interactive loop is deliberate and is the
// whole of this function:
//
//   - It stops at the first failing command instead of reporting and
//     carrying on. A script has nobody to notice a line that failed
//     halfway down, and continuing past it would run the rest against a
//     state the script's author never anticipated.
//   - It writes nothing to the history file — it opens none at all.
//     The history file is a record of what a human typed at a prompt
//     (see "Command history must never contain plaintext"); a script's
//     lines were never typed, and appending them would mix a machine's
//     command list into a human's recall buffer.
func runScriptSession(app *App, lines io.Reader, source string) error {
	// The non-interactive passphrase policy is installed on the App, not
	// just handed to the Session, because not every prompt in a scripted
	// run goes through the Session. `identity add` creates a key rather
	// than opening a vault and asks app.Prompter directly — and that
	// prompter reads the very stream these command lines are arriving
	// on. A policy that covered only Session unlocks would leave exactly
	// the commands that prompt for something *new* reading the script.
	prompter := scriptPrompter(app, source)
	restore := app.Prompter
	app.Prompter = prompter
	defer func() { app.Prompter = restore }()

	sess, _, closeSession, err := openSessionRun(app, prompter)
	if err != nil {
		return err
	}
	defer closeSession()

	sc := bufio.NewScanner(lines)
	// A gage command line is a title, a query and some flags. The
	// default 64KB token limit is far past anything that can be one, and
	// raising it would only make a pathological input allocate more.
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		// Blank lines and # comments, so a checked-in .gage script can
		// be commented like any other file.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		quit, err := func() (bool, error) {
			defer sess.InCommand()()
			return execSessionLine(app, line)
		}()
		if err != nil {
			// The line number is the only way to locate a failure in a
			// file nobody watched run — and it is added without
			// disturbing the error's own exit code, which is what the
			// process exits with.
			reportAmbiguous(app, err)
			return scriptError(err, source, n, line)
		}
		if quit {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: reading %s: %w", source, err))
	}
	return nil
}

// scriptError wraps a failed line's error with where it came from,
// preserving the original exit code so a script fails the way the
// command it ran would have.
func scriptError(err error, source string, n int, line string) error {
	return exitcode.Wrap(exitcode.CodeOf(err), fmt.Errorf("%s:%d: %s: %w", source, n, line, err))
}

// scriptPrompter decides where a non-interactive run's passphrase comes
// from, in the fixed order the M12 plan settled:
//
//  1. GAGE_PASSPHRASE, if set.
//  2. Otherwise a prompt, but only when there is a terminal to prompt on
//     *and* the command lines aren't coming from stdin — `--script FILE`
//     leaves stdin free for a human to answer on, `--stdin` has already
//     spent it on the command stream and can never prompt.
//  3. Otherwise a refusal that names the variable.
//
// The whole of it lives here rather than in internal/gage: "where does a
// secret come from when nobody is watching" is frontend policy, the same
// as --yes and maxPassphraseAttempts. The library still just asks
// through a Prompter and has no idea an environment exists.
func scriptPrompter(app *App, source string) gage.Prompter {
	// Whether a human can be asked anything at all. It is the same
	// question for a new identity's passphrase as for an unlock, so it
	// is answered once here and handed to both decorators.
	canPrompt := source != stdinSourceName && app.IsTerminal()

	// Where a PurposeCreate request goes. Neither decorator may answer
	// one itself (see envPrompter.Unlock), so it is passed through to a
	// human when there is one — and refused outright when there is not,
	// rather than passed to a prompter whose stdin is the command stream
	// or an exhausted pipe. See noCreatePrompter.
	create := app.Prompter
	if !canPrompt {
		create = noCreatePrompter{Prompter: app.Prompter}
	}

	if pass, ok := os.LookupEnv(envPassphraseVar); ok {
		return &envPrompter{Prompter: app.Prompter, create: create, passphrase: pass}
	}
	if canPrompt {
		return app.Prompter
	}
	return refusingPrompter{Prompter: app.Prompter, create: create}
}

// stdinSourceName is what --stdin's command stream is called in
// messages, and the value scriptPrompter recognises as "stdin is already
// spoken for".
const stdinSourceName = "<stdin>"

// envPrompter answers unlock requests from GAGE_PASSPHRASE.
//
// It is a decorator over the real prompter, exactly like
// assumeYesPrompter, and for the same reason: what changes is who
// answers one particular question, not what the question means.
type envPrompter struct {
	gage.Prompter
	// create answers PurposeCreate requests, which this decorator never
	// answers itself. See scriptPrompter.
	create     gage.Prompter
	passphrase string
}

func (p *envPrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	// Never for a *new* identity. There is nothing to check a
	// create-purpose answer against, so a typo in the variable would be
	// unrecoverable — and unlike an unlock, nobody would find out until
	// the next time the vault had to be opened. `init` and `identity
	// add` keep asking a human, twice. UnlockPurpose already draws this
	// line, so this is a switch on data that is there rather than a
	// special case invented here.
	if req.Purpose == gage.PurposeCreate {
		return p.create.Unlock(req)
	}
	// The variable gives the same answer every time, so a second attempt
	// could only fail identically. Refusing it is how this frontend
	// expresses "no retry", the same mechanism maxPassphraseAttempts
	// uses to express "three".
	if req.Attempt > 1 {
		return gage.UnlockResponse{}, exitcode.Newf(exitcode.LockedOrAuth,
			"gage: %s did not unlock vault %q", envPassphraseVar, req.Vault)
	}
	return gage.UnlockResponse{Kind: gage.KindPassphrase, Passphrase: p.passphrase}, nil
}

// inner exposes the wrapped prompter for the few places that need the
// concrete terminalPrompter. See terminalPrompterOf.
func (p *envPrompter) inner() gage.Prompter { return p.Prompter }

// refusingPrompter is what a non-interactive run with no passphrase
// source unlocks through: it doesn't.
//
// This is the fail-fast half of the decision. The alternative — letting
// the ordinary prompter read a stdin that is either exhausted or is the
// command stream — produces either a hang or an empty passphrase
// reported as a wrong one, and both are worse than being told plainly
// that there was nowhere to get a passphrase from.
type refusingPrompter struct {
	gage.Prompter
	// create answers PurposeCreate, which is a different question with a
	// different answer. See scriptPrompter.
	create gage.Prompter
}

func (p refusingPrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	if req.Purpose == gage.PurposeCreate {
		return p.create.Unlock(req)
	}
	return gage.UnlockResponse{}, exitcode.Newf(exitcode.LockedOrAuth,
		"gage: no way to unlock vault %q without a terminal: set %s", req.Vault, envPassphraseVar)
}

func (p refusingPrompter) inner() gage.Prompter { return p.Prompter }

// noCreatePrompter refuses to invent a passphrase for a *new* identity
// when there is no human to type one.
//
// GAGE_PASSPHRASE deliberately does not answer PurposeCreate: a new
// identity's passphrase can't be checked against anything, so a typo is
// unrecoverable. That carve-out passes the request through to a human —
// which works under `--script FILE` on a terminal, and is exactly wrong
// under `--stdin`, where the ordinary prompter's stdin *is* the command
// stream. Left to fall through, `identity add` on a script line prints
// "Choose a passphrase:" into the output and then reads the script: it
// either hits EOF and reports it as a failed read, or — once the script
// is longer than the scanner's read-ahead — silently takes a command
// line as the passphrase protecting a new device key, which nobody will
// ever be able to type again.
//
// Refusing is the same fail-fast the rest of this file applies to
// unlocks, for the same reason: never a read that cannot be answered.
// It reports LockedOrAuth to match refusingPrompter, its sibling for the
// unlock case — both are "there is nowhere to get a passphrase from
// here", which is what that code means. The choice is not really this
// type's to make in any case: requestPassphrase normalises every
// prompter failure to LockedOrAuth before a caller sees it, so returning
// anything else here would only be overridden on the way out.
type noCreatePrompter struct {
	gage.Prompter
}

func (p noCreatePrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	if req.Purpose != gage.PurposeCreate {
		return p.Prompter.Unlock(req)
	}
	return gage.UnlockResponse{}, exitcode.Newf(exitcode.LockedOrAuth,
		"gage: creating an identity for vault %q needs a passphrase typed by a human, "+
			"and this run has no terminal to ask on; run it from a shell instead "+
			"(%s is deliberately not used for a new identity)", req.Vault, envPassphraseVar)
}

func (p noCreatePrompter) inner() gage.Prompter { return p.Prompter }

// openScriptFile opens a --script file for reading.
func openScriptFile(path string) (*os.File, error) {
	// #nosec G304 -- path is the operator's own script, named on their
	// own command line; this is the same trust level as $EDITOR or the
	// configured history file.
	f, err := os.Open(path)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Usage, fmt.Errorf("gage: opening script: %w", err))
	}
	return f, nil
}

// scriptMode reports whether this run is a non-interactive session.
func scriptMode(app *App) bool { return app.ScriptFile != "" || app.ScriptStdin }

// checkScriptFlags rejects the two ways --script/--stdin can be asked
// for something they don't mean.
//
// Both at once has no answer — there would be two command streams — and
// either alongside a subcommand is a category error: they choose where a
// whole session's lines come from, so `gage --stdin show foo` is asking
// for a session and a one-shot command in the same breath. Refusing is
// better than picking one, since either choice would silently ignore
// half of what was typed.
func checkScriptFlags(app *App, cmd, root *cobra.Command) error {
	if !scriptMode(app) {
		return nil
	}
	if app.ScriptFile != "" && app.ScriptStdin {
		return exitcode.New(exitcode.Usage, "gage: --script and --stdin are mutually exclusive")
	}
	if cmd != root {
		return exitcode.Newf(exitcode.Usage,
			"gage: --script/--stdin run a whole session's commands, so they can't be combined "+
				"with the %q command; put %s on a line in the script instead", cmd.Name(), cmd.Name())
	}
	if app.Session != nil {
		return exitcode.New(exitcode.Usage,
			"gage: --script/--stdin start a session and aren't available inside one")
	}
	return nil
}

// dispatchScript runs whichever non-interactive source was named.
func dispatchScript(app *App) error {
	if app.ScriptStdin {
		return runScriptSession(app, app.In, stdinSourceName)
	}
	f, err := openScriptFile(app.ScriptFile)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return runScriptSession(app, f, app.ScriptFile)
}
