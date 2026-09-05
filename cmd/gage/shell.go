package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// defaultPromptTemplate renders as a bare "gage> " until a vault is
// selected and "personal🔓 gage> " once one is, matching the transcript
// in "Session model". The leading tokens collapsing to nothing is why
// renderPrompt trims leading spaces rather than the template carrying
// two spellings of itself.
const defaultPromptTemplate = "{vault}{lock} gage> "

// defaultIdleTimeout matches the value the design doc's own [shell]
// example carries. It is a re-lock, not a logout: the process stays
// alive and the next data command re-prompts. See "Why an idle timeout
// still matters despite process-scoped keys".
const defaultIdleTimeout = 10 * time.Minute

// lockedGlyph/unlockedGlyph are the {lock} token's two renderings, from
// the design doc's prompt example.
const (
	unlockedGlyph = "🔓"
	lockedGlyph   = "🔒"
)

// shellSettings is [shell] from global config, resolved: defaults filled
// in, the timeout parsed, the history path expanded.
type shellSettings struct {
	prompt      string
	idleTimeout time.Duration
	historyFile string
}

// resolveShellSettings turns the raw [shell] table into settings the
// REPL can use. A malformed idle_timeout is a usage error rather than a
// silently-ignored field: a typo'd "10min" that quietly meant "never
// re-lock" would weaken exactly the protection the setting exists for.
func resolveShellSettings(sh config.Shell) (shellSettings, error) {
	s := shellSettings{prompt: defaultPromptTemplate, idleTimeout: defaultIdleTimeout}

	if sh.Prompt != "" {
		s.prompt = sh.Prompt
	}

	if sh.IdleTimeout != "" {
		d, err := time.ParseDuration(sh.IdleTimeout)
		if err != nil {
			return shellSettings{}, exitcode.Newf(exitcode.Usage,
				"gage: [shell].idle_timeout %q is not a duration (e.g. \"10m\"): %v", sh.IdleTimeout, err)
		}
		if d < 0 {
			return shellSettings{}, exitcode.Newf(exitcode.Usage,
				"gage: [shell].idle_timeout %q is negative; use \"0\" to disable the idle re-lock", sh.IdleTimeout)
		}
		s.idleTimeout = d
	}

	path, err := resolveHistoryPath(sh.HistoryFile)
	if err != nil {
		return shellSettings{}, err
	}
	s.historyFile = path
	return s, nil
}

// resolveHistoryPath expands the configured history_file, defaulting to
// $GAGE_STATE/history. The design doc writes the setting with those
// $GAGE_* names in it, so they're expanded here rather than being taken
// literally — a path with "$GAGE_STATE" in it would otherwise be created
// as a directory of that name.
func resolveHistoryPath(configured string) (string, error) {
	if configured == "" {
		dir, err := xdgpaths.StateDir()
		if err != nil {
			return "", exitcode.Wrap(exitcode.Internal, err)
		}
		return filepath.Join(dir, "history"), nil
	}
	return expandGagePath(configured)
}

// expandGagePath resolves the $GAGE_CONFIG/$GAGE_DATA/$GAGE_STATE names
// the design doc uses for gage's three XDG roots, plus a leading ~.
func expandGagePath(path string) (string, error) {
	roots := []struct {
		token   string
		resolve func() (string, error)
	}{
		{"$GAGE_CONFIG", xdgpaths.ConfigDir},
		{"$GAGE_DATA", xdgpaths.DataDir},
		{"$GAGE_STATE", xdgpaths.StateDir},
	}
	for _, r := range roots {
		if !strings.Contains(path, r.token) {
			continue
		}
		dir, err := r.resolve()
		if err != nil {
			return "", exitcode.Wrap(exitcode.Internal, err)
		}
		path = strings.ReplaceAll(path, r.token, dir)
	}

	if path == "~" || strings.HasPrefix(path, "~"+string(os.PathSeparator)) || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: resolving ~: %w", err))
		}
		path = filepath.Join(home, strings.TrimPrefix(path[1:], "/"))
	}
	return filepath.FromSlash(path), nil
}

// promptState is everything the prompt template can render: which vault
// is current, whether its key is held, and whether its working tree has
// uncommitted changes.
type promptState struct {
	vault    string
	unlocked bool
	dirty    bool
}

// renderPrompt substitutes the {vault}/{lock}/{dirty} tokens.
//
// Leading whitespace is trimmed afterwards so the default template reads
// as a bare "gage> " before any vault is selected — with no vault, every
// token renders empty and the separator space would otherwise be the
// first thing on the line. A template that puts literal text before the
// tokens (the doc's "[{vault}{lock}] gage> ") is unaffected.
func renderPrompt(template string, st promptState) string {
	lock := ""
	switch {
	case st.vault == "":
	case st.unlocked:
		lock = unlockedGlyph
	default:
		lock = lockedGlyph
	}
	dirty := ""
	if st.dirty {
		dirty = "*"
	}

	out := strings.NewReplacer(
		"{vault}", st.vault,
		"{lock}", lock,
		"{dirty}", dirty,
	).Replace(template)
	return strings.TrimLeft(out, " ")
}

// wantsDirty reports whether a template references {dirty} at all, so
// the prompt only pays for a git status when it has somewhere to show
// one.
func wantsDirty(template string) bool { return strings.Contains(template, "{dirty}") }

// sessionPromptState reads the current prompt state off the Session.
//
// Lock state comes from Status, which applies the idle timeout first —
// that's what makes the prompt drop to 🔒 on its own when a session has
// been sitting idle, rather than only after the next command discovers
// it.
func sessionPromptState(app *App, template string) promptState {
	st := promptState{vault: app.Session.Current()}
	if st.vault == "" {
		return st
	}
	for _, v := range app.Session.Status() {
		if v.Name == st.vault {
			st.unlocked = v.Unlocked
			break
		}
	}
	if wantsDirty(template) {
		st.dirty = vaultIsDirty(st.vault)
	}
	return st
}

// vaultIsDirty answers the {dirty} token. It's best-effort on purpose:
// an unreadable or not-yet-registered vault renders as clean rather than
// failing a prompt render, since there is nowhere to report an error
// from a prompt and a broken vault will report itself on the next real
// command.
func vaultIsDirty(name string) bool {
	_, entry, err := resolveVaultEntry(name)
	if err != nil || entry.Type != gage.TypeGit {
		return false
	}
	clean, err := gitrepo.IsClean(entry.Path)
	if err != nil {
		return false
	}
	return !clean
}
