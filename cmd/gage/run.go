package main

import (
	"io"

	"github.com/denmark/gage/internal/gage/exitcode"
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
	app.Prompter = newTerminalPrompter(in, errW)
	return runApp(app, args)
}

// runApp is Run with the App already assembled, so tests can supply a
// fake Prompter and in-memory IO without Run having to grow a parameter
// for every seam.
func runApp(app *App, args []string) int {
	root := NewRootCmd(app)
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return int(exitcode.Success)
	}

	// If reporting the error itself fails there is nowhere left to
	// report that to, and the exit code below still carries the outcome.
	writeError(app.Err, err)

	if exitcode.IsCoded(err) {
		return int(exitcode.CodeOf(err))
	}
	// Anything that reaches here without an explicit code is Cobra's own
	// usage/parsing rejection (unknown command, bad flag, ...) — genuine
	// internal errors from our own handlers always construct an
	// explicit exitcode.New/Wrap instead, so falling back to Usage
	// (rather than exitcode.CodeOf's own Internal default) is correct
	// here specifically. See the taxonomy's "no bare, unenumerated exit
	// status" rule.
	return int(exitcode.Usage)
}
