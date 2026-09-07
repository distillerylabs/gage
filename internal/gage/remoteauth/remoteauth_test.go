package remoteauth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// isolateState points $GAGE_STATE at a fresh temp directory so these
// tests never read or write the real machine's tokens.
func isolateState(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
}

func TestStoreWritesTheTokenUnderStateAt0600(t *testing.T) {
	isolateState(t)

	if err := Store("github.com", "ghp_example"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	path, err := TokenPath("github.com")
	if err != nil {
		t.Fatal(err)
	}
	// The token belongs under the state root, not config: it's disposable,
	// machine-local, and re-acquirable by logging in again, and dotfile
	// managers sync $GAGE_CONFIG by convention.
	if !strings.Contains(filepath.ToSlash(path), "/state/gage/tokens/github.com") {
		t.Errorf("token path = %s, want it under $GAGE_STATE/tokens", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("token mode = %04o, want 0600", perm)
		}
	}

	got, err := Load("github.com")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "ghp_example" {
		t.Errorf("Load = %q, want %q", got, "ghp_example")
	}
}

func TestLoadRefusesATokenOthersCanRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits aren't modeled on Windows")
	}
	isolateState(t)

	if err := Store("github.com", "ghp_example"); err != nil {
		t.Fatal(err)
	}
	path, err := TokenPath("github.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Load("github.com")
	if !errors.Is(err, ErrInsecureToken) {
		t.Fatalf("Load on a world-readable token = %v, want ErrInsecureToken", err)
	}
	if strings.Contains(err.Error(), "ghp_example") {
		t.Errorf("the refusal leaked the token: %v", err)
	}
}

func TestLogoutForgetsTheTokenAndIsSafeTwice(t *testing.T) {
	isolateState(t)

	if err := Store("github.com", "ghp_example"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("github.com"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := Load("github.com"); !errors.Is(err, ErrNoToken) {
		t.Errorf("Load after Remove = %v, want ErrNoToken", err)
	}
	// Logging out twice is not a failure.
	if err := Remove("github.com"); err != nil {
		t.Errorf("Remove on an absent token = %v, want nil", err)
	}
}

// TestTwoVaultsOnOneHostShareOneToken is the per-*host* rule: tokens are
// keyed by host, so a second vault on the same host needs no second login.
func TestTwoVaultsOnOneHostShareOneToken(t *testing.T) {
	isolateState(t)

	if err := Store("github.com", "ghp_shared"); err != nil {
		t.Fatal(err)
	}

	for _, url := range []string{
		"https://github.com/me/work-vault.git",
		"https://github.com/me/personal-vault.git",
	} {
		auth, err := Method(url)
		if err != nil {
			t.Fatalf("Method(%s): %v", url, err)
		}
		basic, ok := auth.(*http.BasicAuth)
		if !ok {
			t.Fatalf("Method(%s) = %T, want *http.BasicAuth", url, auth)
		}
		if basic.Password != "ghp_shared" {
			t.Errorf("Method(%s) used password %q, want the stored token", url, basic.Password)
		}
	}

	// A different host is a different token, and doesn't borrow this one.
	auth, err := Method("https://gitlab.com/me/other.git")
	if err != nil {
		t.Fatalf("Method against an unconfigured host: %v", err)
	}
	if auth != nil {
		t.Errorf("Method against an unconfigured host = %v, want nil (attempt it anonymously)", auth)
	}
}

func TestHostReadsBothRemoteSpellings(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/me/vault.git", "github.com"},
		{"https://git.example.internal:8443/me/vault.git", "git.example.internal"},
		{"git@github.com:me/vault.git", "github.com"},
		{"ssh://git@git.example.internal/me/vault.git", "git.example.internal"},
		// A local path has no host; tests and USB-drive vaults use these.
		{"/tmp/remote.git", ""},
	}

	for _, tc := range tests {
		got, err := Host(tc.url)
		if err != nil {
			t.Errorf("Host(%s): %v", tc.url, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Host(%s) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// TestTokenHostOnlyNamesHTTPHosts proves TokenHost agrees with what
// Method would actually do for each protocol: a token is only ever a
// candidate for an HTTP(S) remote. A local path has no host, and an SSH
// remote authenticates through ssh-agent per Q-GIT-AUTH, so neither
// should be reported as something worth prompting for a token over —
// this is what `gage init --remote` uses to decide whether to solicit
// one before it has stored or loaded anything.
func TestTokenHostOnlyNamesHTTPHosts(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/me/vault.git", "github.com"},
		{"http://git.example.internal:8080/me/vault.git", "git.example.internal"},
		{"git@github.com:me/vault.git", ""},
		{"ssh://git@git.example.internal/me/vault.git", ""},
		{"/tmp/remote.git", ""},
	}

	for _, tc := range tests {
		got, err := TokenHost(tc.url)
		if err != nil {
			t.Errorf("TokenHost(%s): %v", tc.url, err)
			continue
		}
		if got != tc.want {
			t.Errorf("TokenHost(%s) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// TestSSHRemoteNeedingSSHConfigFailsWithTheRealReason covers the design
// doc's own example remote. go-git never reads ~/.ssh/config, so a remote
// that only an alias could resolve has to say that rather than fail as a
// generic connection error much later.
func TestSSHRemoteNeedingSSHConfigFailsWithTheRealReason(t *testing.T) {
	isolateState(t)

	_, err := Method("git@internal:secrets/work-vault.git")
	if !errors.Is(err, ErrSSHConfigAlias) {
		t.Fatalf("Method against an alias-dependent remote = %v, want ErrSSHConfigAlias", err)
	}
	if !strings.Contains(err.Error(), "~/.ssh/config") {
		t.Errorf("error = %q, want it to name ~/.ssh/config", err)
	}
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("error = %q, want it to point at the HTTPS spelling", err)
	}
}

// TestSSHRemoteWithARealHostIsAttempted proves best-effort SSH is
// genuinely best-effort rather than absent: a normally-spelled SSH remote
// is not refused as an alias, it goes on to ask ssh-agent.
//
// Whether an agent is actually running differs per machine and CI runner,
// so the assertion is on *which* path was taken, not on the outcome: it
// either returns an auth method (an agent answered) or an error naming
// ssh-agent — never the alias refusal, and never a silent nil.
func TestSSHRemoteWithARealHostIsAttempted(t *testing.T) {
	isolateState(t)

	auth, err := Method("git@github.com:me/vault.git")
	if errors.Is(err, ErrSSHConfigAlias) {
		t.Fatal("a fully-qualified SSH host was refused as a ~/.ssh/config alias")
	}
	switch {
	case err != nil:
		if !strings.Contains(err.Error(), "ssh-agent") {
			t.Errorf("SSH failure = %q, want it to name ssh-agent as the thing that was tried", err)
		}
	case auth == nil:
		t.Error("Method returned no auth and no error for an SSH remote; SSH support is absent, not best-effort")
	}
}

func TestStoreRefusesAnEmptyToken(t *testing.T) {
	isolateState(t)

	if err := Store("github.com", "   "); err == nil {
		t.Error("Store accepted an empty token")
	}
}

// TestHostCannotEscapeTheTokensDirectory covers the path-traversal case:
// the host comes from a remote URL, which is user- and git-editable
// config, so it must never be resolved into a path outside the token
// directory.
func TestHostCannotEscapeTheTokensDirectory(t *testing.T) {
	isolateState(t)

	for _, host := range []string{"../../etc/cron.d/x", "..", "", "a/b", "."} {
		if _, err := TokenPath(host); err == nil {
			t.Errorf("TokenPath(%q) was accepted, want it refused as a path component", host)
		}
	}
}

func TestAuthHintNamesTheHostAndTheCommand(t *testing.T) {
	hint := AuthHint("github.com")
	if !strings.Contains(hint, "github.com") {
		t.Errorf("hint = %q, want it to name the host", hint)
	}
	if !strings.Contains(hint, "gage auth login") {
		t.Errorf("hint = %q, want it to name `gage auth login`", hint)
	}
}
