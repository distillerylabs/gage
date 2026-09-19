package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chzyer/readline"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// errInterrupted is Ctrl-C at the prompt: abandon the line being typed,
// keep the session. It is deliberately not io.EOF, which is Ctrl-D and
// does end the session.
var errInterrupted = errors.New("gage: interrupted")

// lineReader is the REPL's whole relationship with the terminal: read a
// command line, and — because a session owns the input stream for as
// long as it runs — read the two kinds of answer a command can ask a
// human for while it runs.
//
// Those last two are not incidental. chzyer/readline puts the terminal
// in raw mode and reads stdin from a background goroutine for the life
// of its instance, so a passphrase prompt that reached for os.Stdin
// itself mid-session would be racing the line editor for the same bytes.
// Routing every read through this one interface is what keeps that from
// being possible (see runSession, which points the Prompter at these).
type lineReader interface {
	// readLine reads one session command line, rendering prompt first.
	// It returns io.EOF at end of input (Ctrl-D) and errInterrupted on
	// Ctrl-C.
	readLine(prompt string) (string, error)

	// readSecret reads one line without echoing it where the platform
	// allows — a passphrase, or gage insert's value.
	readSecret(prompt string) (string, error)

	// readPlain reads one echoed line — a [y/N] answer, or the number
	// picked from an ambiguous query's candidate list.
	readPlain(prompt string) (string, error)

	close() error
}

// plainLineReader drives a session from an ordinary reader: a test's
// in-memory script, or stdin when it isn't a terminal. There is no line
// editing and no echo to suppress, so a "secret" read here is an
// ordinary one — the same reasoning terminalPrompter.readSecret already
// applies to piped input.
//
// The bufio.Reader persists across calls on purpose: a fresh one per
// call would read ahead and discard whatever followed the line it
// returned, which in a scripted session is the next command.
type plainLineReader struct {
	br  *bufio.Reader
	out io.Writer
}

func newPlainLineReader(in io.Reader, out io.Writer) *plainLineReader {
	return &plainLineReader{br: bufio.NewReader(in), out: out}
}

func (r *plainLineReader) readLine(prompt string) (string, error) {
	_, _ = io.WriteString(r.out, prompt)
	return r.read()
}

func (r *plainLineReader) readSecret(prompt string) (string, error) {
	line, err := r.readLine(prompt)
	if err != nil {
		return "", err
	}
	// Piped input isn't echoed, so without this the next prompt would
	// run on from this one on the same line.
	_, _ = fmt.Fprintln(r.out)
	return line, nil
}

func (r *plainLineReader) readPlain(prompt string) (string, error) {
	return r.readSecret(prompt)
}

func (r *plainLineReader) read() (string, error) {
	line, err := r.br.ReadString('\n')
	if err != nil {
		// A final line with no trailing newline is still a command; only
		// an EOF with nothing before it ends the session.
		if errors.Is(err, io.EOF) && line != "" {
			return strings.TrimRight(line, "\r\n"), nil
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (r *plainLineReader) close() error { return nil }

// historyLimit is how many command lines the line editor keeps for
// recall within one session. The file itself is not truncated to this —
// gage appends to it and never rewrites it, which is also what keeps its
// mode from being reset (see history.go).
const historyLimit = 1000

// ttyLineReader is the real terminal: chzyer/readline, giving line
// editing, history recall, and Ctrl-C/Ctrl-D — on Windows as well as
// Linux/macOS, which is why it was chosen over a hand-rolled x/term
// loop.
type ttyLineReader struct {
	rl *readline.Instance
}

// newTTYLineReader builds the line editor and seeds its recall list from
// gage's own history file. HistoryFile is deliberately left unset:
// history persistence is gage's job, not the library's, because the
// library's own file handling would not honour 0600 (see history.go).
func newTTYLineReader(app *App, hist *history) (*ttyLineReader, error) {
	cfg := &readline.Config{
		HistoryFile:     "",
		HistoryLimit:    historyLimit,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
		Stdout:          app.Out,
		Stderr:          app.Err,
		AutoComplete:    newSessionCompleter(app),
	}
	// readline needs a real file to put into raw mode. Left to itself it
	// raw-modes the *process's* stdin and asks that same descriptor
	// whether it's a terminal — regardless of the reader it was handed.
	// In a real run those are the same file, so pointing all of it at
	// app.In changes nothing there; what it does change is that the
	// terminal this session actually reads is the terminal it configures,
	// which is both the honest wiring and what lets a pty test drive
	// this path at all (see session_pty_test.go).
	if f, ok := app.In.(*os.File); ok {
		fd := int(f.Fd())
		cfg.Stdin = f
		cfg.FuncIsTerminal = func() bool { return true }

		var state *readline.State
		cfg.FuncMakeRaw = func() error {
			s, err := readline.MakeRaw(fd)
			if err != nil {
				return err
			}
			state = s
			return nil
		}
		cfg.FuncExitRaw = func() error {
			if state == nil {
				return nil
			}
			err := readline.Restore(fd, state)
			state = nil
			return err
		}
	}

	rl, err := readline.NewEx(cfg)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: starting the line editor: %w", err))
	}
	for _, line := range hist.lines() {
		_ = rl.SaveHistory(line)
	}
	return &ttyLineReader{rl: rl}, nil
}

func (r *ttyLineReader) readLine(prompt string) (string, error) {
	r.rl.SetPrompt(prompt)
	line, err := r.rl.Readline()
	switch {
	case errors.Is(err, readline.ErrInterrupt):
		return "", errInterrupted
	case err != nil:
		return "", err
	}
	return line, nil
}

func (r *ttyLineReader) readSecret(prompt string) (string, error) {
	// readline builds a fresh config for masked input, and that config
	// decides whether to draw its prompt at all by asking the *process's*
	// stdin/stdout whether they're terminals — not the descriptors this
	// instance was built on. Left alone it silently skips the prompt
	// whenever those differ, so the human is asked for a passphrase by a
	// blank line. Generating the config here and answering that question
	// ourselves is the same wiring correction newTTYLineReader makes for
	// raw mode, and it's why the pty test can see the prompt.
	cfg := r.rl.GenPasswordConfig()
	cfg.Prompt = prompt
	cfg.FuncIsTerminal = func() bool { return true }

	secret, err := r.rl.ReadPasswordWithConfig(cfg)
	if err != nil {
		return "", mapReadlineErr(err)
	}
	return string(secret), nil
}

func (r *ttyLineReader) readPlain(prompt string) (string, error) {
	return r.readLine(prompt)
}

func (r *ttyLineReader) close() error { return r.rl.Close() }

// mapReadlineErr keeps a Ctrl-C inside a prompt from reading as an
// ordinary failure: it's the same "abandon this, keep the session"
// signal it is at the command prompt.
func mapReadlineErr(err error) error {
	if errors.Is(err, readline.ErrInterrupt) {
		return errInterrupted
	}
	return err
}
