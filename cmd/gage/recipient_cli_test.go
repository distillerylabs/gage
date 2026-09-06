package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/recipients"
)

// newRecipientKey returns a fresh, real age public key — what someone
// pastes in from another device's `gage identity add`. Generated rather
// than hardcoded because --reencrypt has to actually encrypt to it, and
// the package's existing testRecipient2 is a plugin key this build
// cannot encrypt to.
func newRecipientKey(t *testing.T) string {
	t.Helper()
	ident, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return ident.Recipient().String()
}

func ageRecipientsFile(t *testing.T, vaultPath string) []string {
	t.Helper()
	keys, err := recipients.Read(filepath.Join(vaultPath, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func hasKey(list []string, want string) bool {
	for _, k := range list {
		if k == want {
			return true
		}
	}
	return false
}

// noInteractionPrompter fails the test the moment anything asks it for a
// human decision. It is how `recipient verify`'s "needs no unlock, safe
// to run in CI" claim is proven rather than asserted.
type noInteractionPrompter struct{ t *testing.T }

func (p noInteractionPrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	p.t.Fatalf("the command asked for an unlock (%+v); verify must never unlock", req)
	return gage.UnlockResponse{}, nil
}

func (p noInteractionPrompter) Value(prompt string) (string, error) {
	p.t.Fatalf("the command asked for a value (%q); verify must never prompt", prompt)
	return "", nil
}

func (p noInteractionPrompter) Confirm(prompt string) (bool, error) {
	p.t.Fatalf("the command asked for a confirmation (%q); verify must never prompt", prompt)
	return false, nil
}

func (p noInteractionPrompter) Choose(list gage.CandidateList) (string, error) {
	p.t.Fatalf("the command asked to choose from %+v; verify must never prompt", list)
	return "", nil
}

func (p noInteractionPrompter) Warn(msg string) {
	p.t.Fatalf("the command warned (%q); verify has nothing to warn about", msg)
}

// TestRecipientAddCommitsBothFilesInOneCommit is the CLI half of the
// resolved decision that an add with no --reencrypt still commits, and
// commits the recipient pair together.
func TestRecipientAddCommitsBothFilesInOneCommit(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	key := newRecipientKey(t)

	before, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"recipient", "add", key, "--device", "phone-1"}, "")
	if res.Code != 0 {
		t.Fatalf("recipient add exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	after, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d (exactly one commit)", after, before+1)
	}

	clean, err := gitrepo.IsClean(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after recipient add; the change must be committed")
	}
	if !hasKey(ageRecipientsFile(t, vaultPath), key) {
		t.Error(".age-recipients does not list the added key")
	}
	found := false
	for _, r := range readVaultConfigForTest(t, "personal").Recipients {
		if r.Pubkey == key {
			found = true
			if r.Device != "phone-1" {
				t.Errorf("added recipient's device = %q, want phone-1", r.Device)
			}
		}
	}
	if !found {
		t.Error(".gage/config.toml does not list the added key")
	}
}

// TestRecipientAddReencryptMakesHistoryReadable drives the --reencrypt
// flag end to end from the CLI, and checks the vault stays coherent
// afterwards: one commit, clean tree, everything still decryptable by
// the device that ran it.
func TestRecipientAddReencryptMakesHistoryReadable(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")

	for _, title := range []string{"ProtonMail", "Bank"} {
		if res := runCLI(t, []string{"insert", title, "--value-stdin"}, "secret-"+title+"\n"); res.Code != 0 {
			t.Fatalf("insert %q: exit %d, stderr=%s", title, res.Code, res.Stderr)
		}
	}

	before, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	key := newRecipientKey(t)
	res := runCLI(t, []string{"recipient", "add", key, "--device", "phone-1", "--reencrypt"}, "")
	if res.Code != 0 {
		t.Fatalf("recipient add --reencrypt exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	after, err := gitrepo.CommitCount(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d — --reencrypt lands in exactly one commit", after, before+1)
	}
	clean, err := gitrepo.IsClean(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after a successful --reencrypt")
	}

	// The device that ran it can still read everything.
	if res := runCLI(t, []string{"show", "ProtonMail"}, ""); res.Code != 0 || !strings.Contains(res.Stdout, "secret-ProtonMail") {
		t.Errorf("show after --reencrypt: exit %d, stdout=%q, stderr=%s", res.Code, res.Stdout, res.Stderr)
	}
}

// TestRecipientRemoveWithoutReencryptIsRejectedByTheCLI: the design's
// "removing a recipient REQUIRES --reencrypt", as an exit code and a
// message that says what to do instead.
func TestRecipientRemoveWithoutReencryptIsRejectedByTheCLI(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	key := newRecipientKey(t)
	if res := runCLI(t, []string{"recipient", "add", key, "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("recipient add: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	head, err := gitrepo.HeadHash(vaultPath)
	if err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"recipient", "remove", "phone-1"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want Usage (%d); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "--reencrypt") {
		t.Errorf("stderr = %q, want it to name the flag that is required", res.Stderr)
	}

	got, err := gitrepo.HeadHash(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != head {
		t.Error("HEAD moved on a rejected remove")
	}
	if !hasKey(ageRecipientsFile(t, vaultPath), key) {
		t.Error("the recipient was removed despite the refusal")
	}
}

// TestRecipientRemoveReencryptPrintsTheRevocationWarning is the plan's
// "`gage recipient remove --reencrypt` prints the 'revokes future access
// only — anything already read can't be unread' warning." Warnings go to
// stderr, so stdout stays usable in a pipe.
func TestRecipientRemoveReencryptPrintsTheRevocationWarning(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")
	key := newRecipientKey(t)
	if res := runCLI(t, []string{"recipient", "add", key, "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("recipient add: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if res := runCLI(t, []string{"insert", "ProtonMail", "--value-stdin"}, "hunter2\n"); res.Code != 0 {
		t.Fatalf("insert: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	res := runCLI(t, []string{"recipient", "remove", "phone-1", "--reencrypt"}, "")
	if res.Code != 0 {
		t.Fatalf("recipient remove --reencrypt exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "future access") || !strings.Contains(res.Stderr, "already read") {
		t.Errorf("stderr = %q, want the revokes-future-access-only warning", res.Stderr)
	}
	if hasKey(ageRecipientsFile(t, vaultPath), key) {
		t.Error(".age-recipients still lists the removed key")
	}
}

// TestRecipientListMatchesConfigAndReflectsChanges is the plan's
// "`gage recipient list` prints every recipient's device name and public
// key, matching .gage/config.toml's [[recipients]] exactly, and reflects
// an add/remove from the same test run."
func TestRecipientListMatchesConfigAndReflectsChanges(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")

	assertListMatchesConfig := func(when string) {
		t.Helper()
		res := runCLI(t, []string{"recipient", "list"}, "")
		if res.Code != 0 {
			t.Fatalf("recipient list (%s): exit %d, stderr=%s", when, res.Code, res.Stderr)
		}
		lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
		want := readVaultConfigForTest(t, "personal").Recipients
		if len(lines) != len(want) {
			t.Fatalf("recipient list (%s) printed %d lines, config.toml has %d recipients:\n%s",
				when, len(lines), len(want), res.Stdout)
		}
		for i, r := range want {
			if !strings.Contains(lines[i], r.Device) || !strings.Contains(lines[i], r.Pubkey) {
				t.Errorf("recipient list (%s) line %d = %q, want it to carry device %q and key %q",
					when, i, lines[i], r.Device, r.Pubkey)
			}
		}
	}

	assertListMatchesConfig("at creation")

	key := newRecipientKey(t)
	if res := runCLI(t, []string{"recipient", "add", key, "--device", "phone-1"}, ""); res.Code != 0 {
		t.Fatalf("recipient add: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	assertListMatchesConfig("after add")
	if res := runCLI(t, []string{"recipient", "list"}, ""); !strings.Contains(res.Stdout, key) {
		t.Errorf("recipient list does not reflect the add: %q", res.Stdout)
	}

	if res := runCLI(t, []string{"recipient", "remove", "phone-1", "--reencrypt"}, ""); res.Code != 0 {
		t.Fatalf("recipient remove: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	assertListMatchesConfig("after remove")
	if res := runCLI(t, []string{"recipient", "list"}, ""); strings.Contains(res.Stdout, key) {
		t.Errorf("recipient list still shows the removed key: %q", res.Stdout)
	}
}

// TestRecipientVerifyExitCodesAndDifferences is the plan's "exits 0 and
// reports 'in sync' when .age-recipients and config.toml agree; exits 1
// and lists the specific differences when they don't."
func TestRecipientVerifyExitCodesAndDifferences(t *testing.T) {
	isolateXDG(t)
	vaultPath := initVaultForTest(t, "personal", "--device", "laptop-1")

	res := runCLI(t, []string{"recipient", "verify"}, "")
	if res.Code != 0 {
		t.Fatalf("verify on a consistent vault: exit %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "in sync") {
		t.Errorf("stdout = %q, want it to report \"in sync\"", res.Stdout)
	}

	// A key appended to .age-recipients alone — the shape of a tampered
	// or hand-edited recipient file, and the signature M10's trust cache
	// keys off.
	stray := newRecipientKey(t)
	keys := append(ageRecipientsFile(t, vaultPath), stray)
	if err := recipients.Write(filepath.Join(vaultPath, ".age-recipients"), keys); err != nil {
		t.Fatal(err)
	}

	res = runCLI(t, []string{"recipient", "verify"}, "")
	if res.Code != 1 {
		t.Fatalf("verify on a diverged vault: exit %d, want 1; stderr=%s", res.Code, res.Stderr)
	}
	out := res.Stdout + res.Stderr
	if !strings.Contains(out, stray) {
		t.Errorf("output = %q, want it to name the specific key that differs (%s)", out, stray)
	}
	if !strings.Contains(out, ".age-recipients") || !strings.Contains(out, "config.toml") {
		t.Errorf("output = %q, want it to say which file each difference is in", out)
	}
}

// TestRecipientVerifyRunsWithNoIdentityAndNoPrompter is the plan's
// second verify bullet: it succeeds "against a vault where the local
// device has no identity file at all (e.g. right after clone, before
// gage identity add) — no Vault.Unlock, no Prompter interaction."
//
// The Prompter handed in fails the test on any interaction, so the claim
// is proven by the run rather than asserted by the test's name.
func TestRecipientVerifyRunsWithNoIdentityAndNoPrompter(t *testing.T) {
	isolateXDG(t)
	initVaultForTest(t, "personal", "--device", "laptop-1")

	// The post-clone state: registered vault, no wrapped identity.
	dir, err := gage.IdentitiesDir("personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	has, err := gage.HasIdentity("personal", "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("the identity file survived removal; this test would prove nothing")
	}

	res, _ := runCLIWithPrompter(t, []string{"recipient", "verify"}, "", false, noInteractionPrompter{t: t})
	if res.Code != 0 {
		t.Fatalf("verify without any local identity: exit %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "in sync") {
		t.Errorf("stdout = %q, want it to report \"in sync\"", res.Stdout)
	}
}

// TestRecipientCommandsAreAvailableInBothModes guards the registry
// wiring M0 requires of every milestone that adds commands: identity and
// recipient are vault-domain commands, so they belong in both help
// surfaces, in their own groups.
func TestRecipientCommandsAreAvailableInBothModes(t *testing.T) {
	for _, name := range []string{
		"identity add", "identity list",
		"recipient add", "recipient remove", "recipient list", "recipient verify",
	} {
		ci, ok := findCommand(name)
		if !ok {
			t.Errorf("%q is missing from the command registry", name)
			continue
		}
		if ci.Availability != AvailBoth {
			t.Errorf("%q availability = %v, want AvailBoth", name, ci.Availability)
		}
		wantGroup := GroupRecipient
		if strings.HasPrefix(name, "identity") {
			wantGroup = GroupIdentity
		}
		if ci.Group != wantGroup {
			t.Errorf("%q group = %q, want %q", name, ci.Group, wantGroup)
		}
		if ci.Short == "" {
			t.Errorf("%q has no Short description", name)
		}
	}
}
