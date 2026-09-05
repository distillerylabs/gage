package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/gittest"
)

// doubledPrefix is what this file exists to keep out of gage's output.
const doubledPrefix = errorPrefix + errorPrefix

// TestNoCommandDoublesGagesOwnPrefix drives a broad set of *failing*
// commands and asserts none of them names gage twice.
//
// The convention this protects: every message the library produces
// carries "gage: " in its own text — Prompter.Warn messages are printed
// verbatim, so a warning can only name gage by saying so itself, and
// errors follow suit. That makes the prefix the message's property, not
// the printer's, so any printer that prepends unconditionally produces
// "gage: gage: ...". writeError is the one place errors are rendered and
// the only thing allowed to add it.
//
// Table-driven over real invocations rather than asserted at each call
// site, because the failure is in *composition*: it appears only once a
// message has been through both the layer that wrote it and the layer
// that printed it.
func TestNoCommandDoublesGagesOwnPrefix(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin string
		// setup runs before the command, for cases that need a vault or a
		// remote to fail against.
		setup func(t *testing.T) []string
	}{
		{name: "unknown command", args: []string{"no-such-command"}},
		{name: "unknown flag", args: []string{"ls", "--no-such-flag"}},
		{name: "vault info on a missing vault", args: []string{"vault", "info", "nosuchvault"}},
		{name: "vault remove on a missing vault", args: []string{"vault", "remove", "nosuchvault"}},
		{name: "vault set-default on a missing vault", args: []string{"vault", "set-default", "nosuchvault"}},
		{name: "git set-remote on a missing vault", args: []string{"git", "set-remote", "nosuchvault", "https://example.com/x.git"}},
		{name: "no current vault", args: []string{"ls"}},
		{name: "show with no vault", args: []string{"show", "anything"}},
		{name: "pull with no vault", args: []string{"pull"}},
		{name: "push with no vault", args: []string{"push"}},
		{name: "sync with no vault", args: []string{"sync"}},
		{name: "auth login with no host and no vault", args: []string{"auth", "login"}},
		{name: "auth logout with no host and no vault", args: []string{"auth", "logout"}},
		{name: "clone with an unusable name", args: []string{"clone", "https://example.com/..git", "--name", ".."}},
		{
			name: "insert with mutually exclusive flags",
			args: []string{"insert", "x", "-m", "--value-stdin"},
		},
		{
			name: "init into an already-registered name",
			args: []string{"init", "personal"},
			setup: func(t *testing.T) []string {
				if res := runCLI(t, []string{"init", "personal"}, ""); res.Code != 0 {
					t.Fatalf("seeding: %s", res.Stderr)
				}
				return nil
			},
		},
		{
			name: "init against a repository that doesn't exist",
			args: []string{"init", "personal", "--remote", "PLACEHOLDER"},
			setup: func(t *testing.T) []string {
				return []string{filepath.Join(t.TempDir(), "nothing-here.git")}
			},
		},
		{
			name: "entry not found",
			args: []string{"show", "nosuchentry"},
			setup: func(t *testing.T) []string {
				if res := runCLI(t, []string{"init", "personal"}, ""); res.Code != 0 {
					t.Fatalf("seeding: %s", res.Stderr)
				}
				return nil
			},
		},
		{
			name: "sync on a genuine conflict",
			args: []string{"sync"},
			setup: func(t *testing.T) []string {
				vaultPath, remote := initVaultWithRemote(t, "personal")
				other := gittest.NewDevice(t, remote)
				other.WriteCommitPush(t, "shared.txt", "theirs", "their edit")
				writeConflictingLocalCommit(t, vaultPath)
				return nil
			},
		},
		{
			name: "clone of an already-registered name",
			args: []string{"clone", "PLACEHOLDER", "--name", "personal"},
			setup: func(t *testing.T) []string {
				_, remote := initVaultWithRemote(t, "personal")
				return []string{remote}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateXDG(t)

			args := tc.args
			if tc.setup != nil {
				if subs := tc.setup(t); len(subs) > 0 {
					args = substitutePlaceholder(args, subs[0])
				}
			}

			res := runCLI(t, args, tc.stdin)
			if res.Code == 0 {
				t.Fatalf("`gage %s` succeeded; this table only proves anything about failures",
					strings.Join(args, " "))
			}
			assertNoDoubledPrefix(t, res)
		})
	}
}

// TestWarningsDoNotDoubleGagesOwnPrefix covers the other surface the
// convention touches: advisories printed through Prompter.Warn, which are
// written verbatim and so carry their own prefix.
func TestWarningsDoNotDoubleGagesOwnPrefix(t *testing.T) {
	isolateXDG(t)
	initVaultWithRemote(t, "personal")

	// Take the remote away so the next write's push fails and warns. The
	// warning is what's under test, so it has to be a real one.
	remote := readGlobalConfigForTest(t).Vaults["personal"].Git.Origin
	if err := os.Rename(remote, remote+".hidden"); err != nil {
		t.Fatal(err)
	}

	// Warnings reach the Prompter, not stderr — that is the whole point
	// of the Prompter seam, and it means a CLI test has to look where the
	// message actually goes rather than at the streams.
	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{"hunter2"}}
	res, _ := runCLIWithPrompter(t, []string{"insert", "ProtonMail"}, "", false, p)
	if res.Code != 0 {
		t.Fatalf("the write should still succeed with the remote gone: %s", res.Stderr)
	}

	if len(p.warnings) == 0 {
		t.Fatal("a write whose push could not reach the remote warned about nothing")
	}
	for _, w := range p.warnings {
		if strings.Contains(w, doubledPrefix) || strings.Count(w, errorPrefix) > 1 {
			t.Errorf("warning names gage more than once:\n  %s", w)
		}
		if !strings.HasPrefix(w, errorPrefix) {
			t.Errorf("warning does not name gage at all (it is printed verbatim, so it must):\n  %s", w)
		}
	}
}

// assertNoDoubledPrefix fails if either stream names gage twice in a row,
// or embeds a second mention mid-sentence.
func assertNoDoubledPrefix(t *testing.T, res cliResult) {
	t.Helper()

	for _, stream := range []struct{ name, text string }{
		{"stdout", res.Stdout},
		{"stderr", res.Stderr},
	} {
		for _, line := range strings.Split(stream.text, "\n") {
			if strings.Contains(line, doubledPrefix) {
				t.Errorf("%s names gage twice in a row:\n  %s", stream.name, line)
			}
			// A message embedded in another one carries its prefix into
			// the middle of the sentence: "gage: doing X: gage: Y failed".
			if strings.Count(line, errorPrefix) > 1 {
				t.Errorf("%s repeats gage's prefix mid-sentence:\n  %s", stream.name, line)
			}
		}
	}
}

// substitutePlaceholder fills in the one argument a setup func had to
// compute (a temp path, a remote URL).
func substitutePlaceholder(args []string, value string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i, a := range out {
		if a == "PLACEHOLDER" {
			out[i] = value
		}
	}
	return out
}

// writeConflictingLocalCommit makes a local commit touching the same file
// the other device just changed, so the next sync finds a real conflict.
func writeConflictingLocalCommit(t *testing.T, vaultPath string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(vaultPath, "shared.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(vaultPath, "my edit"); err != nil {
		t.Fatal(err)
	}
}
