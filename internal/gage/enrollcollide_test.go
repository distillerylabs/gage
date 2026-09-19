package gage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/gittest"
)

// D-ENROLL-COLLISIONS: a device name is a *label*, not an identity. The
// public key is the identity, and every case below follows from that one
// sentence.

// newEnrollFixture is newSyncVault with the owner's device handed back
// instead of an unlocked Identity, because these tests have to switch
// XDG roots between two machines and unlock again on the far side.
func newEnrollFixture(t *testing.T, vaultName, ownerDevice string) (*Vault, testDevice, string) {
	t.Helper()

	remote := gittest.NewBareRemote(t)
	d := newTestDevice(t, vaultName, ownerDevice)

	var v *Vault
	withXDGRoot(t, d.root, func() {
		var err error
		v, err = Create(CreateSpec{
			Name:       vaultName,
			ID:         d.vaultID,
			Path:       filepath.Join(t.TempDir(), vaultName),
			Type:       TypeGit,
			Method:     MethodPassphrase,
			Device:     ownerDevice,
			Recipients: []string{d.pubkey},
			Remote:     remote,
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	if _, err := gitrepo.Push(context.Background(), v.Path); err != nil {
		t.Fatalf("publishing the initial commit: %v", err)
	}
	return v, d, remote
}

// authorize is the approving side of the manual path: a device that can
// already read the vault adds a public key to the recipient list and
// pushes. It is how these tests reach the states enroll has to recognize
// without going anywhere near E4.
func authorize(t *testing.T, v *Vault, owner testDevice, device, pubkey string) {
	t.Helper()
	withXDGRoot(t, owner.root, func() {
		id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("unlocking as the owner: %v", err)
		}
		defer func() { _ = id.Close() }()
		if _, err := v.AddRecipient(device, pubkey, &id); err != nil {
			t.Fatalf("authorizing %q: %v", device, err)
		}
	})
}

// registerLocalIdentity is the whole of `gage identity add` on a joining
// machine: create the key, and record it in global config the way
// cmd/gage does.
func registerLocalIdentity(t *testing.T, j joiner) string {
	t.Helper()
	var pubkey string
	withXDGRoot(t, j.root, func() {
		var err error
		pubkey, err = CreateIdentity(j.vault.ID, j.vault.Name, j.name,
			&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("creating this device's identity: %v", err)
		}
		recordPubkey(t, j.vault.Name, pubkey)
	})
	return pubkey
}

// TestEnrollOnADeviceThatIsAlreadyARecipientIsASuccessThatDoesNoWork,
// constructed the way it actually happens: `identity add`, then a
// `recipient add` of that key from another device, then `identity
// enroll`.
//
// Without this the name check fires and gage advises --device — advice
// that mints a second identity under a second label and publishes a
// request for access this device already has, which is the exact
// label-versus-identity confusion D-ENROLL-COLLISIONS exists to prevent.
func TestEnrollOnADeviceThatIsAlreadyARecipientIsASuccessThatDoesNoWork(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	pubkey := registerLocalIdentity(t, j)
	authorize(t, v, owner, j.name, pubkey)

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	before := headHash(t, j.vault)
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	if err != nil {
		if errors.Is(err, ErrDeviceNameTaken) {
			t.Fatalf("enroll reported this device's own key as a name collision, which advises --device " +
				"and mints a second identity for access it already has")
		}
		t.Fatalf("Enroll on a device that is already a recipient: %v", err)
	}
	if req.Published {
		t.Error("Published = true; a device that can already read the vault has nothing to ask for")
	}
	if req != (EnrollmentRequest{}) {
		t.Errorf("EnrollmentRequest = %+v, want the zero value — no key, no code, nothing sealed", req)
	}
	if len(p.requests) != 0 {
		t.Errorf("the prompter was asked %d times, want 0", len(p.requests))
	}
	if files := pendingFiles(t, j.vault); len(files) != 0 {
		t.Errorf("pending/ holds %v, want nothing published", files)
	}
	// HEAD may have moved, but only by the catch-up pull that brought the
	// authorization in — never by a commit of enroll's own.
	if headHash(t, j.vault) == before {
		t.Log("the pull found nothing new, which is fine; what matters is that nothing was committed here")
	}
	ahead, err := gitrepo.AheadCount(j.vault.Path)
	if err != nil {
		t.Fatal(err)
	}
	if ahead != 0 {
		t.Errorf("%d unpushed local commits, want 0 — nothing should have been committed", ahead)
	}
}

// TestTheAlreadyARecipientCheckIsByKeyNotByName covers the case a name
// comparison misses entirely: the key is a recipient under a *different*
// label, which is the state `approve --device` produces. No name
// collides, so today's check would sail past and publish a pointless
// request.
func TestTheAlreadyARecipientCheckIsByKeyNotByName(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	pubkey := registerLocalIdentity(t, j)
	// The approver relabelled it on the way in.
	authorize(t, v, owner, "andrews-phone", pubkey)

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	if err != nil {
		t.Fatalf("Enroll for a key that is a recipient under another label: %v", err)
	}
	if req.Published {
		t.Error("Published = true; the key is already a recipient, whatever it is labelled")
	}
	if len(p.requests) != 0 {
		t.Errorf("the prompter was asked %d times, want 0", len(p.requests))
	}
	if files := pendingFiles(t, j.vault); len(files) != 0 {
		t.Errorf("pending/ holds %v, want nothing published", files)
	}
}

// TestEnrollRefusesANameThatLabelsADifferentRecipient is the collision
// that is real: the name is taken by someone else's key, so the request
// would be unapprovable. It fails before generating anything.
func TestEnrollRefusesANameThatLabelsADifferentRecipient(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	// Somebody else's key under the name this device wants.
	authorize(t, v, owner, "phone-1", testKeypair2(t))

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	_, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	assertEnrollmentError(t, err, ErrDeviceNameTaken, exitcode.Conflict)

	if len(p.requests) != 0 {
		t.Errorf("the prompter was asked %d times, want 0 — the check precedes the identity step",
			len(p.requests))
	}
	if files := identityFilesFor(t, j); len(files) != 0 {
		t.Errorf("a refused name still wrote %v; nothing may be generated for a request that cannot be made", files)
	}
	if files := pendingFiles(t, j.vault); len(files) != 0 {
		t.Errorf("a refused name still published %v", files)
	}
}

// TestEnrollUnderADifferentDeviceNameSucceedsWhereTheDefaultCollided is
// the recovery the refusal above points at.
func TestEnrollUnderADifferentDeviceNameSucceedsWhereTheDefaultCollided(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	j := newJoiningDevice(t, remote, "personal", "phone-1")
	authorize(t, v, owner, "phone-1", testKeypair2(t))

	j.name = "phone-2"
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("Enroll under a non-colliding name: %v", err)
	}
	if !req.Published {
		t.Fatal("Published = false")
	}
	if req.Device != "phone-2" {
		t.Errorf("EnrollmentRequest.Device = %q, want phone-2", req.Device)
	}
	if opened := openTheOnlyRequest(t, j.vault, req.Code); opened.Device != "phone-2" {
		t.Errorf("the sealed device name = %q, want phone-2", opened.Device)
	}
}

// TestWithNoRecordedPubkeyTheKeyCheckIsSkipped asserts the fallback
// rather than assuming it, since it is the branch every pre-E0 global
// config entry — and every fresh clone — lands in. gage cannot answer
// the pubkey question there, so the name check is the only one left and
// behaves exactly as it does today.
func TestWithNoRecordedPubkeyTheKeyCheckIsSkipped(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	// The device holds a key and is a recipient — but global config
	// records no pubkey, exactly as a clone leaves it.
	var pubkey string
	withXDGRoot(t, j.root, func() {
		var err error
		pubkey, err = CreateIdentity(j.vault.ID, j.vault.Name, j.name,
			&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatal(err)
		}
	})
	authorize(t, v, owner, j.name, pubkey)

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	_, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	assertEnrollmentError(t, err, ErrDeviceNameTaken, exitcode.Conflict)

	if len(p.requests) != 0 {
		t.Errorf("the prompter was asked %d times, want 0", len(p.requests))
	}
}
