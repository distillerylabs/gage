package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// historyFileMode is the only mode a history file is ever created with.
// It records what a human typed at a `gage` prompt — entry titles used
// as queries, vault names — which is device-local state nobody else on
// the machine has any business reading. See "Command history must never
// contain plaintext" for the other half of that rule: decrypted values
// never reach this file at all.
const historyFileMode = 0o600

// history is the session's readline-style command history. gage owns the
// file rather than letting the readline library manage one, for a reason
// the mode constant above states: chzyer/readline creates (and, when it
// compacts, recreates) its history file 0666-before-umask, which on a
// typical machine lands 0644. A gage history file is 0600 or it doesn't
// exist.
//
// Every line written here is a line the human typed at the prompt.
// Nothing renders a decrypted value into it, because nothing but the
// typed line is ever passed to add.
type history struct {
	path string
	f    *os.File
}

// openHistory opens (creating if needed) the history file at path, along
// with its directory. A path of "" disables history entirely and yields
// a usable no-op — which is also what the REPL falls back to when the
// file can't be opened at all, since losing line recall is not a reason
// to refuse to start a session.
func openHistory(path string) (*history, error) {
	if path == "" {
		return &history{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: creating history directory: %w", err))
	}
	// #nosec G304 -- path is the operator's own configured history file
	// (or gage's default under $GAGE_STATE), not attacker-controlled.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, historyFileMode)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: opening history file: %w", err))
	}
	// O_CREATE's mode only applies when the file is created, so a file
	// that already existed with a looser mode is tightened here rather
	// than trusted. On Windows this narrows to the read-only bit, which
	// is the same guarantee every other 0600 file gage writes has.
	if err := os.Chmod(path, historyFileMode); err != nil {
		_ = f.Close()
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: securing history file: %w", err))
	}
	return &history{path: path, f: f}, nil
}

// add appends one typed command line. Blank lines are dropped so that
// bare Enter at the prompt doesn't pad the file.
//
// The write error is returned rather than dropped, but the REPL
// deliberately treats it as non-fatal: a full disk should not end a
// session that is otherwise working.
func (h *history) add(line string) error {
	if h == nil || h.f == nil {
		return nil
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if _, err := h.f.WriteString(line + "\n"); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: writing history: %w", err))
	}
	return nil
}

// lines returns the history file's existing contents, oldest first, for
// preloading into the line editor's in-memory recall list. A missing or
// unreadable file yields no lines rather than an error: history is a
// convenience, and starting a session must not depend on it.
func (h *history) lines() []string {
	if h == nil || h.path == "" {
		return nil
	}
	// #nosec G304 -- same operator-owned path as openHistory's.
	f, err := os.Open(h.path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			out = append(out, line)
		}
	}
	if scanner.Err() != nil {
		return nil
	}
	return out
}

func (h *history) close() error {
	if h == nil || h.f == nil {
		return nil
	}
	err := h.f.Close()
	h.f = nil
	return err
}
