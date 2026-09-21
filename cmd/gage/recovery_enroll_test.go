package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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

// ---------------------------------------------------------------------
// Rollback when the machine still holds the identity it is replacing.
// Every other rollback test here loses the identity file first, which is
// what hid the bug below: with nothing to back up, the restore path never
// ran at all.
// ---------------------------------------------------------------------

// abortingPrompter answers the paste and then refuses the new passphrase,
// which is what a user pressing Ctrl-C at "Choose a passphrase" looks like
// from here.
type abortingPrompter struct {
	fakePrompter
	pasted string
}

func (p *abortingPrompter) Value(prompt string) (string, error) {
	p.valuePrompts = append(p.valuePrompts, prompt)
	p.valueCalls++
	return p.pasted, nil
}

func (p *abortingPrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	p.requests = append(p.requests, req)
	return gage.UnlockResponse{}, errors.New("interrupted")
}

// TestRecoveryEnrollRestoresTheIdentityItDisplacedWhenAborted is the
// promise identityBackup exists to keep: rebuilding a machine under its own
// name is legitimate with a perfectly good key on disk, and a run abandoned
// half-way must not be what destroys it.
func TestRecoveryEnrollRestoresTheIdentityItDisplacedWhenAborted(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")

	path := identityFileForTest(t, "personal", "laptop-1")
	original, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}

	p := &abortingPrompter{
		fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		pasted:       pasted,
	}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "enroll",
		"--device", "laptop-1", "--replaces", "laptop-1",
		"--recovery-key-out", filepath.Join(t.TempDir(), "new.key")}, "", false, p)
	if res.Code == 0 {
		t.Fatal("expected the aborted passphrase to fail the command")
	}

	// The only identity this machine had must still be there, byte for
	// byte, and must still open the vault.
	after, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatalf("the identity file was destroyed by an aborted enroll: %v", err)
	}
	if !bytes.Equal(original, after) {
		t.Error("the identity file came back changed")
	}
	if show := runCLI(t, []string{"recipient", "list"}, ""); show.Code != 0 {
		t.Errorf("the vault is unusable after an aborted enroll: %s", show.Stderr)
	}
}

// TestRecoveryEnrollShowsTheNewKeyEvenIfRegistrationFails: by the time the
// swap has committed and pushed, the replacement key is the vault's and its
// private half exists only in this process. Anything that fails afterwards
// must not be what swallows it.
func TestRecoveryEnrollShowsTheNewKeyEvenIfRegistrationFails(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	loseTheIdentityFile(t)

	// The global config directory is made read-only: reads still work, so
	// the run gets as far as the swap, and the atomic write that records
	// the new identity afterwards is what fails. No injected seam — this
	// is the real failure, produced by the real filesystem.
	if runtime.GOOS == "windows" {
		t.Skip("directory write permission is not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gage")
	if err := os.Chmod(cfgDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgDir, 0o700) })

	out := filepath.Join(t.TempDir(), "new.key")
	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{pasted}}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "enroll",
		"--device", "laptop-2", "--recovery-key-out", out}, "", false, p)

	if res.Code == 0 {
		t.Fatal("expected the failed registration to fail the command")
	}
	// The swap happened, so the key is real. It must have reached the user.
	data, err := os.ReadFile(out) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatalf("the replacement recovery key was never delivered: %v", err)
	}
	if !ageSecretKeyPattern.MatchString(strings.TrimSpace(string(data))) {
		t.Errorf("the delivered file holds no key: %q", data)
	}
}

// TestRecoveryEnrollRefusesAColidingNameBeforeThePaste: a name already in
// use is knowable from the recipient list alone, so it must not cost a
// paste and a full passphrase entry to find out.
func TestRecoveryEnrollRefusesACollidingNameBeforeThePaste(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	loseTheIdentityFile(t)

	// laptop-1 is still a recipient and --replaces does not name it.
	res, _, p := runEnroll(t, pasted, "--device", "laptop-1")
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if len(p.valuePrompts) != 0 {
		t.Errorf("it asked for the key before refusing: %v", p.valuePrompts)
	}
	if len(p.requests) != 0 {
		t.Error("it asked for a passphrase before refusing")
	}
	if !strings.Contains(res.Stderr, "--replaces") {
		t.Errorf("the refusal doesn't name the way out:\n%s", res.Stderr)
	}
}

// TestRecoveryEnrollReplacesADifferentLostDevice covers the other --replaces
// shape: a new machine taking over from one that is gone, under a new name.
func TestRecoveryEnrollReplacesADifferentLostDevice(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	loseTheIdentityFile(t)

	res, _, _ := runEnroll(t, pasted, "--device", "laptop-2", "--replaces", "laptop-1")
	if res.Code != 0 {
		t.Fatalf("recovery enroll failed: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	vf := readVaultConfigForTest(t, "personal")
	got := make([]string, 0, len(vf.Recipients))
	for _, r := range vf.Recipients {
		got = append(got, r.Device)
	}
	want := "laptop-2," + gage.RecoveryDeviceLabel
	if strings.Join(got, ",") != want {
		t.Errorf("recipients = %v, want [%s]", got, want)
	}
	if !strings.Contains(res.Stdout, "laptop-1") {
		t.Errorf("the summary doesn't say what was removed:\n%s", res.Stdout)
	}
}

func TestRecoveryEnrollLocalIdentityErrorIsMatchable(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")

	// A device with a local identity file whose name is not a recipient,
	// so the identity-file check is what refuses rather than the name
	// collision.
	if res := runCLI(t, []string{"identity", "add", "--device", "spare-1"}, ""); res.Code != 0 {
		t.Fatalf("seeding a second identity: %s", res.Stderr)
	}
	res, _, _ := runEnroll(t, pasted, "--device", "spare-1")
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, ErrLocalIdentityExists.Error()) {
		t.Errorf("the refusal doesn't carry the sentinel's text:\n%s", res.Stderr)
	}
}

// TestNoRecoveryKeyWarningNamesARealCommand: pointing at a command that
// does not exist is worse than saying nothing — the suggested fix prints
// usage and exits 0, so the user believes the vault has a recovery key.
func TestNoRecoveryKeyWarningNamesARealCommand(t *testing.T) {
	isolateXDG(t)
	pasted := initVaultWithRecoveryKeyFile(t, "personal")
	loseTheIdentityFile(t)

	p := &fakePrompter{passphrases: []string{testPassphrase}, values: []string{pasted}}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "enroll",
		"--device", "laptop-2", "--no-new-recovery-key"}, "", false, p)
	if res.Code != 0 {
		t.Fatalf("recovery enroll failed: %s", res.Stderr)
	}

	// Whatever the warning tells the user to run has to be a real command.
	for _, line := range strings.Split(res.Stderr, "\n") {
		for _, word := range []string{"gage recovery rotate", "gage recovery enroll", "gage recipient add"} {
			if !strings.Contains(line, word) {
				continue
			}
			args := strings.Fields(strings.TrimPrefix(word, "gage "))
			if _, ok := findCommand(strings.Join(args, " ")); !ok {
				t.Errorf("the warning suggests %q, which is not a registered command:\n%s", word, line)
			}
		}
	}
}

// TestCloneAndSessionPointAtRecoveryEnroll: a sole owner holding a paper
// key was told only to publish an enrollment request, which needs somebody
// else to approve it. For them that is a dead end.
func TestCloneAndSessionPointAtRecoveryEnroll(t *testing.T) {
	isolateXDG(t)

	remote := gittest.NewBareRemote(t)
	if res := runCLI(t, []string{"init", "personal", "--device", "laptop-1",
		"--remote", remote, "--recovery-key-out", filepath.Join(t.TempDir(), "rk")}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	isolateXDG(t)
	res := runCLI(t, []string{"clone", remote, "--name", "personal"}, "")
	if res.Code != 0 {
		t.Fatalf("clone failed: %s", res.Stderr)
	}
	out := res.Stdout + res.Stderr
	if !strings.Contains(out, "recovery enroll") {
		t.Errorf("clone's no-identity advice never mentions the recovery key:\n%s", out)
	}
}

// ---------------------------------------------------------------------
// gage recovery rotate
// ---------------------------------------------------------------------

// rotatePrompter answers rotate's two questions: the owner's passphrase
// (through the embedded fakePrompter) and the re-type confirmation, read
// off the screen the way recoveryPrompter does for init.
type rotatePrompter struct {
	fakePrompter
	screen *bytes.Buffer
}

func (p *rotatePrompter) Value(prompt string) (string, error) {
	p.valuePrompts = append(p.valuePrompts, prompt)
	p.valueCalls++
	keys := ageSecretKeyPattern.FindAllString(p.screen.String(), -1)
	if len(keys) == 0 {
		return "", nil
	}
	key := keys[len(keys)-1]
	return key[len(key)-recoveryConfirmChars:], nil
}

func runRotate(t *testing.T, args ...string) (cliResult, string, *rotatePrompter) {
	t.Helper()
	p := &rotatePrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithApp(t, append([]string{"recovery", "rotate"}, args...),
		strings.NewReader(""), true, p, func(app *App) {
			buf, ok := app.Err.(*bytes.Buffer)
			if !ok {
				t.Fatalf("app.Err is %T, want *bytes.Buffer", app.Err)
			}
			p.screen = buf
		})
	var shown string
	if keys := ageSecretKeyPattern.FindAllString(res.Stderr, -1); len(keys) > 0 {
		shown = keys[len(keys)-1]
	}
	return res, shown, p
}

func TestRecoveryRotateReplacesTheKey(t *testing.T) {
	isolateXDG(t)
	old := initVaultWithRecoveryKeyFile(t, "personal")
	if res := runCLI(t, []string{"insert", "api-token", "--value-stdin"}, "s3cr3t\n"); res.Code != 0 {
		t.Fatalf("seeding: %s", res.Stderr)
	}

	res, shown, p := runRotate(t)
	if res.Code != 0 {
		t.Fatalf("rotate failed: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if shown == "" || shown == old {
		t.Fatalf("shown key = %q; want a new key distinct from the old one", shown)
	}
	// It needed an unlock, and asked for the owner's passphrase once.
	if len(p.requests) != 1 || p.requests[0].Purpose != gage.PurposeUnlock {
		t.Errorf("unlock requests = %+v, want exactly one PurposeUnlock", p.requests)
	}

	assertVerifies(t, shown, true)
	assertVerifies(t, old, false)
	// The vault still reads for the owner, and holds the same two recipients.
	if show := runCLI(t, []string{"show", "api-token"}, ""); show.Code != 0 {
		t.Fatalf("the vault is unreadable after rotate: %s", show.Stderr)
	}
	vf := readVaultConfigForTest(t, "personal")
	if len(vf.Recipients) != 2 || vf.Recipients[1].Device != gage.RecoveryDeviceLabel {
		t.Errorf("recipients = %+v", vf.Recipients)
	}
	if !strings.Contains(res.Stdout, vf.Recipients[1].Pubkey) {
		t.Errorf("the summary doesn't name the new recovery key:\n%s", res.Stdout)
	}
}

// TestRecoveryRotateAddsOneToAVaultWithout: the natural way to recover from
// having said --no-recovery-key.
func TestRecoveryRotateAddsOneToAVaultWithout(t *testing.T) {
	isolateXDG(t)
	if res := runCLI(t, []string{"init", "personal", "--device", "laptop-1", "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	if res := runCLI(t, []string{"insert", "api-token", "--value-stdin"}, "s3cr3t\n"); res.Code != 0 {
		t.Fatalf("seeding: %s", res.Stderr)
	}

	res, shown, _ := runRotate(t)
	if res.Code != 0 {
		t.Fatalf("rotate failed: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	assertVerifies(t, shown, true)
	if vf := readVaultConfigForTest(t, "personal"); len(vf.Recipients) != 2 {
		t.Errorf("recipients = %+v, want the device and a new recovery recipient", vf.Recipients)
	}
}

// TestRecoveryRotateWithoutATerminalRefusesBeforeAnyPrompt: the new key is
// shown once, so a run with nowhere to show it must not even ask for the
// passphrase — and unlike init and enroll there is no opt-out to name,
// since rotate exists to produce a key.
func TestRecoveryRotateWithoutATerminalRefusesBeforeAnyPrompt(t *testing.T) {
	isolateXDG(t)
	initVaultWithRecoveryKeyFile(t, "personal")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "rotate"}, "", false, p)
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "--recovery-key-out") {
		t.Errorf("the refusal doesn't name --recovery-key-out:\n%s", res.Stderr)
	}
	if strings.Contains(res.Stderr, "--no-") {
		t.Errorf("the refusal names an opt-out flag rotate does not have:\n%s", res.Stderr)
	}
	if len(p.requests) != 0 {
		t.Error("it asked for a passphrase before refusing")
	}
	if ageSecretKeyPattern.MatchString(res.Stdout + res.Stderr) {
		t.Error("a refusal still printed a key")
	}
}

func TestRecoveryRotateKeyOutWritesAPrivateFile(t *testing.T) {
	isolateXDG(t)
	old := initVaultWithRecoveryKeyFile(t, "personal")

	out := filepath.Join(t.TempDir(), "next.key")
	p := &fakePrompter{passphrases: []string{testPassphrase}}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "rotate", "--recovery-key-out", out}, "", false, p)
	if res.Code != 0 {
		t.Fatalf("rotate failed: %s", res.Stderr)
	}
	data, err := os.ReadFile(out) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	written := strings.TrimSpace(string(data))
	if strings.Contains(res.Stdout+res.Stderr, written) {
		t.Error("--recovery-key-out also printed the key")
	}
	assertVerifies(t, written, true)
	assertVerifies(t, old, false)
}

// TestRecoveryRotateKeepsNoCopyOfEitherKey: neither the retired key nor the
// new one may be written anywhere gage owns, and the new one is shown
// exactly once.
func TestRecoveryRotateKeepsNoCopyOfEitherKey(t *testing.T) {
	isolateXDG(t)
	old := initVaultWithRecoveryKeyFile(t, "personal")
	if res := runCLI(t, []string{"insert", "api-token", "--value-stdin"}, "s3cr3t\n"); res.Code != 0 {
		t.Fatalf("seeding: %s", res.Stderr)
	}

	res, shown, _ := runRotate(t)
	if res.Code != 0 {
		t.Fatalf("rotate failed: %s", res.Stderr)
	}
	for _, root := range []string{os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_STATE_HOME")} {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, rerr := os.ReadFile(path) // #nosec G304 -- test walks its own temp dir
			if rerr != nil {
				return rerr
			}
			for name, key := range map[string]string{"retired": old, "new": shown} {
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
	if n := strings.Count(res.Stdout+res.Stderr, shown); n != 1 {
		t.Errorf("the new key appears %d times in the output, want exactly 1", n)
	}
}

// TestRecoveryRotateStillPublishesWhenTheConfirmationFails mirrors init and
// enroll: the confirmation is the last gate, not an early exit.
func TestRecoveryRotateStillPublishesWhenTheConfirmationFails(t *testing.T) {
	isolateXDG(t)
	remote := gittest.NewBareRemote(t)
	if res := runCLI(t, []string{"init", "personal", "--device", "laptop-1", "--remote", remote,
		"--recovery-key-out", filepath.Join(t.TempDir(), "rk")}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	p := &enrollPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		pasted: "unused", confirmWith: "NOTTHEKEY"}
	// enrollPrompter answers its first Value call as the paste; rotate has
	// no paste, so burn that answer on the first (confirmation) call.
	p.valueCalls = 1
	res, _ := runCLIWithApp(t, []string{"recovery", "rotate"}, strings.NewReader(""), true, p, p.watchEnroll(t))
	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	other := gittest.NewDevice(t, remote)
	if !other.Exists(t, ".age-recipients") {
		t.Error("the vault never reached the remote")
	}
	if keys := ageSecretKeyPattern.FindAllString(res.Stderr, -1); len(keys) != 1 {
		t.Errorf("the key was shown %d times, want 1", len(keys))
	}
}

func TestRecoveryRotateIsListedInHelp(t *testing.T) {
	isolateXDG(t)
	res := runCLI(t, []string{"help"}, "")
	if res.Code != 0 || !strings.Contains(res.Stdout, "recovery rotate") {
		t.Errorf("`gage help` doesn't list recovery rotate (exit %d):\n%s", res.Code, res.Stdout)
	}
}

// TestRecoveryRotateChecksTheKeyDestinationBeforeTheUnlock: init and
// recovery enroll both reject an unusable --recovery-key-out before any
// prompt, and rotate's own comment claims the same. A bad path found after
// the passphrase and a remote sync is a knowable failure charged to the
// user twice.
func TestRecoveryRotateChecksTheKeyDestinationBeforeTheUnlock(t *testing.T) {
	isolateXDG(t)
	initVaultWithRecoveryKeyFile(t, "personal")

	// Inside the vault, which the destination rules refuse: gage resets a
	// vault's working tree and would delete the key.
	entry := readGlobalConfigForTest(t).Vaults["personal"]
	p := &fakePrompter{passphrases: []string{testPassphrase}}
	res, _ := runCLIWithPrompter(t, []string{"recovery", "rotate",
		"--recovery-key-out", filepath.Join(entry.Path, "rk")}, "", false, p)

	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if len(p.requests) != 0 {
		t.Errorf("rotate asked for a passphrase before rejecting the destination: %+v", p.requests)
	}
}

// TestNoCommandLetsADeviceTakeTheRecoveryLabel walks the CLI surface that
// names a device. The label has to be unavailable on every one of them,
// because `recovery rotate` evicts whatever holds it.
func TestNoCommandLetsADeviceTakeTheRecoveryLabel(t *testing.T) {
	label := gage.RecoveryDeviceLabel

	t.Run("init, even with --no-recovery-key", func(t *testing.T) {
		isolateXDG(t)
		dir := filepath.Join(t.TempDir(), "personal")
		res := runCLI(t, []string{"init", "personal", "--dir", dir,
			"--device", label, "--no-recovery-key"}, "")
		if res.Code != int(exitcode.Usage) {
			t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, ".gage")); !os.IsNotExist(err) {
			t.Error("a refused init created the vault")
		}
	})

	t.Run("identity add", func(t *testing.T) {
		isolateXDG(t)
		initVaultWithRecoveryKeyFile(t, "personal")
		res := runCLI(t, []string{"identity", "add", "--device", label}, "")
		if res.Code != int(exitcode.Usage) {
			t.Errorf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
		}
	})

	t.Run("recipient add", func(t *testing.T) {
		isolateXDG(t)
		initVaultWithRecoveryKeyFile(t, "personal")
		spare, err := gage.NewRecoveryKey()
		if err != nil {
			t.Fatal(err)
		}
		res := runCLI(t, []string{"recipient", "add", spare.Pubkey, "--device", label}, "")
		if res.Code == 0 {
			t.Error("recipient add let a key take the recovery label")
		}
	})

	t.Run("recovery enroll", func(t *testing.T) {
		isolateXDG(t)
		pasted := initVaultWithRecoveryKeyFile(t, "personal")
		loseTheIdentityFile(t)
		res, _, _ := runEnroll(t, pasted, "--device", label)
		if res.Code != int(exitcode.Usage) {
			t.Errorf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
		}
	})
}
