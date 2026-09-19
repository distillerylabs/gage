package main

import (
	"io"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// Run builds the command tree, executes it against args, and returns the
// process exit code to use — main() is the only caller that turns this
// into an actual os.Exit, so every other test can call Run directly and
// inspect the int without a subprocess.
func Run(args []string, in io.Reader, out, errW io.Writer, isTerminal func() bool, build BuildInfo) int {
	app := &App{Out: out, Err: errW, In: in, Build: build, IsTerminal: isTerminal}
	// Prompts and warnings go to stderr, not stdout: a `gage show foo >
	// secret.txt` must put only the secret in the file, while the human
	// still sees the passphrase prompt they're answering.
	prompter := newTerminalPrompter(in, errW)
	// Whether there is anyone to ask. Only conflict resolution consults
	// it, and it is what makes `gage sync` refuse to pick a version of a
	// secret in a script or in CI rather than choosing one silently.
	prompter.interactive = isTerminal()
	app.Prompter = prompter
	return runApp(app, args)
}

// runApp is Run with the App already assembled, so tests can supply a
// fake Prompter and in-memory IO without Run having to grow a parameter
// for every seam.
func runApp(app *App, args []string) int {
	root := NewRootCmd(app)
	root.SetArgs(args)

	// --yes wraps app.Prompter from the root's PersistentPreRunE, since
	// that is the first point the flag has been parsed. This is the
	// other half of that: the wrap lasts exactly one execution.
	defer scopeAssumeYes(app)()

	cmd, err := root.ExecuteC()
	if err == nil {
		return int(exitcode.Success)
	}

	// Anything that reaches here without an explicit code is Cobra's own
	// usage/parsing rejection (unknown command, bad flag, wrong argument
	// count, ...) — genuine internal errors from our own handlers always
	// construct an explicit exitcode.New/Wrap instead. usageError folds
	// in the failing command's own usage text and codes it Usage, so an
	// operator gets more than just "accepts 1 arg(s), received 0" — see
	// the taxonomy's "no bare, unenumerated exit status" rule.
	if !exitcode.IsCoded(err) {
		err = usageError(root, cmd, err)
	}

	// If reporting the error itself fails there is nowhere left to
	// report that to, and the exit code below still carries the outcome.
	writeError(app.Err, err)

	return int(exitcode.CodeOf(err))
}
