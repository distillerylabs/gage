package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
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

// TestExpandGagePathExpandsTilde: a configured path is something a human
// typed into a config file, where `~` is the ordinary way to say "my
// home directory". Both spellings are handled — bare `~`, and `~/` with
// something after it — and a path with no tilde is left alone.
func TestExpandGagePathExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads USERPROFILE on Windows, so both are set to
	// keep this test meaningful on all three CI platforms.
	t.Setenv("USERPROFILE", home)

	t.Run("bare tilde", func(t *testing.T) {
		got, err := expandGagePath("~")
		if err != nil {
			t.Fatal(err)
		}
		if got != home {
			t.Errorf("expandGagePath(\"~\") = %q, want %q", got, home)
		}
	})

	t.Run("tilde with a subpath", func(t *testing.T) {
		got, err := expandGagePath("~/notes/history")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, "notes", "history"); got != want {
			t.Errorf("expandGagePath = %q, want %q", got, want)
		}
	})

	t.Run("a tilde inside the path is not expanded", func(t *testing.T) {
		// Only a leading ~ means home; "a~b" is a filename.
		in := filepath.Join("some", "a~b")
		got, err := expandGagePath(in)
		if err != nil {
			t.Fatal(err)
		}
		if got != in {
			t.Errorf("expandGagePath(%q) = %q, want it unchanged", in, got)
		}
	})
}

// TestVaultIsDirtyIsBestEffort: the {dirty} prompt token has nowhere to
// report an error from, so anything it can't answer reads as clean. A
// vault that isn't registered, and one whose files are gone, both fall
// into that — the next real command will report the problem properly.
func TestVaultIsDirtyIsBestEffort(t *testing.T) {
	isolateXDG(t)

	if got := vaultIsDirty("never-registered"); got {
		t.Error("an unregistered vault rendered as dirty")
	}

	initEntryTestVault(t, "personal")
	if got := vaultIsDirty("personal"); got {
		t.Error("a freshly created vault rendered as dirty")
	}

	// And with the vault's files removed, it still answers rather than
	// failing the prompt render.
	entry := readGlobalConfigForTest(t).Vaults["personal"]
	if err := os.RemoveAll(entry.Path); err != nil {
		t.Fatal(err)
	}
	if got := vaultIsDirty("personal"); got {
		t.Error("a vault whose files are gone rendered as dirty")
	}
}
