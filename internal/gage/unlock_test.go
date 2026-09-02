package gage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/exitcode"
)

const testPassphrase = "correct horse battery staple"

// newUnlockableVault sets up the whole local side of one vault: a global
// config record naming this device and its method, and a wrapped identity
// file on disk. It returns the Vault and the identity's public key.
func newUnlockableVault(t *testing.T, name, device string) (*Vault, string) {
	t.Helper()
	isolateXDG(t)
	registerVault(t, name, device, MethodPassphrase)

	pubkey, err := CreateIdentity(name, device, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}
	return &Vault{Name: name, Path: filepath.Join(t.TempDir(), name)}, pubkey
}

// TestUnlockProducesAnIdentityThatDecrypts is the milestone's definition
// of done at the library level: a real identity, unlocked with a real
// passphrase, decrypting something encrypted to its own public key
// through the wrapper.
func TestUnlockProducesAnIdentityThatDecrypts(t *testing.T) {
	v, pubkey := newUnlockableVault(t, "personal", "laptop-1")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	id, err := v.Unlock(p)
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer func() { _ = id.Close() }()

	if id.Device() != "laptop-1" {
		t.Errorf("Device() = %q, want %q", id.Device(), "laptop-1")
	}
	if id.Recipient() != pubkey {
		t.Errorf("Recipient() = %q, want the public key CreateIdentity returned (%q)", id.Recipient(), pubkey)
	}

	recipient, err := ParseRecipient(pubkey)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("only this identity can read this")
	ct, err := Encrypt(payload, recipient)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(ct, &id)
	if err != nil {
		t.Fatalf("Decrypt with the unlocked identity: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decrypted %q, want %q", got, payload)
	}
}

// TestUnlockRequestsThePassphraseThroughThePrompter pins the library's
// purity rule at the one place it would be most tempting to break: the
// library never reads a terminal, it asks.
func TestUnlockRequestsThePassphraseThroughThePrompter(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	id, err := v.Unlock(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = id.Close() }()

	if len(p.requests) != 1 {
		t.Fatalf("Prompter.Unlock called %d times, want exactly 1", len(p.requests))
	}
	req := p.requests[0]
	if req.Kind != KindPassphrase {
		t.Errorf("request Kind = %q, want %q", req.Kind, KindPassphrase)
	}
	if req.Purpose != PurposeUnlock {
		t.Errorf("request Purpose = %q, want %q", req.Purpose, PurposeUnlock)
	}
	if req.Vault != "personal" || req.Device != "laptop-1" {
		t.Errorf("request identified %q/%q, want personal/laptop-1", req.Vault, req.Device)
	}
	if req.Attempt != 1 {
		t.Errorf("first request Attempt = %d, want 1", req.Attempt)
	}
}

func TestUnlockWithTheWrongPassphraseIsDistinguishable(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")

	p := &fakePrompter{passphrases: []string{"not the passphrase"}}
	id, err := v.Unlock(p)
	if err == nil {
		_ = id.Close()
		t.Fatal("expected Unlock with the wrong passphrase to fail")
	}

	// No partial or garbage key is ever produced.
	if id.Recipient() != "" || id.Device() != "" {
		t.Errorf("a failed unlock produced an Identity carrying %q/%q", id.Device(), id.Recipient())
	}
	if _, err := id.ageIdentity(); err == nil {
		t.Error("a failed unlock produced a usable key")
	}
	if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("error = %v, want it to wrap ErrWrongPassphrase", err)
	}
	// The error must not be confusable with the other two unlock
	// failures — that's the whole point of them being typed.
	if errors.Is(err, ErrCorruptIdentityFile) || errors.Is(err, ErrNoLocalIdentity) {
		t.Errorf("a wrong passphrase reported as a corrupt/missing identity: %v", err)
	}
}

// TestUnlockRetriesThroughThePrompterAndStopsWhenItStops proves the
// decision this milestone had to make: retries re-enter through the
// Prompter with an incrementing Attempt, and the library keeps no count
// of its own — the Prompter (cmd/gage, in production) decides when to
// stop.
func TestUnlockRetriesThroughThePrompterAndStopsWhenItStops(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")

	t.Run("a later attempt succeeds", func(t *testing.T) {
		p := &fakePrompter{passphrases: []string{"wrong", "also wrong", testPassphrase}}
		id, err := v.Unlock(p)
		if err != nil {
			t.Fatalf("Unlock: %v", err)
		}
		defer func() { _ = id.Close() }()

		if len(p.requests) != 3 {
			t.Fatalf("Prompter.Unlock called %d times, want 3", len(p.requests))
		}
		for i, req := range p.requests {
			if req.Attempt != i+1 {
				t.Errorf("request %d had Attempt = %d, want %d", i, req.Attempt, i+1)
			}
		}
	})

	t.Run("the prompter giving up ends the loop", func(t *testing.T) {
		p := &fakePrompter{passphrases: []string{"wrong", "still wrong"}}
		id, err := v.Unlock(p)
		if err == nil {
			_ = id.Close()
			t.Fatal("expected Unlock to fail once the prompter stopped answering")
		}
		if !errors.Is(err, errPrompterGaveUp) {
			t.Errorf("error = %v, want the prompter's own give-up error to survive", err)
		}
		// Three calls: two answered wrongly, the third refused.
		if len(p.requests) != 3 {
			t.Errorf("Prompter.Unlock called %d times, want 3 (2 answers + 1 refusal)", len(p.requests))
		}
	})
}

// TestUnlockDoesNotRetryOnNonPassphraseFailures: re-asking for a
// passphrase can't repair a damaged file, so a corrupt identity file must
// fail on the first attempt rather than prompting a user three times for
// something that was never going to work.
func TestUnlockDoesNotRetryOnNonPassphraseFailures(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")
	corruptIdentityFile(t, "personal", "laptop-1")

	p := &fakePrompter{passphrases: []string{"a", "b", "c", testPassphrase}}
	id, err := v.Unlock(p)
	if err == nil {
		_ = id.Close()
		t.Fatal("expected Unlock on a corrupt identity file to fail")
	}
	if len(p.requests) != 1 {
		t.Errorf("Prompter.Unlock called %d times on a corrupt file, want exactly 1", len(p.requests))
	}
}

func TestUnlockWithACorruptIdentityFileIsDistinguishable(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")
	corruptIdentityFile(t, "personal", "laptop-1")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	id, err := v.Unlock(p)
	if err == nil {
		_ = id.Close()
		t.Fatal("expected Unlock on a corrupt identity file to fail")
	}
	if !errors.Is(err, ErrCorruptIdentityFile) {
		t.Errorf("error = %v, want it to wrap ErrCorruptIdentityFile", err)
	}
	if errors.Is(err, ErrWrongPassphrase) || errors.Is(err, ErrNoLocalIdentity) {
		t.Errorf("a corrupt file reported as a wrong passphrase / missing identity: %v", err)
	}
}

// TestUnlockWithAWellFormedButWrongKindOfFileIsCorruptNotWrongPassphrase
// covers the case the two failure modes are easiest to confuse: a
// perfectly valid age file that simply isn't passphrase-wrapped. age
// reports it the same way it reports a bad passphrase, so gage has to
// look at the file's own recipient stanzas to tell them apart — otherwise
// a user would be re-prompted forever for a passphrase that cannot work.
func TestUnlockWithAWellFormedButWrongKindOfFileIsCorruptNotWrongPassphrase(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")

	_, recipient := testKeypair(t)
	ct, err := Encrypt([]byte("a valid age file wrapped to a public key, not a passphrase"), recipient)
	if err != nil {
		t.Fatal(err)
	}
	writeIdentityFileForTest(t, "personal", "laptop-1", ct)

	p := &fakePrompter{passphrases: []string{testPassphrase, testPassphrase, testPassphrase}}
	id, err := v.Unlock(p)
	if err == nil {
		_ = id.Close()
		t.Fatal("expected Unlock on an X25519-wrapped identity file to fail")
	}
	if !errors.Is(err, ErrCorruptIdentityFile) {
		t.Errorf("error = %v, want ErrCorruptIdentityFile", err)
	}
	if len(p.requests) != 1 {
		t.Errorf("Prompter.Unlock called %d times, want 1 — this failure cannot be fixed by re-asking", len(p.requests))
	}
}

// TestUnlockRejectsAnEmptyPassphrase: an empty answer is a Prompter bug
// or an empty stdin, never a real passphrase. Deriving a key from it
// would be worse than failing — it would produce a plausible-looking
// unlock attempt against a passphrase nobody chose.
func TestUnlockRejectsAnEmptyPassphrase(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")

	p := &fakePrompter{passphrases: []string{""}}
	id, err := v.Unlock(p)
	if err == nil {
		_ = id.Close()
		t.Fatal("expected an empty passphrase to be refused")
	}
	if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
	// Refused outright rather than retried: an empty answer means the
	// Prompter has nothing to give, so re-asking would spin.
	if len(p.requests) != 1 {
		t.Errorf("Prompter.Unlock called %d times for an empty passphrase, want 1", len(p.requests))
	}
}

// TestCreateIdentityRejectsAnEmptyPassphrase is the same rule on the
// write path, where accepting it would be worse still: it would produce a
// real, long-lived key file protected by nothing.
func TestCreateIdentityRejectsAnEmptyPassphrase(t *testing.T) {
	isolateXDG(t)

	if _, err := CreateIdentity("personal", "laptop-1", &fakePrompter{passphrases: []string{""}}); err == nil {
		t.Fatal("expected CreateIdentity to refuse an empty passphrase")
	}
	path, err := IdentityFilePath("personal", "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("an identity file was written despite the empty passphrase being refused")
	}
}

// TestUnlockRejectsAPrompterAnsweringTheWrongKind: a response carrying a
// different Kind than the request means the frontend and the library
// disagree about what was asked, and proceeding would use whatever
// happened to be in the Passphrase field.
func TestUnlockRejectsAPrompterAnsweringTheWrongKind(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")

	id, err := v.Unlock(&mismatchedPrompter{})
	if err == nil {
		_ = id.Close()
		t.Fatal("expected a mismatched response Kind to be refused")
	}
	if !strings.Contains(err.Error(), "yubikey") {
		t.Errorf("error %v does not name the mismatched kind", err)
	}
}

func TestUnlockWithNoLocalIdentityIsDistinguishable(t *testing.T) {
	t.Run("no identity file", func(t *testing.T) {
		isolateXDG(t)
		registerVault(t, "personal", "laptop-1", MethodPassphrase)

		v := &Vault{Name: "personal"}
		id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
		if err == nil {
			_ = id.Close()
			t.Fatal("expected Unlock with no identity file to fail")
		}
		assertNoLocalIdentity(t, err)
	})

	t.Run("vault not registered on this machine", func(t *testing.T) {
		isolateXDG(t)
		registerVault(t, "personal", "laptop-1", MethodPassphrase)

		v := &Vault{Name: "some-other-vault"}
		id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
		if err == nil {
			_ = id.Close()
			t.Fatal("expected Unlock of an unregistered vault to fail")
		}
		assertNoLocalIdentity(t, err)
	})

	t.Run("no global config at all", func(t *testing.T) {
		isolateXDG(t)

		v := &Vault{Name: "personal"}
		id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
		if err == nil {
			_ = id.Close()
			t.Fatal("expected Unlock with no global config to fail")
		}
		assertNoLocalIdentity(t, err)
	})
}

func assertNoLocalIdentity(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrNoLocalIdentity) {
		t.Errorf("error = %v, want it to wrap ErrNoLocalIdentity", err)
	}
	if errors.Is(err, ErrWrongPassphrase) || errors.Is(err, ErrCorruptIdentityFile) {
		t.Errorf("a missing identity reported as a wrong passphrase / corrupt file: %v", err)
	}
	if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
}

// unsafeDeviceNames are the shapes a device name must never be allowed
// to take, given it becomes a filename component. They arrive from
// config — the vault's own committed file, which any git-writer can
// edit, or the global one.
var unsafeDeviceNames = []string{
	"../../../../etc/cron.d/x",
	"..",
	".",
	"sub/dir",
	`sub\dir`,
	"C:evil",
	"with space",
	"UPPER",
	strings.Repeat("a", 200),
}

// TestUnlockNeverBuildsAPathFromAnUnvalidatedDeviceName is Q-DEVICE-NAME's
// rule at the one place it bites.
//
// The assertion is specifically ErrUnsafePathComponent, and that
// precision is the whole test. Asserting merely that Unlock *failed*
// would prove nothing: with validation removed, the traversed path
// simply wouldn't exist, Unlock would report ErrNoLocalIdentity, and a
// weaker test would go green while gage happily built and opened a path
// outside $GAGE_DATA/identities/. The distinguishing evidence that the
// name was rejected — rather than resolved and found empty — is the
// error type.
func TestUnlockNeverBuildsAPathFromAnUnvalidatedDeviceName(t *testing.T) {
	for _, device := range unsafeDeviceNames {
		t.Run(device, func(t *testing.T) {
			isolateXDG(t)
			registerVault(t, "personal", device, MethodPassphrase)

			v := &Vault{Name: "personal"}
			id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
			if err == nil {
				_ = id.Close()
				t.Fatal("expected Unlock with a traversal-style device name to fail")
			}
			if !errors.Is(err, ErrUnsafePathComponent) {
				t.Errorf("error = %v, want it to wrap ErrUnsafePathComponent; "+
					"anything else means the name was resolved rather than refused", err)
			}
			if errors.Is(err, ErrNoLocalIdentity) {
				t.Errorf("the name was resolved into a path that merely didn't exist, not refused: %v", err)
			}
			if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
				t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
			}

			// Unlock reads; it never creates. Nothing at all should have
			// appeared under $GAGE_DATA while handling a hostile name.
			dataDir := os.Getenv("XDG_DATA_HOME")
			if _, statErr := os.Stat(filepath.Join(dataDir, "gage", "identities")); !os.IsNotExist(statErr) {
				t.Errorf("handling device name %q created something under identities/", device)
			}
		})
	}
}

// TestIdentityFilePathRefusesAnUnsafeDeviceName is the same rule at the
// constructor rather than through Unlock, so the refusal is pinned to the
// one function every caller — present and future — goes through.
func TestIdentityFilePathRefusesAnUnsafeDeviceName(t *testing.T) {
	for _, device := range append([]string{""}, unsafeDeviceNames...) {
		t.Run(device, func(t *testing.T) {
			got, err := IdentityFilePath("personal", device)
			if err == nil {
				t.Fatalf("IdentityFilePath(_, %q) = %q, want an error", device, got)
			}
			if !errors.Is(err, ErrUnsafePathComponent) {
				t.Errorf("error = %v, want it to wrap ErrUnsafePathComponent", err)
			}
			if got != "" {
				t.Errorf("a refused name still produced a path: %q", got)
			}
		})
	}
}

// TestUnlockWithNoRecordedDeviceIsAMissingIdentityNotAnUnsafeName: an
// empty device name never reaches the path builder — the global config
// record is incomplete, which is a different failure with a different
// error. Pinned separately so it isn't mistaken for path validation.
func TestUnlockWithNoRecordedDeviceIsAMissingIdentityNotAnUnsafeName(t *testing.T) {
	isolateXDG(t)
	registerVault(t, "personal", "", MethodPassphrase)

	v := &Vault{Name: "personal"}
	id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err == nil {
		_ = id.Close()
		t.Fatal("expected Unlock with no recorded device to fail")
	}
	assertNoLocalIdentity(t, err)
}

// TestIdentityFilePathRefusesAnUnsafeVaultName covers the other half of
// the same path: the vault name is a directory component too.
func TestIdentityFilePathRefusesAnUnsafeVaultName(t *testing.T) {
	for _, vault := range []string{"", ".", "..", "../evil", "a/b", `a\b`, "C:evil", "trailing/"} {
		t.Run(vault, func(t *testing.T) {
			got, err := IdentityFilePath(vault, "laptop-1")
			if err == nil {
				t.Fatalf("IdentityFilePath(%q, ...) = %q, want an error", vault, got)
			}
			if !errors.Is(err, ErrUnsafePathComponent) {
				t.Errorf("error = %v, want it to wrap ErrUnsafePathComponent", err)
			}
		})
	}
}

// TestUnlockDispatchesOnTheLocalMethodNotTheVaultDefault is Q-METHOD-SCOPE
// made testable. A vault's [method].default is a suggestion for devices
// joining it, not a constraint — so the two can legitimately differ, and
// reading the vault's copy would be the wrong source. Today both values
// are "passphrase" in every real vault, which is precisely why this bug
// would be invisible without a test that forces them apart.
func TestUnlockDispatchesOnTheLocalMethodNotTheVaultDefault(t *testing.T) {
	isolateXDG(t)

	// The vault on disk says the default method is "passphrase"...
	vaultPath := filepath.Join(t.TempDir(), "personal")
	if _, err := Create(CreateSpec{
		Name:       "personal",
		Path:       vaultPath,
		Type:       TypeGit,
		Method:     MethodPassphrase,
		Device:     "laptop-1",
		Recipients: []string{testX25519Recipient(t)},
	}); err != nil {
		t.Fatal(err)
	}

	// ...while this device's own local record says something else.
	dir := configDirForTest(t)
	g := config.Global{Vaults: map[string]config.VaultEntry{
		"personal": {Path: vaultPath, Type: TypeGit, Device: "laptop-1", Method: "yubikey"},
	}}
	if err := config.Write(filepath.Join(dir, "config.toml"), g); err != nil {
		t.Fatal(err)
	}

	v := &Vault{Name: "personal", Path: vaultPath}
	p := &fakePrompter{passphrases: []string{testPassphrase}}
	id, err := v.Unlock(p)
	if err == nil {
		_ = id.Close()
		t.Fatal("expected Unlock to fail: this device's method is one this build can't perform")
	}
	if !errors.Is(err, ErrUnsupportedMethod) {
		t.Errorf("error = %v, want it to wrap ErrUnsupportedMethod — Unlock read the vault's default instead of this device's method", err)
	}
	if !strings.Contains(err.Error(), "yubikey") {
		t.Errorf("error %v does not name the locally-recorded method", err)
	}
	if len(p.requests) != 0 {
		t.Error("Unlock prompted for a passphrase despite this device's method not being passphrase")
	}
}

func testX25519Recipient(t *testing.T) string {
	t.Helper()
	ident, _ := testKeypair(t)
	return ident.Recipient().String()
}

// corruptIdentityFile truncates the wrapped identity file to a fragment,
// which is the shape of an interrupted copy or a damaged disk.
func corruptIdentityFile(t *testing.T, vault, device string) {
	t.Helper()
	path, err := IdentityFilePath(vault, device)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	writeIdentityFileForTest(t, vault, device, data[:len(data)/3])
}

func writeIdentityFileForTest(t *testing.T, vault, device string, data []byte) {
	t.Helper()
	path, err := IdentityFilePath(vault, device)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCreateIdentityWritesTheFileWithTightPermissions covers the storage
// half of "Local identity storage": the right path, 0700 directory, 0600
// file, and a public key that matches what went in.
func TestCreateIdentityWritesTheFileWithTightPermissions(t *testing.T) {
	isolateXDG(t)

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	pubkey, err := CreateIdentity("personal", "laptop-1", p)
	if err != nil {
		t.Fatal(err)
	}

	path, err := IdentityFilePath("personal", "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	want := filepath.Join(dataDir, "gage", "identities", "personal", "laptop-1.age")
	if path != want {
		t.Errorf("identity path = %q, want %q", path, want)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected an identity file at %s: %v", path, err)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	// Windows doesn't model Unix permission bits; asserting them there
	// would be asserting a fiction rather than a guarantee.
	if runtime.GOOS != "windows" {
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("identity file mode = %04o, want 0600", got)
		}
		if got := di.Mode().Perm(); got != 0o700 {
			t.Errorf("identities directory mode = %04o, want 0700", got)
		}
	}

	if _, err := ParseRecipient(pubkey); err != nil {
		t.Errorf("CreateIdentity returned %q, which is not a usable recipient: %v", pubkey, err)
	}

	// The request asked for a *new* passphrase, not an unlock.
	if len(p.requests) != 1 || p.requests[0].Purpose != PurposeCreate {
		t.Errorf("requests = %+v, want exactly one with Purpose %q", p.requests, PurposeCreate)
	}
}

// TestCreateIdentityFileIsPassphraseWrappedAndSoleRecipient checks the
// file on disk really is scrypt-only ciphertext — not the plaintext key,
// and not wrapped to anything else.
func TestCreateIdentityFileIsPassphraseWrappedAndSoleRecipient(t *testing.T) {
	isolateXDG(t)

	pubkey, err := CreateIdentity("personal", "laptop-1", &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}
	path, err := IdentityFilePath("personal", "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(data, []byte(identityFileSecretPrefix)) {
		t.Fatal("the identity file contains a plaintext AGE-SECRET-KEY line")
	}
	if bytes.Contains(data, []byte(pubkey)) {
		t.Error("the identity file leaks its public key in plaintext")
	}
	if !bytes.Contains(data, []byte("-> scrypt")) {
		t.Error("the identity file has no scrypt recipient stanza")
	}
	// The stanza also records the work factor the file was wrapped at,
	// so the deliberate choice is checked on a real identity file and not
	// only on a synthetic Encrypt call.
	if got, ok := scryptStanzaWorkFactor(string(data)); !ok {
		t.Error("could not read the work factor out of the identity file's scrypt stanza")
	} else if got != scryptWorkFactor {
		t.Errorf("identity file wrapped at work factor %d, want %d", got, scryptWorkFactor)
	}
	if bytes.Contains(data, []byte("-> X25519")) {
		t.Error("the identity file has a second, non-scrypt recipient — the sole-recipient invariant (A3) is broken")
	}
}

// TestCreateIdentityRefusesToOverwrite: overwriting an identity file
// destroys the only copy of a private key. Losing one is a recovery
// problem by design; manufacturing that situation silently is not
// something gage should do.
func TestCreateIdentityRefusesToOverwrite(t *testing.T) {
	isolateXDG(t)

	first, err := CreateIdentity("personal", "laptop-1", &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}

	p := &fakePrompter{passphrases: []string{"a different passphrase"}}
	if _, err := CreateIdentity("personal", "laptop-1", p); err == nil {
		t.Fatal("expected CreateIdentity to refuse an existing identity file")
	} else if !errors.Is(err, ErrIdentityExists) {
		t.Errorf("error = %v, want it to wrap ErrIdentityExists", err)
	}
	if len(p.requests) != 0 {
		t.Error("CreateIdentity prompted for a passphrase before checking whether it could write at all")
	}

	// And the original file is untouched: it still opens with the
	// original passphrase and yields the original key.
	registerVault(t, "personal", "laptop-1", MethodPassphrase)
	v := &Vault{Name: "personal"}
	id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("the original identity no longer unlocks: %v", err)
	}
	defer func() { _ = id.Close() }()
	if id.Recipient() != first {
		t.Errorf("public key = %q, want the original %q", id.Recipient(), first)
	}
}

func TestCreateIdentityRefusesAnUnsafeDeviceName(t *testing.T) {
	isolateXDG(t)

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	if _, err := CreateIdentity("personal", "../../escape", p); err == nil {
		t.Fatal("expected CreateIdentity to refuse a traversal-style device name")
	} else if !errors.Is(err, ErrUnsafePathComponent) {
		t.Errorf("error = %v, want it to wrap ErrUnsafePathComponent", err)
	}
}

// TestIdentityFileIsAgeKeygenCompatible pins the interop property: the
// decrypted contents are exactly what age-keygen writes, so the stock age
// CLI can use a gage identity file directly.
func TestIdentityFileIsAgeKeygenCompatible(t *testing.T) {
	isolateXDG(t)
	registerVault(t, "personal", "laptop-1", MethodPassphrase)

	pubkey, err := CreateIdentity("personal", "laptop-1", &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}
	path, err := IdentityFilePath("personal", "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}

	scryptID, err := newScryptIdentityForTest(testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := decryptBytes(wrapped, scryptID)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(string(plaintext), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("identity file has %d lines, want age-keygen's 3: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "# created: ") {
		t.Errorf("line 1 = %q, want a \"# created: \" comment", lines[0])
	}
	if lines[1] != "# public key: "+pubkey {
		t.Errorf("line 2 = %q, want %q", lines[1], "# public key: "+pubkey)
	}
	if !strings.HasPrefix(lines[2], identityFileSecretPrefix) {
		t.Errorf("line 3 = %q, want an %s… line", lines[2], identityFileSecretPrefix)
	}
}

func newScryptIdentityForTest(passphrase string) (*age.ScryptIdentity, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	id.SetMaxWorkFactor(scryptMaxWorkFactor)
	return id, nil
}

// TestRemoveIdentityIsTheRollbackPathAndNothingMore: it deletes only the
// one file, tolerates the file already being gone (a rollback runs on an
// error path, where the state is uncertain), leaves another device's
// identity for the same vault alone, and refuses an unsafe name like
// every other path-constructing call.
func TestRemoveIdentityIsTheRollbackPathAndNothingMore(t *testing.T) {
	isolateXDG(t)

	if _, err := CreateIdentity("personal", "laptop-1", &fakePrompter{passphrases: []string{testPassphrase}}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateIdentity("personal", "desktop-2", &fakePrompter{passphrases: []string{testPassphrase}}); err != nil {
		t.Fatal(err)
	}

	if err := RemoveIdentity("personal", "laptop-1"); err != nil {
		t.Fatalf("RemoveIdentity: %v", err)
	}
	gone, err := IdentityFilePath("personal", "laptop-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("%s still exists after RemoveIdentity", gone)
	}

	kept, err := IdentityFilePath("personal", "desktop-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("RemoveIdentity took another device's identity with it: %v", err)
	}

	// Idempotent: a rollback runs where the state is already uncertain.
	if err := RemoveIdentity("personal", "laptop-1"); err != nil {
		t.Errorf("second RemoveIdentity: %v", err)
	}

	if err := RemoveIdentity("personal", "../../escape"); err == nil {
		t.Error("RemoveIdentity accepted a traversal-style device name")
	} else if !errors.Is(err, ErrUnsafePathComponent) {
		t.Errorf("error = %v, want it to wrap ErrUnsafePathComponent", err)
	}

	// And the identity that's left is still unlockable — the removal
	// touched nothing about it.
	registerVault(t, "personal", "desktop-2", MethodPassphrase)
	v := &Vault{Name: "personal"}
	id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("the surviving identity no longer unlocks: %v", err)
	}
	_ = id.Close()
}
