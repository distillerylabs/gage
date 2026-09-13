package gage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/denmark/gage/internal/gage/gitrepo"
)

// Crash-safety of a partial enroll. Enroll seals the request, writes it
// into pending/, then commits; process death between those two steps
// leaves an untracked file behind. No new machinery handles that — and
// none is wanted — because resetDirtyWorkTree already covers the *whole*
// working tree and deletes untracked files along with modified ones (see
// A21, which corrected both design documents on this point).

// strayRequest writes a real, openable request into pending/ without
// committing it: exactly what an enroll killed between its write and its
// commit leaves behind.
func strayRequest(t *testing.T, v *Vault, device, pubkey string) (EnrollmentRequest, string) {
	t.Helper()
	req := sealInto(t, v, device, pubkey, DefaultEnrollmentTTL)
	path := filepath.Join(v.pendingDir(), pendingFileName(req.ID, req.Expires))

	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Fatal("the fixture committed the stray; this test is about an *uncommitted* leftover")
	}
	return req, path
}

// TestAStrayPendingFileIsInert: the file an interrupted enroll leaves
// grants nothing while it sits there. It is a valid request — it opens
// with its own code — so this is inertness rather than a broken fixture.
func TestAStrayPendingFileIsInert(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop-1")
	ident := unlockAs(t, v, d)
	defer func() { _ = ident.Close() }()

	outsider, _ := testKeypair(t)
	req, _ := strayRequest(t, v, "phone-1", outsider.Recipient().String())

	if opened, err := openAll(t, v, req.Code); err != nil || len(opened) != 1 {
		t.Fatalf("the stray does not open with its own code (%v, %d opened), so this test proves nothing",
			err, len(opened))
	}

	id := NewEntryID()
	if err := v.WriteEntry(id, sampleEntry(time.Now())); err != nil {
		t.Fatalf("writing an entry with a stray request in the tree: %v", err)
	}
	if _, err := v.ReadEntry(id, wrapIdentity(t, outsider)); !errors.Is(err, ErrNotARecipient) {
		t.Fatalf("an entry written while an uncommitted request named that key decrypted with it (err = %v)", err)
	}
}

// TestTheNextWriteDiscardsAStrayPendingFile: the stray is swept by the
// ordinary preamble every mutating method runs, warning once and naming
// what it threw away — not by anything this feature added.
//
// The reset runs *before* the write's own changes, so by the time
// anything is staged the stray is gone. That is what makes it safe for
// every commit path to keep using gitrepo.CommitAll (`git add -A`)
// rather than a path-scoped variant.
func TestTheNextWriteDiscardsAStrayPendingFile(t *testing.T) {
	v, d := newRecipientTestVault(t, "personal", "laptop-1")
	p := &confirmingPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}, confirm: true}
	ident := unlockAsWith(t, v, d, p)
	defer func() { _ = ident.Close() }()

	_, path := strayRequest(t, v, "phone-1", testKeypair2(t))

	// An unrelated write: nothing to do with enrollment.
	if _, err := v.Insert(sampleEntry(time.Now()), false, &ident); err != nil {
		t.Fatalf("inserting an entry: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the stray survived the next write: %v", err)
	}
	if !anyWarningMentions(p.warnings, "discarded uncommitted changes") {
		t.Errorf("warnings = %v, want the reset to say once what it threw away", p.warnings)
	}
	if !anyWarningMentions(p.warnings, pendingDirName) {
		t.Errorf("warnings = %v, want the discard to name the stray it removed", p.warnings)
	}

	// And it is absent from the commit, not merely from the working tree.
	if committedPaths(t, v)[pendingDirName+"/"+filepath.Base(path)] {
		t.Error("the stray was folded into the write's own commit")
	}
}

// committedPaths returns the vault-relative paths present at HEAD, which
// is how "absent from the commit" is asserted as something stronger than
// "absent from the working tree".
func committedPaths(t *testing.T, v *Vault) map[string]bool {
	t.Helper()

	repo, err := git.PlainOpen(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	if err := tree.Files().ForEach(func(f *object.File) error {
		out[f.Name] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPendingEnrollmentsListsAStrayLocallyAndNowhereElse: a read takes
// no lock and resets nothing, so the machine that failed to publish sees
// its stray for as long as it survives — and no other machine has ever
// heard of it, because it was never committed.
func TestPendingEnrollmentsListsAStrayLocallyAndNowhereElse(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	req, _ := strayRequest(t, j.vault, "phone-1", testKeypair2(t))

	pending, err := j.vault.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing pending requests: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != req.ID {
		t.Errorf("PendingEnrollments = %+v, want the stray %s", pending, req.ID)
	}

	other := newJoiningDevice(t, remote, "personal", "phone-2")
	elsewhere, err := other.vault.PendingEnrollments()
	if err != nil {
		t.Fatalf("listing pending requests on another machine: %v", err)
	}
	if len(elsewhere) != 0 {
		t.Errorf("another machine sees %+v; an uncommitted stray exists only where it was written", elsewhere)
	}
}

// TestAStrayDoesNotWedgeTheJoiningDevice is the one that matters most on
// a joining device: enroll is the *only* write available there, so if
// the reset did not live in enroll's own preamble a single interrupted
// run would wedge the machine permanently — recoverable only by
// hand-deleting a file under .gage/ that nothing in the tool's output
// ever names.
func TestAStrayDoesNotWedgeTheJoiningDevice(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	stray, strayPath := strayRequest(t, j.vault, "phone-1", testKeypair2(t))

	req, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("enrolling again after an interrupted run: %v", err)
	}
	if !req.Published {
		t.Fatal("Published = false")
	}
	if _, err := os.Stat(strayPath); !os.IsNotExist(err) {
		t.Errorf("the stray survived: %v", err)
	}
	// And it was not published along the way: the commit carries the new
	// request and nothing else.
	published := committedPaths(t, j.vault)
	if published[pendingDirName+"/"+pendingFileName(stray.ID, stray.Expires)] {
		t.Error("the stray was swept into the enroll's own commit and published")
	}
	if !published[pendingDirName+"/"+pendingFileName(req.ID, req.Expires)] {
		t.Error("the new request is not in the commit")
	}
}
