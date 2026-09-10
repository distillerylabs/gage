package gage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage/gitrepo"
)

// ---------------------------------------------------------------------
// "Pruning rides the next recipient change." The TDD's pruning rule has
// two halves: approve and deny prune what they encounter, and so does an
// ordinary recipient change that has nothing to do with enrollment. This
// is the second half, which has no other home — E1a is the milestone
// that touches AddRecipient, but pruning does not exist until E2.
// ---------------------------------------------------------------------

// TestRecipientAddPrunesExpiredRequests, with no enrollment involved at
// all: someone adds a key by hand, and the dead request in pending/ goes
// with it, in that same commit rather than a second one.
func TestRecipientAddPrunesExpiredRequests(t *testing.T) {
	v, owner := newRecipientTestVault(t, "personal", "laptop-1")
	id := unlockAs(t, v, owner)
	defer func() { _ = id.Close() }()

	stale := layStaleRequest(t, v, time.Now().Add(-time.Hour))
	beyond := layStaleRequest(t, v, time.Now().Add(2*MaxEnrollmentTTL))

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.AddRecipient("desktop-1", testKeypair2(t), &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}

	for _, name := range []string{stale, beyond} {
		if _, err := os.Stat(filepath.Join(v.pendingDir(), name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived an ordinary recipient add", name)
		}
	}
	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("recipient add produced %d commits, want 1 — pruning must ride the same commit", after-before)
	}
}

// TestRecipientRemovePrunesExpiredRequests — the same rule from the
// other verb, since "the next recipient change" is not "the next
// addition".
func TestRecipientRemovePrunesExpiredRequests(t *testing.T) {
	v, owner := newRecipientTestVault(t, "personal", "laptop-1")
	id := unlockAs(t, v, owner)
	defer func() { _ = id.Close() }()

	other := testKeypair2(t)
	if _, err := v.AddRecipient("desktop-1", other, &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}

	stale := layStaleRequest(t, v, time.Now().Add(-time.Hour))
	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.RemoveRecipient("desktop-1", true, &id); err != nil {
		t.Fatalf("RemoveRecipient: %v", err)
	}

	if _, err := os.Stat(filepath.Join(v.pendingDir(), stale)); !os.IsNotExist(err) {
		t.Fatalf("%s survived an ordinary recipient remove", stale)
	}
	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("recipient remove produced %d commits, want 1", after-before)
	}
}

// TestPruningLeavesLiveRequestsAlone: a recipient change is not a reason
// to throw away a request somebody is waiting on.
func TestPruningLeavesLiveRequestsAlone(t *testing.T) {
	v, owner := newRecipientTestVault(t, "personal", "laptop-1")
	id := unlockAs(t, v, owner)
	defer func() { _ = id.Close() }()

	live := sealInto(t, v, "phone-1", testKeypair2(t), DefaultEnrollmentTTL)
	if _, err := gitrepo.CommitAll(v.Path, "test: a live enrollment request"); err != nil {
		t.Fatalf("committing the live request: %v", err)
	}

	if _, err := v.AddRecipient("desktop-1", testKeypair2(t), &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}

	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != live.ID {
		t.Fatalf("pending after an unrelated recipient add = %+v, want the one live request %s", pending, live.ID)
	}
}
