package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
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
	v := &gage.Vault{Name: "personal", ID: entry.ID, Path: entry.Path}
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

// TestInitAfterVaultRemoveGeneratesAFreshKeyRatherThanReusingTheOldOne
// is a deliberate behaviour change from #39, and A20 is what changed it.
//
// `init` now mints a new vault id every run, so the identities directory
// it addresses is always a fresh one and CreateIdentity's reuse path
// cannot be reached from here at all. The property #39 actually cared
// about is untouched and asserted below: the first vault's wrapped key
// is never overwritten or destroyed. What changed is that the second
// vault gets a keypair of its own instead of silently inheriting the
// first's — which is the whole point of keying by an id, since the two
// vaults are unrelated and only happen to share a local name.
func TestInitAfterVaultRemoveGeneratesAFreshKeyRatherThanReusingTheOldOne(t *testing.T) {
	isolateXDG(t)

	if res := runCLI(t, []string{"init", "personal", "--device", "laptop-1"}, ""); res.Code != 0 {
		t.Fatalf("init failed: %s", res.Stderr)
	}
	firstID := vaultIDForTest(t, "personal")
	firstPath := identityFileForTest(t, "personal", "laptop-1")
	firstFile, err := os.ReadFile(firstPath) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	firstPubkey := readVaultConfigForTest(t, "personal").Recipients[0].Pubkey

	// Drop the registration but leave the vault's files where they are,
	// so `vault remove` keeps the identity file (laptop-1 is still a
	// recipient there) — the state #39 was reported against.
	if res := runCLI(t, []string{"vault", "remove", "personal"}, ""); res.Code != 0 {
		t.Fatalf("vault remove failed: %s", res.Stderr)
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("the first vault's identity file should have survived vault remove: %v", err)
	}

	// A second, unrelated vault under the same local name.
	res := runCLI(t, []string{"init", "personal", "--device", "laptop-1", "--dir", filepath.Join(t.TempDir(), "again")}, "")
	if res.Code != 0 {
		t.Fatalf("second init failed: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stderr, "reusing the existing local identity file") {
		t.Errorf("the second vault reused the first's keypair; ids exist so that it cannot: %s", res.Stderr)
	}

	secondID := vaultIDForTest(t, "personal")
	if secondID == firstID {
		t.Fatalf("both vaults got the id %q; a new vault gets a new id", secondID)
	}
	if got := readVaultConfigForTest(t, "personal").Recipients[0].Pubkey; got == firstPubkey {
		t.Error("the second vault is encrypted to the first vault's key; each vault gets its own keypair")
	}

	// And the first vault's key is exactly where it was, byte for byte.
	after, err := os.ReadFile(firstPath) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatalf("the first vault's identity file is gone: %v", err)
	}
	if !bytes.Equal(firstFile, after) {
		t.Error("the first vault's identity file was overwritten")
	}
}

// TestTwoFailedInitsLeaveNoOrphanedIdentities is the knock-on A20
// creates, and it is why init's rollback stopped being a tidiness
// measure. Under name-keying a leftover identity was picked back up by
// the next attempt; under id-keying every attempt addresses a brand-new
// directory, so a rollback that did nothing would leave one directory
// per failed attempt, each holding a private key for a vault that was
// never created.
func TestTwoFailedInitsLeaveNoOrphanedIdentities(t *testing.T) {
	isolateXDG(t)

	// A non-empty target directory: knowable, but only checked after the
	// identity has been generated.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if res := runCLI(t, []string{"init", "personal", "--dir", dir}, ""); res.Code == 0 {
			t.Fatalf("attempt %d: expected init into a non-empty directory to fail", attempt)
		}
	}

	identities := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gage", "identities")
	if got := wrappedIdentityFiles(t, identities); len(got) != 0 {
		t.Errorf("two failed inits left orphaned identity files: %v", got)
	}
	// The directories go too, not just the keys inside them: a tree of
	// empty UUID directories is the visible half of the same leak.
	entries, err := os.ReadDir(identities)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("two failed inits left %d directories under identities/: %v", len(entries), entries)
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

	v := &gage.Vault{Name: "never-registered", ID: "9f3a1c2e-7b41-4d58-a0c6-2e5f81b3d497"}
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
