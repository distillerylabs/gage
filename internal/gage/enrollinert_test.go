package gage

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The load-bearing invariant: pending/ is inert. Nothing in it is ever
// consulted at encryption time, and nothing in it counts as a recipient.
// Only `gage recipient approve` — a human decision, with a code, on a
// device that already holds access — turns a proposal into a grant, and
// it does so by writing the two recipient files that already exist.
//
// These are the tests that must never be weakened: everything the rest of
// this feature does rests on a pending request granting nothing on its
// own.

// TestPendingRequestGrantsNoDecryption is the invariant stated as
// ciphertext. A valid, in-date, correctly-sealed request for a key must
// not make that key able to read anything the vault writes.
//
// It writes the entry *after* the request is pending, which is the
// ordering that would fail if any encrypt path consulted the directory:
// encryption reads .age-recipients, exactly as it did before this feature
// existed, and this feature changes neither which file that is nor when
// it is read.
func TestPendingRequestGrantsNoDecryption(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")
	ident := unlockAs(t, v, d)
	defer func() { _ = ident.Close() }()

	// A key that is not a recipient, requested properly and pending.
	outsider, _ := testKeypair(t)
	req := sealInto(t, v, "phone", outsider.Recipient().String(), DefaultEnrollmentTTL)

	// The request really is valid: it opens with its own code, which is
	// what makes this test about inertness rather than about a broken
	// fixture.
	opened, err := openAll(t, v, req.Code)
	if err != nil {
		t.Fatalf("the pending request does not open with its own code, so this test proves nothing: %v", err)
	}
	if len(opened) != 1 || opened[0].Pubkey != outsider.Recipient().String() {
		t.Fatalf("opened %+v, want a request for the outsider's key", opened)
	}

	id := NewEntryID()
	now := time.Now()
	if err := v.WriteEntry(id, sampleEntry(now)); err != nil {
		t.Fatalf("writing an entry with a request pending: %v", err)
	}

	if _, err := v.ReadEntry(id, wrapIdentity(t, outsider)); !errors.Is(err, ErrNotARecipient) {
		t.Fatalf("an entry written while a valid request for that key was pending decrypted with it "+
			"(err = %v); pending/ must grant nothing until a human approves it", err)
	}

	// And the device that is a recipient still reads it, so the entry was
	// encrypted to the list it should have been.
	if _, err := v.ReadEntry(id, &ident); err != nil {
		t.Fatalf("the vault's own recipient cannot read the entry: %v", err)
	}
}

// TestPendingRequestsAreNotRecipients is the same invariant seen from the
// control plane. `recipient verify` compares .age-recipients against
// .gage/config.toml and nothing else, so a vault with a directory full of
// requests still reports "in sync" — a pending request is a proposal, and
// a proposal is not a discrepancy.
func TestPendingRequestsAreNotRecipients(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")

	before, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}

	joining := testKeypair2(t)
	for i := 0; i < 10; i++ {
		sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)
	}

	after, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("recipient list grew from %d to %d with ten requests pending; a request is a proposal, not a grant",
			len(before), len(after))
	}
	for _, r := range after {
		if r.Pubkey == joining {
			t.Errorf("a pending request's key appears in the recipient list as %q", r.Device)
		}
	}
	if len(after) != 1 || after[0].Device != d.name {
		t.Errorf("recipient list = %+v, want only %s", after, d.name)
	}

	verification, err := v.VerifyRecipients()
	if err != nil {
		t.Fatal(err)
	}
	if !verification.InSync {
		t.Errorf("verify reports out of sync with ten requests pending: only in %v / only in config %v",
			verification.OnlyInRecipientsFile, verification.OnlyInConfig)
	}
}

// TestPendingDirectoryNeedsNoIdentity pins the signature E4's late unlock
// rests on, structurally rather than by convention: neither listing nor
// opening takes an Identity, so neither can quietly grow a dependency on
// one.
//
// Opening is keyed by the code, which is the whole point — the approver's
// unlock comes after the human has said yes, and it could not if the
// library needed a key to show them what they were saying yes to.
func TestPendingDirectoryNeedsNoIdentity(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	req := sealInto(t, v, "phone", joining, DefaultEnrollmentTTL)

	// No Unlock anywhere in this test: there is no identity in scope to
	// pass even if a signature wanted one.
	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing without an identity: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("listed %+v, want one request", pending)
	}
	if _, err := v.ResolveEnrollment(req.ID); err != nil {
		t.Fatalf("resolving without an identity: %v", err)
	}
	opened, err := v.OpenEnrollment(pending, []string{req.Code})
	if err != nil {
		t.Fatalf("opening without an identity: %v", err)
	}
	if len(opened) != 1 || opened[0].ID != req.ID {
		t.Fatalf("opened %+v, want %s", opened, req.ID)
	}
}

// TestGarbageInPendingIsNeverFatal is the posture rather than any single
// case: a committed directory anyone with write access can fill must
// degrade into "skipped", never into an error that takes a read or an
// approval down with it.
func TestGarbageInPendingIsNeverFatal(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop")
	req := sealInto(t, v, "phone", d.pubkey, DefaultEnrollmentTTL)

	live := time.Now().Add(time.Hour)
	writePending(t, v, pendingFileName(uuid.NewString(), live), []byte{})
	writePending(t, v, pendingFileName(uuid.NewString(), live), []byte("age-encryption.org/v1\ntruncated"))
	writePending(t, v, pendingFileName(uuid.NewString(), live), []byte{0x00, 0xff, 0x00, 0xff})
	writePending(t, v, "a-directory-listing", []byte("nothing to do with gage"))

	if _, err := v.PendingEnrollments(); err != nil {
		t.Fatalf("listing over garbage: %v", err)
	}
	opened, err := openAll(t, v, req.Code)
	if err != nil {
		t.Fatalf("opening over garbage: %v", err)
	}
	if len(opened) != 1 || opened[0].ID != req.ID {
		t.Fatalf("opened %+v, want the one real request %s", opened, req.ID)
	}
}
