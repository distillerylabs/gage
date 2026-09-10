package gage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// ---------------------------------------------------------------------
// The approving side. Every test here starts from a published request an
// owner's vault has caught up to, which is the only state approval is
// ever run in.
// ---------------------------------------------------------------------

// enrolled publishes a request from a fresh joining device and catches
// the owner's vault up to it — the state `gage recipient approve` starts
// from, built through the real joining path rather than by writing a
// file into pending/ by hand.
//
// It leaves the process's XDG roots pointed at the joining device, since
// withXDGRoot's switch outlives its callback; callers that then act as
// the owner go through unlockAs, which repoints them.
func enrolled(t *testing.T, v *Vault, remote, device string) (joiner, EnrollmentRequest) {
	t.Helper()

	j := newJoiningDevice(t, remote, v.Name, device)
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("publishing %q's enrollment request: %v", device, err)
	}
	if _, err := v.Pull(context.Background()); err != nil {
		t.Fatalf("catching the owner's vault up to %q's request: %v", device, err)
	}
	return j, req
}

// openedFor opens the one request code unlocks, from the owner's side.
func openedFor(t *testing.T, v *Vault, code string) OpenedRequest {
	t.Helper()

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing pending requests: %v", err)
	}
	opened, err := v.OpenEnrollment(pending, []string{code})
	if err != nil {
		t.Fatalf("opening the request with its code: %v", err)
	}
	if len(opened) != 1 {
		t.Fatalf("opened %d requests with one code, want 1", len(opened))
	}
	return opened[0]
}

// TestCheckEnrollmentLabelsRefusesANameAnotherRecipientTook is the
// collision D-ENROLL-COLLISIONS says --device resolves: the request's
// sealed name was free when it was written and labels a *different* key
// by the time somebody approves it.
//
// The pass holds no Identity on purpose — it reads the recipient list,
// which is plaintext — because that is what lets cmd/gage run it before
// the confirmation and before any unlock.
func TestCheckEnrollmentLabelsRefusesANameAnotherRecipientTook(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	_, req := enrolled(t, v, remote, "phone-1")

	// Somebody else takes "phone-1" in the window, with a different key.
	other := newTestDevice(t, "personal", "phone-1")
	authorize(t, v, owner, "phone-1", other.pubkey)

	opened := openedFor(t, v, req.Code)
	err := v.CheckEnrollmentLabels([]Approval{{Request: opened}})
	if !errors.Is(err, ErrEnrollmentNameTaken) {
		t.Fatalf("CheckEnrollmentLabels over a taken name = %v, want ErrEnrollmentNameTaken", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.Conflict {
		t.Fatalf("exit code = %v, want Conflict", got)
	}
}

// TestCheckEnrollmentLabelsAcceptsARelabel: --device is the whole fix
// for the case above, so the same request with a label the approver
// chose must pass the same pass.
func TestCheckEnrollmentLabelsAcceptsARelabel(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	_, req := enrolled(t, v, remote, "phone-1")

	other := newTestDevice(t, "personal", "phone-1")
	authorize(t, v, owner, "phone-1", other.pubkey)

	opened := openedFor(t, v, req.Code)
	if err := v.CheckEnrollmentLabels([]Approval{{Request: opened, Label: "phone-2"}}); err != nil {
		t.Fatalf("CheckEnrollmentLabels with an approver-chosen label: %v", err)
	}
}

// TestCheckEnrollmentLabelsIgnoresTheDevicesOwnEntry: a request whose
// *pubkey* is already listed is the already-a-recipient no-op, not a
// name collision. Reporting it as one would advise --device, which mints
// a second label for a key that already has access — the same
// label-versus-identity confusion the joining side refuses to make.
func TestCheckEnrollmentLabelsIgnoresTheDevicesOwnEntry(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	j, req := enrolled(t, v, remote, "phone-1")

	opened := openedFor(t, v, req.Code)
	authorize(t, v, owner, j.name, opened.Pubkey)

	if err := v.CheckEnrollmentLabels([]Approval{{Request: opened}}); err != nil {
		t.Fatalf("CheckEnrollmentLabels over this request's own already-listed key = %v, want nil", err)
	}
}

// TestCheckEnrollmentLabelsNeedsNoIdentityOrNetwork pins the property
// that makes the late unlock possible at all: this pass is answerable
// from plaintext, so it can run before the confirmation.
func TestCheckEnrollmentLabelsNeedsNoIdentityOrNetwork(t *testing.T) {
	v, _, remote := newEnrollFixture(t, "personal", "laptop-1")
	_, req := enrolled(t, v, remote, "phone-1")

	opened := openedFor(t, v, req.Code)
	// No syncer at all: a pass that reached the network would fail here
	// rather than quietly succeeding against a real remote.
	v.remoteSyncer = &fakeSyncer{fetchErr: unreachableError(), pushErr: unreachableError()}
	defer func() { v.remoteSyncer = nil }()

	if err := v.CheckEnrollmentLabels([]Approval{{Request: opened}}); err != nil {
		t.Fatalf("CheckEnrollmentLabels: %v", err)
	}
	if got := opened.Expires.Sub(opened.Created).Round(time.Minute); got != DefaultEnrollmentTTL {
		t.Fatalf("opened request's lifetime = %v, want %v", got, DefaultEnrollmentTTL)
	}
}

// approveAs runs an approval as the vault's owner, with that device's
// XDG roots installed and its Identity open for the call.
func approveAs(t *testing.T, v *Vault, d testDevice, approvals []Approval, codes []string) (ApprovalResult, error) {
	t.Helper()
	id := unlockAs(t, v, d)
	defer func() { _ = id.Close() }()
	return v.ApproveEnrollments(context.Background(), approvals, codes, &id)
}

// insertAs writes one entry as the vault's owner, so that approval has
// history to make readable.
func insertAs(t *testing.T, v *Vault, d testDevice, title string) {
	t.Helper()
	id := unlockAs(t, v, d)
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	e.Title = title
	if _, err := v.Insert(e, true, &id); err != nil {
		t.Fatalf("inserting %q: %v", title, err)
	}
}

// readsEveryEntry unlocks as the joining device and reads every entry in
// the vault, which is the property approval exists to establish —
// asserted against the ciphertext rather than against the recipient
// list, which is only a claim about it.
//
// It repoints the process's XDG roots at the joining machine and leaves
// them there, so callers run it last.
func readsEveryEntry(t *testing.T, v *Vault, j joiner) bool {
	t.Helper()

	ids, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatal("the vault holds no entries, so this asserts nothing")
	}

	readable := true
	withXDGRoot(t, j.root, func() {
		id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("unlocking as the joining device: %v", err)
		}
		defer func() { _ = id.Close() }()
		for _, entryID := range ids {
			if _, err := v.ReadEntry(entryID, &id); err != nil {
				readable = false
			}
		}
	})
	return readable
}

// listRecipientByHand adds a key to both recipient-defining files and
// commits, without re-encrypting anything — the hand-edited
// .age-recipients the design acknowledges it cannot prevent, and the
// only way to reach the partial-access damage ErrCannotGrantFullAccess
// exists to surface now that A19 made re-encryption unconditional.
func listRecipientByHand(t *testing.T, v *Vault, device, pubkey string) {
	t.Helper()

	current, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.writeRecipientFiles(append(current, VaultRecipient{Device: device, Pubkey: pubkey})); err != nil {
		t.Fatalf("hand-listing %q: %v", device, err)
	}
	if _, err := gitrepo.CommitAll(v.Path, "test: a recipient added without re-encrypting"); err != nil {
		t.Fatal(err)
	}
}

// TestApprovalAddsOneRecipientAndKeepsTheFilesInSync.
func TestApprovalAddsOneRecipientAndKeepsTheFilesInSync(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "before the phone existed")
	_, req := enrolled(t, v, remote, "phone-1")

	opened := openedFor(t, v, req.Code)
	res, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code})
	if err != nil {
		t.Fatalf("ApproveEnrollments: %v", err)
	}

	if len(res.Outcomes) != 1 || !res.Outcomes[0].Added {
		t.Fatalf("outcomes = %+v, want one Added approval", res.Outcomes)
	}
	if res.Outcomes[0].Label != "phone-1" {
		t.Fatalf("recorded label = %q, want %q", res.Outcomes[0].Label, "phone-1")
	}

	list, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if recipientHoldingKey(list, opened.Pubkey) != "phone-1" {
		t.Fatalf("recipients = %+v, want the approved key listed as phone-1", list)
	}
	got, err := v.VerifyRecipients()
	if err != nil {
		t.Fatal(err)
	}
	if !got.InSync {
		t.Fatalf(".age-recipients and config.toml disagree after approval: %+v", got)
	}
	if files := pendingFiles(t, v); len(files) != 0 {
		t.Fatalf("the approved request survived: %v", files)
	}
}

// TestApprovalMakesEveryPreExistingEntryReadable is the property the
// whole "approval always re-encrypts" rule exists for: the device can
// read entries written long before it existed. No flag and no code path
// produces a partially-readable recipient.
func TestApprovalMakesEveryPreExistingEntryReadable(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "written long before the phone existed")
	insertAs(t, v, owner, "and a second one")
	j, req := enrolled(t, v, remote, "phone-1")

	opened := openedFor(t, v, req.Code)
	res, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code})
	if err != nil {
		t.Fatalf("ApproveEnrollments: %v", err)
	}
	if res.Reencrypted != 2 {
		t.Fatalf("re-encrypted %d entries, want 2", res.Reencrypted)
	}
	if !readsEveryEntry(t, v, j) {
		t.Fatal("an entry predating the approval is not readable by the approved device")
	}
}

// TestApprovalLandsInExactlyOneCommit — the recipient files, every
// re-encrypted entry, and the removal of the request's file.
func TestApprovalLandsInExactlyOneCommit(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	opened := openedFor(t, v, req.Code)
	if _, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code}); err != nil {
		t.Fatalf("ApproveEnrollments: %v", err)
	}

	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("approval produced %d commits, want 1", after-before)
	}
	committed := committedPaths(t, v)
	if committed[pendingDirName+"/"+pendingFileName(opened.ID, opened.Expires)] {
		t.Fatal("the approved request is still present at HEAD")
	}
	if !committed[recipientsFileName] {
		t.Fatal(".age-recipients is absent from HEAD")
	}
}

// TestBatchApprovalIsOnePassAndOneCommit — the whole reason batch
// approval exists. N codes, one decrypt-and-rewrite of every entry.
func TestBatchApprovalIsOnePassAndOneCommit(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "first")
	insertAs(t, v, owner, "second")
	phone, first := enrolled(t, v, remote, "phone-1")
	_, second := enrolled(t, v, remote, "tablet-1")

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	codes := []string{first.Code, second.Code}
	opened, err := v.OpenEnrollment(pending, codes)
	if err != nil {
		t.Fatalf("opening both requests: %v", err)
	}
	if len(opened) != 2 {
		t.Fatalf("opened %d requests, want 2", len(opened))
	}

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := approveAs(t, v, owner, []Approval{{Request: opened[0]}, {Request: opened[1]}}, codes)
	if err != nil {
		t.Fatalf("ApproveEnrollments over a batch: %v", err)
	}

	// Two entries, rewritten once each — not once per approval.
	if res.Reencrypted != 2 {
		t.Fatalf("re-encrypted %d entries over a 2-request batch of a 2-entry vault, want 2 — "+
			"a second pass would report 4", res.Reencrypted)
	}
	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("batch approval produced %d commits, want 1", after-before)
	}
	if files := pendingFiles(t, v); len(files) != 0 {
		t.Fatalf("requests survived the batch: %v", files)
	}
	if !readsEveryEntry(t, v, phone) {
		t.Fatal("a device approved as part of a batch cannot read every entry")
	}
}

// TestApprovingAKeyThatIsAlreadyARecipientIsANoOp: a success that does
// no work, mirroring the joining side's answer to the same duplicate.
func TestApprovingAKeyThatIsAlreadyARecipientIsANoOp(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	j, req := enrolled(t, v, remote, "phone-1")

	opened := openedFor(t, v, req.Code)
	authorize(t, v, owner, j.name, opened.Pubkey)

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code})
	if err != nil {
		t.Fatalf("approving an already-listed key must succeed, not fail: %v", err)
	}

	if len(res.Outcomes) != 1 || res.Outcomes[0].Added {
		t.Fatalf("outcomes = %+v, want one outcome reporting Added: false", res.Outcomes)
	}
	if res.Reencrypted != 0 {
		t.Fatalf("re-encrypted %d entries for a device that already had access, want 0", res.Reencrypted)
	}
	if files := pendingFiles(t, v); len(files) != 0 {
		t.Fatalf("the request was not cleared: %v", files)
	}
	list, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("recipients = %+v, want the two that were already there", list)
	}
	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("the no-op produced %d commits, want 1 clearing the request", after-before)
	}
}

// TestApprovingTheSameDeviceTwiceLeavesOneRecipient: two requests from
// one device, approved one after the other. The second is the no-op
// above, not an error.
func TestApprovingTheSameDeviceTwiceLeavesOneRecipient(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")

	j := newJoiningDevice(t, remote, v.Name, "phone-1")
	first, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	second, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("second enroll: %v", err)
	}
	if _, err := v.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}

	openedFirst := openedFor(t, v, first.Code)
	if _, err := approveAs(t, v, owner, []Approval{{Request: openedFirst}}, []string{first.Code}); err != nil {
		t.Fatalf("approving the first request: %v", err)
	}

	openedSecond := openedFor(t, v, second.Code)
	res, err := approveAs(t, v, owner, []Approval{{Request: openedSecond}}, []string{second.Code})
	if err != nil {
		t.Fatalf("approving the duplicate request must be a no-op, not an error: %v", err)
	}
	if res.Outcomes[0].Added {
		t.Fatal("the second approval reported a new grant")
	}

	list, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("recipients = %+v, want exactly the owner and the one device", list)
	}
}

// TestApprovalRelabelsWithoutChangingTheKey. --device is the approver's
// call to make: the code authenticated the key, not the label.
func TestApprovalRelabelsWithoutChangingTheKey(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")

	opened := openedFor(t, v, req.Code)
	res, err := approveAs(t, v, owner, []Approval{{Request: opened, Label: "phone-2"}}, []string{req.Code})
	if err != nil {
		t.Fatalf("ApproveEnrollments with a relabel: %v", err)
	}
	if res.Outcomes[0].Label != "phone-2" {
		t.Fatalf("recorded label = %q, want the approver's", res.Outcomes[0].Label)
	}

	list, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if got := recipientHoldingKey(list, opened.Pubkey); got != "phone-2" {
		t.Fatalf("the sealed key is listed as %q, want %q", got, "phone-2")
	}
}

// TestApprovalRefusesAnApproverWhoCannotReadEveryEntry, before the write
// lock and before M10's recipient-change prompt. The error names the
// count of unreadable entries rather than failing mid-write on an opaque
// entry UUID.
func TestApprovalRefusesAnApproverWhoCannotReadEveryEntry(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "readable by the owner alone")
	_, req := enrolled(t, v, remote, "phone-1")

	// A second device that is a recipient but cannot read the entry
	// above — the pre-existing partial-access damage this refusal is the
	// one place that surfaces.
	partial := newTestDevice(t, "personal", "desktop-1")
	listRecipientByHand(t, v, "desktop-1", partial.pubkey)

	opened := openedFor(t, v, req.Code)
	head := headHash(t, v)
	trust := newTrustPrompter(true)

	id := unlockAsWith(t, v, partial, trust)
	_, err := v.ApproveEnrollments(context.Background(), []Approval{{Request: opened}}, []string{req.Code}, &id)
	_ = id.Close()

	if !errors.Is(err, ErrCannotGrantFullAccess) {
		t.Fatalf("approval by a partially-admitted device = %v, want ErrCannotGrantFullAccess", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.Conflict {
		t.Fatalf("exit code = %v, want Conflict", got)
	}
	if !strings.Contains(err.Error(), "1") {
		t.Fatalf("the refusal does not name how many entries are unreadable: %v", err)
	}
	if len(trust.changes) != 0 {
		t.Fatalf("M10's recipient-change prompt was shown %d times before the refusal, want 0", len(trust.changes))
	}
	if headHash(t, v) != head {
		t.Fatal("the refusal committed something")
	}
	if files := pendingFiles(t, v); len(files) != 1 {
		t.Fatalf("the request should still be pending after a refusal, got %v", files)
	}
}

// TestAMalformedRequestProducesNothingToRender is the approving side's
// half of E2's validation: the confirmation screen is the one screen in
// gage whose correctness depends on a human reading it, and the device
// name sits directly above the question that grants vault-wide access.
//
// The property that protects it is structural rather than a matter of
// careful printing — a malformed payload yields *no* OpenedRequest, so a
// frontend has nothing to render even if it wanted to. Asserted here
// because it is a claim about what the library hands out, which is where
// it can be checked rather than hoped for.
func TestAMalformedRequestProducesNothingToRender(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)
	created := time.Now().UTC().Truncate(time.Second)
	expires := created.Add(DefaultEnrollmentTTL)

	id := uuid.NewString()
	p := goodPayload(v, id, "phone", joining, created, expires)
	// A name that can move the cursor can rewrite the question the human
	// is answering.
	p.Device = "phone\x1b[2K\x1b[1GApprove? [Y/n] "
	code := spoiled(t, v, id, expires, p)

	opened, err := openAll(t, v, code)
	if !errors.Is(err, ErrEnrollmentMalformedRequest) {
		t.Fatalf("opening a malformed request = %v, want ErrEnrollmentMalformedRequest", err)
	}
	if len(opened) != 0 {
		t.Fatalf("a malformed request produced %d OpenedRequests; a frontend would have "+
			"something to render", len(opened))
	}
	if strings.ContainsRune(err.Error(), '\x1b') {
		t.Errorf("the refusal echoes the escape sequence back: %q", err.Error())
	}
}

// TestClockSkewNamesTheRequestingDevicesClock. E2 proves the error is
// its own; what an approver needs from it is the fix, and the fix is on
// the *other* machine — telling them their request is stale would send
// them to ask for another one that would be dead on arrival too.
func TestClockSkewNamesTheRequestingDevicesClock(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop")
	joining := testKeypair2(t)

	id := uuid.NewString()
	code := generateCode(t)
	created := time.Now().UTC().Truncate(time.Second)
	writePending(t, v, pendingFileName(id, time.Now().Add(time.Hour)),
		handSeal(t, goodPayload(v, id, "phone", joining, created, created.Add(-DefaultEnrollmentTTL)), code))

	_, err := openAll(t, v, code)
	if !errors.Is(err, ErrEnrollmentClockSkew) {
		t.Fatalf("err = %v, want ErrEnrollmentClockSkew", err)
	}
	if !strings.Contains(err.Error(), "clock") {
		t.Errorf("the refusal does not name a clock, so it reads as a stale request: %v", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.Conflict {
		t.Fatalf("exit code = %v, want Conflict", got)
	}
}
