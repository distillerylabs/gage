package gage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/gittest"
	"github.com/denmark/gage/internal/gage/vaultlock"
)

// resolvingPrompter is a Prompter that can also answer an entry conflict —
// the fake standing in for a human at the [l/r/b/s/q] prompt. It records
// every conflict it was shown, which is how these tests assert on the
// *presentation* (both sides, decrypted, as a typed value) rather than
// only on what the resolution did.
type resolvingPrompter struct {
	fakePrompter

	// answers is consumed in order, one per conflict presented.
	answers   []Resolution
	answerErr error

	// before, if set, runs at the start of every ResolveConflict call —
	// the seam the lock test uses to observe the world mid-resolution.
	before func()

	presented []EntryConflict
}

func (p *resolvingPrompter) ResolveConflict(c EntryConflict) (Resolution, error) {
	if p.before != nil {
		p.before()
	}
	p.presented = append(p.presented, c)
	if p.answerErr != nil {
		return 0, p.answerErr
	}
	i := len(p.presented) - 1
	if i >= len(p.answers) {
		return 0, errPrompterGaveUp
	}
	return p.answers[i], nil
}

// answering builds a resolvingPrompter that answers the conflicts it is
// shown with answers, in order, and can unlock if asked.
func answering(answers ...Resolution) *resolvingPrompter {
	return &resolvingPrompter{
		fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		answers:      answers,
	}
}

// ciphertextFor encrypts e to this vault's current recipients — how a
// stand-in "other device" publishes a *genuine*, decryptable entry rather
// than the opaque filler M8a's detection tests could get away with.
// Resolution has to decrypt both sides, so both sides have to be real.
func ciphertextFor(t *testing.T, v *Vault, e Entry) string {
	t.Helper()

	plaintext, err := MarshalEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	to, err := v.encryptRecipients()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := Encrypt(plaintext, to...)
	if err != nil {
		t.Fatal(err)
	}
	return string(ciphertext)
}

// conflictFixture is the situation this whole milestone exists for: two
// devices edited the same entry, both versions are real ciphertext, and
// the local side has already discovered the divergence the way M8a leaves
// it — detected, reported, nothing resolved.
type conflictFixture struct {
	vault   *Vault
	ident   Identity
	remote  string
	other   *gittest.Device
	entryID uuid.UUID
	path    string

	// mine and theirs are the two versions, as plaintext, so a test can
	// assert which one survived without re-deriving it.
	mine   Entry
	theirs Entry

	// headBefore is HEAD as of the moment resolution begins, for the
	// abort/skip tests that assert nothing moved.
	headBefore string
}

func (f *conflictFixture) close() { _ = f.ident.Close() }

// newConflictFixture builds that situation. The local write's automatic
// push is what discovers the divergence, exactly as it would in use.
func newConflictFixture(t *testing.T) *conflictFixture {
	t.Helper()

	v, id, remote := newSyncVault(t, "personal", "laptop-1")

	base := sampleEntry(time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC))
	entryID, err := v.Insert(base, false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	path := entriesDirName + "/" + entryID.String() + entryFileExt

	theirs := base
	theirs.Value = "their-rotated-password"
	theirs.Updated = NewTimestamp(time.Date(2026, 8, 29, 9, 3, 0, 0, time.UTC))
	theirs.UpdatedBy = "phone"

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, path, ciphertextFor(t, v, theirs), "the other device's edit")

	mine := base
	mine.Value = "my-rotated-password"
	mine.Updated = NewTimestamp(time.Date(2026, 8, 30, 14, 22, 0, 0, time.UTC))
	mine.UpdatedBy = "laptop-1"
	if err := v.Update(entryID, mine, &id); err != nil {
		t.Fatalf("Update: %v", err)
	}

	head, err := gitrepo.HeadHash(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	return &conflictFixture{
		vault: v, ident: id, remote: remote, other: other,
		entryID: entryID, path: path, mine: mine, theirs: theirs,
		headBefore: head,
	}
}

// resolveWith runs the resolving sync with p answering, using this
// fixture's already-unlocked identity.
func (f *conflictFixture) resolveWith(t *testing.T, p *resolvingPrompter) (SyncReport, error) {
	t.Helper()

	return f.vault.SyncResolving(context.Background(), ConflictResolver{
		Prompter: p,
		Unlock:   func() (*Identity, error) { return &f.ident, nil },
	})
}

// entryIDsOtherThan returns every entry in the vault except id — the
// fresh-UUID side of a `keep both`.
func entryIDsOtherThan(t *testing.T, v *Vault, id uuid.UUID) []uuid.UUID {
	t.Helper()

	ids, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	var out []uuid.UUID
	for _, got := range ids {
		if got != id {
			out = append(out, got)
		}
	}
	return out
}

// headCommit reads a repository's HEAD commit object, for the assertions
// about the merge commit's own shape.
func headCommit(t *testing.T, dir string) *object.Commit {
	t.Helper()

	repo, err := git.PlainOpen(dir)
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
	return commit
}

// walkHistory traverses every commit reachable from HEAD, which is the
// structural stand-in for what `gage log` and M12's `history --decrypt`
// will do. A merge commit is the first thing in this vault's history with
// two parents, so "traversal completes" is worth asserting before M12
// depends on it.
func walkHistory(t *testing.T, dir string) []*object.Commit {
	t.Helper()

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash(), Order: git.LogOrderCommitterTime})
	if err != nil {
		t.Fatalf("walking history: %v", err)
	}
	defer iter.Close()

	var commits []*object.Commit
	if err := iter.ForEach(func(c *object.Commit) error {
		commits = append(commits, c)
		return nil
	}); err != nil {
		t.Fatalf("traversing a history containing a merge commit: %v", err)
	}
	return commits
}

// ---------------------------------------------------------------------
// Resolution mechanics
// ---------------------------------------------------------------------

// TestConflictIsPresentedWithBothSidesDecrypted is the presentation
// contract: the library hands the frontend a typed value carrying both
// decrypted versions and their provenance, and renders nothing itself.
// The prompt in "Resolving an entry conflict" is built from this value.
func TestConflictIsPresentedWithBothSidesDecrypted(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	p := answering(KeepLocal)
	if _, err := f.resolveWith(t, p); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	if len(p.presented) != 1 {
		t.Fatalf("%d conflicts presented, want 1", len(p.presented))
	}
	c := p.presented[0]

	if c.ID != f.entryID {
		t.Errorf("conflict ID = %s, want %s", c.ID, f.entryID)
	}
	if c.Path != f.path {
		t.Errorf("conflict Path = %q, want %q", c.Path, f.path)
	}
	if !c.Local.Present || !c.Remote.Present {
		t.Fatalf("both sides changed the entry, so both must be present: local=%v remote=%v",
			c.Local.Present, c.Remote.Present)
	}

	// Decrypted, not raw: the value is what makes this a choice a human
	// can actually make.
	if c.Local.Entry.Value != f.mine.Value {
		t.Errorf("local value = %q, want %q", c.Local.Entry.Value, f.mine.Value)
	}
	if c.Remote.Entry.Value != f.theirs.Value {
		t.Errorf("remote value = %q, want %q", c.Remote.Entry.Value, f.theirs.Value)
	}

	// Who wrote each, and when — the two lines the design doc's prompt
	// shows under "local" and "remote".
	if c.Local.Entry.UpdatedBy != "laptop-1" {
		t.Errorf("local updated_by = %q, want laptop-1", c.Local.Entry.UpdatedBy)
	}
	if c.Remote.Entry.UpdatedBy != "phone" {
		t.Errorf("remote updated_by = %q, want phone", c.Remote.Entry.UpdatedBy)
	}
	if !c.Local.Entry.Updated.Equal(f.mine.Updated.Time) {
		t.Errorf("local updated = %v, want %v", c.Local.Entry.Updated, f.mine.Updated)
	}
	if !c.Remote.Entry.Updated.Equal(f.theirs.Updated.Time) {
		t.Errorf("remote updated = %v, want %v", c.Remote.Entry.Updated, f.theirs.Updated)
	}
}

// TestKeepLocalKeepsTheLocalVersion: the local version stays, at its own
// id, and the remote's version is not left anywhere in the tree.
func TestKeepLocalKeepsTheLocalVersion(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	report, err := f.resolveWith(t, answering(KeepLocal))
	if err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}
	if report.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1", report.Resolved)
	}

	got, err := f.vault.ReadEntry(f.entryID, &f.ident)
	if err != nil {
		t.Fatalf("reading the resolved entry: %v", err)
	}
	if got.Value != f.mine.Value {
		t.Errorf("value = %q, want the local version %q", got.Value, f.mine.Value)
	}

	if extra := entryIDsOtherThan(t, f.vault, f.entryID); len(extra) != 0 {
		t.Errorf("keep local left %d extra entr(y/ies) behind: %v", len(extra), extra)
	}
}

// TestKeepRemoteReplacesTheLocalVersion: same id, the other device's
// content.
func TestKeepRemoteReplacesTheLocalVersion(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	if _, err := f.resolveWith(t, answering(KeepRemote)); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	got, err := f.vault.ReadEntry(f.entryID, &f.ident)
	if err != nil {
		t.Fatalf("reading the resolved entry: %v", err)
	}
	if got.Value != f.theirs.Value {
		t.Errorf("value = %q, want the remote version %q", got.Value, f.theirs.Value)
	}
	if extra := entryIDsOtherThan(t, f.vault, f.entryID); len(extra) != 0 {
		t.Errorf("keep remote left %d extra entr(y/ies) behind: %v", len(extra), extra)
	}
}

// TestKeepBothKeepsBothVersions is the substance of the milestone: no
// version of a secret is destroyed by resolving a conflict.
func TestKeepBothKeepsBothVersions(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	if _, err := f.resolveWith(t, answering(KeepBoth)); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	// The local version stays where it was.
	local, err := f.vault.ReadEntry(f.entryID, &f.ident)
	if err != nil {
		t.Fatalf("reading the local version after keep both: %v", err)
	}
	if local.Value != f.mine.Value {
		t.Errorf("the entry at the original id = %q, want the local version %q", local.Value, f.mine.Value)
	}

	// The losing version is a second entry under a fresh id.
	extra := entryIDsOtherThan(t, f.vault, f.entryID)
	if len(extra) != 1 {
		t.Fatalf("keep both produced %d extra entr(y/ies), want exactly 1: %v", len(extra), extra)
	}
	if extra[0] == uuid.Nil {
		t.Fatal("the losing version was written under the nil UUID, want a fresh one")
	}

	kept, err := f.vault.ReadEntry(extra[0], &f.ident)
	if err != nil {
		t.Fatalf("the version kept by keep both is not decryptable: %v", err)
	}
	if kept.Value != f.theirs.Value {
		t.Errorf("the new entry = %q, want the remote version %q", kept.Value, f.theirs.Value)
	}
	if kept.Title != f.theirs.Title {
		t.Errorf("the new entry's title = %q, want it verbatim (%q) rather than suffixed",
			kept.Title, f.theirs.Title)
	}
}

// TestKeepBothPreservesTheLosingSidesProvenance: the point of keeping
// both is that nothing is lost — including *who* wrote the losing version
// and when. Restamping it as written by this device, now, would discard
// exactly the information the conflict prompt just showed.
func TestKeepBothPreservesTheLosingSidesProvenance(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	if _, err := f.resolveWith(t, answering(KeepBoth)); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	extra := entryIDsOtherThan(t, f.vault, f.entryID)
	if len(extra) != 1 {
		t.Fatalf("keep both produced %d extra entr(y/ies), want 1", len(extra))
	}
	kept, err := f.vault.ReadEntry(extra[0], &f.ident)
	if err != nil {
		t.Fatal(err)
	}

	if kept.UpdatedBy != f.theirs.UpdatedBy {
		t.Errorf("updated_by = %q, want the losing side's own %q — not this device",
			kept.UpdatedBy, f.theirs.UpdatedBy)
	}
	if !kept.Updated.Equal(f.theirs.Updated.Time) {
		t.Errorf("updated = %v, want the losing side's own %v — not the resolution time",
			kept.Updated, f.theirs.Updated)
	}
	if !kept.Created.Equal(f.theirs.Created.Time) {
		t.Errorf("created = %v, want the losing side's own %v", kept.Created, f.theirs.Created)
	}
}

// TestKeepBothLeavesTwoEntriesTheResolverDisambiguates closes the loop
// with M5: keep both is only survivable because duplicate titles already
// have an answer. Neither entry shadows the other — the query is
// ambiguous, and both are offered.
func TestKeepBothLeavesTwoEntriesTheResolverDisambiguates(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	if _, err := f.resolveWith(t, answering(KeepBoth)); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	_, _, err := f.vault.Resolve(f.mine.Title, &f.ident)
	if err == nil {
		t.Fatal("resolving the shared title succeeded, want it to be ambiguous — one version is shadowing the other")
	}
	if !errors.Is(err, ErrAmbiguousQuery) {
		t.Fatalf("Resolve error = %v, want ErrAmbiguousQuery", err)
	}

	var amb *AmbiguousQueryError
	if !errors.As(err, &amb) {
		t.Fatalf("Resolve error = %v, want an *AmbiguousQueryError carrying the candidates", err)
	}
	if len(amb.List.Candidates) != 2 {
		t.Errorf("%d candidates offered, want both versions: %+v", len(amb.List.Candidates), amb.List.Candidates)
	}
	// One-shot mode treats a candidate list as a failure; the exit code
	// is what a script sees.
	if code := exitcode.CodeOf(err); code != exitcode.Ambiguous {
		t.Errorf("exit code = %v, want %v", code, exitcode.Ambiguous)
	}
}

// TestSkipLeavesTheEntryConflictedAndDoesNotPush: skip is not a silent
// "keep local". Nothing is applied, no merge commit is made, and the sync
// ends without publishing — the divergence is still there to resolve.
func TestSkipLeavesTheEntryConflictedAndDoesNotPush(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	report, err := f.resolveWith(t, answering(SkipConflict))
	if err == nil {
		t.Fatal("a sync that skipped its only conflict succeeded, want the unresolved divergence reported")
	}
	if report.Pushed {
		t.Error("a sync with an unresolved conflict pushed")
	}
	if report.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", report.Skipped)
	}
	if report.Merged {
		t.Error("a skipped conflict produced a merge, want the tree left conflicted")
	}

	head, err := gitrepo.HeadHash(f.vault.Path)
	if err != nil {
		t.Fatal(err)
	}
	if head != f.headBefore {
		t.Errorf("HEAD moved (%s -> %s) despite the conflict being skipped", f.headBefore, head)
	}

	// The local version is untouched, and still the only one.
	got, err := f.vault.ReadEntry(f.entryID, &f.ident)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != f.mine.Value {
		t.Errorf("value = %q, want the local version untouched (%q)", got.Value, f.mine.Value)
	}
}

// TestSkipStillPresentsTheRemainingConflicts: skip means "not this one",
// not "stop asking" — that is what distinguishes it from abort.
func TestSkipStillPresentsTheRemainingConflicts(t *testing.T) {
	f := newTwoConflictFixture(t)
	defer f.close()

	p := answering(SkipConflict, SkipConflict)
	if _, err := f.resolveWith(t, p); err == nil {
		t.Fatal("a sync with skipped conflicts succeeded, want it reported")
	}
	if len(p.presented) != 2 {
		t.Errorf("%d conflicts presented after a skip, want both", len(p.presented))
	}
}

// TestAbortRestoresThePreSyncState: quitting stops the questions and
// leaves the vault exactly as it was found.
func TestAbortRestoresThePreSyncState(t *testing.T) {
	f := newTwoConflictFixture(t)
	defer f.close()

	p := answering(AbortSync, KeepRemote)
	report, err := f.resolveWith(t, p)
	if err == nil {
		t.Fatal("an aborted sync succeeded, want it reported as unresolved")
	}
	if !report.Aborted {
		t.Error("report.Aborted = false after an abort")
	}
	if report.Pushed {
		t.Error("an aborted sync pushed")
	}

	// Abort stops immediately: the second conflict is never asked about.
	if len(p.presented) != 1 {
		t.Errorf("%d conflicts presented, want 1 — abort must stop asking", len(p.presented))
	}

	head, err := gitrepo.HeadHash(f.vault.Path)
	if err != nil {
		t.Fatal(err)
	}
	if head != f.headBefore {
		t.Errorf("HEAD moved (%s -> %s) on an aborted sync", f.headBefore, head)
	}

	clean, err := gitrepo.IsClean(f.vault.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		paths, _ := gitrepo.DirtyPaths(f.vault.Path)
		t.Errorf("an aborted sync left the working tree dirty: %v", paths)
	}

	ids, err := f.vault.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Errorf("%d entries after an abort, want the 2 that were there before", len(ids))
	}
}

// twoConflictFixture is conflictFixture with a second conflicting entry,
// for the tests that need to observe what happens *after* one conflict is
// answered.
func newTwoConflictFixture(t *testing.T) *conflictFixture {
	t.Helper()

	v, id, remote := newSyncVault(t, "personal", "laptop-1")

	base := sampleEntry(time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC))
	firstID, err := v.Insert(base, false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	second := base
	second.Title = "Bank"
	secondID, err := v.Insert(second, false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	firstPath := entriesDirName + "/" + firstID.String() + entryFileExt
	secondPath := entriesDirName + "/" + secondID.String() + entryFileExt

	theirFirst := base
	theirFirst.Value = "their-rotated-password"
	theirFirst.UpdatedBy = "phone"
	theirSecond := second
	theirSecond.Value = "their-bank-password"
	theirSecond.UpdatedBy = "phone"

	other := gittest.NewDevice(t, remote)
	other.Write(t, firstPath, ciphertextFor(t, v, theirFirst))
	other.Write(t, secondPath, ciphertextFor(t, v, theirSecond))
	other.Commit(t, "the other device's edits")
	other.Push(t)

	mine := base
	mine.Value = "my-rotated-password"
	mine.UpdatedBy = "laptop-1"
	if err := v.Update(firstID, mine, &id); err != nil {
		t.Fatalf("Update: %v", err)
	}
	mineSecond := second
	mineSecond.Value = "my-bank-password"
	mineSecond.UpdatedBy = "laptop-1"
	if err := v.Update(secondID, mineSecond, &id); err != nil {
		t.Fatalf("Update: %v", err)
	}

	head, err := gitrepo.HeadHash(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	return &conflictFixture{
		vault: v, ident: id, remote: remote, other: other,
		entryID: firstID, path: firstPath, mine: mine, theirs: theirFirst,
		headBefore: head,
	}
}

// TestDeleteModifyConflictShowsTheSurvivingVersion: one side removed the
// entry, the other edited it. There is only one version to show, and
// either answer has to leave a tree that still makes sense.
func TestDeleteModifyConflictShowsTheSurvivingVersion(t *testing.T) {
	f := newDeleteModifyFixture(t)
	defer f.close()

	p := answering(KeepRemote)
	if _, err := f.resolveWith(t, p); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	if len(p.presented) != 1 {
		t.Fatalf("%d conflicts presented, want 1", len(p.presented))
	}
	c := p.presented[0]
	if c.Local.Present {
		t.Error("the local side deleted the entry, so it must be presented as absent")
	}
	if !c.Remote.Present {
		t.Fatal("the surviving (remote) version must be presented")
	}
	if c.Remote.Entry.Value != f.theirs.Value {
		t.Errorf("surviving value = %q, want %q", c.Remote.Entry.Value, f.theirs.Value)
	}

	// Keeping the survivor brings the entry back, decryptable.
	got, err := f.vault.ReadEntry(f.entryID, &f.ident)
	if err != nil {
		t.Fatalf("keep remote on a delete/modify conflict left no readable entry: %v", err)
	}
	if got.Value != f.theirs.Value {
		t.Errorf("value = %q, want the surviving version %q", got.Value, f.theirs.Value)
	}
}

// TestDeleteModifyConflictKeepingTheDeletion is the same conflict
// answered the other way: the deletion stands, and the tree is consistent
// rather than carrying a file git thinks should not be there.
func TestDeleteModifyConflictKeepingTheDeletion(t *testing.T) {
	f := newDeleteModifyFixture(t)
	defer f.close()

	if _, err := f.resolveWith(t, answering(KeepLocal)); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	if _, err := os.Stat(filepath.Join(f.vault.Path, filepath.FromSlash(f.path))); !os.IsNotExist(err) {
		t.Errorf("keeping the local deletion left the entry file in place (stat err = %v)", err)
	}
	clean, err := gitrepo.IsClean(f.vault.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		paths, _ := gitrepo.DirtyPaths(f.vault.Path)
		t.Errorf("resolving a delete/modify conflict left the tree dirty: %v", paths)
	}
}

// newDeleteModifyFixture: this device removes an entry the other device
// edits.
func newDeleteModifyFixture(t *testing.T) *conflictFixture {
	t.Helper()

	v, id, remote := newSyncVault(t, "personal", "laptop-1")

	base := sampleEntry(time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC))
	entryID, err := v.Insert(base, false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	path := entriesDirName + "/" + entryID.String() + entryFileExt

	theirs := base
	theirs.Value = "their-rotated-password"
	theirs.UpdatedBy = "phone"

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, path, ciphertextFor(t, v, theirs), "the other device's edit")

	if _, err := v.Remove(entryID.String(), &id); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	head, err := gitrepo.HeadHash(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	return &conflictFixture{
		vault: v, ident: id, remote: remote, other: other,
		entryID: entryID, path: path, theirs: theirs, headBefore: head,
	}
}

// ---------------------------------------------------------------------
// History
// ---------------------------------------------------------------------

// TestResolvedSyncProducesATwoParentMergeCommit: the result is a real
// merge, not a rebase and not a flattened snapshot.
func TestResolvedSyncProducesATwoParentMergeCommit(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	remoteHead, err := trackingHash(f.vault.Path)
	if err != nil {
		t.Fatal(err)
	}

	report, err := f.resolveWith(t, answering(KeepBoth))
	if err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}
	if !report.Merged {
		t.Error("report.Merged = false after a resolved conflict")
	}

	merge := headCommit(t, f.vault.Path)
	if merge.NumParents() != 2 {
		t.Fatalf("the resolution commit has %d parent(s), want 2", merge.NumParents())
	}

	parents := []string{merge.ParentHashes[0].String(), merge.ParentHashes[1].String()}
	if parents[0] != f.headBefore {
		t.Errorf("first parent = %s, want the local head %s", parents[0], f.headBefore)
	}
	if parents[1] != remoteHead.String() {
		t.Errorf("second parent = %s, want the fetched remote head %s", parents[1], remoteHead)
	}
}

// trackingHash is what the fetch left in the remote-tracking ref — the
// commit a merge's second parent has to be.
func trackingHash(dir string) (plumbing.Hash, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	head, err := repo.Head()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", head.Name().Short()), true)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ref.Hash(), nil
}

// TestLocalCommitGranularitySurvivesTheMerge: the per-write history a
// secrets manager may later want is still there afterwards — the merge
// added a commit, it didn't replace the ones underneath it.
func TestLocalCommitGranularitySurvivesTheMerge(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	if _, err := f.resolveWith(t, answering(KeepLocal)); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	var found bool
	for _, c := range walkHistory(t, f.vault.Path) {
		if c.Hash.String() == f.headBefore {
			found = true
		}
	}
	if !found {
		t.Errorf("the local commit %s is no longer reachable after the merge; local history was flattened or rebased",
			f.headBefore)
	}
}

// TestHistoryWalksThroughAMergeCommit is asserted here, structurally, so
// M12's `history --decrypt` doesn't discover late that this milestone
// introduced the first two-parent commit in a gage vault.
func TestHistoryWalksThroughAMergeCommit(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	if _, err := f.resolveWith(t, answering(KeepBoth)); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	commits := walkHistory(t, f.vault.Path)
	if len(commits) < 3 {
		t.Fatalf("history has %d commit(s), want the merge plus both sides' work", len(commits))
	}

	var merges int
	for _, c := range commits {
		if c.NumParents() == 2 {
			merges++
		}
	}
	if merges != 1 {
		t.Errorf("%d merge commits reachable, want exactly 1", merges)
	}
}

// TestMergeCommitMessageNamesTheCountAndNoPlaintext extends M4's
// commit-message confidentiality rule to merges: the message says how
// much was resolved and nothing about *what*.
func TestMergeCommitMessageNamesTheCountAndNoPlaintext(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	if _, err := f.resolveWith(t, answering(KeepBoth)); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	msg := headCommit(t, f.vault.Path).Message
	if !strings.Contains(msg, "1") {
		t.Errorf("merge message = %q, want it to name the number of entries resolved", msg)
	}

	for _, secret := range []string{
		f.mine.Title,
		f.mine.Value,
		f.theirs.Value,
		f.mine.Description,
		f.mine.UpdatedBy,
		f.theirs.UpdatedBy,
	} {
		if secret != "" && strings.Contains(msg, secret) {
			t.Errorf("merge message = %q, want it to leak no plaintext — it contains %q", msg, secret)
		}
	}
}

// ---------------------------------------------------------------------
// Unlocking, and interaction with other milestones
// ---------------------------------------------------------------------

// countingUnlock records how many times resolution asked for an identity,
// which is how the lazy-unlock tests assert that a sync needing no
// plaintext never asked at all.
func countingUnlock(id *Identity) (func() (*Identity, error), *int) {
	var calls int
	return func() (*Identity, error) {
		calls++
		return id, nil
	}, &calls
}

// TestCleanFastForwardNeverUnlocks: catching up decrypts nothing, so it
// must not ask for a key. This is the property that keeps a passphrase
// prompt off every ordinary sync.
func TestCleanFastForwardNeverUnlocks(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "notes.txt", "from elsewhere", "another device's commit")

	unlock, calls := countingUnlock(&id)
	p := answering()
	if _, err := v.SyncResolving(context.Background(), ConflictResolver{Prompter: p, Unlock: unlock}); err != nil {
		t.Fatalf("SyncResolving on a fast-forward: %v", err)
	}

	if *calls != 0 {
		t.Errorf("a fast-forward unlocked the vault %d time(s), want 0", *calls)
	}
	if len(p.presented) != 0 {
		t.Errorf("a fast-forward presented %d conflict(s), want 0", len(p.presented))
	}
}

// TestDisjointDivergenceNeverUnlocks: a merge whose two sides touched
// different entries is M8a's to complete, and it decrypts nothing either.
func TestDisjointDivergenceNeverUnlocks(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "notes.txt", "the other device's file", "another device's commit")

	// A local write that can't push, so a real divergence exists.
	v.remoteSyncer = &fakeSyncer{pushErr: unreachableError()}
	if _, err := v.Insert(sampleEntry(time.Now()), false, &id); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	v.remoteSyncer = nil

	unlock, calls := countingUnlock(&id)
	p := answering()
	report, err := v.SyncResolving(context.Background(), ConflictResolver{Prompter: p, Unlock: unlock})
	if err != nil {
		t.Fatalf("SyncResolving on a disjoint divergence: %v", err)
	}
	if !report.Merged {
		t.Error("a disjoint divergence did not merge")
	}
	if *calls != 0 {
		t.Errorf("a disjoint merge unlocked the vault %d time(s), want 0", *calls)
	}
	if len(p.presented) != 0 {
		t.Errorf("a disjoint merge presented %d conflict(s), want 0", len(p.presented))
	}
}

// TestUnlockHappensOnlyWhenAConflictIsReached: the unlock is what
// resolution forces, at the point plaintext is actually needed.
func TestUnlockHappensOnlyWhenAConflictIsReached(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	unlock, calls := countingUnlock(&f.ident)
	p := answering(KeepLocal)
	p.before = func() {
		if *calls == 0 {
			t.Error("a conflict was presented before the vault was unlocked; both sides must be decrypted first")
		}
	}

	if _, err := f.vault.SyncResolving(context.Background(),
		ConflictResolver{Prompter: p, Unlock: unlock}); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}
	if *calls != 1 {
		t.Errorf("resolution unlocked %d time(s), want exactly 1", *calls)
	}
}

// TestResolutionReusesASessionsCachedIdentity: in a session that is
// already unlocked, resolving a conflict must not re-prompt. The cached
// identity is what the Unlock callback hands back, and no passphrase
// request reaches the Prompter.
func TestResolutionReusesASessionsCachedIdentity(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	p := answering(KeepRemote)
	if _, err := f.resolveWith(t, p); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	if len(p.requests) != 0 {
		t.Errorf("resolution sent %d unlock request(s) despite a cached identity: %+v",
			len(p.requests), p.requests)
	}
}

// TestResolutionUpdatesTheSessionIndex: entries a resolution replaced or
// added are visible to the very next ls, with no manual reindex — the
// same requirement M7 places on a pull.
func TestResolutionUpdatesTheSessionIndex(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	s := newSyncSession(t, f.vault)
	defer func() { _ = s.Close() }()

	// Build the index first, deliberately: a stale cache would still
	// report one entry after the resolution below.
	before, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("%d entries before resolving, want 1", len(before))
	}

	v, err := s.VaultWithoutUnlocking("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.SyncResolving(context.Background(), ConflictResolver{
		Prompter: answering(KeepBoth),
		Unlock:   func() (*Identity, error) { return &f.ident, nil },
	}); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}

	after, err := s.List("")
	if err != nil {
		t.Fatalf("List after resolving: %v", err)
	}
	if len(after) != 2 {
		t.Errorf("%d entries after keep both, want 2 — the index was not invalidated by the resolution", len(after))
	}
}

// TestRecipientConflictDoesNotReachTheEntryResolver: the files that
// define who can read the vault are never one of these [l/r/b] questions.
// M10 owns what *does* happen to them; this asserts only that they don't
// land here.
func TestRecipientConflictDoesNotReachTheEntryResolver(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, recipientsFile,
		readVaultFile(t, v, recipientsFile)+"age1theirextrarecipient\n", "their recipient change")

	v.remoteSyncer = &fakeSyncer{pushErr: unreachableError()}
	writeVaultFile(t, v, recipientsFile, readVaultFile(t, v, recipientsFile)+"age1myextrarecipient\n")
	if _, err := gitrepo.CommitAll(v.Path, "my recipient change"); err != nil {
		t.Fatal(err)
	}
	v.remoteSyncer = nil

	p := answering(KeepLocal)
	report, err := v.SyncResolving(context.Background(), ConflictResolver{
		Prompter: p,
		Unlock:   func() (*Identity, error) { return &id, nil },
	})
	if err == nil {
		t.Fatal("a recipient-file divergence resolved silently, want it refused")
	}
	if len(p.presented) != 0 {
		t.Errorf("a recipient-file conflict was presented through the entry resolver: %+v", p.presented)
	}
	if report.ConflictKind() != ConflictRecipients {
		t.Errorf("ConflictKind = %v, want ConflictRecipients", report.ConflictKind())
	}
}

// ---------------------------------------------------------------------
// Non-interactive
// ---------------------------------------------------------------------

// TestNonInteractivePrompterRefusesOnTheFirstConflict: with nobody to
// ask, `gage sync` fails rather than picking a side. Choosing silently is
// the last-write-wins outcome the whole sync model exists to refuse.
func TestNonInteractivePrompterRefusesOnTheFirstConflict(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	// A plain Prompter: it can unlock and warn, but it cannot resolve a
	// conflict, which is exactly what a script or a CI run looks like.
	plain := &fakePrompter{passphrases: []string{testPassphrase}}

	report, err := f.vault.SyncResolving(context.Background(), ConflictResolver{
		Prompter: plain,
		Unlock:   func() (*Identity, error) { return &f.ident, nil },
	})
	if err == nil {
		t.Fatal("a non-interactive sync resolved a conflict, want it refused")
	}
	if !errors.Is(err, ErrNotInteractive) {
		t.Errorf("error = %v, want it to match ErrNotInteractive", err)
	}
	if code := exitcode.CodeOf(err); code != exitcode.Conflict {
		t.Errorf("exit code = %v, want %v", code, exitcode.Conflict)
	}
	if report.Resolved != 0 {
		t.Errorf("Resolved = %d on a refusal, want 0", report.Resolved)
	}

	// And nothing was chosen on the human's behalf.
	head, err := gitrepo.HeadHash(f.vault.Path)
	if err != nil {
		t.Fatal(err)
	}
	if head != f.headBefore {
		t.Errorf("HEAD moved (%s -> %s) on a refused non-interactive sync", f.headBefore, head)
	}
	got, err := f.vault.ReadEntry(f.entryID, &f.ident)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != f.mine.Value {
		t.Errorf("value = %q, want the local version untouched", got.Value)
	}
}

// TestNilPrompterRefusesRatherThanPanicking: a library caller that
// supplied no Prompter at all is the same situation as a non-interactive
// one, and must read as a refusal rather than a nil dereference.
func TestNilPrompterRefusesRatherThanPanicking(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	_, err := f.vault.SyncResolving(context.Background(), ConflictResolver{
		Unlock: func() (*Identity, error) { return &f.ident, nil },
	})
	if err == nil {
		t.Fatal("a sync with no Prompter resolved a conflict, want it refused")
	}
	if !errors.Is(err, ErrNotInteractive) {
		t.Errorf("error = %v, want it to match ErrNotInteractive", err)
	}
}

// ---------------------------------------------------------------------
// Locking
// ---------------------------------------------------------------------

// TestResolutionHoldsTheWriteLockThroughout: the lock is taken before the
// first question and held until the last, so no other process can land a
// write in the middle of a resolution and move the local head out from
// under the merge being assembled.
func TestResolutionHoldsTheWriteLockThroughout(t *testing.T) {
	f := newConflictFixture(t)
	defer f.close()

	lockPath, err := LockFilePath(f.vault.Name)
	if err != nil {
		t.Fatal(err)
	}

	var heldDuringPrompt bool
	p := answering(KeepLocal)
	p.before = func() {
		// A zero timeout tries once: if resolution holds the lock, this
		// is contended, which is the whole assertion.
		lock, err := vaultlock.Acquire(lockPath, 0)
		var contended *vaultlock.ContendedError
		switch {
		case errors.As(err, &contended):
			heldDuringPrompt = true
		case err != nil:
			t.Errorf("acquiring the vault lock during resolution: %v", err)
		default:
			_ = lock.Release()
		}
	}

	if _, err := f.resolveWith(t, p); err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}
	if !heldDuringPrompt {
		t.Error("the vault write lock was free while a conflict prompt was waiting for an answer; " +
			"resolution must hold it throughout")
	}

	// And released afterwards, so the next process isn't locked out.
	lock, err := vaultlock.Acquire(lockPath, 0)
	if err != nil {
		t.Fatalf("the write lock was still held after resolution finished: %v", err)
	}
	_ = lock.Release()
}
