package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// TestInitThenUnlockDecryptsSomethingEncryptedToTheVault is the
// milestone's definition of done, end to end and through the real CLI: a
// vault created with a real identity, that identity unlocked from what
// init recorded, and used to decrypt something encrypted to the public
// key in the vault's own recipient file. There are still no entries — the
// payload here is just bytes.
func TestInitThenUnlockDecryptsSomethingEncryptedToTheVault(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal", "--device", "laptop-1"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	entry := readGlobalConfigForTest(t).Vaults["personal"]
	vf := readVaultConfigForTest(t, "personal")
	fromConfig := vf.Recipients[0].Pubkey

	data, err := os.ReadFile(filepath.Join(entry.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	fromRecipientsFile := strings.TrimSpace(string(data))
	if fromRecipientsFile != fromConfig {
		t.Fatalf(".age-recipients has %q but config.toml has %q", fromRecipientsFile, fromConfig)
	}

	// Unlock resolves the identity file from the device name init
	// recorded in global config, and dispatches on the method it recorded
	// there too.
	v := &gage.Vault{Name: "personal", Path: entry.Path}
	id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("Unlock after init: %v", err)
	}
	defer func() { _ = id.Close() }()

	if id.Device() != "laptop-1" {
		t.Errorf("unlocked as device %q, want laptop-1", id.Device())
	}
	// The public key derived from the generated identity must be exactly
	// what both recipient files carry.
	if id.Recipient() != fromConfig {
		t.Errorf("unlocked identity's public key = %q, want the one in the recipient files (%q)", id.Recipient(), fromConfig)
	}

	recipient, err := gage.ParseRecipient(fromRecipientsFile)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("a secret, encrypted to the vault's recipient file")
	ct, err := gage.Encrypt(payload, recipient)
	if err != nil {
		t.Fatal(err)
	}
	got, err := gage.Decrypt(ct, &id)
	if err != nil {
		t.Fatalf("decrypting with the unlocked identity: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decrypted %q, want %q", got, payload)
	}
}

// TestInitFailsBeforeWritingAnythingWhenTheTargetIsUnusable: the
// passphrase prompt comes first, so a failure that was knowable all along
// must not cost the user two blind passphrase entries and then leave a
// half-built vault behind.
func TestInitLeavesNoVaultBehindWhenCreateFails(t *testing.T) {
	isolateXDG(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := runCLI(t, []string{"init", "personal", "--dir", dir}, "")
	if res.Code == 0 {
		t.Fatal("expected init into a non-empty directory to fail")
	}
	if _, ok := readGlobalConfigForTest(t).Vaults["personal"]; ok {
		t.Error("a failed init registered the vault anyway")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gage")); !os.IsNotExist(err) {
		t.Error("a failed init wrote into the target directory")
	}

	// The identity generated before the failure must be rolled back.
	// Left behind it would be an orphan nothing references, and
	// CreateIdentity's refusal to overwrite one would then block every
	// retry — permanently, for a failure the user can trivially fix.
	identities := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")
	if got := wrappedIdentityFiles(t, identities); len(got) != 0 {
		t.Errorf("a failed init left orphaned identity files: %v", got)
	}

	// And the retry, once the obstacle is gone, actually works.
	if err := os.Remove(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatal(err)
	}
	if res := runCLI(t, []string{"init", "personal", "--dir", dir}, ""); res.Code != 0 {
		t.Fatalf("retrying init after fixing the target directory failed: %s", res.Stderr)
	}
}

// wrappedIdentityFiles lists every .age identity file under root.
func wrappedIdentityFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".age") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return found
}

// TestInitTwiceOnOneDeviceReusesRatherThanDestroysTheFirstIdentity:
// `init` is already refused for an already-registered name, but the
// identity file is keyed on vault+device and lives outside global
// config, so the stronger guarantee is that the wrapped key is never
// overwritten. Losing it is a recovery problem by design; silently
// causing that is not — but the file's mere existence, with the correct
// passphrase behind it, is not a reason to fail either (see #39):
// CreateIdentity reuses it instead.
func TestInitTwiceOnOneDeviceReusesRatherThanDestroysTheFirstIdentity(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal", "--device", "laptop-1"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	path := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities", "personal", "laptop-1.age")
	before, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	pubkey := readVaultConfigForTest(t, "personal").Recipients[0].Pubkey

	// A different vault name, so the already-registered check doesn't
	// short-circuit — but the same vault directory name for identities
	// would only collide if the vault name matched, so force the
	// collision directly by re-running init for the same vault after
	// dropping only its registration.
	if res := runCLI(t, []string{"vault", "remove", "personal"}, ""); res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	res := runCLI(t, []string{"init", "personal", "--device", "laptop-1", "--dir", filepath.Join(t.TempDir(), "again")}, "")
	if res.Code != 0 {
		t.Fatalf("expected init to reuse the existing identity file, got exit %d: %s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "reusing the existing local identity file") {
		t.Errorf("stderr = %q, want it to say the identity file was reused", res.Stderr)
	}

	after, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the existing identity file was overwritten")
	}
	if got := readVaultConfigForTest(t, "personal").Recipients[0].Pubkey; got != pubkey {
		t.Errorf("public key = %q, want the original %q", got, pubkey)
	}
}

// TestInitReportsTheDeviceAndPublicKey: the public key is the one thing
// a user has to hand to another device's `recipient add`, so init has to
// print it rather than making them go read a file.
func TestInitReportsTheDeviceAndPublicKey(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"init", "personal", "--device", "laptop-1"}, "")
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	pubkey := readVaultConfigForTest(t, "personal").Recipients[0].Pubkey
	for _, want := range []string{"laptop-1", pubkey} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("init output doesn't mention %q:\n%s", want, res.Stdout)
		}
	}
}

// TestInitAsksForTheNewPassphraseThroughThePrompter pins the library
// purity rule on the write path too: generating an identity needs a
// passphrase, and internal/gage asks for it rather than reading a
// terminal.
func TestInitAsksForTheNewPassphraseThroughThePrompter(t *testing.T) {
	isolateXDG(t)

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	res, _ := runCLIWithPrompter(t, []string{"init", "personal", "--device", "laptop-1"}, "", false, p)
	if res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}

	if len(p.requests) != 1 {
		t.Fatalf("Prompter.Unlock called %d times, want 1", len(p.requests))
	}
	req := p.requests[0]
	if req.Kind != gage.KindPassphrase {
		t.Errorf("request Kind = %q, want %q", req.Kind, gage.KindPassphrase)
	}
	if req.Purpose != gage.PurposeCreate {
		t.Errorf("request Purpose = %q, want %q — init establishes a passphrase, it doesn't verify one", req.Purpose, gage.PurposeCreate)
	}
	if req.Vault != "personal" || req.Device != "laptop-1" {
		t.Errorf("request identified %q/%q, want personal/laptop-1", req.Vault, req.Device)
	}
}

// TestInitWithARejectedDeviceNameWritesNoIdentityFile: --device is
// validated before anything is created, so a name that could not be a
// safe path component never reaches the filesystem.
func TestInitWithARejectedDeviceNameWritesNoIdentityFile(t *testing.T) {
	isolateXDG(t)

	res := runCLI(t, []string{"init", "personal", "--device", "../../escape"}, "")
	if res.Code != int(exitcode.Usage) {
		t.Errorf("exit code = %d, want %d (Usage)", res.Code, exitcode.Usage)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")); !os.IsNotExist(err) {
		t.Error("a rejected device name still created something under identities/")
	}
}

// TestUnlockErrorsSurfaceAsLockedOrAuthExitCode connects the library's
// typed errors to the process exit status a script would branch on.
func TestUnlockTypedErrorsCarryTheRightExitCode(t *testing.T) {
	isolateXDG(t)

	v := &gage.Vault{Name: "never-registered"}
	_, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err == nil {
		t.Fatal("expected unlocking an unregistered vault to fail")
	}
	if !errors.Is(err, gage.ErrNoLocalIdentity) {
		t.Errorf("error = %v, want it to wrap ErrNoLocalIdentity", err)
	}
	if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
}
