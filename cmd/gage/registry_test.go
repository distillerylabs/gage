package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

// commandTreePaths walks the whole Cobra tree under root and returns
// every command's path relative to root, space-separated — "use",
// "vault list", "git set-remote". Recursion is the point: from M1 on,
// most commands are nested (vault list/info/remove/set-default,
// identity add/list, recipient add/remove/list/verify, git set-remote,
// auth login/status/logout), and a depth-1 walk would see only the
// "vault"/"recipient"/"git" parents and silently pass while every leaf
// under them went unregistered.
//
// A parent that exists purely to group subcommands (no RunE of its own)
// is not itself a command a user can invoke, so it isn't required to be
// in the registry — but it is still descended into.
func commandTreePaths(root *cobra.Command) []string {
	var paths []string
	var walk func(c *cobra.Command, prefix []string)
	walk = func(c *cobra.Command, prefix []string) {
		for _, sub := range c.Commands() {
			path := append(append([]string{}, prefix...), sub.Name())
			runnable := sub.RunE != nil || sub.Run != nil
			if runnable {
				paths = append(paths, strings.Join(path, " "))
			}
			walk(sub, path)
		}
	}
	walk(root, nil)
	return paths
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
	for _, path := range commandTreePaths(root) {
		if path == "help" {
			continue
		}
		seen[path] = true
		if _, ok := findCommand(path); !ok {
			t.Errorf("Cobra command %q exists with no registry entry", path)
		}
	}

	for _, ci := range registry {
		if !seen[ci.Name] {
			t.Errorf("registry entry %q has no corresponding Cobra command", ci.Name)
		}
	}
}

// TestRegistryCompletenessDetectsNestedDrift is a meta-test: it proves
// TestRegistryCompleteness would actually catch an unregistered *nested*
// command, which is the shape every command from M1 on takes. Without
// this, the completeness test could quietly regress to a depth-1 walk
// (as it originally was) and still pass its own assertions, since M0's
// only commands happen to be top-level.
func TestRegistryCompletenessDetectsNestedDrift(t *testing.T) {
	app := testApp()
	root := NewRootCmd(app)

	// A name guaranteed not to collide with any real, already-registered
	// nested command (M1 on genuinely has "vault list" registered, so
	// reusing that name here would no longer prove anything).
	parent := &cobra.Command{Use: "zzz-fake-group", Short: "fake group for this test only"}
	parent.AddCommand(&cobra.Command{
		Use:   "leaf",
		Short: "fake leaf for this test only",
		RunE:  func(cmd *cobra.Command, args []string) error { return nil },
	})
	root.AddCommand(parent)

	paths := commandTreePaths(root)

	var sawLeaf, sawParent bool
	for _, p := range paths {
		if p == "zzz-fake-group leaf" {
			sawLeaf = true
		}
		if p == "zzz-fake-group" {
			sawParent = true
		}
	}
	if !sawLeaf {
		t.Errorf("tree walk missed nested command %q; paths = %v", "zzz-fake-group leaf", paths)
	}
	if sawParent {
		t.Errorf("tree walk treated non-runnable group %q as an invocable command; paths = %v", "zzz-fake-group", paths)
	}
	if _, ok := findCommand("zzz-fake-group leaf"); ok {
		t.Error("findCommand resolved \"zzz-fake-group leaf\", which is not in the registry — the drift this test simulates would go undetected")
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
