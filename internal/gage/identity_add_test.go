package gage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// identityFileFor is the path M9 promises each device's wrapped key
// lands at, resolved under whatever XDG root is currently in effect. It
// takes the vault's *name* and derives its id the way the test helpers
// do, so a caller reads as "this vault, this device" rather than
// restating A20's keying at every assertion.
func identityFileFor(t *testing.T, vault, device string) string {
	t.Helper()
	path, err := IdentityFilePath(vaultIDForTest(vault), device)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAddIdentityWritesItsOwnWrappedFileAndReturnsAPublicKey is the
// plan's "`gage identity add` registers a device and prints a public
// key" at the library level: the printing is cmd/gage's, the key and the
// file are this method's.
func TestAddIdentityWritesItsOwnWrappedFileAndReturnsAPublicKey(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	withXDGRoot(t, laptop.root, func() {
		pubkey, err := v.AddIdentity("laptop-2", &fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("AddIdentity: %v", err)
		}
		if pubkey == "" {
			t.Fatal("AddIdentity returned an empty public key")
		}
		if _, err := ParseRecipient(pubkey); err != nil {
			t.Errorf("AddIdentity returned %q, which is not a usable age recipient: %v", pubkey, err)
		}
		if pubkey == laptop.pubkey {
			t.Error("the second identity reused the first device's key; each device gets its own keypair")
		}

		path := identityFileFor(t, "personal", "laptop-2")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("no wrapped identity file at %s: %v", path, err)
		}
		// Windows doesn't model Unix permission bits; asserting them
		// there would be asserting a fiction rather than a guarantee.
		// The mode itself is CreateIdentity's, covered on Unix here and
		// in unlock_test.go — this asserts the second device's file gets
		// it too, not just the first.
		if runtime.GOOS != "windows" {
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("identity file mode = %04o, want 0600", perm)
			}
		}
		// The first device's file is untouched — one file per device,
		// not one file per vault.
		if _, err := os.Stat(identityFileFor(t, "personal", "laptop-1")); err != nil {
			t.Errorf("registering a second identity disturbed the first device's file: %v", err)
		}
	})
}

// TestAddIdentityRejectsANameAlreadyRegisteredAsARecipient is the plan's
// "`gage identity add` with a device name already registered as a
// recipient of that vault is rejected rather than silently overwriting
// an existing identity file."
//
// The check is against the *vault's* recipient list, not against local
// files, because that is what makes the two-machines-sharing-a-hostname
// case fail: the second machine has no identity file of its own to
// collide with, only a name the vault already knows.
func TestAddIdentityRejectsANameAlreadyRegisteredAsARecipient(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	// The colliding machine: a fresh root with no identity file at all,
	// standing in for a second laptop whose normalized hostname happens
	// to match the first's.
	other := t.TempDir()
	withXDGRoot(t, other, func() {
		registerVault(t, "personal", "laptop-1", MethodPassphrase)

		_, err := v.AddIdentity("laptop-1", &fakePrompter{passphrases: []string{testPassphrase}})
		if !errors.Is(err, ErrDeviceNameTaken) {
			t.Fatalf("AddIdentity with a name the vault already lists = %v, want ErrDeviceNameTaken", err)
		}
		if _, statErr := os.Stat(identityFileFor(t, "personal", "laptop-1")); statErr == nil {
			t.Error("a rejected AddIdentity still wrote an identity file")
		}
	})

	// And on the machine that does hold the colliding name, the refusal
	// is what keeps its only copy of a private key from being replaced.
	withXDGRoot(t, laptop.root, func() {
		before, err := os.ReadFile(identityFileFor(t, "personal", "laptop-1"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.AddIdentity("laptop-1", &fakePrompter{passphrases: []string{testPassphrase}}); err == nil {
			t.Fatal("AddIdentity overwrote this device's existing identity")
		}
		after, err := os.ReadFile(identityFileFor(t, "personal", "laptop-1"))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Error("the existing wrapped identity file changed despite the refusal")
		}
	})
}

// TestEachDeviceHasItsOwnIdentityFile is the plan's "Each device
// registered via `gage identity add` gets its own wrapped identity file
// at $GAGE_DATA/identities/<vault>/<device>.age; deleting one device's
// file doesn't affect another device's ability to decrypt."
//
// Two real devices, two roots, one shared vault — the arrangement the
// claim is actually about.
func TestEachDeviceHasItsOwnIdentityFile(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}
	entryID, err := v.Insert(sampleEntry(time.Now()), true, &id)
	if err != nil {
		t.Fatal(err)
	}
	_ = id.Close()

	// Each device's file lives under its own root, at the documented
	// path, and neither is inside the vault's git tree.
	for _, d := range []testDevice{laptop, phone} {
		withXDGRoot(t, d.root, func() {
			path := identityFileFor(t, "personal", d.name)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("no identity file for %s at %s: %v", d.name, path, err)
			}
			if strings.HasPrefix(path, v.Path+string(filepath.Separator)) {
				t.Errorf("%s's identity file at %s is inside the vault's git tree", d.name, path)
			}
		})
	}

	// Losing the laptop's file leaves the phone entirely unaffected.
	withXDGRoot(t, laptop.root, func() {
		if err := os.Remove(identityFileFor(t, "personal", "laptop-1")); err != nil {
			t.Fatal(err)
		}
	})

	id2 := unlockAs(t, v, phone)
	defer func() { _ = id2.Close() }()
	if _, err := v.ReadEntry(entryID, &id2); err != nil {
		t.Fatalf("phone-1 cannot decrypt after laptop-1's identity file was deleted: %v", err)
	}

	// And the laptop can no longer unlock, which is what makes the
	// recovery path below necessary rather than decorative.
	withXDGRoot(t, laptop.root, func() {
		if _, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}}); !errors.Is(err, ErrNoLocalIdentity) {
			t.Errorf("unlocking with no identity file = %v, want ErrNoLocalIdentity", err)
		}
	})
}

// TestListIdentitiesReportsThisDevicesRegisteredIdentities is the plan's
// "`gage identity list` reports the local device's registered identities
// for a vault." It reads the local identities directory, so it reports
// what this machine actually holds rather than what the vault's
// recipient list claims.
func TestListIdentitiesReportsThisDevicesRegisteredIdentities(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	withXDGRoot(t, laptop.root, func() {
		got, err := v.ListIdentities()
		if err != nil {
			t.Fatalf("ListIdentities: %v", err)
		}
		if len(got) != 1 || got[0].Device != "laptop-1" {
			t.Fatalf("ListIdentities = %+v, want exactly laptop-1", got)
		}
		if !got[0].Current {
			t.Error("laptop-1 is not marked current, but global config points at it")
		}
		if got[0].Path != identityFileFor(t, "personal", "laptop-1") {
			t.Errorf("Path = %q, want %q", got[0].Path, identityFileFor(t, "personal", "laptop-1"))
		}

		if _, err := v.AddIdentity("laptop-2", &fakePrompter{passphrases: []string{testPassphrase}}); err != nil {
			t.Fatalf("AddIdentity: %v", err)
		}
		got, err = v.ListIdentities()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("ListIdentities after a second add = %+v, want two entries", got)
		}
		names := []string{got[0].Device, got[1].Device}
		if !listHas(names, "laptop-1") || !listHas(names, "laptop-2") {
			t.Errorf("ListIdentities = %v, want both laptop-1 and laptop-2", names)
		}
	})

	// A vault this device holds nothing for reports nothing, rather than
	// failing — "no identities here" is a normal answer right after a
	// clone.
	stranger := t.TempDir()
	withXDGRoot(t, stranger, func() {
		got, err := v.ListIdentities()
		if err != nil {
			t.Fatalf("ListIdentities on a device with no identities: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("ListIdentities = %+v, want empty", got)
		}
	})
}

// TestRecoveringALostIdentityFileNeedsNoFileRestore is the plan's
// "Recovering from a lost identity file needs no file restore: a fresh
// `gage identity add` on the affected device plus `gage recipient add`
// from any surviving recipient restores access under a new identity."
//
// This is the design's whole answer to "losing this file is a recovery
// problem, not a backup problem," run end to end.
func TestRecoveringALostIdentityFileNeedsNoFileRestore(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	// Both devices can read everything, including history.
	id := unlockAs(t, v, laptop)
	entryID, err := v.Insert(sampleEntry(time.Now()), true, &id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient --reencrypt: %v", err)
	}
	_ = id.Close()

	// The laptop's identity file is gone — a wiped disk, a botched
	// restore, a deleted directory.
	var newPubkey string
	withXDGRoot(t, laptop.root, func() {
		if err := os.Remove(identityFileFor(t, "personal", "laptop-1")); err != nil {
			t.Fatal(err)
		}

		// Step one, on the affected device: a fresh identity under a new
		// name. Nothing is restored, and the old key is simply gone.
		newPubkey, err = v.AddIdentity("laptop-1-new", &fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("AddIdentity after losing the old file: %v", err)
		}
		registerVault(t, "personal", "laptop-1-new", MethodPassphrase)
	})

	// Step two, from a surviving recipient: add the new key, with
	// --reencrypt so the recovered device gets history back too.
	idPhone := unlockAs(t, v, phone)
	if _, err := v.AddRecipient("laptop-1-new", newPubkey, &idPhone); err != nil {
		t.Fatalf("the surviving recipient could not add the recovered device: %v", err)
	}
	_ = idPhone.Close()

	recovered := testDevice{name: "laptop-1-new", root: laptop.root, pubkey: newPubkey}
	idNew := unlockAs(t, v, recovered)
	defer func() { _ = idNew.Close() }()

	got, err := v.ReadEntry(entryID, &idNew)
	if err != nil {
		t.Fatalf("the recovered device cannot read the vault's history: %v", err)
	}
	if got.Title == "" {
		t.Error("the recovered entry decrypted to nothing")
	}
}
