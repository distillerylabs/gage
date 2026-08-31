package main

import (
	"bytes"
	"strings"
	"testing"
)

func testApp() *App {
	return &App{
		Out:        &bytes.Buffer{},
		Err:        &bytes.Buffer{},
		In:         strings.NewReader(""),
		Build:      BuildInfo{Version: "v1.2.3", Commit: "abcdef1"},
		IsTerminal: func() bool { return false },
	}
}

// TestRegistryCompleteness walks the real Cobra command tree and fails
// if any registered Cobra command is absent from the registry, or any
// registry entry has no corresponding command. This is what keeps every
// later milestone honest without needing a "remember to register"
// reminder — a command added later that skips the registry fails this
// test immediately, and a registry entry nothing ever wires up to a real
// command fails it too.
//
// Cobra's own "help" command is exempted by name: it's CLI/framework
// plumbing (wired by installHelp so `gage help <unknown>` can fail with
// the usage exit code, which Cobra's default help command doesn't), not
// a vault-domain command the design doc's command reference describes —
// see registry.go's doc comment on the registry itself. Shell completion
// is disabled outright (CompletionOptions.DisableDefaultCmd) so there's
// nothing else to exempt.
func TestRegistryCompleteness(t *testing.T) {
	app := testApp()
	root := NewRootCmd(app)
	root.InitDefaultHelpCmd()

	seen := map[string]bool{}
	for _, c := range root.Commands() {
		if c.Name() == "help" {
			continue
		}
		seen[c.Name()] = true
		if _, ok := findCommand(c.Name()); !ok {
			t.Errorf("Cobra command %q exists with no registry entry", c.Name())
		}
	}

	for _, ci := range registry {
		if !seen[ci.Name] {
			t.Errorf("registry entry %q has no corresponding Cobra command", ci.Name)
		}
	}
}

func TestFindCommandResolvesAliases(t *testing.T) {
	ci, ok := findCommand("whoami")
	if !ok {
		t.Fatal("findCommand(\"whoami\") not found")
	}
	if ci.Name != "status" {
		t.Errorf("findCommand(\"whoami\").Name = %q, want %q", ci.Name, "status")
	}
}

func TestFindCommandUnknown(t *testing.T) {
	if _, ok := findCommand("not-a-command"); ok {
		t.Error("findCommand should not find a nonexistent command")
	}
}

// TestAliasResolvesToSameCommandAtCLILevel checks alias resolution where
// it actually matters — an invocation of the alias must behave exactly
// like the canonical name, not just agree with it in registry data (that
// half is TestFindCommandResolvesAliases above). status/whoami is M0's
// one aliased command: both are session-only, so both should fail
// one-shot mode with the identical message.
func TestAliasResolvesToSameCommandAtCLILevel(t *testing.T) {
	canonical := runCLI(t, []string{"status"}, "")
	alias := runCLI(t, []string{"whoami"}, "")

	if canonical.Code != alias.Code {
		t.Errorf("exit code: status=%d whoami=%d, want equal", canonical.Code, alias.Code)
	}
	if canonical.Stderr != alias.Stderr {
		t.Errorf("stderr differs between canonical and alias:\nstatus: %q\nwhoami: %q", canonical.Stderr, alias.Stderr)
	}
}
