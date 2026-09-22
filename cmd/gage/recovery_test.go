package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// initVaultWithRecoveryKeyFile creates a vault through the real command and
// returns the recovery secret it wrote, so a verify test checks against a
// key the vault genuinely has rather than one a test fabricated.
func initVaultWithRecoveryKeyFile(t *testing.T, name string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "recovery.key")
	res := runCLI(t, []string{"init", name, "--device", "laptop-1", "--recovery-key-out", out}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	data, err := os.ReadFile(out) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func runRecoveryVerify(t *testing.T, pasted string, args ...string) (cliResult, *fakePrompter) {
	t.Helper()
	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{pasted}}
	res, _ := runCLIWithPrompter(t, append([]string{"recovery", "verify"}, args...), "", false, p)
	return res, p
}

func TestRecoveryVerifyAcceptsTheVaultsRecoveryKey(t *testing.T) {
	isolateXDG(t)
	secret := initVaultWithRecoveryKeyFile(t, "personal")

	res, _ := runRecoveryVerify(t, secret)
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "personal") {
		t.Errorf("the result doesn't name the vault:\n%s", res.Stdout)
	}
}

func TestRecoveryVerifyToleratesPastedWhitespace(t *testing.T) {
	isolateXDG(t)
	secret := initVaultWithRecoveryKeyFile(t, "personal")

	if res, _ := runRecoveryVerify(t, "  "+secret+"\n"); res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
}

func TestRecoveryVerifyNeedsNoUnlock(t *testing.T) {
	isolateXDG(t)
	secret := initVaultWithRecoveryKeyFile(t, "personal")

	res, p := runRecoveryVerify(t, secret)
	if res.Code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", res.Code, res.Stderr)
	}
	// One prompt, for the pasted key. A passphrase prompt would mean the
	// command needed this device's identity, which is the very thing a
	// recovery check exists to not depend on.
	if len(p.requests) != 0 {
		t.Errorf("verify asked for %d unlocks, want 0", len(p.requests))
	}
}

func TestRecoveryVerifyRejectsAKeyThatIsNotARecipient(t *testing.T) {
	isolateXDG(t)
	initVaultWithRecoveryKeyFile(t, "personal")

	stranger, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	res, _ := runRecoveryVerify(t, stranger.String())
	if res.Code != int(exitcode.LockedOrAuth) {
		t.Fatalf("exit code = %d, want %d (LockedOrAuth); stderr=%s", res.Code, exitcode.LockedOrAuth, res.Stderr)
	}
	if res.Stdout != "" {
		t.Errorf("a non-match wrote to stdout, which is where success goes:\n%s", res.Stdout)
	}
}

func TestRecoveryVerifyRejectsAMalformedKey(t *testing.T) {
	isolateXDG(t)
	initVaultWithRecoveryKeyFile(t, "personal")

	for _, bad := range []string{"", "not a key", "age1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0savhh7m"} {
		res, _ := runRecoveryVerify(t, bad)
		if res.Code != int(exitcode.Usage) {
			t.Errorf("%q: exit code = %d, want %d (Usage); stderr=%s", bad, res.Code, exitcode.Usage, res.Stderr)
		}
	}
}

// TestRecoveryVerifyNeverEchoesTheKey: the key arrives through the masked
// prompt and must not come back out — not on success, not in either kind
// of failure, on either stream.
func TestRecoveryVerifyNeverEchoesTheKey(t *testing.T) {
	isolateXDG(t)
	secret := initVaultWithRecoveryKeyFile(t, "personal")
	stranger, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	for name, pasted := range map[string]string{
		"match":         secret,
		"non-recipient": stranger.String(),
		"malformed":     "x" + secret,
	} {
		res, p := runRecoveryVerify(t, pasted)
		if len(p.valuePrompts) != 1 {
			t.Errorf("%s: Prompter.Value called %d times, want 1", name, len(p.valuePrompts))
		}
		if strings.Contains(res.Stdout+res.Stderr, pasted) {
			t.Errorf("%s: the pasted key was echoed:\n%s%s", name, res.Stdout, res.Stderr)
		}
	}
}

func TestRecoveryVerifyIsListedInHelp(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"help"}, "")
	if res.Code != 0 {
		t.Fatalf("help failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "recovery verify") {
		t.Errorf("`gage help` doesn't list recovery verify:\n%s", res.Stdout)
	}
}
