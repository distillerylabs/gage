package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gittest"
)

// ---------------------------------------------------------------------
// The situation this command exists for: a vault whose only device
// identity is gone, and a paper key.
// ---------------------------------------------------------------------

// loseTheIdentityFile deletes every wrapped identity this machine holds,
// which is the state `recovery enroll` is for. The vault's registration
// stays, as it would on a real machine whose $GAGE_DATA/identities was
// lost while its config survived.
func loseTheIdentityFile(t *testing.T) {
	t.Helper()
	root := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
}

// enrollPrompter answers the two questions `recovery enroll` asks: the
// pasted recovery key, then the new device's passphrase. It also reads the
// replacement recovery key off the screen for the re-type confirmation,
// the way recoveryPrompter does for init.
type enrollPrompter struct {
	fakePrompter
	screen *bytes.Buffer
	pasted string

	// confirmWith, when non-empty, is typed at the confirmation instead of
	// what is on screen.
	confirmWith string
}

func (p *enrollPrompter) Value(prompt string) (string, error) {
	p.valuePrompts = append(p.valuePrompts, prompt)
	p.valueCalls++
	// The first Value call is the paste; any later one is the re-type
	// confirmation for the replacement key.
	if p.valueCalls == 1 {
		return p.pasted, nil
	}
	if p.confirmWith != "" {
		return p.confirmWith, nil
	}
	shown := ageSecretKeyPattern.FindAllString(p.screen.String(), -1)
	if len(shown) == 0 {
		return "", nil
	}
	key := shown[len(shown)-1]
	return key[len(key)-recoveryConfirmChars:], nil
}

func (p *enrollPrompter) watchEnroll(t *testing.T) func(*App) {
	t.Helper()
	return func(app *App) {
		buf, ok := app.Err.(*bytes.Buffer)
		if !ok {
			t.Fatalf("app.Err is %T, want *bytes.Buffer", app.Err)
		}
		p.screen = buf
	}
}

// runEnroll runs `recovery enroll` on a terminal with the pasted key, and
// returns the result plus the replacement key it displayed.
func runEnroll(t *testing.T, pasted string, args ...string) (cliResult, string, *enrollPrompter) {
	t.Helper()
	p := &enrollPrompter{
		fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		pasted:       pasted,
	}
	res, _ := runCLIWithApp(t, append([]string{"recovery", "enroll"}, args...),
		strings.NewReader(""), true, p, p.watchEnroll(t))

	var shown string
	if keys := ageSecretKeyPattern.FindAllString(res.Stderr, -1); len(keys) > 0 {
		shown = keys[len(keys)-1]
	}
	return res, shown, p
}

// TestRecoveryEnrollGetsAWorkingDeviceBack is the milestone end to end: a
// vault whose only identity is gone, opened again with nothing but the
// paper key.
func TestRecoveryEnrollGetsAWorkingDeviceBack(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")

	// Something worth recovering, then the loss.
	if res := runCLI(t, []string{"insert", "api-token", "--value-stdin"}, "s3cr3t\n"); res.Code != 0 {
		t.Fatalf("seeding an entry: %s", res.Stderr)
	}
	loseTheIdentityFile(t)
	if res := runCLI(t, []string{"show", "api-token"}, ""); res.Code == 0 {
		t.Fatal("the vault is still readable, so this proves nothing")
	}

	res, newKey, _ := runEnroll(t, pasted, "--device", "laptop-2")
	if res.Code != 0 {
		t.Fatalf("recovery enroll failed: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	// The vault reads again, through the ordinary unlock.
	show := runCLI(t, []string{"show", "api-token"}, "")
	if show.Code != 0 {
		t.Fatalf("the vault is still unreadable after recovery: %s", show.Stderr)
	}
	if !strings.Contains(show.Stdout, "s3cr3t") {
		t.Errorf("show returned %q", show.Stdout)
	}

	// The recipient list is exactly what was asked for.
	vf := readVaultConfigForTest(t, "personal")
	got := make([]string, 0, len(vf.Recipients))
	for _, r := range vf.Recipients {
		got = append(got, r.Device)
	}
	want := []string{"laptop-1", "laptop-2", gage.RecoveryDeviceLabel}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("recipients = %v, want %v", got, want)
	}

	// The pasted key is retired; the one just shown is live.
	if newKey == "" {
		t.Fatal("no replacement recovery key was shown")
	}
	if newKey == pasted {
		t.Fatal("the replacement is the same key that was just retired")
	}
	assertVerifies(t, newKey, true)
	assertVerifies(t, pasted, false)
}

// assertVerifies runs `recovery verify` against a key and asserts whether
// it is still a recipient.
func assertVerifies(t *testing.T, key string, want bool) {
	t.Helper()
	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{key}}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "verify"}, "", false, p)
	if got := res.Code == 0; got != want {
		t.Errorf("recovery verify exit %d (recipient=%v), want recipient=%v; stderr=%s",
			res.Code, got, want, res.Stderr)
	}
}

// TestRecoveryEnrollRebuildsADeviceWhosePassphraseIsForgotten is the other
// half of the goal, and the one a blanket "refuse an existing identity
// file" rule would have blocked: the file is still there, it is simply
// unopenable.
func TestRecoveryEnrollRebuildsADeviceWhosePassphraseIsForgotten(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	if res := runCLI(t, []string{"insert", "api-token", "--value-stdin"}, "s3cr3t\n"); res.Code != 0 {
		t.Fatalf("seeding an entry: %s", res.Stderr)
	}

	before := identityFileForTest(t, "personal", "laptop-1")
	original, err := os.ReadFile(before) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}

	// The file survives; only the passphrase is gone.
	res, _, p := runEnroll(t, pasted, "--device", "laptop-1", "--replaces", "laptop-1")
	if res.Code != 0 {
		t.Fatalf("recovery enroll failed: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	// Crucially, it never asked to open the old file.
	for _, req := range p.requests {
		if req.Purpose != gage.PurposeCreate {
			t.Errorf("enroll asked for a %q passphrase; the old one is what was forgotten", req.Purpose)
		}
	}

	after, err := os.ReadFile(before) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatalf("the identity file is gone rather than replaced: %v", err)
	}
	if bytes.Equal(original, after) {
		t.Error("the unusable identity file was left in place")
	}

	// One laptop-1, with the new key, and the vault reads.
	vf := readVaultConfigForTest(t, "personal")
	if len(vf.Recipients) != 2 || vf.Recipients[0].Device != "laptop-1" {
		t.Fatalf("recipients = %+v", vf.Recipients)
	}
	if show := runCLI(t, []string{"show", "api-token"}, ""); show.Code != 0 {
		t.Fatalf("the rebuilt device cannot read: %s", show.Stderr)
	}
}

func TestRecoveryEnrollRefusesAnExistingIdentityFile(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")

	// laptop-1's file is present and --replaces does not name it.
	res, _, p := runEnroll(t, pasted, "--device", "laptop-1")
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if len(p.requests) != 0 {
		t.Error("enroll prompted for a passphrase before refusing")
	}
	if !strings.Contains(res.Stderr, "--replaces") {
		t.Errorf("the refusal doesn't name the way out:\n%s", res.Stderr)
	}
}

// TestRecoveryEnrollChecksTheKeyBeforeTheNewPassphrase: a wrong paste is
// the likeliest mistake, and it must not cost two passphrase entries first.
func TestRecoveryEnrollChecksTheKeyBeforeTheNewPassphrase(t *testing.T) {
	isolateXDG(t)
	initVaultWithRecoveryKeyFile(t, "personal")
	loseTheIdentityFile(t)

	stranger, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	res, _, p := runEnroll(t, stranger.String(), "--device", "laptop-2")
	if res.Code != int(exitcode.LockedOrAuth) {
		t.Fatalf("exit code = %d, want %d (LockedOrAuth); stderr=%s", res.Code, exitcode.LockedOrAuth, res.Stderr)
	}
	if len(p.requests) != 0 {
		t.Errorf("a wrong paste still cost %d passphrase prompts", len(p.requests))
	}
	if got := wrappedIdentityFiles(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")); len(got) != 0 {
		t.Errorf("a refused enroll generated an identity anyway: %v", got)
	}
}

func TestRecoveryEnrollRollsBackTheIdentityWhenTheSwapFails(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	loseTheIdentityFile(t)
	before := readVaultConfigForTest(t, "personal")

	// --replaces names nobody, which the library refuses after the new
	// identity has already been generated.
	res, _, _ := runEnroll(t, pasted, "--device", "laptop-2", "--replaces", "desktop-9")
	if res.Code != int(exitcode.NotFound) {
		t.Fatalf("exit code = %d, want %d (NotFound); stderr=%s", res.Code, exitcode.NotFound, res.Stderr)
	}
	if got := wrappedIdentityFiles(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")); len(got) != 0 {
		t.Errorf("a failed enroll left an orphaned identity: %v", got)
	}
	after := readVaultConfigForTest(t, "personal")
	if len(after.Recipients) != len(before.Recipients) {
		t.Errorf("recipients changed: %+v", after.Recipients)
	}
	// The registration still points at the device that was there before.
	if got := readGlobalConfigForTest(t).Vaults["personal"].Device; got != "laptop-1" {
		t.Errorf("global config device = %q, want laptop-1", got)
	}
}

// TestRecoveryEnrollWithoutATerminalRefusesBeforeAnything is D2 again: the
// replacement key is shown once, and a run with nowhere to show it must
// not create anything.
func TestRecoveryEnrollWithoutATerminalRefusesBeforeAnything(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	loseTheIdentityFile(t)

	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{pasted}}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "enroll", "--device", "laptop-2"}, "", false, p)
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	for _, want := range []string{"--recovery-key-out", "--no-new-recovery-key"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("the refusal doesn't name %s:\n%s", want, res.Stderr)
		}
	}
	if len(p.requests) != 0 {
		t.Error("it prompted before refusing")
	}
	if ageSecretKeyPattern.MatchString(res.Stdout + res.Stderr) {
		t.Error("a refusal still printed a key")
	}
}

func TestRecoveryEnrollNoNewRecoveryKeyLeavesNone(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	loseTheIdentityFile(t)

	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{pasted}}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "enroll",
		"--device", "laptop-2", "--no-new-recovery-key"}, "", false, p)
	if res.Code != 0 {
		t.Fatalf("recovery enroll failed: %s", res.Stderr)
	}

	vf := readVaultConfigForTest(t, "personal")
	for _, r := range vf.Recipients {
		if r.Device == gage.RecoveryDeviceLabel {
			t.Error("--no-new-recovery-key still left a recovery recipient")
		}
	}
	if ageSecretKeyPattern.MatchString(res.Stdout + res.Stderr) {
		t.Error("--no-new-recovery-key still printed a key")
	}
	// And it warns that the vault now has no way back.
	if !strings.Contains(res.Stderr, "no recovery key") {
		t.Errorf("it doesn't say the vault is left without a recovery key:\n%s", res.Stderr)
	}
}

func TestRecoveryEnrollKeyOutWritesAPrivateFile(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	loseTheIdentityFile(t)

	out := filepath.Join(t.TempDir(), "new-recovery.key")
	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{pasted}}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "enroll",
		"--device", "laptop-2", "--recovery-key-out", out}, "", false, p)
	if res.Code != 0 {
		t.Fatalf("recovery enroll failed: %s", res.Stderr)
	}

	data, err := os.ReadFile(out) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	written := strings.TrimSpace(string(data))
	if !ageSecretKeyPattern.MatchString(written) {
		t.Fatalf("the file holds no age secret key: %q", written)
	}
	if strings.Contains(res.Stdout+res.Stderr, written) {
		t.Error("--recovery-key-out also printed the key")
	}
	assertVerifies(t, written, true)
	assertVerifies(t, pasted, false)
}

// TestRecoveryEnrollKeepsNoCopyOfEitherKey: the pasted key and the one it
// was replaced with must both be absent from everything gage writes.
func TestRecoveryEnrollKeepsNoCopyOfEitherKey(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	if res := runCLI(t, []string{"insert", "api-token", "--value-stdin"}, "s3cr3t\n"); res.Code != 0 {
		t.Fatalf("seeding: %s", res.Stderr)
	}
	loseTheIdentityFile(t)

	res, newKey, _ := runEnroll(t, pasted, "--device", "laptop-2")
	if res.Code != 0 {
		t.Fatalf("recovery enroll failed: %s", res.Stderr)
	}
	if newKey == "" {
		t.Fatal("no replacement key was shown")
	}

	for _, root := range []string{
		os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_STATE_HOME"),
	} {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, readErr := os.ReadFile(path) // #nosec G304 -- test walks its own temp dir
			if readErr != nil {
				return readErr
			}
			for name, key := range map[string]string{"pasted": pasted, "new": newKey} {
				if bytes.Contains(b, []byte(key)) {
					t.Errorf("the %s recovery key was written to %s", name, path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if n := strings.Count(res.Stdout+res.Stderr, newKey); n != 1 {
		t.Errorf("the new key appears %d times in the output, want exactly 1", n)
	}
	if strings.Contains(res.Stdout+res.Stderr, pasted) {
		t.Error("the pasted key was echoed back")
	}
}

// TestRecoveryEnrollStillPublishesWhenTheConfirmationFails mirrors init's
// rule: the confirmation is the last gate, not an early exit.
func TestRecoveryEnrollStillPublishesWhenTheConfirmationFails(t *testing.T) {
	isolateXDG(t)

	remote := gittest.NewBareRemote(t)
	out := filepath.Join(t.TempDir(), "recovery.key")
	if res := runCLI(t, []string{"init", "personal", "--device", "laptop-1",
		"--remote", remote, "--recovery-key-out", out}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	data, err := os.ReadFile(out) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	pasted := strings.TrimSpace(string(data))
	loseTheIdentityFile(t)

	p := &enrollPrompter{
		fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		pasted:       pasted,
		confirmWith:  "NOTTHEKEY",
	}
	res, _ := runCLIWithApp(t, []string{"recovery", "enroll", "--device", "laptop-2"},
		strings.NewReader(""), true, p, p.watchEnroll(t))

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	// The swap happened and reached the remote anyway.
	other := gittest.NewDevice(t, remote)
	if !other.Exists(t, ".age-recipients") {
		t.Fatal("the vault never reached the remote")
	}
	keys := ageSecretKeyPattern.FindAllString(res.Stderr, -1)
	if len(keys) != 1 {
		t.Errorf("the key was shown %d times, want 1", len(keys))
	}
}

// TestRecoveryEnrollWorksOnAFreshClone: the realistic shape of "this
// machine is gone" is a new machine that has just cloned the vault.
func TestRecoveryEnrollWorksOnAFreshClone(t *testing.T) {
	isolateXDG(t)

	remote := gittest.NewBareRemote(t)
	out := filepath.Join(t.TempDir(), "recovery.key")
	if res := runCLI(t, []string{"init", "personal", "--device", "laptop-1",
		"--remote", remote, "--recovery-key-out", out}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"insert", "api-token", "--value-stdin"}, "s3cr3t\n"); res.Code != 0 {
		t.Fatalf("seeding: %s", res.Stderr)
	}
	data, err := os.ReadFile(out) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	pasted := strings.TrimSpace(string(data))

	// A brand-new machine: fresh XDG roots, nothing but the clone.
	isolateXDG(t)
	if res := runCLI(t, []string{"clone", remote, "--name", "personal"}, ""); res.Code != 0 {
		t.Fatalf("clone failed: %s", res.Stderr)
	}

	res, _, _ := runEnroll(t, pasted, "--device", "laptop-2")
	if res.Code != 0 {
		t.Fatalf("recovery enroll on a fresh clone failed: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if show := runCLI(t, []string{"show", "api-token"}, ""); show.Code != 0 {
		t.Fatalf("the enrolled device cannot read: %s", show.Stderr)
	}
}

// ---------------------------------------------------------------------
// recovery verify agrees with what enroll will accept
// ---------------------------------------------------------------------

// TestRecoveryVerifyDistinguishesDecryptingFromEnrolling is the R3 review's
// third finding: verify used to answer "is this a recipient", which is a
// different question from "can this run recovery enroll", and a user whose
// key was registered under another label was told everything was fine.
func TestRecoveryVerifyDistinguishesDecryptingFromEnrolling(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")

	// A second key, added as an ordinary recipient rather than under the
	// recovery label — the shape a hand-rolled backup takes.
	spare, err := gage.NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	if res := runCLI(t, []string{"recipient", "add", spare.Pubkey, "--device", "paper-backup"}, ""); res.Code != 0 {
		t.Fatalf("adding the spare recipient: %s", res.Stderr)
	}

	t.Run("the recovery key says both", func(t *testing.T) {
		p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{pasted}}
		res, _ := runCLIWithPrompter(t, []string{"recovery", "verify"}, "", false, p)
		if res.Code != 0 {
			t.Fatalf("exit %d, want 0: %s", res.Code, res.Stderr)
		}
		if !strings.Contains(res.Stdout, gage.RecoveryDeviceLabel) {
			t.Errorf("it doesn't name the label:\n%s", res.Stdout)
		}
	})

	t.Run("another recipient's key is honest about both", func(t *testing.T) {
		p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{string(spare.Secret)}}
		res, _ := runCLIWithPrompter(t, []string{"recovery", "verify"}, "", false, p)
		// It is a recipient, so the backup does decrypt: exit 0.
		if res.Code != 0 {
			t.Fatalf("exit %d, want 0 — the key really does decrypt this vault: %s", res.Code, res.Stderr)
		}
		// But it names the label it is registered under, and says enroll
		// will not take it.
		if !strings.Contains(res.Stdout+res.Stderr, "paper-backup") {
			t.Errorf("it doesn't name the label the key is registered under:\n%s%s", res.Stdout, res.Stderr)
		}
		if !strings.Contains(res.Stdout+res.Stderr, "recovery enroll") {
			t.Errorf("it doesn't say enroll won't accept it:\n%s%s", res.Stdout, res.Stderr)
		}
	})

	t.Run("and enroll really does refuse it", func(t *testing.T) {
		loseTheIdentityFile(t)
		res, _, _ := runEnroll(t, string(spare.Secret), "--device", "laptop-2")
		if res.Code != int(exitcode.LockedOrAuth) {
			t.Errorf("exit %d, want %d — verify's caveat has to be true",
				res.Code, exitcode.LockedOrAuth)
		}
	})
}

func TestRecoveryEnrollIsListedInHelp(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"help"}, "")
	if res.Code != 0 {
		t.Fatalf("help failed: %s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "recovery enroll") {
		t.Errorf("`gage help` doesn't list recovery enroll:\n%s", res.Stdout)
	}
}
