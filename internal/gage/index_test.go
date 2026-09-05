package gage

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// insertEntry is a small helper for tests that need control over more
// than just the title — description and value, mainly, for the search
// tests below.
func insertEntry(t *testing.T, v *Vault, ident *Identity, e Entry) uuid.UUID {
	t.Helper()
	now := NewTimestamp(time.Now())
	e.Created, e.Updated = now, now
	if e.UpdatedBy == "" {
		e.UpdatedBy = "laptop-1"
	}
	id, err := v.Insert(e, false, ident)
	if err != nil {
		t.Fatalf("inserting %q: %v", e.Title, err)
	}
	return id
}

// TestFirstListSearchOrResolveBuildsIndexWithOneDecryptPass is the
// milestone's core property, restated for the index specifically: the
// first ls/show/search per vault decrypts every entry exactly once.
func TestFirstListBuildsIndexWithOneDecryptPass(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS")
	insertTitled(t, v, ident, "GitHub")
	insertTitled(t, v, ident, "ProtonMail")

	var decrypts int
	v.onDecrypt = func() { decrypts++ }

	if _, err := s.List(""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if decrypts != 3 {
		t.Fatalf("first List decrypted %d times, want exactly 3 (one pass over the vault)", decrypts)
	}

	// Subsequent List/Resolve calls reuse the index without decrypting
	// anything beyond the single entry a Resolve match returns.
	if _, err := s.List(""); err != nil {
		t.Fatalf("second List: %v", err)
	}
	if decrypts != 3 {
		t.Errorf("second List re-decrypted the vault: %d total, want still 3", decrypts)
	}
	if _, _, err := s.Resolve("", "AWS"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decrypts != 4 {
		t.Errorf("Resolve decrypted %d total, want 4 (3 for the index, 1 for the matched entry)", decrypts)
	}
}

// TestNoteEntryUpdatesIndexWithoutFullRebuild covers insert's half of
// "insert/edit/rm update the in-memory index incrementally, without
// triggering a full rebuild" — cmd/gage's noteIndexEntry, called here
// directly the way the CLI layer calls it after a successful
// Vault.Insert.
func TestNoteEntryUpdatesIndexWithoutFullRebuild(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS")
	insertTitled(t, v, ident, "GitHub")

	if _, err := s.List(""); err != nil {
		t.Fatalf("building the index: %v", err)
	}

	var decrypts int
	v.onDecrypt = func() { decrypts++ }

	// Insert itself decrypts the vault once, for its own duplicate-title
	// guard (titleExists) — that cost belongs to Insert, not to the
	// index. What this test is really pinning is that NoteEntry, and the
	// List right after it, add nothing on top of that.
	e3 := Entry{Title: "ProtonMail", UpdatedBy: "laptop-1", Value: "v3"}
	id3 := insertEntry(t, v, ident, e3)
	afterInsert := decrypts

	s.NoteEntry("personal", id3, e3)
	if decrypts != afterInsert {
		t.Fatalf("NoteEntry itself decrypted %d more times, want 0", decrypts-afterInsert)
	}

	rows, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if decrypts != afterInsert {
		t.Errorf("List after NoteEntry re-decrypted the vault: %d more, want 0 (no full rebuild)", decrypts-afterInsert)
	}
	if len(rows) != 3 {
		t.Fatalf("List returned %d rows, want 3", len(rows))
	}
	var saw bool
	for _, r := range rows {
		if r.ID == id3 {
			saw = true
		}
	}
	if !saw {
		t.Error("List doesn't include the entry NoteEntry registered")
	}
}

// TestSessionResolveSeesARenameAfterNoteEntry is "rename updates the
// indexed title, and a subsequent query resolves against the new title
// and not the old one."
func TestSessionResolveSeesARenameAfterNoteEntry(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	idStr := insertTitled(t, v, ident, "Old Title")
	id := uuid.MustParse(idStr)

	if _, err := s.List(""); err != nil {
		t.Fatalf("building the index: %v", err)
	}

	renamedID, err := v.Rename(idStr, "New Title", false, ident)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	renamed, err := v.ReadEntry(renamedID, ident)
	if err != nil {
		t.Fatal(err)
	}
	s.NoteEntry("personal", renamedID, renamed)

	gotID, gotEntry, err := s.Resolve("", "New Title")
	if err != nil {
		t.Fatalf("Resolve by new title: %v", err)
	}
	if gotID != id {
		t.Errorf("resolved id = %s, want %s", gotID, id)
	}
	if gotEntry.Title != "New Title" {
		t.Errorf("resolved title = %q, want %q", gotEntry.Title, "New Title")
	}

	if _, _, err := s.Resolve("", "Old Title"); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("Resolve by old title = %v, want ErrEntryNotFound", err)
	}
}

// TestForgetEntryRemovesFromIndex is `rm`'s half of the same property.
func TestForgetEntryRemovesFromIndex(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	idStr := insertTitled(t, v, ident, "AWS")
	id := uuid.MustParse(idStr)
	insertTitled(t, v, ident, "GitHub")

	if _, err := s.List(""); err != nil {
		t.Fatalf("building the index: %v", err)
	}

	if _, err := v.Remove(idStr, ident); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	s.ForgetEntry("personal", id)

	rows, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			t.Error("List still shows an entry ForgetEntry dropped")
		}
	}
	if _, _, err := s.Resolve("", idStr); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("Resolve for a forgotten id = %v, want ErrEntryNotFound", err)
	}
}

// TestMultiVaultSessionsHaveSeparateIndexes: unlocking/listing a second
// vault must not rebuild or disturb the first's index.
func TestMultiVaultSessionsHaveSeparateIndexes(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal", "work")
	vp, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	vw, err := open("work")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{})

	_, identP, err := s.Vault("personal")
	if err != nil {
		t.Fatal(err)
	}
	_, identW, err := s.Vault("work")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, vp, identP, "Personal Entry")
	insertTitled(t, vw, identW, "Work Entry")

	var decryptsP, decryptsW int
	vp.onDecrypt = func() { decryptsP++ }
	vw.onDecrypt = func() { decryptsW++ }

	if _, err := s.List("personal"); err != nil {
		t.Fatalf("List personal: %v", err)
	}
	if decryptsP != 1 {
		t.Errorf("listing personal decrypted it %d times, want 1", decryptsP)
	}
	if decryptsW != 0 {
		t.Errorf("listing personal decrypted work %d times, want 0", decryptsW)
	}

	if _, err := s.List("work"); err != nil {
		t.Fatalf("List work: %v", err)
	}
	if decryptsW != 1 {
		t.Errorf("listing work decrypted it %d times, want 1", decryptsW)
	}
	if decryptsP != 1 {
		t.Errorf("listing work re-decrypted personal: %d total, want still 1", decryptsP)
	}
}

// TestLockDiscardsIndex: the index must not outlive the key that built
// it, whether locked explicitly or via the idle timeout (Lock and the
// idle timeout share lockHeld, so this covers both).
func TestLockDiscardsIndex(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS")

	var decrypts int
	v.onDecrypt = func() { decrypts++ }
	if _, err := s.List(""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if decrypts != 1 {
		t.Fatalf("got %d decrypts, want 1", decrypts)
	}

	if err := s.Lock("personal"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if s.vaults["personal"].index != nil {
		t.Error("Lock left an index behind")
	}

	if err := s.Use("personal"); err != nil {
		t.Fatalf("re-Use after Lock: %v", err)
	}
	if _, err := s.List(""); err != nil {
		t.Fatalf("List after re-unlock: %v", err)
	}
	if decrypts != 2 {
		t.Errorf("total decrypts after re-unlock + List = %d, want 2 (the index was rebuilt from scratch)", decrypts)
	}
}

// TestReindexForcesFullRebuildAndPicksUpOutOfBandChanges: gage reindex's
// whole reason to exist — a change to entries/ that didn't come through
// this session (a manual `git pull`, simulated here as a plain
// v.Insert with no NoteEntry) isn't visible until Reindex runs.
func TestReindexForcesFullRebuildAndPicksUpOutOfBandChanges(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS")

	if _, err := s.List(""); err != nil {
		t.Fatalf("building the index: %v", err)
	}

	newID := insertTitled(t, v, ident, "GitHub") // out-of-band: no NoteEntry

	rows, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stale index shows %d rows, want 1 (it shouldn't have noticed the out-of-band insert)", len(rows))
	}

	if err := s.Reindex(""); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	rows, err = s.List("")
	if err != nil {
		t.Fatalf("List after Reindex: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("after Reindex, List shows %d rows, want 2", len(rows))
	}
	var saw bool
	for _, r := range rows {
		if r.ID.String() == newID {
			saw = true
		}
	}
	if !saw {
		t.Error("Reindex didn't pick up the out-of-band entry")
	}
}

// TestSearchMatchesTitleDescriptionAndBody covers all three fields
// search is documented to match against, and that only the body match
// is flagged as such.
func TestSearchMatchesTitleDescriptionAndBody(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}

	titleMatch := insertEntry(t, v, ident, Entry{Title: "AWS root account", Value: "x"})
	descMatch := insertEntry(t, v, ident, Entry{Title: "Cloud creds", Description: "shared aws credentials", Value: "y"})
	bodyMatch := insertEntry(t, v, ident, Entry{Title: "Unrelated", Value: "the aws secret key is here"})
	noMatch := insertEntry(t, v, ident, Entry{Title: "Nothing", Value: "irrelevant"})

	results, err := s.Search("", "aws")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	byID := map[uuid.UUID]SearchResult{}
	for _, r := range results {
		byID[r.ID] = r
	}
	for _, id := range []uuid.UUID{titleMatch, descMatch, bodyMatch} {
		if _, ok := byID[id]; !ok {
			t.Errorf("Search(%q) is missing %s", "aws", id)
		}
	}
	if _, ok := byID[noMatch]; ok {
		t.Error("Search matched an unrelated entry")
	}
	if byID[titleMatch].MatchedBody {
		t.Error("title match incorrectly flagged as a body match")
	}
	if byID[descMatch].MatchedBody {
		t.Error("description match incorrectly flagged as a body match")
	}
	if !byID[bodyMatch].MatchedBody {
		t.Error("body match not flagged as MatchedBody")
	}
	for id, r := range byID {
		if strings.Contains(r.Title+r.Description, "the aws secret key is here") {
			t.Errorf("search result for %s leaks the matched value: %+v", id, r)
		}
	}
}

// TestListAgreesBetweenOneShotAndSession is the same two-implementations
// invariant for `ls`: Vault.List decrypts the vault fresh, Session.List
// serves the index, and cmd/gage renders whichever it gets through one
// function — so the rows themselves have to match field for field,
// including the id tie-break that orders entries sharing a title.
func TestListAgreesBetweenOneShotAndSession(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}

	insertEntry(t, v, ident, Entry{Title: "AWS root account", Description: "prod", Value: "x"})
	insertEntry(t, v, ident, Entry{Title: "a", Value: "y"})
	insertEntry(t, v, ident, Entry{Title: "Caf\u00e9", Value: "z"})
	// Two entries sharing a title: only the id tie-break orders these.
	dup := sampleEntry(time.Now())
	dup.Title = "a"
	if _, err := v.Insert(dup, true, ident); err != nil {
		t.Fatalf("inserting a duplicate title: %v", err)
	}

	oneShot, err := v.List(ident)
	if err != nil {
		t.Fatalf("Vault.List: %v", err)
	}
	inSession, err := s.List("")
	if err != nil {
		t.Fatalf("Session.List: %v", err)
	}
	if !reflect.DeepEqual(oneShot, inSession) {
		t.Errorf("List differs between modes:\none-shot: %+v\nsession:  %+v", oneShot, inSession)
	}
	if len(oneShot) != 4 {
		t.Errorf("List returned %d rows, want 4", len(oneShot))
	}
	// The dates and updated_by ls prints have to survive the round trip
	// through the index, not just the titles.
	for _, r := range inSession {
		if r.Updated.IsZero() || r.Created.IsZero() {
			t.Errorf("row %+v came back with a zero timestamp", r)
		}
		if r.UpdatedBy == "" {
			t.Errorf("row %+v came back with no updated_by", r)
		}
	}
}

// TestSearchAgreesBetweenOneShotAndSession is M6's "the same thing
// happens in both modes" invariant applied to the one command M7 splits
// across two implementations: one-shot decrypts everything and matches
// it, a session matches title/description from the index and only the
// body fresh. Those are two code paths, so nothing but a test comparing
// them keeps them answering identically.
//
// The overlap case is the one that actually caught a bug: an entry whose
// title *and* value both match used to come back MatchedBody=true in a
// session and MatchedBody=false one-shot, because the session's body
// pass overwrote the index's own classification instead of deferring to
// it.
func TestSearchAgreesBetweenOneShotAndSession(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}

	insertEntry(t, v, ident, Entry{Title: "AWS root account", Value: "x"})
	insertEntry(t, v, ident, Entry{Title: "Cloud creds", Description: "shared aws credentials", Value: "y"})
	insertEntry(t, v, ident, Entry{Title: "Unrelated", Value: "the aws secret key is here"})
	// Matches on both title and body — the classification overlap.
	insertEntry(t, v, ident, Entry{Title: "AWS backup", Value: "aws-secret-value"})
	// Matches on both description and body.
	insertEntry(t, v, ident, Entry{Title: "Ops", Description: "aws notes", Value: "aws again"})
	insertEntry(t, v, ident, Entry{Title: "Nothing", Value: "irrelevant"})

	for _, query := range []string{"aws", "AWS", "root", "nothing", "no-such-match"} {
		t.Run(query, func(t *testing.T) {
			oneShot, err := v.Search(query, ident)
			if err != nil {
				t.Fatalf("one-shot Search: %v", err)
			}
			inSession, err := s.Search("", query)
			if err != nil {
				t.Fatalf("session Search: %v", err)
			}
			if !reflect.DeepEqual(oneShot, inSession) {
				t.Errorf("Search(%q) differs between modes:\none-shot: %+v\nsession:  %+v", query, oneShot, inSession)
			}
		})
	}
}

// TestSearchFirstCallBuildsIndexWithoutADoubleDecrypt is the M7 plan's
// "a first search builds the index from that same pass rather than
// decrypting twice."
func TestSearchFirstCallBuildsIndexWithoutADoubleDecrypt(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS")
	insertTitled(t, v, ident, "GitHub")

	var decrypts int
	v.onDecrypt = func() { decrypts++ }

	if _, err := s.Search("", "aws"); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if decrypts != 2 {
		t.Fatalf("first Search decrypted %d times, want exactly 2 (one pass, shared between the index build and the body check)", decrypts)
	}
}

// TestSearchBodyHalfRedecryptsButTitleHalfDoesNot is the M7 plan's "a
// body-text search doesn't cache what it decrypted: a second identical
// search re-decrypts, while a title-only query in between still hits the
// index."
func TestSearchBodyHalfRedecryptsButTitleHalfDoesNot(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS")
	insertTitled(t, v, ident, "GitHub")

	var decrypts int
	v.onDecrypt = func() { decrypts++ }

	if _, err := s.Search("", "aws"); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	if decrypts != 2 {
		t.Fatalf("first Search: got %d decrypts, want 2", decrypts)
	}

	if _, err := s.List(""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if decrypts != 2 {
		t.Errorf("a title-only List after Search re-decrypted: got %d, want still 2", decrypts)
	}

	if _, err := s.Search("", "aws"); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if decrypts != 4 {
		t.Errorf("second Search: got %d decrypts total, want 4 (2 more for a fresh body pass)", decrypts)
	}
}

// TestIndexArenaNeverHoldsASecretValue is the memory-exposure property
// the M7 plan's "Decisions made" is built around: the index caches
// title/description only, never value or fields.
func TestIndexArenaNeverHoldsASecretValue(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}

	const sentinel = "sentinel-value-should-never-be-cached-93f7"
	insertEntry(t, v, ident, Entry{
		Title:       "Sentinel Title",
		Description: "Sentinel Description",
		Value:       sentinel,
		Fields:      map[string]string{"k": sentinel + "-field"},
	})

	if _, err := s.List(""); err != nil {
		t.Fatalf("List: %v", err)
	}
	// show, too: Resolve is the one index path that decrypts a whole
	// entry — value and fields included — to return it, so it's the most
	// plausible way for a secret to end up cached by accident.
	if _, _, err := s.Resolve("", "Sentinel Title"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := s.Search("", "sentinel"); err != nil {
		t.Fatalf("Search: %v", err)
	}

	held := s.vaults["personal"]
	if held == nil || held.index == nil || held.index.arena == nil {
		t.Fatal("expected a built index with an allocated arena")
	}
	arena := string(held.index.arena)
	if strings.Contains(arena, sentinel) {
		t.Error("the index arena contains the secret value")
	}
	if !strings.Contains(arena, "Sentinel Title") {
		t.Error("sanity check failed: the index arena doesn't even contain the title, so this test isn't exercising anything")
	}
}

// sometimesFailsLocker fails Lock from the (failAfter+1)th call onward —
// a controllable stand-in for a page-lock that starts succeeding and
// later runs into a limit (or one that never worked at all, with
// failAfter 0), without depending on the real machine's ulimit -l.
type sometimesFailsLocker struct {
	failAfter int
	calls     int
}

func (l *sometimesFailsLocker) Lock(b []byte) error {
	l.calls++
	if l.calls > l.failAfter {
		return errors.New("sometimesFailsLocker: forced failure")
	}
	return nil
}

func (l *sometimesFailsLocker) Unlock(b []byte) error { return nil }

// TestIndexArenaLocksThroughTheSameLockerAsTheIdentity: the index's
// backing arena goes through the vault's own Locker seam, not a second,
// independent one — a test can prove this by injecting the same fake M2's
// vault tests already use and watching its Lock count grow again when the
// index is built.
func TestIndexArenaLocksThroughTheSameLockerAsTheIdentity(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingLocker{inner: alwaysLocks{}}
	v.locker = counter

	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS")

	before := counter.locks
	if _, err := s.List(""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if counter.locks <= before {
		t.Errorf("building the index called Lock %d times (was %d before), want at least one more via the vault's own locker", counter.locks, before)
	}
}

// TestIndexPageLockFailureWarnsOnceAndProceeds: the identity's own key
// locks fine, but the (larger) index arena's lock fails — a real,
// distinct failure mode from the identity's, and it must warn exactly
// once no matter how many times the index is subsequently read.
func TestIndexPageLockFailureWarnsOnceAndProceeds(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	// Call #1 (the identity's own key) succeeds; call #2+ (the index
	// arena) fails.
	v.locker = &sometimesFailsLocker{failAfter: 1}

	s, p := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	if !ident.PageLocked() {
		t.Fatal("expected the identity's own key to be page-locked")
	}
	if len(p.warnings) != 0 {
		t.Fatalf("unlock warned %d times, want 0 (its own lock succeeded)", len(p.warnings))
	}

	insertTitled(t, v, ident, "AWS")

	if _, err := s.List(""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(p.warnings) != 1 {
		t.Fatalf("building the index warned %d times, want exactly 1: %v", len(p.warnings), p.warnings)
	}

	if _, err := s.List(""); err != nil {
		t.Fatalf("second List: %v", err)
	}
	if len(p.warnings) != 1 {
		t.Errorf("a second List warned again: %d total, want still 1", len(p.warnings))
	}
}

// TestIndexDoesNotWarnAgainWhenUnlockAlreadyWarned: when the identity's
// own key failed to page-lock (and already warned once about it), the
// index must not try — and must not warn a second time about the same
// underlying constraint.
func TestIndexDoesNotWarnAgainWhenUnlockAlreadyWarned(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	v.locker = &sometimesFailsLocker{failAfter: 0} // every Lock call fails, including the identity's own

	s, p := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	if ident.PageLocked() {
		t.Fatal("expected the identity's own key to have failed to page-lock")
	}
	if len(p.warnings) != 1 {
		t.Fatalf("unlock warned %d times, want exactly 1", len(p.warnings))
	}

	insertTitled(t, v, ident, "AWS")
	if _, err := s.List(""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(p.warnings) != 1 {
		t.Errorf("building the index warned again even though unlock already did: %d total, want still 1: %v", len(p.warnings), p.warnings)
	}
}

// TestIndexArenaGrowthPreservesEveryEntryAndItsPageLocks exercises the
// one part of Index that a handful of short-titled entries never
// reaches: outgrowing the initial arena. Growth allocates a new arena,
// copies the live bytes across, locks the new one, and unlocks/zeroes
// the old — pointer-swapping under page locks, which is exactly where a
// silent corruption or a leaked lock would hide.
//
// The entries here are sized so several doublings are guaranteed
// (indexArenaMinSize is the starting point), and the counting locker
// pins the lock bookkeeping: every arena after the first replaces one
// that must have been released, so unlocks always trail locks by exactly
// one until discard squares them up.
func TestIndexArenaGrowthPreservesEveryEntryAndItsPageLocks(t *testing.T) {
	counter := &countingLocker{inner: alwaysLocks{}}
	idx := newIndex(counter, true)

	const entries = 400
	want := map[uuid.UUID]ListEntry{}
	for i := range entries {
		id := uuid.MustParse(fmt.Sprintf("%08x-0000-4000-8000-000000000000", i))
		e := Entry{
			Title:       fmt.Sprintf("title-%03d-%s", i, strings.Repeat("t", 20)),
			Description: fmt.Sprintf("desc-%03d-%s", i, strings.Repeat("d", 30)),
			UpdatedBy:   "laptop-1",
		}
		idx.put(id, e)
		want[id] = ListEntry{ID: id, Title: e.Title, Description: e.Description, UpdatedBy: e.UpdatedBy}
	}

	if idx.used <= indexArenaMinSize {
		t.Fatalf("stored only %d bytes, which never outgrows the %d-byte starting arena — this test isn't exercising growth",
			idx.used, indexArenaMinSize)
	}
	if counter.locks < 2 {
		t.Fatalf("the arena was locked %d time(s), so it never grew", counter.locks)
	}
	if counter.unlocks != counter.locks-1 {
		t.Errorf("locks=%d unlocks=%d: every arena but the current one must have been released",
			counter.locks, counter.unlocks)
	}

	got := idx.list()
	if len(got) != entries {
		t.Fatalf("list returned %d rows, want %d", len(got), entries)
	}
	for _, row := range got {
		w := want[row.ID]
		if row.Title != w.Title || row.Description != w.Description {
			t.Fatalf("entry %s survived growth as %q/%q, want %q/%q",
				row.ID, row.Title, row.Description, w.Title, w.Description)
		}
	}

	// Resolution reads the same spans through a different path, so it
	// would catch an offset that only list() happens to get right.
	id, err := resolveTitleAndUUID(want[uuid.MustParse("00000063-0000-4000-8000-000000000000")].Title, idx.titles())
	if err != nil {
		t.Fatalf("resolving a post-growth title: %v", err)
	}
	if id.String() != "00000063-0000-4000-8000-000000000000" {
		t.Errorf("resolved %s, want the entry that owns that title", id)
	}

	live := idx.arena
	idx.discard()
	if counter.unlocks != counter.locks {
		t.Errorf("after discard, locks=%d unlocks=%d — the live arena's lock leaked", counter.locks, counter.unlocks)
	}
	for i, b := range live {
		if b != 0 {
			t.Fatalf("arena byte %d is %#x after discard, want zeroed", i, b)
		}
	}
}

// TestIdleTimeoutDiscardsIndexLikeAnExplicitLock: the M7 test list names
// both triggers ("explicit `lock` or idle timeout"). They share
// lockHeld today, but that's a fact about the current implementation
// rather than one the explicit-lock test can assert — this is what
// stops a future refactor from expiring a key while leaving its
// decrypted metadata behind.
func TestIdleTimeoutDiscardsIndexLikeAnExplicitLock(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	s, _ := newTestSession(t, open, SessionConfig{
		Current:     "personal",
		IdleTimeout: time.Minute,
		Now:         clock.now,
	})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS")

	var decrypts int
	v.onDecrypt = func() { decrypts++ }
	if _, err := s.List(""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if decrypts != 1 {
		t.Fatalf("got %d decrypts building the index, want 1", decrypts)
	}
	if s.vaults["personal"].index == nil {
		t.Fatal("no index after List")
	}

	// Age the vault out. Status is enough to trigger the check — the
	// timeout is evaluated at the start of every Session call.
	clock.advance(2 * time.Minute)
	if s.Status()[0].Unlocked {
		t.Fatal("vault still reports unlocked after the idle timeout")
	}
	if s.vaults["personal"].index != nil {
		t.Error("the idle timeout dropped the key but left the decrypted metadata index behind")
	}

	// And the next command rebuilds from scratch rather than serving
	// rows from a cache that outlived its key.
	if _, err := s.List(""); err != nil {
		t.Fatalf("List after the timeout: %v", err)
	}
	if decrypts != 2 {
		t.Errorf("total decrypts = %d, want 2 (the index was rebuilt after the timeout)", decrypts)
	}
}

// TestIndexDiscardZeroesArenaAndIsIdempotent is Index.discard's contract
// in isolation, the same shape as Identity.Close's own tests.
func TestIndexDiscardZeroesArenaAndIsIdempotent(t *testing.T) {
	counter := &countingLocker{inner: alwaysLocks{}}
	idx := newIndex(counter, true)
	idx.put(uuid.New(), Entry{Title: "hello", Description: "world"})

	if idx.arena == nil {
		t.Fatal("expected an allocated arena")
	}
	if !idx.locked {
		t.Fatal("expected the arena to be locked")
	}
	orig := idx.arena
	if !bytes.Contains(orig, []byte("hello")) {
		t.Fatal("sanity check failed: arena doesn't contain the stored title")
	}

	idx.discard()
	if idx.arena != nil {
		t.Error("discard left the arena slice non-nil")
	}
	for i, b := range orig {
		if b != 0 {
			t.Fatalf("arena byte %d is %#x after discard, want zeroed", i, b)
		}
	}

	// Idempotent: a second discard must not panic or unlock an
	// already-released page.
	idx.discard()
	if counter.unlocks != 1 {
		t.Errorf("discard called Unlock %d times across two calls, want exactly 1", counter.unlocks)
	}
}

// TestIndexNeverWrittenToDisk: building and using the index — ls,
// search, and reindex — must not create any file under $GAGE_STATE,
// $GAGE_DATA, or the vault itself.
func TestIndexNeverWrittenToDisk(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	v, err := open("personal")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})
	_, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS")
	insertTitled(t, v, ident, "GitHub")

	roots := []string{filepath.Dir(v.Path), filepath.Dir(os.Getenv("XDG_CONFIG_HOME"))}
	before := snapshotFiles(t, roots)

	if _, err := s.List(""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := s.Search("", "aws"); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if err := s.Reindex(""); err != nil {
		t.Fatalf("Reindex: %v", err)
	}

	after := snapshotFiles(t, roots)
	if len(before) != len(after) {
		t.Fatalf("file count under $GAGE_STATE/$GAGE_DATA/the vault changed from %d to %d building/using the index:\nbefore: %v\nafter:  %v",
			len(before), len(after), before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("file set changed building/using the index:\nbefore: %v\nafter:  %v", before, after)
		}
	}
}

// snapshotFiles lists every regular file under roots, sorted.
func snapshotFiles(t *testing.T, roots []string) []string {
	t.Helper()
	var files []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(files)
	return files
}
