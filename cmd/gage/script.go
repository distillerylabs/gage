package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
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
	sess, _, closeSession, err := openSessionRun(app, scriptPrompter(app, source))
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
	if pass, ok := os.LookupEnv(envPassphraseVar); ok {
		return &envPrompter{Prompter: app.Prompter, passphrase: pass}
	}
	if source != stdinSourceName && app.IsTerminal() {
		return app.Prompter
	}
	return refusingPrompter{Prompter: app.Prompter}
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
		return p.Prompter.Unlock(req)
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
}

func (p refusingPrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	if req.Purpose == gage.PurposeCreate {
		return p.Prompter.Unlock(req)
	}
	return gage.UnlockResponse{}, exitcode.Newf(exitcode.LockedOrAuth,
		"gage: no way to unlock vault %q without a terminal: set %s", req.Vault, envPassphraseVar)
}

func (p refusingPrompter) inner() gage.Prompter { return p.Prompter }

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
