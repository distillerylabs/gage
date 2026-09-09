package gage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/gittest"
)

// Two devices enrolling at once.
//
// .gitattributes deliberately does *not* cover pending/, because a union
// of two devices' requests is the correct merge outcome — unlike a union
// of two recipient lists, which would be an access list neither device
// wrote. That argument had no assertion under it until here.

// TestTwoDevicesEnrollAgainstTheSameRemoteAndBothRequestsSurvive is the
// sequential case, which is what actually happens most of the time: the
// second device's own catch-up brings the first device's request in
// before it commits its own.
func TestTwoDevicesEnrollAgainstTheSameRemoteAndBothRequestsSurvive(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()

	first := newJoiningDevice(t, remote, "personal", "phone-1")
	firstReq, err := enrollAs(t, first, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("the first device's enroll: %v", err)
	}

	second := newJoiningDevice(t, remote, "personal", "phone-2")
	secondReq, err := enrollAs(t, second, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("the second device's enroll: %v", err)
	}

	assertBothRequestsLive(t, remote, firstReq, secondReq)
}

// TestConcurrentEnrollsMergeIntoAUnion is the case the .gitattributes
// omission is actually about: two requests committed independently, both
// surviving the merge that reconciles them.
//
// Constructed the way it is reachable — the joining side refuses a
// divergence outright (ErrEnrollmentDiverged), so the merge is the
// ordinary push-side one a human reaches through `gage push`/`gage
// sync`, not something enroll performs.
func TestConcurrentEnrollsMergeIntoAUnion(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()

	// The first device commits its request but cannot publish it.
	first := newJoiningDevice(t, remote, "personal", "phone-1")
	first.vault.remoteSyncer = &fakeSyncer{pushErr: authRejectedPush()}
	// The request itself is swallowed by the error that ended the run, so
	// it is read back off disk — which is also the only place it exists.
	if _, err := enrollAs(t, first, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}}); err == nil {
		t.Fatal("the first enroll was supposed to fail at the push")
	}
	firstReq := onlyPendingRequest(t, first.vault)

	// Meanwhile the second device enrolls successfully, so the two sides
	// have each moved on.
	second := newJoiningDevice(t, remote, "personal", "phone-2")
	secondReq, err := enrollAs(t, second, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("the second device's enroll: %v", err)
	}

	// The first device's write access comes back and it publishes.
	first.vault.remoteSyncer = nil
	report, err := first.vault.Push(context.Background())
	if err != nil {
		t.Fatalf("publishing over a divergence that is only pending/ files: %v", err)
	}
	if len(report.Conflicts) != 0 {
		t.Fatalf("the merge conflicted on %v; two devices' requests are disjoint paths and must merge",
			report.Conflicts)
	}
	if !report.Merged || !report.Pushed {
		t.Errorf("report = %+v, want a merge that was then pushed", report)
	}

	assertBothRequestsLive(t, remote, firstReq, secondReq)
}

// onlyPendingRequest reads back the single request in a vault's pending/
// as a PendingRequest — used where the EnrollmentRequest itself was
// swallowed by the error that ended the run.
func onlyPendingRequest(t *testing.T, v *Vault) EnrollmentRequest {
	t.Helper()
	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending/ holds %d requests, want 1", len(pending))
	}
	return EnrollmentRequest{ID: pending[0].ID, Expires: pending[0].Expires}
}

// assertBothRequestsLive checks that the remote carries both requests,
// byte-for-byte openable rather than merely present: a truncated or
// conflict-marked blob is a file too.
func assertBothRequestsLive(t *testing.T, remote string, reqs ...EnrollmentRequest) {
	t.Helper()

	d := gittest.NewDevice(t, remote)
	for _, req := range reqs {
		name := pendingDirName + "/" + pendingFileName(req.ID, req.Expires)
		if !d.Exists(t, name) {
			t.Errorf("the remote does not hold %s after the merge", name)
			continue
		}
		if len(d.Read(t, name)) == 0 {
			t.Errorf("%s survived the merge as an empty file", name)
		}
	}

	// And a vault built on the merged tree agrees: both are live,
	// well-formed requests, not two files that happen to be there.
	merged := newJoiningDevice(t, remote, "personal", "phone-3")
	pending, err := merged.vault.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing the merged pending/: %v", err)
	}
	if len(pending) != len(reqs) {
		t.Errorf("the merged tree holds %d live requests, want %d", len(pending), len(reqs))
	}
}

// TestMergedRequestsStillGrantNothing: inertness holds across a merge,
// which is what makes the union safe rather than merely convenient.
//
// The assertion uses each joining device's *real* private key — opened
// out of the identity file that device actually holds — rather than a
// stand-in outsider, since "the key the request names cannot read this"
// is the claim and any other key would prove something weaker.
func TestMergedRequestsStillGrantNothing(t *testing.T) {
	v, ownerDevice, remote := newEnrollFixture(t, "personal", "laptop-1")

	first := newJoiningDevice(t, remote, "personal", "phone-1")
	firstReq, err := enrollAs(t, first, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("the first device's enroll: %v", err)
	}
	second := newJoiningDevice(t, remote, "personal", "phone-2")
	secondReq, err := enrollAs(t, second, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("the second device's enroll: %v", err)
	}

	requesters := []*Identity{joinerIdentity(t, first, firstReq.Pubkey), joinerIdentity(t, second, secondReq.Pubkey)}

	// The owner catches up on both, then writes an entry.
	var id Identity
	withXDGRoot(t, ownerDevice.root, func() {
		var err error
		id, err = v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("unlocking as the owner: %v", err)
		}
	})
	defer func() { _ = id.Close() }()

	if _, err := v.Pull(context.Background()); err != nil {
		t.Fatalf("catching up on both requests: %v", err)
	}
	if got := len(mustPending(t, v)); got != 2 {
		t.Fatalf("the owner sees %d pending requests, want both", got)
	}

	entryID, err := v.Insert(sampleEntry(time.Now()), false, &id)
	if err != nil {
		t.Fatalf("inserting an entry with two requests pending: %v", err)
	}
	for _, requester := range requesters {
		if _, err := v.ReadEntry(entryID, requester); !errors.Is(err, ErrNotARecipient) {
			t.Errorf("an entry written after the merge decrypted with a pending requester's own key "+
				"(err = %v); a merged request grants no more than an unmerged one", err)
		}
	}
}

func mustPending(t *testing.T, v *Vault) []PendingRequest {
	t.Helper()
	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

// joinerIdentity opens the wrapped identity file a joining device holds
// and returns it as an Identity, so a test can attempt a decrypt with
// the very private key whose public half a pending request names.
func joinerIdentity(t *testing.T, j joiner, wantPubkey string) *Identity {
	t.Helper()

	var parsed *age.X25519Identity
	withXDGRoot(t, j.root, func() {
		path, err := IdentityFilePath(j.vault.ID, j.name)
		if err != nil {
			t.Fatal(err)
		}
		wrapped, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading the joining device's identity file: %v", err)
		}
		plaintext, err := decryptIdentityFile(j.vault.Name, j.name, wrapped,
			&fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("opening the joining device's identity file: %v", err)
		}
		parsed, _, err = parseIdentityFile(plaintext)
		if err != nil {
			t.Fatal(err)
		}
	})
	if got := parsed.Recipient().String(); got != wantPubkey {
		t.Fatalf("the identity file holds %s, but the request names %s", got, wantPubkey)
	}
	return wrapIdentity(t, parsed)
}

// TestADivergenceInvolvingPendingIsReportedHonestly.
//
// The union argument covers the merge; nothing covered what SyncReport
// says on the way there. ConflictKind() returns ConflictEntries for any
// conflicting path that is not one of the two recipient files, and
// resolveConflicts then calls entryIDFromPath on every conflict and
// fails outright on anything that is not an entry — so a conflict under
// pending/ would be classified as an entry conflict and then refuse to
// resolve, leaving `gage sync` with no way through.
//
// This asserts the reachable case rather than a contrived one: two
// devices that both resolve the same request (one approves, one denies)
// delete the same path, which git merges without a conflict. That is
// what pins the claim — if a conflicting pending/ path could be
// constructed, this is where it would stop being a surprise inside M8b's
// resolver.
func TestADivergenceInvolvingPendingIsReportedHonestly(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()

	joining := newJoiningDevice(t, remote, "personal", "phone-1")
	req, err := enrollAs(t, joining, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("publishing the request both sides will resolve: %v", err)
	}
	name := pendingDirName + "/" + pendingFileName(req.ID, req.Expires)

	// Two devices resolve it independently: one approves, one denies.
	// Both outcomes delete the request file, which is the only way
	// pending/ can appear in a divergence at all. The denier clones
	// before the approver publishes, so the two sides genuinely diverge
	// rather than one simply being behind.
	denier := newJoiningDevice(t, remote, "personal", "phone-2")

	approver := gittest.NewDevice(t, remote)
	approver.Remove(t, name)
	approver.Commit(t, "gage: approve")
	approver.Push(t)

	if err := os.Remove(filepath.Join(denier.vault.Path, filepath.FromSlash(name))); err != nil {
		t.Fatal(err)
	}
	// A second, unrelated local change, so the two sides really diverge
	// rather than the denier simply being behind.
	if err := os.WriteFile(filepath.Join(denier.vault.Path, "NOTES"), []byte("denied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(denier.vault.Path, "gage: deny"); err != nil {
		t.Fatal(err)
	}

	report, err := denier.vault.Push(context.Background())
	if err != nil {
		t.Fatalf("reconciling two resolutions of the same request: %v", err)
	}
	if !report.Diverged {
		t.Fatal("the two sides did not diverge, so this fixture proves nothing")
	}
	if len(report.Conflicts) != 0 {
		t.Errorf("Conflicts = %v; deleting the same path on both sides merges cleanly, and a conflict "+
			"under pending/ would be classified as ConflictEntries and then refuse to resolve",
			report.Conflicts)
	}
	if report.ConflictKind() != ConflictNone {
		t.Errorf("ConflictKind() = %v, want ConflictNone", report.ConflictKind())
	}
	if !report.Merged || !report.Pushed {
		t.Errorf("report = %+v, want a clean merge that was pushed", report)
	}

	// Pinned as a fact rather than assumed: were a pending/ path ever to
	// conflict, this is how it would be classified — as an entry
	// conflict, which resolveConflicts cannot resolve.
	hypothetical := SyncReport{Conflicts: []string{name}}
	if got := hypothetical.ConflictKind(); got != ConflictEntries {
		t.Errorf("ConflictKind() for a pending/ path = %v, want ConflictEntries", got)
	}
}
