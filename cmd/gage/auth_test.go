package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/distillerylabs/gage/internal/gage/gittest"
	"github.com/distillerylabs/gage/internal/gage/remoteauth"
)

// testToken is what the fake prompter answers `auth login`'s token
// prompt with. It's deliberately distinctive so a leak test can look for
// it anywhere output goes.
const testToken = "ghp_TESTTOKENVALUE0123456789" // #nosec G101 -- deliberately fake token value, not a real credential

// runAuthCLI runs the CLI with a prompter that answers the token prompt.
func runAuthCLI(t *testing.T, args []string) cliResult {
	t.Helper()

	res, _ := runCLIWithPrompter(t, args, "", false, &fakePrompter{
		passphrases: []string{testPassphrase},
		values:      []string{testToken},
	})
	return res
}

func TestAuthLoginStoresATokenAndStatusReportsIt(t *testing.T) {
	isolateXDG(t)

	res := runAuthCLI(t, []string{"auth", "login", "--host", "github.com"})
	if res.Code != 0 {
		t.Fatalf("auth login exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "gage", "tokens", "github.com")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected a token at %s: %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("token mode = %04o, want 0600", perm)
		}
	}

	res = runAuthCLI(t, []string{"auth", "status", "--host", "github.com"})
	if res.Code != 0 {
		t.Fatalf("auth status exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "github.com") || !strings.Contains(res.Stdout, "configured") {
		t.Errorf("auth status said %q, want it to report github.com as configured", res.Stdout)
	}
}

// TestNoTokenValueEverAppearsInOutput is the confidentiality half of
// token handling: the token is prompted rather than passed as an
// argument (so it never lands in shell or session history), and nothing
// gage prints ever echoes it back.
func TestNoTokenValueEverAppearsInOutput(t *testing.T) {
	isolateXDG(t)

	for _, args := range [][]string{
		{"auth", "login", "--host", "github.com"},
		{"auth", "status", "--host", "github.com"},
		{"auth", "status"},
		{"auth", "logout", "--host", "github.com"},
		{"auth", "status", "--host", "github.com"},
	} {
		res := runAuthCLI(t, args)
		if strings.Contains(res.Stdout, testToken) || strings.Contains(res.Stderr, testToken) {
			t.Errorf("`gage %s` echoed the token value:\nstdout=%q\nstderr=%q",
				strings.Join(args, " "), res.Stdout, res.Stderr)
		}
	}

	// The token is never an argument, so there is no command line
	// carrying it for a history file to record in the first place.
	for _, cmd := range findCommandsUnder(t, "auth") {
		if strings.Contains(cmd.Use, "token") || strings.Contains(cmd.Use, "TOKEN") {
			t.Errorf("`gage auth %s` takes the token as an argument; it must be prompted for", cmd.Use)
		}
	}
}

func TestAuthLogoutForgetsTheToken(t *testing.T) {
	isolateXDG(t)

	if res := runAuthCLI(t, []string{"auth", "login", "--host", "github.com"}); res.Code != 0 {
		t.Fatalf("auth login failed: %s", res.Stderr)
	}
	if res := runAuthCLI(t, []string{"auth", "logout", "--host", "github.com"}); res.Code != 0 {
		t.Fatalf("auth logout exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "gage", "tokens", "github.com")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the token file survived logout: %v", err)
	}

	res := runAuthCLI(t, []string{"auth", "status", "--host", "github.com"})
	if !strings.Contains(res.Stdout, "no token") {
		t.Errorf("auth status after logout said %q, want it to report no token", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "gage auth login") {
		t.Errorf("auth status after logout said %q, want it to name `gage auth login`", res.Stdout)
	}
}

// TestHostDefaultsToTheCurrentVaultsOrigin covers the ergonomics rule
// from "Git-specific commands": --host is per-host, and defaults to the
// host of the current vault's remote so you rarely type it.
func TestHostDefaultsToTheCurrentVaultsOrigin(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal", "--remote", "https://git.example.com/me/vault.git", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	res := runAuthCLI(t, []string{"auth", "login"})
	if res.Code != 0 {
		t.Fatalf("auth login without --host exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "git.example.com") {
		t.Errorf("auth login said %q, want it to name the current vault's host", res.Stdout)
	}

	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "gage", "tokens", "git.example.com")); err != nil {
		t.Errorf("the token was not stored under the vault's host: %v", err)
	}
}

// TestAuthOnALocalRemoteSaysNoTokenIsNeeded keeps the failure legible for
// the case tests and USB-drive vaults use, where there is no host at all.
func TestAuthOnALocalRemoteSaysNoTokenIsNeeded(t *testing.T) {
	isolateXDG(t)

	remote := gittest.NewBareRemote(t)
	if res := runCLI(t, []string{"init", "personal", "--remote", remote, "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	res := runAuthCLI(t, []string{"auth", "login"})
	if res.Code == 0 {
		t.Fatal("auth login against a local-path remote succeeded, want a usage error")
	}
	if !strings.Contains(res.Stderr, "local path") {
		t.Errorf("stderr = %q, want it to explain that a local remote needs no token", res.Stderr)
	}
}

// TestExpiredTokenErrorNamesTheHostAndTheCommand is Q-OAUTH-APP's one
// real cost: user-supplied tokens expire, and when one does gage has to
// say so in those terms rather than surfacing a bare 403.
//
// The message is asserted at its source rather than against a live host,
// since a real expiry needs a server to refuse us.
func TestExpiredTokenErrorNamesTheHostAndTheCommand(t *testing.T) {
	hint := remoteauth.AuthHint("github.com")
	for _, want := range []string{"github.com", "gage auth login", "expired"} {
		if !strings.Contains(hint, want) {
			t.Errorf("auth hint = %q, want it to mention %q", hint, want)
		}
	}
}

// TestNoHostSpecificDependencies pins the "no host-specific code paths"
// half of Q-OAUTH-APP: no go-github, no device flow, no shipped client
// ID. A dependency creeping in here would be how host-specialness
// returns.
func TestNoHostSpecificDependencies(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"go-github", "oauth2", "githubapp"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("go.mod depends on %q; remote auth is deliberately host-neutral (Q-OAUTH-APP)", forbidden)
		}
	}
}

// TestAuthCommandsWorkInBothModes checks the registry tagging rather than
// re-testing the REPL: `auth` is management, and management commands work
// inside a session (Q-CMD-AVAILABILITY).
func TestAuthCommandsWorkInBothModes(t *testing.T) {
	for _, name := range []string{"auth login", "auth status", "auth logout", "sync", "pull", "push"} {
		ci, ok := findCommand(name)
		if !ok {
			t.Errorf("%q is not in the registry", name)
			continue
		}
		if ci.Availability != AvailBoth {
			t.Errorf("%q availability = %v, want AvailBoth", name, ci.Availability)
		}
	}

	// clone creates a vault rather than operating on one, so it is
	// one-shot only for the same reason init is.
	ci, ok := findCommand("clone")
	if !ok {
		t.Fatal("clone is not in the registry")
	}
	if ci.Availability != AvailOneShotOnly {
		t.Errorf("clone availability = %v, want AvailOneShotOnly", ci.Availability)
	}
}

// findCommandsUnder returns the Cobra subcommands of a parent command.
func findCommandsUnder(t *testing.T, parent string) []*cobra.Command {
	t.Helper()

	root := NewRootCmd(testApp())
	for _, c := range root.Commands() {
		if c.Name() == parent {
			return c.Commands()
		}
	}
	t.Fatalf("no %q command", parent)
	return nil
}

// TestAuthStatusWithNoTokensAndNoVaultSaysSo: the blank case. With
// nothing stored and no vault to infer a host from, `auth status` has to
// say that plainly rather than printing an empty list.
func TestAuthStatusWithNoTokensAndNoVaultSaysSo(t *testing.T) {
	isolateXDG(t)

	res := runAuthCLI(t, []string{"auth", "status"})
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "no tokens are stored on this machine") {
		t.Errorf("stdout = %q, want it to say nothing is stored", res.Stdout)
	}
}

// TestAuthStatusWithNoTokensFallsBackToTheCurrentVaultsHost: with no
// tokens but a vault that has a remote, the answer worth giving is about
// *that* host — "no token, here is the command" is actionable, where a
// bare "nothing stored" leaves the user to work out which host they
// needed.
func TestAuthStatusWithNoTokensFallsBackToTheCurrentVaultsHost(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal",
		"--remote", "https://git.example.com/me/vault.git", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	res := runAuthCLI(t, []string{"auth", "status"})
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "git.example.com") {
		t.Errorf("stdout = %q, want it to name the current vault's host", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "no token") {
		t.Errorf("stdout = %q, want it to report that host as having no token", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "gage auth login") {
		t.Errorf("stdout = %q, want it to name the command that fixes this", res.Stdout)
	}
}

// TestAuthLoginReadsTheOriginFromTheRepositoryWhenConfigLags: global
// config records a vault's origin, but a `git remote set-url` run
// outside gage moves the real one without touching it. The repository is
// the authority, so a stale (here: absent) recorded origin must not make
// gage ask for a host the user already configured.
func TestAuthLoginReadsTheOriginFromTheRepositoryWhenConfigLags(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal",
		"--remote", "https://git.example.com/me/vault.git", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	// Exactly what a remote configured outside gage looks like from
	// here: the repository knows, global config doesn't.
	g := readGlobalConfigForTest(t)
	entry := g.Vaults["personal"]
	entry.Git.Origin = ""
	g.Vaults["personal"] = entry
	if err := writeGlobalConfig(g); err != nil {
		t.Fatal(err)
	}

	res := runAuthCLI(t, []string{"auth", "login"})
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "git.example.com") {
		t.Errorf("stdout = %q, want the host read off the repository", res.Stdout)
	}
}

// TestAuthLoginOnAVaultWithNoRemoteNamesTheVault: a vault that was never
// given a remote has no host to authenticate to, and the refusal has to
// say which vault it means — a session can hold several.
func TestAuthLoginOnAVaultWithNoRemoteNamesTheVault(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	res := runAuthCLI(t, []string{"auth", "login"})
	if res.Code == 0 {
		t.Fatal("auth login against a remoteless vault succeeded, want a usage error")
	}
	for _, want := range []string{"personal", "no remote", "--host"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", res.Stderr, want)
		}
	}
}
