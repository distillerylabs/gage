package gage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
)

// ---------------------------------------------------------------------
// What an approval leaves behind when it does not finish, and what it
// re-checks under the lock before it starts.
// ---------------------------------------------------------------------

// TestInterruptedApprovalLeavesHEADUntouchedAndRequestsPending. The
// all-or-nothing guarantee is about HEAD: a crash partway through the
// re-encryption leaves nothing committed and the request still waiting,
// so re-running is the whole recovery procedure.
func TestInterruptedApprovalLeavesHEADUntouchedAndRequestsPending(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "first")
	insertAs(t, v, owner, "second")
	insertAs(t, v, owner, "third")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	before := headHash(t, v)
	beforePending := pendingFiles(t, v)

	v.onReencryptEntry = crashAfter(2)
	crashed := runAndRecoverCrash(t, func() {
		_, _ = approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code})
	})
	v.onReencryptEntry = nil
	if !crashed {
		t.Fatal("the injected failure never fired, so this asserts nothing")
	}

	if got := headHash(t, v); got != before {
		t.Errorf("HEAD moved from %s to %s; an interrupted approval must commit nothing", before, got)
	}
	// The request is still pending — on disk, and at HEAD, which is what
	// another device would see.
	if got := pendingFiles(t, v); len(got) != len(beforePending) {
		t.Errorf("pending/ = %v, want %v — an interrupted approval must clear nothing", got, beforePending)
	}
	if !committedPaths(t, v)[pendingDirName+"/"+pendingFileName(req.ID, req.Expires)] {
		t.Error("the request is gone from HEAD after an interrupted approval")
	}
	list, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if recipientHoldingKey(list, opened.Pubkey) != "" {
		t.Error("an interrupted approval still listed the recipient")
	}
}

// TestApprovalWritesWhatItVerifiedUnderTheLockNotWhatWasDisplayed.
//
// OpenEnrollment runs outside the lock and before a human answers a
// question, so the file can be swapped in between. What gets written has
// to be the copy verified under the lock — and when that copy is not the
// one the approver was shown, nothing is written at all: the consent was
// given for a particular device and a particular key.
func TestApprovalWritesWhatItVerifiedUnderTheLockNotWhatWasDisplayed(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	// The swap: the same filename, the same code, a different key under
	// the seal. An approval that trusted what it was handed would admit
	// this key without anyone having seen it.
	swapped := testKeypair2(t)
	name := pendingFileName(req.ID, req.Expires)
	normalized, ok := normalizeEnrollmentCode(req.Code)
	if !ok {
		t.Fatalf("the request's own code does not normalize: %q", req.Code)
	}
	sealed, err := sealEnrollmentPayload(
		goodPayload(v, req.ID, "phone-1", swapped, req.Created, req.Expires), normalized)
	if err != nil {
		t.Fatal(err)
	}
	writePending(t, v, name, sealed)
	if _, err := gitrepo.CommitAll(v.Path, "test: the request, swapped after it was shown"); err != nil {
		t.Fatal(err)
	}

	head := headHash(t, v)
	_, err = approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code})
	if err == nil {
		t.Fatal("a request swapped between the open and the commit was approved")
	}

	list, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if recipientHoldingKey(list, swapped) != "" {
		t.Fatal("the swapped-in key was written into the recipient list; " +
			"what was written is what was displayed, not what was verified")
	}
	if recipientHoldingKey(list, opened.Pubkey) != "" {
		t.Error("the displayed key was written even though the file no longer holds it")
	}
	if got := headHash(t, v); got != head {
		t.Error("the refusal committed something")
	}
}

// TestDecliningTheTrustPromptAbortsWithNothingCommitted.
//
// Two [y/N] questions can appear and must not be collapsed: "let this
// device in" is cmd/gage's, and M10's "do you trust the list you are
// about to encrypt to" is this one. It fires when someone *else* changed
// the recipient list since this device last encrypted, and declining it
// has to leave the request pending.
func TestDecliningTheTrustPromptAbortsWithNothingCommitted(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	// Another device changes the recipient list, which is what makes
	// M10's question a real one here.
	listRecipientByHand(t, v, "desktop-1", testKeypair2(t))

	head := headHash(t, v)
	declines := newTrustPrompter(false)
	id := unlockAsWith(t, v, owner, declines)
	_, err := v.ApproveEnrollments(context.Background(), []Approval{{Request: opened}}, []string{req.Code}, &id)
	_ = id.Close()

	if err == nil {
		t.Fatal("declining the recipient-change question still approved the request")
	}
	if len(declines.changes) != 1 {
		t.Fatalf("M10's question was asked %d times, want exactly 1", len(declines.changes))
	}
	if got := headHash(t, v); got != head {
		t.Error("a declined trust question still committed something")
	}
	if files := pendingFiles(t, v); len(files) != 1 {
		t.Errorf("pending/ = %v, want the request still waiting", files)
	}
}

// TestTheAuthoritativeDuplicateCheckStillFiresUnderTheLock.
//
// CheckEnrollmentLabels is UX: it runs before the confirmation and names
// --device. The check that has to be *correct* against a concurrent
// writer runs inside the shared recipient write, under the lock, and
// returns the pre-existing ErrRecipientExists. Two errors for one
// condition is deliberate — see the TDD's "Library surface".
func TestTheAuthoritativeDuplicateCheckStillFiresUnderTheLock(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	// The pre-check passes, because at this moment the name is free.
	if err := v.CheckEnrollmentLabels([]Approval{{Request: opened}}); err != nil {
		t.Fatalf("the pre-check refused a name that is free: %v", err)
	}

	// Another writer takes it in the window between the two checks.
	listRecipientByHand(t, v, "phone-1", testKeypair2(t))

	_, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code})
	if !errors.Is(err, ErrRecipientExists) {
		t.Fatalf("the check under the lock = %v, want the pre-existing ErrRecipientExists", err)
	}
	if errors.Is(err, ErrEnrollmentNameTaken) {
		t.Error("the check under the lock returned enrollment's error; that one belongs to the pre-check")
	}
	if got := exitcode.CodeOf(err); got != exitcode.Conflict {
		t.Fatalf("exit code = %v, want Conflict", got)
	}
}

// TestApprovalPrunesExpiredRequestsInTheSameCommit — approve is already
// writing and already holds the lock, which is the whole of the pruning
// rule.
func TestApprovalPrunesExpiredRequestsInTheSameCommit(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	stale := layStaleRequest(t, v, time.Now().Add(-time.Hour))
	beyond := layStaleRequest(t, v, time.Now().Add(2*MaxEnrollmentTTL))

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code}); err != nil {
		t.Fatalf("ApproveEnrollments: %v", err)
	}

	for _, name := range []string{stale, beyond} {
		if _, err := os.Stat(filepath.Join(v.pendingDir(), name)); !os.IsNotExist(err) {
			t.Errorf("%s survived approval's prune", name)
		}
	}
	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("approval produced %d commits, want 1 — pruning must ride the same commit", after-before)
	}
}

// TestApprovalRefusesAnEmptyBatch: nothing to approve is a caller bug,
// not a success that did no work — the no-op has an outcome and a
// cleared request, and reporting the two the same way would hide it.
func TestApprovalRefusesAnEmptyBatch(t *testing.T) {
	v, owner := newRecipientTestVault(t, "personal", "laptop-1")
	id := unlockAs(t, v, owner)
	defer func() { _ = id.Close() }()

	_, err := v.ApproveEnrollments(context.Background(), nil, nil, &id)
	if err == nil {
		t.Fatal("an empty batch was approved")
	}
	if got := exitcode.CodeOf(err); got != exitcode.Usage {
		t.Fatalf("exit code = %v, want Usage", got)
	}
	if !strings.Contains(err.Error(), "approve") {
		t.Errorf("error = %q, want it to say what was missing", err)
	}
}

// ---------------------------------------------------------------------
// Interaction with the local trust cache. Approval is a recipient
// change, so it behaves like one in both directions: the device that
// made it is not asked to review its own work, and every other device
// is.
// ---------------------------------------------------------------------

// TestTheApprovingDeviceDoesNotWarnItselfAboutItsOwnApproval. The
// operator just reviewed this list by approving the request; warning
// them about it at their very next write is the fastest way to teach
// someone to stop reading the warning.
func TestTheApprovingDeviceDoesNotWarnItselfAboutItsOwnApproval(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	p := newTrustPrompter(true)
	id := unlockAsWith(t, v, owner, p)
	defer func() { _ = id.Close() }()

	if _, err := v.ApproveEnrollments(context.Background(), []Approval{{Request: opened}}, []string{req.Code}, &id); err != nil {
		t.Fatalf("ApproveEnrollments: %v", err)
	}
	asked := len(p.changes)

	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("a write straight after this device's own approval was blocked: %v", err)
	}
	if len(p.changes) != asked {
		t.Errorf("the approver was asked to review their own approval: %+v", p.changes[asked:])
	}
}

// TestASecondDeviceGetsTheOrdinaryWarningAfterSomeoneElseApproves. From
// every other device's side an approval is just a recipient change made
// elsewhere, which is exactly what M10's warning is for.
func TestASecondDeviceGetsTheOrdinaryWarningAfterSomeoneElseApproves(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")

	// A second device that can already read the vault, with its trust
	// cache primed by a write of its own.
	second := newTestDevice(t, "personal", "desktop-1")
	authorize(t, v, owner, "desktop-1", second.pubkey)
	primed := newTrustPrompter(true)
	secondID := unlockAsWith(t, v, second, primed)
	if _, err := v.Insert(sampleEntry(time.Now()), true, &secondID); err != nil {
		t.Fatalf("priming the second device's cache: %v", err)
	}
	_ = secondID.Close()

	// The owner approves an enrollment the second device knows nothing
	// about.
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)
	if _, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code}); err != nil {
		t.Fatalf("ApproveEnrollments: %v", err)
	}

	// The second device's next write asks about it.
	asks := newTrustPrompter(true)
	again := unlockAsWith(t, v, second, asks)
	defer func() { _ = again.Close() }()
	if _, err := v.Insert(sampleEntry(time.Now()), true, &again); err != nil {
		t.Fatalf("the second device's write: %v", err)
	}
	if len(asks.changes) != 1 {
		t.Fatalf("the second device was asked %d times about someone else's approval, want 1", len(asks.changes))
	}
	if !changeMentions(asks.changes[0], opened.Pubkey) {
		t.Errorf("the warning does not name the newly approved key: %+v", asks.changes[0])
	}
}

// TestAPendingRequestAloneTriggersNoWarning. pending/ is inert: nothing
// in it changes who can read the vault, so nothing in it is a recipient
// change for any device to review.
func TestAPendingRequestAloneTriggersNoWarning(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")

	primed := newTrustPrompter(true)
	id := unlockAsWith(t, v, owner, primed)
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("priming the cache: %v", err)
	}
	_ = id.Close()

	enrolled(t, v, remote, "phone-1")

	asks := newTrustPrompter(true)
	after := unlockAsWith(t, v, owner, asks)
	defer func() { _ = after.Close() }()
	if _, err := v.Insert(sampleEntry(time.Now()), true, &after); err != nil {
		t.Fatalf("a write with a request merely pending was blocked: %v", err)
	}
	if len(asks.changes) != 0 {
		t.Errorf("a pending request alone produced %d recipient-change questions: %+v",
			len(asks.changes), asks.changes)
	}
	if len(asks.warnings) != 0 {
		t.Errorf("a pending request alone produced warnings: %v", asks.warnings)
	}
}

// changeMentions reports whether a recipient-change warning names a key,
// on either side of the diff.
func changeMentions(w RecipientChangeWarning, pubkey string) bool {
	for _, r := range append(append([]VaultRecipient{}, w.Added...), w.Removed...) {
		if r.Pubkey == pubkey {
			return true
		}
	}
	return false
}
