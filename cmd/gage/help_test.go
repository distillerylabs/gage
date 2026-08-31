package main

import (
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/exitcode"
)

func TestHelpFlagExitsZero(t *testing.T) {
	res := runCLI(t, []string{"--help"}, "")
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if res.Stdout == "" {
		t.Fatal("--help produced no output")
	}
}

func TestHelpCommandExitsZeroAndMatchesHelpFlag(t *testing.T) {
	flagRes := runCLI(t, []string{"--help"}, "")
	cmdRes := runCLI(t, []string{"help"}, "")

	if cmdRes.Code != 0 {
		t.Fatalf("gage help exit code = %d, want 0", cmdRes.Code)
	}
	if cmdRes.Stdout != flagRes.Stdout {
		t.Errorf("gage help output differs from gage --help:\nhelp:   %q\n--help: %q", cmdRes.Stdout, flagRes.Stdout)
	}
}

func TestHelpSubcommandPrintsUsageAndExitsZero(t *testing.T) {
	res := runCLI(t, []string{"help", "use"}, "")
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "use") {
		t.Errorf("gage help use output doesn't mention 'use': %q", res.Stdout)
	}
}

func TestHelpUnknownTopicFailsWithUsageCode(t *testing.T) {
	res := runCLI(t, []string{"help", "not-a-real-command"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
}

// TestNeitherHelpSpellingListsSessionOnlyCommands checks the grouped,
// registry-driven listing gage --help/gage help render never includes
// use/lock/status/whoami/exit/quit as addressable top-level commands —
// they don't exist outside a session (M6), and listing them here would
// point an operator at a path that can't work. This checks for the name
// as its own listed entry (a line starting with the name, as
// commandListLine renders it), not merely as a word anywhere in the
// prose, so a future explanatory sentence mentioning "use" in passing
// can't produce a false pass.
func TestNeitherHelpSpellingListsSessionOnlyCommands(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help"}} {
		res := runCLI(t, args, "")
		for _, name := range []string{"use", "lock", "status", "whoami", "exit", "quit"} {
			for _, line := range strings.Split(res.Stdout, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == name || strings.HasPrefix(trimmed, name+",") || strings.HasPrefix(trimmed, name+" ") {
					t.Errorf("%v output lists session-only command %q as a top-level entry: %q", args, name, line)
				}
			}
		}
	}
}

// TestOneShotHelpMatchesRegistryExactly is the "compared against the
// registry, not a hardcoded list" bullet: every OneShotVisible registry
// command's name appears in the rendered help, and no other
// registry-known command name appears — computed from the registry
// itself, not literal strings.
func TestOneShotHelpMatchesRegistryExactly(t *testing.T) {
	res := runCLI(t, []string{"--help"}, "")

	for _, ci := range registry {
		listed := strings.Contains(res.Stdout, ci.Name)
		if ci.Availability.OneShotVisible() && !listed {
			t.Errorf("one-shot-visible command %q missing from gage --help", ci.Name)
		}
		if !ci.Availability.OneShotVisible() {
			for _, line := range strings.Split(res.Stdout, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == ci.Name || strings.HasPrefix(trimmed, ci.Name+",") || strings.HasPrefix(trimmed, ci.Name+" ") {
					t.Errorf("non-one-shot command %q appears as a listed entry in gage --help: %q", ci.Name, line)
				}
			}
		}
	}
}

func TestEveryRegistryEntryHasShortAndGroup(t *testing.T) {
	for _, ci := range registry {
		if strings.TrimSpace(ci.Short) == "" {
			t.Errorf("command %q has no short description", ci.Name)
		}
		if strings.TrimSpace(ci.Group) == "" {
			t.Errorf("command %q has no group", ci.Name)
		}
	}
}

func TestAliasesListedAlongsideCanonicalName(t *testing.T) {
	// status/whoami is the one M0 registry entry with an alias — assert
	// generically, from the registry, rather than hardcoding "status".
	for _, ci := range registry {
		if len(ci.Aliases) == 0 {
			continue
		}
		line := commandListLine(ci)
		if !strings.Contains(line, ci.Name) {
			t.Errorf("command list line for %q doesn't contain its own name: %q", ci.Name, line)
		}
		for _, alias := range ci.Aliases {
			if !strings.Contains(line, alias) {
				t.Errorf("command list line for %q doesn't contain alias %q: %q", ci.Name, alias, line)
			}
		}
	}
}

// TestHelpOutputIsGrouped proves the grouping mechanism renders section
// headers for whichever groups the current registry actually has
// one-shot-visible commands in. M0's real registry has none (every
// command is session-only), so this exercises the mechanism directly
// against groupedOneShotCommands rather than gage --help's necessarily-
// empty-for-now rendered output.
func TestHelpOutputIsGrouped(t *testing.T) {
	fake := []CommandInfo{
		{Name: "fake-vault-cmd", Short: "does vault things", Group: GroupVault, Availability: AvailBoth},
		{Name: "fake-entry-cmd", Short: "does entry things", Group: GroupEntry, Availability: AvailBoth},
	}
	original := registry
	registry = append(append([]CommandInfo{}, original...), fake...)
	defer func() { registry = original }()

	res := runCLI(t, []string{"--help"}, "")

	vaultIdx := strings.Index(res.Stdout, GroupVault+":")
	entryIdx := strings.Index(res.Stdout, GroupEntry+":")
	if vaultIdx == -1 || entryIdx == -1 {
		t.Fatalf("expected both group headers in output:\n%s", res.Stdout)
	}
	if vaultIdx >= entryIdx {
		t.Errorf("group order wrong: Vault lifecycle should render before Entry CRUD, got:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "fake-vault-cmd") || !strings.Contains(res.Stdout, "fake-entry-cmd") {
		t.Errorf("grouped commands missing from output:\n%s", res.Stdout)
	}
}
