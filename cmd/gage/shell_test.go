package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

func TestResolveShellSettingsDefaults(t *testing.T) {
	isolateXDG(t)

	got, err := resolveShellSettings(config.Shell{})
	if err != nil {
		t.Fatalf("resolveShellSettings: %v", err)
	}
	if got.prompt != defaultPromptTemplate {
		t.Errorf("prompt = %q, want the default %q", got.prompt, defaultPromptTemplate)
	}
	if got.idleTimeout != defaultIdleTimeout {
		t.Errorf("idleTimeout = %v, want the default %v", got.idleTimeout, defaultIdleTimeout)
	}

	stateDir, err := xdgpaths.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(stateDir, "history"); got.historyFile != want {
		t.Errorf("historyFile = %q, want %q", got.historyFile, want)
	}
}

func TestResolveShellSettingsParsesIdleTimeout(t *testing.T) {
	isolateXDG(t)

	got, err := resolveShellSettings(config.Shell{IdleTimeout: "45s"})
	if err != nil {
		t.Fatalf("resolveShellSettings: %v", err)
	}
	if got.idleTimeout != 45*time.Second {
		t.Errorf("idleTimeout = %v, want 45s", got.idleTimeout)
	}
}

// TestResolveShellSettingsRejectsAMalformedIdleTimeout: a typo must not
// silently mean "never re-lock", which is what ignoring the field would
// amount to.
func TestResolveShellSettingsRejectsAMalformedIdleTimeout(t *testing.T) {
	isolateXDG(t)

	for _, bad := range []string{"10min", "later", "-5m"} {
		t.Run(bad, func(t *testing.T) {
			if _, err := resolveShellSettings(config.Shell{IdleTimeout: bad}); err == nil {
				t.Fatalf("idle_timeout %q was accepted", bad)
			} else if exitcode.CodeOf(err) != exitcode.Usage {
				t.Errorf("exit code for %q = %v, want Usage", bad, exitcode.CodeOf(err))
			}
		})
	}
}

// TestZeroIdleTimeoutDisablesTheRelock: "0" is how an operator turns the
// idle re-lock off, distinct from leaving the field unset (which takes
// the default).
func TestZeroIdleTimeoutDisablesTheRelock(t *testing.T) {
	isolateXDG(t)

	got, err := resolveShellSettings(config.Shell{IdleTimeout: "0"})
	if err != nil {
		t.Fatalf("resolveShellSettings: %v", err)
	}
	if got.idleTimeout != 0 {
		t.Errorf("idleTimeout = %v, want 0 (disabled)", got.idleTimeout)
	}
}

// TestHistoryPathExpandsGageRoots: the design doc writes history_file as
// "$GAGE_STATE/history", so those names resolve rather than being taken
// as a literal directory called "$GAGE_STATE".
func TestHistoryPathExpandsGageRoots(t *testing.T) {
	isolateXDG(t)

	got, err := resolveShellSettings(config.Shell{HistoryFile: "$GAGE_STATE/gage-history"})
	if err != nil {
		t.Fatalf("resolveShellSettings: %v", err)
	}
	stateDir, err := xdgpaths.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(stateDir, "gage-history"); got.historyFile != want {
		t.Errorf("historyFile = %q, want %q", got.historyFile, want)
	}
	if strings.Contains(got.historyFile, "$GAGE_STATE") {
		t.Errorf("historyFile still carries an unexpanded root: %q", got.historyFile)
	}
}

// TestHistoryFileHonoursAnAbsolutePath: an operator who names a path
// outright gets exactly that path.
func TestHistoryFileHonoursAnAbsolutePath(t *testing.T) {
	isolateXDG(t)
	want := filepath.Join(t.TempDir(), "elsewhere", "history")

	got, err := resolveShellSettings(config.Shell{HistoryFile: want})
	if err != nil {
		t.Fatalf("resolveShellSettings: %v", err)
	}
	if got.historyFile != want {
		t.Errorf("historyFile = %q, want %q", got.historyFile, want)
	}

	// And it's actually usable: openHistory creates the directory it
	// names rather than failing on a missing parent.
	h, err := openHistory(got.historyFile)
	if err != nil {
		t.Fatalf("openHistory: %v", err)
	}
	if err := h.add("show something"); err != nil {
		t.Fatalf("history add: %v", err)
	}
	if err := h.close(); err != nil {
		t.Fatal(err)
	}
	if lines := (&history{path: want}).lines(); len(lines) != 1 || lines[0] != "show something" {
		t.Errorf("history round trip = %v, want [\"show something\"]", lines)
	}
}

// TestHistoryDisabledWithoutAPath: a no-op history is a usable one, so
// the REPL can carry on when the file can't be opened.
func TestHistoryDisabledWithoutAPath(t *testing.T) {
	h, err := openHistory("")
	if err != nil {
		t.Fatalf("openHistory(\"\"): %v", err)
	}
	if err := h.add("show something"); err != nil {
		t.Errorf("add on a disabled history: %v", err)
	}
	if lines := h.lines(); lines != nil {
		t.Errorf("lines on a disabled history = %v, want none", lines)
	}
	if err := h.close(); err != nil {
		t.Errorf("close on a disabled history: %v", err)
	}
}
