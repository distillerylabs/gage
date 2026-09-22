package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gittest"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

// ageSecretKeyPattern matches a bare age secret key wherever it appears —
// in output, in a file, or in a git object. Every hygiene assertion below
// searches for the real generated key rather than this pattern; the
// pattern is only how a test reads the key back off the screen in the
// first place.
var ageSecretKeyPattern = regexp.MustCompile(`AGE-SECRET-KEY-1[A-Z0-9]+`)

// recoveryPrompter answers init's re-type confirmation the way a human
// does: by reading the key off the screen. It is pointed at the run's
// stderr buffer (through runCLIWithApp's customize hook), scans it for the
// key init just displayed, and types that key's last characters back.
//
// Answering from the screen rather than from a value the test planted is
// the point: it is what makes "the key shown is the key that satisfies the
// confirmation" a thing the test proves rather than assumes.
type recoveryPrompter struct {
	fakePrompter
	screen *bytes.Buffer

	// answer, when non-empty, is typed instead of what is on screen — how
	// a test drives the wrong-answer path.
	answer string
}

func (p *recoveryPrompter) Value(prompt string) (string, error) {
	p.valuePrompts = append(p.valuePrompts, prompt)
	p.valueCalls++
	if p.answer != "" {
		return p.answer, nil
	}
	key := ageSecretKeyPattern.FindString(p.screen.String())
	if key == "" {
		return "", nil
	}
	return key[len(key)-recoveryConfirmChars:], nil
}

// watch points this prompter at the buffer init draws the key on, as
// runCLIWithApp's customize hook.
func (p *recoveryPrompter) watch(t *testing.T) func(*App) {
	t.Helper()
	return func(app *App) {
		buf, ok := app.Err.(*bytes.Buffer)
		if !ok {
			t.Fatalf("app.Err is %T, want *bytes.Buffer", app.Err)
		}
		p.screen = buf
	}
}

// runInitWithRecoveryKey runs `init` with a terminal and a prompter that
// confirms the recovery key from the screen, and returns the result
// alongside the secret that was displayed.
func runInitWithRecoveryKey(t *testing.T, args ...string) (cliResult, string) {
	t.Helper()
	p := &recoveryPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithApp(t, args, strings.NewReader(""), true, p, p.watch(t))
	return res, ageSecretKeyPattern.FindString(res.Stderr)
}

func TestInitRegistersARecoveryRecipientByDefault(t *testing.T) {
	isolateXDG(t)

	res, secret := runInitWithRecoveryKey(t, "init", "personal", "--device", "laptop-1")
	if res.Code != 0 {
		t.Fatalf("init failed: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if secret == "" {
		t.Fatalf("init showed no recovery key:\n%s", res.Stderr)
	}

	vf := readVaultConfigForTest(t, "personal")
	if len(vf.Recipients) != 2 {
		t.Fatalf("config has %d recipients, want 2: %+v", len(vf.Recipients), vf.Recipients)
	}
	if vf.Recipients[0].Device != "laptop-1" {
		t.Errorf("recipients[0].Device = %q, want laptop-1", vf.Recipients[0].Device)
	}
	if vf.Recipients[1].Device != gage.RecoveryDeviceLabel {
		t.Errorf("recipients[1].Device = %q, want %q", vf.Recipients[1].Device, gage.RecoveryDeviceLabel)
	}

	// The displayed secret's public half is exactly the recipient that was
	// recorded — the two halves of the same key, not merely two keys.
	ident, err := age.ParseX25519Identity(secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := ident.Recipient().String(); got != vf.Recipients[1].Pubkey {
		t.Errorf("shown secret's pubkey = %s, want the recorded %s", got, vf.Recipients[1].Pubkey)
	}

	// And .age-recipients agrees with config.toml, in the same order.
	entry := readGlobalConfigForTest(t).Vaults["personal"]
	data, err := os.ReadFile(filepath.Join(entry.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	want := vf.Recipients[0].Pubkey + "\n" + vf.Recipients[1].Pubkey + "\n"
	if string(data) != want {
		t.Errorf(".age-recipients = %q, want %q", data, want)
	}

	// The recovery recipient's public key is reported, so a user can tell
	// which key the printed secret belongs to later.
	if !strings.Contains(res.Stdout, vf.Recipients[1].Pubkey) {
		t.Errorf("init output doesn't name the recovery recipient's public key:\n%s", res.Stdout)
	}
}

// TestInitRecoveryKeyAloneDecryptsTheVault is the milestone's reason to
// exist: the printed key, and nothing else, opens what the vault encrypts.
func TestInitRecoveryKeyAloneDecryptsTheVault(t *testing.T) {
	isolateXDG(t)

	res, secret := runInitWithRecoveryKey(t, "init", "personal", "--device", "laptop-1")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	entry := readGlobalConfigForTest(t).Vaults["personal"]
	data, err := os.ReadFile(filepath.Join(entry.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	var to []gage.Recipient
	for _, line := range strings.Fields(string(data)) {
		r, err := gage.ParseRecipient(line)
		if err != nil {
			t.Fatal(err)
		}
		to = append(to, r)
	}
	payload := []byte("encrypted to the vault as init left it")
	ct, err := gage.Encrypt(payload, to...)
	if err != nil {
		t.Fatal(err)
	}

	ident, err := age.ParseX25519Identity(secret)
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(bytes.NewReader(ct), ident)
	if err != nil {
		t.Fatalf("the recovery key could not decrypt the vault's ciphertext: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := r.Read(got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decrypted %q, want %q", got, payload)
	}
}

// TestInitRecoveryKeyIsNeverStored is the hygiene assertion: gage keeps no
// copy of the secret anywhere it writes, and shows it exactly once.
func TestInitRecoveryKeyIsNeverStored(t *testing.T) {
	isolateXDG(t)

	res, secret := runInitWithRecoveryKey(t, "init", "personal", "--device", "laptop-1")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	if secret == "" {
		t.Fatal("no recovery key was shown")
	}

	// Everything gage owns: the vault (working tree and .git objects), the
	// identities directory, and global config.
	roots := []string{
		os.Getenv("XDG_DATA_HOME"),
		os.Getenv("XDG_CONFIG_HOME"),
		os.Getenv("XDG_STATE_HOME"),
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue // nothing was written under this root at all
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, readErr := os.ReadFile(path) // #nosec G304 -- test walks its own temp dir
			if readErr != nil {
				return readErr
			}
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("the recovery secret was written to %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if n := strings.Count(res.Stdout+res.Stderr, secret); n != 1 {
		t.Errorf("the secret appears %d times in the output, want exactly 1", n)
	}
	if strings.Contains(res.Stdout, secret) {
		t.Error("the secret was written to stdout; it belongs on stderr with the prompts")
	}
	// The confirmation prompt must not carry the answer with it.
	if strings.Contains(res.Stderr, "\n"+secret[len(secret)-recoveryConfirmChars:]+"\n") {
		t.Error("the confirmation echoed the characters it was asking for")
	}
}

// TestInitRecoveryKeyIsShownWithItsWarningsAndAQR: the key is unencrypted,
// so what is printed beside it is the only thing standing between a user
// and a screenshot in a cloud-synced notes app.
func TestInitRecoveryKeyIsShownWithItsWarningsAndAQR(t *testing.T) {
	isolateXDG(t)

	res, secret := runInitWithRecoveryKey(t, "init", "personal", "--device", "laptop-1")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	for _, want := range []string{
		"not encrypted",
		"anyone who has it can read",
		"cannot be shown again",
		"offline",
	} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("the recovery key warning doesn't say %q:\n%s", want, res.Stderr)
		}
	}
	if !strings.Contains(res.Stderr, string(qrBoth)) {
		t.Error("no QR code was rendered beside the key")
	}
	if !strings.Contains(res.Stderr, secret) {
		t.Error("the key itself was not printed in text form")
	}
}

func TestInitNoRecoveryKeyOptsOut(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"init", "personal", "--device", "laptop-1", "--no-recovery-key"}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	vf := readVaultConfigForTest(t, "personal")
	if len(vf.Recipients) != 1 {
		t.Fatalf("config has %d recipients, want 1: %+v", len(vf.Recipients), vf.Recipients)
	}
	if ageSecretKeyPattern.MatchString(res.Stdout + res.Stderr) {
		t.Errorf("--no-recovery-key still printed a secret key:\n%s%s", res.Stdout, res.Stderr)
	}
	if strings.Contains(res.Stdout+res.Stderr, gage.RecoveryDeviceLabel) {
		t.Error("--no-recovery-key still mentioned a recovery recipient")
	}
}

// TestInitWithoutATerminalRefusesRatherThanLoggingTheKey is D2: with
// nowhere a human can read, init will not create a vault whose recovery
// key would land in a log file — and it says which flag resolves that.
func TestInitWithoutATerminalRefusesRatherThanLoggingTheKey(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"init", "personal", "--device", "laptop-1"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	for _, want := range []string{"--recovery-key-out", "--no-recovery-key"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("the refusal doesn't name %s:\n%s", want, res.Stderr)
		}
	}
	if ageSecretKeyPattern.MatchString(res.Stdout + res.Stderr) {
		t.Error("a refusal still generated and printed a key")
	}

	// Nothing was created: not the vault, not the registration, not an
	// identity file.
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("a refused init registered the vault anyway")
	}
	identities := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")
	if got := wrappedIdentityFiles(t, identities); len(got) != 0 {
		t.Errorf("a refused init left identity files: %v", got)
	}
	vaults := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "vaults")
	if _, err := os.Stat(filepath.Join(vaults, "personal")); !os.IsNotExist(err) {
		t.Error("a refused init created the vault directory")
	}
}

// TestInitWithRedirectedStderrRefusesToo closes the half of D2 that a
// stdin-only check misses: `gage init foo 2>build.log` has a human at the
// keyboard but nowhere on screen for the key to appear, so the key would
// go into the log instead.
func TestInitWithRedirectedStderrRefusesToo(t *testing.T) {
	isolateXDG(t)

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	res, _ := runCLIWithApp(t, []string{"init", "personal", "--device", "laptop-1"},
		strings.NewReader(""), true, p,
		func(app *App) { app.IsErrTerminal = func() bool { return false } })

	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if ageSecretKeyPattern.MatchString(res.Stdout + res.Stderr) {
		t.Error("a key was generated and printed into the redirected stream")
	}
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("a refused init registered the vault anyway")
	}
}

func TestInitRecoveryKeyOutWritesAPrivateFileAndNothingToStdout(t *testing.T) {
	isolateXDG(t)

	out := filepath.Join(t.TempDir(), "recovery.key")
	res := runCLI(t, []string{"init", "personal", "--device", "laptop-1", "--recovery-key-out", out}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	data, err := os.ReadFile(out) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(string(data))
	if !ageSecretKeyPattern.MatchString(secret) {
		t.Fatalf("the file doesn't hold an age secret key: %q", secret)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("perm = %v, want 0600", perm)
		}
	}

	// The key is in the file and nowhere else.
	if strings.Contains(res.Stdout+res.Stderr, secret) {
		t.Error("--recovery-key-out also printed the key")
	}
	// But the user is told where it went, and that it is unencrypted.
	if !strings.Contains(res.Stderr, out) {
		t.Errorf("init doesn't say where the key was written:\n%s", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "not encrypted") {
		t.Errorf("init doesn't warn that the written key is unencrypted:\n%s", res.Stderr)
	}

	// It is a real recipient of the vault it belongs to.
	vf := readVaultConfigForTest(t, "personal")
	if len(vf.Recipients) != 2 || vf.Recipients[1].Device != gage.RecoveryDeviceLabel {
		t.Fatalf("recipients = %+v", vf.Recipients)
	}
	ident, err := age.ParseX25519Identity(secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := ident.Recipient().String(); got != vf.Recipients[1].Pubkey {
		t.Errorf("the written key's pubkey = %s, want %s", got, vf.Recipients[1].Pubkey)
	}
}

func TestInitRecoveryKeyOutRefusesToClobberAnExistingFile(t *testing.T) {
	isolateXDG(t)

	out := filepath.Join(t.TempDir(), "recovery.key")
	if err := os.WriteFile(out, []byte("someone else's key"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"init", "personal", "--device", "laptop-1", "--recovery-key-out", out}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	data, err := os.ReadFile(out) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "someone else's key" {
		t.Error("the existing file was overwritten")
	}
	// Checked before anything is created, like every other knowable
	// failure init has.
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("a refused init registered the vault anyway")
	}
	identities := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")
	if got := wrappedIdentityFiles(t, identities); len(got) != 0 {
		t.Errorf("a refused init left identity files: %v", got)
	}
}

func TestInitRejectsRecoveryKeyOutWithNoRecoveryKey(t *testing.T) {
	isolateXDG(t)

	out := filepath.Join(t.TempDir(), "recovery.key")
	res := runCLI(t, []string{"init", "personal", "--no-recovery-key", "--recovery-key-out", out}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a rejected flag combination still wrote the key file")
	}
}

// TestInitRejectsADeviceNamedLikeTheRecoveryLabel: the label is fixed, so
// a device claiming it would collide. Caught before the passphrase prompt
// rather than by Create, so nothing is generated and rolled back.
func TestInitRejectsADeviceNamedLikeTheRecoveryLabel(t *testing.T) {
	isolateXDG(t)

	res := runCLIWithTerminal(t, []string{"init", "personal", "--device", gage.RecoveryDeviceLabel}, "", true)
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, gage.RecoveryDeviceLabel) {
		t.Errorf("the error doesn't name the reserved label:\n%s", res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")); !os.IsNotExist(err) {
		t.Error("a rejected device name still created something under identities/")
	}

	// --no-recovery-key does NOT free the label, and that is a deliberate
	// change from how this milestone first shipped. A vault made without a
	// recovery key can still grow one through `gage recovery rotate`, which
	// replaces whatever holds the label — so a device there would be
	// evicted by its own rotation, and the vault re-encrypted to a paper
	// key alone. See TestNoCommandLetsADeviceTakeTheRecoveryLabel.
	if res := runCLI(t, []string{"init", "personal", "--device", gage.RecoveryDeviceLabel, "--no-recovery-key"}, ""); res.Code != int(exitcode.Usage) {
		t.Errorf("the label must stay reserved even with --no-recovery-key: exit %d, %s", res.Code, res.Stderr)
	}
}

// TestInitShowsNoRecoveryKeyWhenCreateFails is the ordering rule: the key
// is displayed only once the vault it opens actually exists, so a failed
// init never leaves a user holding a key to nothing.
func TestInitShowsNoRecoveryKeyWhenCreateFails(t *testing.T) {
	isolateXDG(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, secret := runInitWithRecoveryKey(t, "init", "personal", "--device", "laptop-1", "--dir", dir)
	if res.Code == 0 {
		t.Fatal("expected init into a non-empty directory to fail")
	}
	if secret != "" {
		t.Errorf("a failed init displayed a recovery key:\n%s", res.Stderr)
	}
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("a failed init registered the vault anyway")
	}
	identities := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")
	if got := wrappedIdentityFiles(t, identities); len(got) != 0 {
		t.Errorf("a failed init left orphaned identity files: %v", got)
	}
}

// TestInitRecoveryKeyConfirmationAsksForTheKeysOwnCharacters pins D3's
// shape: a masked re-type of a fixed number of the key's last characters.
func TestInitRecoveryKeyConfirmationAsksForTheKeysOwnCharacters(t *testing.T) {
	isolateXDG(t)

	if recoveryConfirmChars != 6 {
		t.Fatalf("recoveryConfirmChars = %d; the prompt below is written for 6", recoveryConfirmChars)
	}

	p := &recoveryPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}}
	res, _ := runCLIWithApp(t, []string{"init", "personal", "--device", "laptop-1"},
		strings.NewReader(""), true, p, p.watch(t))
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	if len(p.valuePrompts) != 1 {
		t.Fatalf("Prompter.Value called %d times, want 1: %v", len(p.valuePrompts), p.valuePrompts)
	}
	if !strings.Contains(p.valuePrompts[0], "6") {
		t.Errorf("the prompt doesn't say how many characters: %q", p.valuePrompts[0])
	}
}

// TestInitRecoveryKeyWrongConfirmationRetriesThenFailsWithoutReshowing:
// the vault exists by the time the question is asked, so a wrong answer
// cannot undo anything — but it must not pass silently either, and it must
// never print the key a second time.
func TestInitRecoveryKeyWrongConfirmationRetriesThenFailsWithoutReshowing(t *testing.T) {
	isolateXDG(t)

	p := &recoveryPrompter{
		fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		answer:       "NOTTHEKEY",
	}
	res, _ := runCLIWithApp(t, []string{"init", "personal", "--device", "laptop-1"},
		strings.NewReader(""), true, p, p.watch(t))

	if res.Code != int(exitcode.Conflict) {
		t.Errorf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	if p.valueCalls != maxRecoveryConfirmAttempts {
		t.Errorf("asked %d times, want %d", p.valueCalls, maxRecoveryConfirmAttempts)
	}

	// The vault is real and still registered: the confirmation is about
	// the human, not about the vault.
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; !ok {
		t.Error("the vault was created but not registered")
	}
	vf := readVaultConfigForTest(t, "personal")
	if len(vf.Recipients) != 2 {
		t.Errorf("recipients = %+v, want the recovery key among them", vf.Recipients)
	}

	// Shown once, on the first pass, and never again.
	secret := ageSecretKeyPattern.FindString(res.Stderr)
	if secret == "" {
		t.Fatal("no key was shown at all")
	}
	if n := strings.Count(res.Stderr, secret); n != 1 {
		t.Errorf("the key was shown %d times, want 1", n)
	}
	if !strings.Contains(res.Stderr, "cannot be shown again") {
		t.Errorf("the failure doesn't tell the user the key is gone:\n%s", res.Stderr)
	}
}

// TestInitRefusesToWriteTheRecoveryKeyWhereGageWillDeleteIt is the whole
// point of the destination check: gage resets a vault's working tree on
// the next write after an interrupted one, and ResetHard removes untracked
// files. A key filed inside a vault — any vault — is a key gage deletes
// itself, with a message about discarding an interrupted write.
func TestInitRefusesToWriteTheRecoveryKeyWhereGageWillDeleteIt(t *testing.T) {
	isolateXDG(t)

	// An existing vault, to aim at from the second case below.
	existing := filepath.Join(t.TempDir(), "other")
	if res := runCLI(t, []string{"init", "other", "--dir", existing, "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("seeding a vault: %s", res.Stderr)
	}
	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "personal")

	tests := []struct {
		name string
		out  string
		why  string
	}{
		{"inside the vault being created", filepath.Join(target, "recovery.key"), "vault"},
		{"below the vault being created", filepath.Join(target, "keys", "recovery.key"), "vault"},
		{"inside an existing vault", filepath.Join(existing, "recovery.key"), "vault"},
		{"below an existing vault's entries", filepath.Join(existing, "entries", "r.key"), "vault"},
		{"under $GAGE_DATA", filepath.Join(dataDir, "recovery.key"), "gage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := runCLI(t, []string{"init", "personal", "--dir", target,
				"--recovery-key-out", tc.out}, "")
			if res.Code != int(exitcode.Usage) {
				t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
			}
			if !strings.Contains(res.Stderr, tc.why) {
				t.Errorf("the refusal doesn't explain itself (%q):\n%s", tc.why, res.Stderr)
			}
			if _, err := os.Stat(tc.out); !os.IsNotExist(err) {
				t.Error("a refused path was written to anyway")
			}
			// Refused before anything is created, like every other
			// knowable failure init has.
			if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
				t.Error("a refused init registered the vault anyway")
			}
			if _, err := os.Stat(filepath.Join(target, ".gage")); !os.IsNotExist(err) {
				t.Error("a refused init created the vault directory")
			}
		})
	}
}

// TestInitChecksTheRecoveryKeyDestinationIsWritableBeforeCreating: a
// destination that cannot be written to is knowable up front, and finding
// out afterwards means a vault that exists with a recovery key nobody has.
func TestInitChecksTheRecoveryKeyDestinationIsWritableBeforeCreating(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permission is not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	isolateXDG(t)

	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	res := runCLI(t, []string{"init", "personal", "--recovery-key-out", filepath.Join(dir, "recovery.key")}, "")
	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("init created the vault before discovering it could not write the key")
	}
	identities := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")
	if got := wrappedIdentityFiles(t, identities); len(got) != 0 {
		t.Errorf("a refused init left identity files: %v", got)
	}
}

// TestInitStillPublishesWhenTheRecoveryConfirmationFails: the confirmation
// is the last gate, not an early exit. Everything init was asked to do has
// already happened by then, and skipping the push would leave a vault
// created, registered and quietly unpublished.
func TestInitStillPublishesWhenTheRecoveryConfirmationFails(t *testing.T) {
	isolateXDG(t)

	remote := gittest.NewBareRemote(t)
	p := &recoveryPrompter{
		fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		answer:       "NOTTHEKEY",
	}
	res, _ := runCLIWithApp(t, []string{"init", "personal", "--device", "laptop-1", "--remote", remote},
		strings.NewReader(""), true, p, p.watch(t))

	if res.Code != int(exitcode.Conflict) {
		t.Fatalf("exit code = %d, want %d (Conflict); stderr=%s", res.Code, exitcode.Conflict, res.Stderr)
	}
	// The summary still reports what exists, so the failure is not also a
	// mystery about where the vault went.
	entry, ok := readGlobalConfigForTest(t).Vaults["personal"]
	if !ok {
		t.Fatal("the vault was created but not registered")
	}
	if !strings.Contains(res.Stdout, entry.Path) {
		t.Errorf("the summary doesn't name the vault's path:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "published") {
		t.Errorf("init did not publish the vault:\n%s", res.Stdout)
	}
	// And the push really happened, not just the message.
	other := gittest.NewDevice(t, remote)
	if !other.Exists(t, ".age-recipients") {
		t.Error("the vault never reached the remote")
	}
}

// TestRecoveryKeyOutRejectsARelativePathInsideAVault. The ancestor walk
// that finds "am I inside a vault" has to start from an absolute path:
// filepath.Dir(".") is ".", so a relative destination would walk its own
// prefix, find no .gage/config.toml, and let the key land exactly where
// gage's next reset deletes it.
func TestRecoveryKeyOutRejectsARelativePathInsideAVault(t *testing.T) {
	isolateXDG(t)

	vault := filepath.Join(t.TempDir(), "other")
	if res := runCLI(t, []string{"init", "other", "--dir", vault, "--no-recovery-key"}, ""); res.Code != 0 {
		t.Fatalf("seeding a vault: %s", res.Stderr)
	}

	// Run from inside that vault, so a bare filename resolves into it.
	t.Chdir(filepath.Join(vault, "entries"))

	for i, rel := range []string{"recovery.key", "./recovery.key", "../recovery.key"} {
		t.Run(rel, func(t *testing.T) {
			// A fresh vault name per case: a refusal must leave nothing
			// registered, but a *passing* case would, and the collision
			// would then mask what this is testing.
			name := fmt.Sprintf("personal-%d", i)
			res := runCLI(t, []string{"init", name,
				"--dir", filepath.Join(t.TempDir(), name),
				"--recovery-key-out", rel}, "")
			if res.Code != int(exitcode.Usage) {
				t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
			}
			if !strings.Contains(res.Stderr, "vault") {
				t.Errorf("the refusal doesn't explain itself:\n%s", res.Stderr)
			}
			if _, err := os.Stat(rel); !os.IsNotExist(err) {
				t.Error("a refused path was written to anyway")
			}
		})
	}
}

// TestRecoveryKeyOutRejectsAParentThatDoesNotExist is the destination
// refusal that is about the *parent* rather than about gage's own
// directories, and it is knowable before the vault is created — which is
// the whole point of checking the destination up front.
// TestInitChecksTheRecoveryKeyDestinationIsWritableBeforeCreating covers
// the neighbouring shape, a parent that exists but refuses writes.
func TestRecoveryKeyOutRejectsAParentThatDoesNotExist(t *testing.T) {
	isolateXDG(t)

	out := filepath.Join(t.TempDir(), "no", "such", "dir", "recovery.key")
	res := runCLI(t, []string{"init", "personal",
		"--dir", filepath.Join(t.TempDir(), "personal"),
		"--recovery-key-out", out}, "")

	if res.Code != int(exitcode.Usage) {
		t.Fatalf("exit code = %d, want %d (Usage); stderr=%s", res.Code, exitcode.Usage, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "cannot write") {
		t.Errorf("the refusal doesn't say what went wrong:\n%s", res.Stderr)
	}
	// Refused before anything is created, like every other knowable
	// failure init has.
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("a refused init registered the vault anyway")
	}
}

// TestRecoveryKeyOutRejectsAParentThatIsARegularFile pins what actually
// happens today, which is *not* what checkRecoveryKeyOutPath's own
// "%s is not a directory" branch intends.
//
// On Unix, os.Stat("<regular-file>/recovery.key") fails with ENOTDIR, and
// os.IsNotExist is false for it — so the earlier "does this already
// exist?" check takes its !os.IsNotExist arm and returns Internal wrapping
// a bare `stat ...: not a directory`, never reaching the parent stat below
// it that would have produced Usage and the readable message. Windows maps
// the same situation to ERROR_PATH_NOT_FOUND, which os.IsNotExist *does*
// recognize, so the intended branch is presumably reachable there — this
// test does not assume either way.
//
// So it asserts the property that holds everywhere and matters most: the
// destination is refused, and nothing is created. The exit code is left
// unpinned deliberately, because pinning today's Internal would pin the
// inconsistency. See the R1 plan doc's "Findings" for the fix this wants;
// tighten this test to Usage + "is not a directory" when that lands.
func TestRecoveryKeyOutRejectsAParentThatIsARegularFile(t *testing.T) {
	isolateXDG(t)

	notADir := filepath.Join(t.TempDir(), "iam-a-file")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"init", "personal",
		"--dir", filepath.Join(t.TempDir(), "personal"),
		"--recovery-key-out", filepath.Join(notADir, "recovery.key")}, "")

	if res.Code == 0 {
		t.Fatalf("init accepted a destination underneath a regular file; stdout=%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "not a directory") {
		t.Errorf("the refusal doesn't mention the reason:\n%s", res.Stderr)
	}
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("a refused init registered the vault anyway")
	}
	// Nothing was written through the path. (Checking the child for
	// non-existence would not work: stat through a non-directory reports
	// ENOTDIR, not ENOENT, which is the very quirk this test is about.)
	got, err := os.ReadFile(notADir)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "not a directory" {
		t.Errorf("the file standing in for the parent was modified: %q", got)
	}
}

// TestPathIsInsideWithNoRootIsNotInside is pathIsInside's own empty-root
// guard. Neither caller can currently pass one — init passes the vault
// being created and `recovery rotate` an existing vault's path — so this
// drives the helper directly to pin the answer it gives rather than one
// caller's current inability to ask the question.
func TestPathIsInsideWithNoRootIsNotInside(t *testing.T) {
	inside, err := pathIsInside(filepath.Join(t.TempDir(), "recovery.key"), "")
	if err != nil {
		t.Fatalf("pathIsInside with an empty root: %v", err)
	}
	if inside {
		t.Error("pathIsInside reported a path as inside an empty root")
	}
}
