package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// runSession is bare `gage`'s session mode: one Session held across many
// commands, driven by a line editor. Everything here is wiring — the
// behavior it drives (unlock once, hold, re-lock on `lock` or on going
// idle) belongs to gage.Session, which is where it's tested. See
// "Session model".
func runSession(app *App) error {
	g, err := readGlobalConfig()
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	settings, err := resolveShellSettings(g.Shell)
	if err != nil {
		return err
	}

	sess := gage.NewSession(gage.SessionConfig{
		Open:        openSessionVault,
		Prompter:    app.Prompter,
		IdleTimeout: settings.idleTimeout,
		// A session starts pointed at the same vault a one-shot command
		// would use, so `show foo` works before any `use`. Naming it
		// doesn't unlock it — the first command that needs a key does.
		Current: g.Current,
		// nil means the real clock; only a test sets this.
		Now: app.Now,
	})
	// Close is what zeroes every held key on the way out, so it runs on
	// every exit path — `exit`, Ctrl-D, or an error out of the loop.
	// Setting App.Session back to nil keeps a returned-to one-shot
	// command (in tests, which reuse an App) from borrowing a closed
	// session.
	defer func() {
		app.Session = nil
		_ = sess.Close()
	}()
	app.Session = sess

	hist, err := openHistory(settings.historyFile)
	if err != nil {
		// Losing line recall is not a reason to refuse a session: say so
		// once and carry on without it.
		writeOut(app.Err, []string{"gage: continuing without command history: " + errorClause(err)})
		hist = &history{}
	}
	defer func() { _ = hist.close() }()

	lr, err := newSessionLineReader(app, hist)
	if err != nil {
		return err
	}
	defer func() { _ = lr.close() }()

	// From here until the session ends, every human-facing read — a
	// passphrase, an insert value, a [y/N], a candidate number — goes
	// through the same reader as the command lines. See lineReader.
	if tp, ok := app.Prompter.(*terminalPrompter); ok {
		defer tp.useSessionReader(lr, app.Out)()
	}

	r := &repl{app: app, sess: sess, lr: lr, hist: hist, settings: settings}
	return r.run()
}

// repl is one running session's loop and the pieces it drives. It's a
// struct rather than a pile of parameters so a test can build one
// directly over its own Session and line reader — which is how "every
// held Identity is closed on the way out" is asserted at this level
// rather than only inside gage.Session.
type repl struct {
	app      *App
	sess     *gage.Session
	lr       lineReader
	hist     *history
	settings shellSettings
}

// newSessionLineReader picks the line editor: chzyer/readline against a
// real terminal, an ordinary buffered reader otherwise.
//
// Both conditions matter. IsTerminal is the dispatch decision
// (Q-ROOT-CMD), while the *os.File check is what the line editor itself
// needs — it puts a real file descriptor into raw mode, so an in-memory
// stdin (a test, a scripted session) has to take the plain path. That is
// also what lets the whole REPL be tested without a pty.
func newSessionLineReader(app *App, hist *history) (lineReader, error) {
	if _, ok := app.In.(*os.File); ok && app.IsTerminal() {
		return newTTYLineReader(app, hist)
	}
	return newPlainLineReader(app.In, app.Out), nil
}

// run reads, records, and runs one line at a time until `exit`, Ctrl-D,
// or end of input — then drops every key the session holds.
//
// The Close here is the one that matters: it runs on every way out of
// the loop, including an error return. runSession defers one too, which
// is redundant by design (Close is idempotent) — it covers the paths
// that fail before there is a loop to run at all.
func (r *repl) run() error {
	defer func() { _ = r.sess.Close() }()

	app, lr, hist, settings := r.app, r.lr, r.hist, r.settings
	for {
		prompt := renderPrompt(settings.prompt, sessionPromptState(app, settings.prompt))
		line, err := lr.readLine(prompt)
		switch {
		case errors.Is(err, errInterrupted):
			// Ctrl-C abandons the line being typed, not the session.
			continue
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: reading a command: %w", err))
		}

		if strings.TrimSpace(line) == "" {
			continue
		}
		// The typed line, and only the typed line — see "Command history
		// must never contain plaintext". A failure to record it is
		// reported but doesn't end the session.
		if err := hist.add(line); err != nil {
			writeError(app.Err, err)
		}
		// One typed line is one command, and the idle timeout is
		// evaluated once at the start of it — a command that makes
		// several Session calls (take the vault, then resolve a query in
		// it) can't have its key dropped between them. See
		// Session.InCommand.
		quit := func() bool {
			defer r.sess.InCommand()()
			return execSessionLine(app, line)
		}()
		if quit {
			return nil
		}
	}
}

// execSessionLine runs one typed line, reporting any failure without
// ending the session. It returns true only for `exit`/`quit`.
func execSessionLine(app *App, line string) bool {
	args, err := splitLine(line)
	if err != nil {
		writeError(app.Err, err)
		return false
	}
	if len(args) == 0 {
		return false
	}
	if ci, ok := findCommand(args[0]); ok && ci.Name == "exit" {
		return true
	}
	if err := runSessionCommand(app, args); err != nil {
		reportAmbiguous(app, err)
		writeError(app.Err, err)
	}
	return false
}

// runSessionCommand dispatches one already-split command line: the
// session's own meta-verbs here, everything else through the same Cobra
// tree one-shot mode runs, with App.Session set so the handlers borrow
// the session's key instead of unlocking their own.
//
// The tree is rebuilt per line rather than reused, because Cobra's flag
// values persist on a command once parsed — a reused tree would carry
// the previous line's --use or -f into this one.
func runSessionCommand(app *App, args []string) error {
	if ci, ok := findCommand(args[0]); ok {
		switch ci.Name {
		case "use":
			return sessionUse(app, args[1:])
		case "lock":
			return sessionLock(app, args[1:])
		case "status":
			return sessionStatus(app, args[1:])
		case "help":
			return sessionHelp(app, args[1:])
		}
	}

	// init and clone create a vault rather than operating on one, which
	// leaves "does the new vault become current?" unanswered — so they
	// report that plainly rather than failing as unknown commands. The
	// rule comes off the registry, not a hardcoded pair, so a later
	// one-shot-only command is covered the day it's registered.
	if ci, ok := findCommand(commandPath(args)); ok && !ci.Availability.SessionVisible() {
		return exitcode.Newf(exitcode.Usage,
			"gage: %s is a one-shot command and isn't available in a session; run `gage %s` from your shell instead",
			ci.Name, ci.Name)
	}

	root := NewRootCmd(app)
	target, _, err := root.Find(args)
	if err != nil || target == root {
		return exitcode.Newf(exitcode.Usage,
			"gage: unknown command %q; type `help` for what's available in a session", args[0])
	}

	root.SetArgs(args)
	return root.Execute()
}

// commandPath joins the first two words of a command line, so a nested
// registry name ("vault list") can be looked up as well as a top-level
// one. Registry names are at most two words today.
func commandPath(args []string) string {
	if len(args) >= 2 {
		if _, ok := findCommand(args[0] + " " + args[1]); ok {
			return args[0] + " " + args[1]
		}
	}
	return args[0]
}

// sessionUse is the `use <vault>` meta-verb: unlock if needed, and make
// it the vault every later bare command operates against.
func sessionUse(app *App, args []string) error {
	if len(args) != 1 {
		return exitcode.New(exitcode.Usage, "gage: usage: use <vault>")
	}
	return app.Session.Use(args[0])
}

// sessionLock is `lock [vault]`: one vault, or — per the M6 plan's
// decision — all of them when no name is given.
func sessionLock(app *App, args []string) error {
	if len(args) > 1 {
		return exitcode.New(exitcode.Usage, "gage: usage: lock [vault]")
	}

	// What gets reported is what was actually holding a key: a bare
	// `lock` over a session with one unlocked vault and three already
	// locked ones should not claim to have locked four.
	targets := args
	if len(targets) == 0 {
		for _, st := range app.Session.Status() {
			if st.Unlocked {
				targets = append(targets, st.Name)
			}
		}
		if len(targets) == 0 {
			writeOut(app.Out, []string{"gage: no vaults are unlocked in this session."})
			return nil
		}
	}

	if err := app.Session.Lock(args...); err != nil {
		return err
	}

	lines := make([]string, 0, len(targets))
	for _, name := range targets {
		lines = append(lines, fmt.Sprintf("%s locked. run `use %s` to unlock again.", name, name))
	}
	writeOut(app.Out, lines)
	return nil
}

// sessionStatus is `status`/`whoami`: every vault touched this session
// and its lock state, with the idle timeout already applied (Status
// evaluates it), so a vault that aged out reads as locked here.
func sessionStatus(app *App, args []string) error {
	if len(args) != 0 {
		return exitcode.New(exitcode.Usage, "gage: usage: status")
	}

	states := app.Session.Status()
	if len(states) == 0 {
		writeOut(app.Out, []string{"gage: no vaults have been unlocked in this session yet."})
		return nil
	}

	lines := make([]string, 0, len(states))
	for _, st := range states {
		marker := "  "
		if st.Current {
			marker = "* "
		}
		state := "locked"
		if st.Unlocked {
			state = "unlocked"
		}
		line := fmt.Sprintf("%s%-12s %-8s", marker, st.Name, state)
		if !st.LastUsed.IsZero() {
			line += fmt.Sprintf("  (last used %s ago)", time.Since(st.LastUsed).Round(time.Second))
		}
		lines = append(lines, strings.TrimRight(line, " "))
	}
	writeOut(app.Out, lines)
	return nil
}

// sessionHelp is in-session `help` and `help <command>`, both rendered
// from the same registry and the same Cobra tree the one-shot surfaces
// use — see Q-HELP-SURFACES.
func sessionHelp(app *App, args []string) error {
	if len(args) == 0 {
		renderSessionHelp(app.Out)
		return nil
	}

	root := NewRootCmd(app)
	target, _, err := root.Find(args)
	if err != nil || target == root {
		return exitcode.Newf(exitcode.Usage,
			"gage: unknown help topic %q; type `help` for what's available in a session", strings.Join(args, " "))
	}
	renderCommandHelp(app.Out, target)
	return nil
}

// openSessionVault is the Session's VaultOpener: the same global-config
// lookup one-shot commands resolve --use through, so both modes agree on
// what a vault name means.
func openSessionVault(name string) (*gage.Vault, error) {
	name, entry, err := resolveVaultEntry(name)
	if err != nil {
		return nil, err
	}
	return &gage.Vault{Name: name, Path: entry.Path}, nil
}

// splitLine splits a typed command line into arguments on whitespace,
// honouring single and double quotes so an entry title with a space in
// it can be typed as `show "AWS root account"`.
//
// There is deliberately no backslash escaping: a session is where
// someone types a Windows path (`insert C:\keys\prod`), and a shell-like
// escape rule would silently eat those separators on the one platform
// this milestone specifically has to behave identically on. Quotes are
// the only grouping mechanism, which is also the rule Windows' own
// command line uses.
func splitLine(line string) ([]string, error) {
	var (
		args  []string
		cur   strings.Builder
		inArg bool
		quote rune
	)
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			inArg = true
		case unicode.IsSpace(r):
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, exitcode.Newf(exitcode.Usage, "gage: unbalanced %c in command line", quote)
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
