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
	"github.com/distillerylabs/gage/internal/gage/gittest"
)

// ---------------------------------------------------------------------
// "Approval fetches before it commits." An approval rewrites every
// entry, so one made onto a stale tip does not diverge in one place — it
// diverges in all of them, and every entry another device touched
// meanwhile becomes a conflict the approver answers [l/r/b] to, one at a
// time. recipient add has the same shape and gets away with it because
// it is rare; approval is the ordinary way a device joins a vault.
// ---------------------------------------------------------------------

// TestApprovalFastForwardsBeforeItWrites: against a remote that moved
// ahead, the approval commit lands on top of the remote's tip and the
// push is a fast-forward.
func TestApprovalFastForwardsBeforeItWrites(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	// The remote moves after this device last caught up.
	gittest.NewDevice(t, remote).WriteCommitPush(t, "README", "moved on", "another device wrote this")

	if _, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code}); err != nil {
		t.Fatalf("ApproveEnrollments against a remote that moved ahead: %v", err)
	}

	// The catch-up really happened: the other device's commit is in this
	// device's history, and nothing is left unpushed.
	if !strings.Contains(readFileAt(t, v.Path, "README"), "moved on") {
		t.Error("the approval committed onto a stale tip; the other device's commit is missing")
	}
	ahead, err := gitrepo.AheadCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if ahead != 0 {
		t.Errorf("%d local commits unpushed after approval, want 0 — the push was not a fast-forward", ahead)
	}
	if !remoteHas(t, remote, recipientsFileName) {
		t.Error("the remote does not hold .age-recipients; the push did not land")
	}
}

// TestApprovalRefusesADivergedRemoteBeforeWritingAnything, with the
// *ordinary* advice. This is the one place this milestone's advice
// deliberately differs from the joining side's: an approver holds a key
// and can decrypt, so `gage sync` is exactly the command that resolves
// this for them, and ErrEnrollmentDiverged — which exists because that
// advice is wrong for a device with no key — must not appear.
func TestApprovalRefusesADivergedRemoteBeforeWritingAnything(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	divergeFrom(t, v, remote)

	before := headHash(t, v)
	beforeList, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}

	_, err = approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code})
	if err == nil {
		t.Fatal("approval over a diverged remote succeeded")
	}
	if got := exitcode.CodeOf(err); got != exitcode.Conflict {
		t.Fatalf("exit code = %v, want Conflict", got)
	}
	if !strings.Contains(err.Error(), "gage sync") {
		t.Fatalf("error = %q, want the ordinary `gage sync` advice, which is correct on this side", err)
	}
	if errors.Is(err, ErrEnrollmentDiverged) {
		t.Error("approval used ErrEnrollmentDiverged, whose whole reason for existing is that it is " +
			"for a device that cannot decrypt")
	}

	if got := headHash(t, v); got != before {
		t.Error("a diverged remote still committed something")
	}
	after, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(beforeList) {
		t.Error("a diverged remote still changed the recipient list")
	}
	if files := pendingFiles(t, v); len(files) != 1 {
		t.Errorf("pending/ = %v, want the request still waiting", files)
	}
}

// divergeFrom puts v and its remote on histories that have both moved,
// without going anywhere near approval.
func divergeFrom(t *testing.T, v *Vault, remote string) {
	t.Helper()

	gittest.NewDevice(t, remote).WriteCommitPush(t, "README", "theirs", "the remote moved")
	if err := os.WriteFile(filepath.Join(v.Path, "LOCAL"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(v.Path, "a local commit"); err != nil {
		t.Fatal(err)
	}
}

// TestApprovalFindsDivergenceInTheReportRatherThanAnError pins the
// nil-error trap on this side too: v.pull reports a divergence by
// setting SyncReport.Diverged, not by returning an error, so an
// implementation that only inspects err walks straight past it and
// rewrites every entry onto the diverged branch.
//
// The same test shape E3 uses, for the same reason, against a second
// call site.
func TestApprovalFindsDivergenceInTheReportRatherThanAnError(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	divergeFrom(t, v, remote)

	// The pull the implementation runs: nil error, Diverged set. If this
	// ever stops being true the trap is gone and so is the reason for
	// checking the field.
	report, err := v.Pull(context.Background())
	if err != nil {
		t.Fatalf("v.Pull over a divergence returned %v; this test exists because it returns nil", err)
	}
	if !report.Diverged {
		t.Fatal("v.Pull did not report a divergence, so this fixture is not diverged")
	}

	before := headHash(t, v)
	if _, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code}); err == nil {
		t.Fatal("approval walked past a divergence the report was carrying")
	}
	if got := headHash(t, v); got != before {
		t.Error("approval committed onto a diverged branch")
	}
}

// TestApprovalOverAnUnreachableRemoteWarnsAndProceeds — the asymmetry
// with enroll, which refuses. An unpublished enrollment request
// accomplishes nothing; an approval that lands locally is real work.
func TestApprovalOverAnUnreachableRemoteWarnsAndProceeds(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	j, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	fake := &fakeSyncer{fetchErr: unreachableError(), pushErr: unreachableError()}
	v.remoteSyncer = fake
	defer func() { v.remoteSyncer = nil }()

	trust := newTrustPrompter(true)
	id := unlockAsWith(t, v, owner, trust)
	res, err := v.ApproveEnrollments(context.Background(), []Approval{{Request: opened}}, []string{req.Code}, &id)
	_ = id.Close()
	if err != nil {
		t.Fatalf("an unreachable remote must warn and proceed, not fail: %v", err)
	}

	// The local work actually happened, all of it.
	if res.Reencrypted != 1 {
		t.Fatalf("re-encrypted %d entries, want 1", res.Reencrypted)
	}
	list, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if recipientHoldingKey(list, opened.Pubkey) == "" {
		t.Error("the recipient was not listed")
	}
	if files := pendingFiles(t, v); len(files) != 0 {
		t.Errorf("the request was not cleared: %v", files)
	}
	if !anyWarningMentions(trust.warnings, "could not reach") {
		t.Errorf("warnings = %v, want the unreachable remote said once", trust.warnings)
	}
	if !readsEveryEntry(t, v, j) {
		t.Error("the approved device cannot read the vault, so the local work did not really happen")
	}
}

// TestApprovalWarnsWhenItsPushFails, and the local commit stands.
//
// Approval's local/remote split is the widest in the tool, so the
// warning has to say more than "not pushed": locally the recipient is
// listed, every entry is re-encrypted and the request file is gone; on
// the remote none of that happened and the joining device is still
// locked out with `gage sync` reporting nothing wrong.
func TestApprovalWarnsWhenItsPushFails(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	v.remoteSyncer = &fakeSyncer{pushErr: authRejectedPush()}
	defer func() { v.remoteSyncer = nil }()

	trust := newTrustPrompter(true)
	id := unlockAsWith(t, v, owner, trust)
	res, err := v.ApproveEnrollments(context.Background(), []Approval{{Request: opened}}, []string{req.Code}, &id)
	_ = id.Close()
	if err != nil {
		t.Fatalf("a failed push must warn, not fail: %v", err)
	}
	if res.Commit == "" {
		t.Fatal("the local commit did not stand")
	}

	got := strings.Join(trust.warnings, "\n")
	if got == "" {
		t.Fatal("a failed push produced no warning")
	}
	if strings.Contains(got, unpushedWriteClause) {
		t.Fatalf("the warning is the generic one:\n%s\n\nE1b's failed-push clause was never threaded through", got)
	}
	for _, want := range []string{"locked out", "gage push"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failed-push warning never says %q:\n%s", want, got)
		}
	}

	// Locally everything happened; on the remote none of it did.
	if files := pendingFiles(t, v); len(files) != 0 {
		t.Errorf("pending/ = %v locally, want the request cleared", files)
	}
	if !remoteHas(t, remote, pendingDirName+"/"+pendingFileName(req.ID, req.Expires)) {
		t.Error("the remote no longer holds the request, so this fixture did not actually fail to push")
	}
}

// TestReapprovalAfterAFailedPushIsTheNoOp is what makes the failed-push
// state self-healing: once the push lands, approving the same device
// again is the already-a-recipient no-op rather than a second grant.
func TestReapprovalAfterAFailedPushIsTheNoOp(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	v.remoteSyncer = &fakeSyncer{pushErr: authRejectedPush()}
	if _, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code}); err != nil {
		t.Fatalf("the first approval must still commit locally: %v", err)
	}
	v.remoteSyncer = nil

	// The push lands on the retry, which is what the human is told to do.
	if _, err := v.Push(context.Background()); err != nil {
		t.Fatalf("publishing the approval: %v", err)
	}

	// A second device re-approves the same request from its own copy —
	// constructed here by putting the request back and approving again.
	restorePending(t, v, req)
	again := openedFor(t, v, req.Code)
	res, err := approveAs(t, v, owner, []Approval{{Request: again}}, []string{req.Code})
	if err != nil {
		t.Fatalf("re-approving after the push landed must be a no-op, not an error: %v", err)
	}
	if res.Outcomes[0].Added {
		t.Error("the re-approval reported a second grant")
	}
	if res.Reencrypted != 0 {
		t.Errorf("the re-approval re-encrypted %d entries, want 0", res.Reencrypted)
	}
}

// restorePending puts a request back into pending/ and commits it — the
// state another device is in when it has not yet seen the approval.
func restorePending(t *testing.T, v *Vault, req EnrollmentRequest) {
	t.Helper()

	// The normalized form, which is what a seal is keyed by: every
	// accepted spelling of a code reaches the same passphrase, and the
	// displayed one is not it.
	normalized, ok := normalizeEnrollmentCode(req.Code)
	if !ok {
		t.Fatalf("the request's own code does not normalize: %q", req.Code)
	}
	sealed, err := sealEnrollmentPayload(
		goodPayload(v, req.ID, req.Device, req.Pubkey, req.Created, req.Expires), normalized)
	if err != nil {
		t.Fatalf("re-sealing the request: %v", err)
	}
	writePending(t, v, pendingFileName(req.ID, req.Expires), sealed)
	if _, err := gitrepo.CommitAll(v.Path, "test: the request as another device still sees it"); err != nil {
		t.Fatal(err)
	}
}

// TestApprovalReportsARequestResolvedByAnotherDevice. The fetch can
// resolve the request out from under the run: another device may have
// approved or denied it in the window between the open and the lock.
// Constructed exactly that way.
func TestApprovalReportsARequestResolvedByAnotherDevice(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")

	// Opened here, while it really is pending.
	opened := openedFor(t, v, req.Code)

	// Another device denies it and pushes, before this run reaches its
	// fetch.
	other := gittest.NewDevice(t, remote)
	other.Remove(t, pendingDirName+"/"+pendingFileName(req.ID, req.Expires))
	other.Commit(t, "another device denied it")
	other.Push(t)

	before := headHash(t, v)
	_, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code})
	if !errors.Is(err, ErrEnrollmentNoSuchRequest) {
		t.Fatalf("approving a request resolved elsewhere = %v, want ErrEnrollmentNoSuchRequest", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.NotFound {
		t.Fatalf("exit code = %v, want NotFound", got)
	}
	if !strings.Contains(err.Error(), "another device") {
		t.Fatalf("error = %q, want it to name the likely cause rather than imply a mistyped id", err)
	}
	if got := headHash(t, v); got == before {
		t.Log("HEAD moved only by the fetch, which is expected")
	}
	list, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if recipientHoldingKey(list, opened.Pubkey) != "" {
		t.Error("a request resolved elsewhere was still granted")
	}
}

// TestUncontendedApprovalNeverReportsContention. A Pull called from
// inside approval's own lock fails as *vaultlock.ContendedError after
// the full timeout, because vaultlock is not re-entrant — so the
// assertion that catches it is a plain success, completing well inside
// vaultLockTimeout.
func TestUncontendedApprovalNeverReportsContention(t *testing.T) {
	v, owner, remote := newEnrollFixture(t, "personal", "laptop-1")
	insertAs(t, v, owner, "an entry")
	_, req := enrolled(t, v, remote, "phone-1")
	opened := openedFor(t, v, req.Code)

	start := time.Now()
	if _, err := approveAs(t, v, owner, []Approval{{Request: opened}}, []string{req.Code}); err != nil {
		t.Fatalf("an uncontended approval failed: %v — a Pull taken from inside its own lock looks "+
			"exactly like this", err)
	}
	if elapsed := time.Since(start); elapsed >= vaultLockTimeout {
		t.Fatalf("an uncontended approval took %s, at or beyond the %s lock timeout — "+
			"it is blocking against its own lock", elapsed, vaultLockTimeout)
	}
}
