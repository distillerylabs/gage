package gage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/vaultlock"
)

// TestInsertCommitsExactlyOnceWithUUIDMessageAndFixedAuthor is the M4 plan's
// confidentiality decision, checked end to end: one commit per insert, its
// message is exactly the new entry's UUID (no verb, no title), and its
// author is the fixed, anonymous "gage <gage@localhost>" identity rather
// than anything derived from whoever ran the command.
func TestInsertCommitsExactlyOnceWithUUIDMessageAndFixedAuthor(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	e := sampleEntry(time.Now())
	entryID, err := v.Insert(e, false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d (exactly one new commit)", after, before+1)
	}

	message, authorName, authorEmail, err := gitrepo.HeadCommit(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if message != entryID.String() {
		t.Errorf("commit message = %q, want exactly the entry's UUID %q", message, entryID.String())
	}
	if authorName != "gage" || authorEmail != "gage@localhost" {
		t.Errorf("commit author = %q <%s>, want \"gage\" <gage@localhost>", authorName, authorEmail)
	}
	if strings.Contains(message, e.Title) || strings.Contains(message, "insert") {
		t.Errorf("commit message leaks the entry title or an operation verb: %q", message)
	}
}

// TestInsertRoundTripsThroughReadEntry is Insert's own definition of
// done: the id it returns actually decrypts back to what was given it.
func TestInsertRoundTripsThroughReadEntry(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	entryID, err := v.Insert(e, false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := v.ReadEntry(entryID, &id)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if got.Title != e.Title || got.Value != e.Value {
		t.Errorf("read back = %+v, want %+v", got, e)
	}
}

// TestInsertDuplicateTitleRejectedWithoutForce covers the M4 test list's
// duplicate-title bullet: a second insert with the same title fails, and
// nothing is written for the rejected attempt.
func TestInsertDuplicateTitleRejectedWithoutForce(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	if _, err := v.Insert(e, false, &id); err != nil {
		t.Fatalf("first Insert: %v", err)
	}

	_, err := v.Insert(e, false, &id)
	if err == nil {
		t.Fatal("expected a duplicate-title Insert to fail")
	}
	if !errors.Is(err, ErrDuplicateTitle) {
		t.Errorf("error = %v, want it to wrap ErrDuplicateTitle", err)
	}
	if exitcode.CodeOf(err) != exitcode.Conflict {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(err), exitcode.Conflict)
	}

	ids, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Errorf("entries after a rejected duplicate = %d, want 1", len(ids))
	}
}

// TestInsertDuplicateTitleAllowedWithForce is the other half: force lets
// two entries share a title.
func TestInsertDuplicateTitleAllowedWithForce(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	if _, err := v.Insert(e, false, &id); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	if _, err := v.Insert(e, true, &id); err != nil {
		t.Fatalf("forced duplicate Insert: %v", err)
	}

	ids, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Errorf("entries after a forced duplicate = %d, want 2", len(ids))
	}
}

// TestResolveByUUID and TestResolveByExactTitle cover Resolve's two
// addressing modes.
func TestResolveByUUID(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	entryID, err := v.Insert(e, false, &id)
	if err != nil {
		t.Fatal(err)
	}

	gotID, gotEntry, err := v.Resolve(entryID.String(), &id)
	if err != nil {
		t.Fatalf("Resolve by UUID: %v", err)
	}
	if gotID != entryID {
		t.Errorf("resolved id = %s, want %s", gotID, entryID)
	}
	if gotEntry.Title != e.Title {
		t.Errorf("resolved title = %q, want %q", gotEntry.Title, e.Title)
	}
}

func TestResolveByExactTitle(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	entryID, err := v.Insert(e, false, &id)
	if err != nil {
		t.Fatal(err)
	}

	gotID, gotEntry, err := v.Resolve(e.Title, &id)
	if err != nil {
		t.Fatalf("Resolve by title: %v", err)
	}
	if gotID != entryID {
		t.Errorf("resolved id = %s, want %s", gotID, entryID)
	}
	if gotEntry.Value != e.Value {
		t.Errorf("resolved value = %q, want %q", gotEntry.Value, e.Value)
	}
}

// TestResolveUnknownQueryFails covers both addressing modes' not-found
// case: a UUID that parses but matches nothing, and a title that matches
// nothing.
func TestResolveUnknownQueryFails(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	for _, query := range []string{"no such title", uuid.New().String()} {
		t.Run(query, func(t *testing.T) {
			_, _, err := v.Resolve(query, &id)
			if !errors.Is(err, ErrEntryNotFound) {
				t.Errorf("error = %v, want it to wrap ErrEntryNotFound", err)
			}
			if exitcode.CodeOf(err) != exitcode.NotFound {
				t.Errorf("exit code = %v, want %v", exitcode.CodeOf(err), exitcode.NotFound)
			}
		})
	}
}

// TestResolveAmbiguousTitleFails: once a duplicate title has been forced
// into existence, resolving it by title has nobody to ask in one-shot
// mode and must fail with the Ambiguous exit code — see M0's taxonomy and
// the M4 plan's note that M5's shared resolver replaces this.
func TestResolveAmbiguousTitleFails(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	if _, err := v.Insert(e, false, &id); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Insert(e, true, &id); err != nil {
		t.Fatal(err)
	}

	_, _, err := v.Resolve(e.Title, &id)
	if !errors.Is(err, ErrAmbiguousQuery) {
		t.Errorf("error = %v, want it to wrap ErrAmbiguousQuery", err)
	}
	if exitcode.CodeOf(err) != exitcode.Ambiguous {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(err), exitcode.Ambiguous)
	}
}

// TestRemoveDeletesFileAndCommits covers Remove's whole contract: the
// file is gone, a commit records the deletion (message and author held
// to the same standard as Insert's), and the entry is unreadable
// afterward.
func TestRemoveDeletesFileAndCommits(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	entryID, err := v.Insert(e, false, &id)
	if err != nil {
		t.Fatal(err)
	}
	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	gotID, err := v.Remove(entryID.String(), &id)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if gotID != entryID {
		t.Errorf("Remove returned id %s, want %s", gotID, entryID)
	}

	if _, err := os.Stat(v.entryPath(entryID)); !os.IsNotExist(err) {
		t.Errorf("entry file still exists after Remove (stat err = %v)", err)
	}

	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d (exactly one new commit)", after, before+1)
	}

	message, authorName, authorEmail, err := gitrepo.HeadCommit(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if message != entryID.String() {
		t.Errorf("commit message = %q, want exactly the entry's UUID %q", message, entryID.String())
	}
	if authorName != "gage" || authorEmail != "gage@localhost" {
		t.Errorf("commit author = %q <%s>, want \"gage\" <gage@localhost>", authorName, authorEmail)
	}

	if _, err := v.ReadEntry(entryID, &id); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("ReadEntry after Remove: error = %v, want it to wrap ErrEntryNotFound", err)
	}
}

// TestRemoveUnknownQueryFails: rm on a title/UUID that doesn't resolve
// must fail without touching the working tree.
func TestRemoveUnknownQueryFails(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = v.Remove("no such entry", &id)
	if !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("error = %v, want it to wrap ErrEntryNotFound", err)
	}

	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("commit count changed (%d -> %d) on a Remove that found nothing", before, after)
	}
}

// TestWriteLockSerializesConcurrentInsertsAndLosesNoCommit drives two
// Inserts concurrently against the same vault (goroutines within one
// process, which vaultlock's own tests establish is a real test of
// flock/LockFileEx: each Acquire opens its own file description and
// contends exactly as two separate processes would). Both writes must
// still land — the vault lock's whole purpose is that a second writer
// waits rather than interleaving with or clobbering the first.
func TestWriteLockSerializesConcurrentInsertsAndLosesNoCommit(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	titles := []string{"Entry A", "Entry B"}
	var wg sync.WaitGroup
	errs := make(chan error, len(titles))
	for _, title := range titles {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			e := sampleEntry(time.Now())
			e.Title = title
			if _, err := v.Insert(e, true, &id); err != nil {
				errs <- err
			}
		}(title)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Insert failed: %v", err)
	}

	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+len(titles) {
		t.Errorf("commit count = %d, want %d — a concurrent write was lost or clobbered", after, before+len(titles))
	}

	ids, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, eid := range ids {
		e, err := v.ReadEntry(eid, &id)
		if err != nil {
			t.Fatal(err)
		}
		found[e.Title] = true
	}
	for _, title := range titles {
		if !found[title] {
			t.Errorf("entry %q is missing after concurrent inserts", title)
		}
	}
}

// TestInsertBlocksOnAConcurrentlyHeldWriteLock is the more direct half of
// the same property: with the vault's lock held externally, Insert must
// actually wait rather than proceeding, and must succeed the moment the
// lock is released.
func TestInsertBlocksOnAConcurrentlyHeldWriteLock(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	path, err := LockFilePath(v.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := vaultlock.Acquire(path, 0)
	if err != nil {
		t.Fatalf("acquiring the lock externally: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := v.Insert(sampleEntry(time.Now()), true, &id)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("Insert returned (err=%v) while the lock was still held externally; nothing was ever blocked", err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := held.Release(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Insert after the external lock released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Insert never returned after the external lock was released")
	}
}

// TestReadDoesNotBlockOnAHeldWriteLock: "reads never take it" — see
// "Concurrent processes and the vault lock" in the design doc. A read
// must proceed immediately even while a write holds the lock.
func TestReadDoesNotBlockOnAHeldWriteLock(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID := NewEntryID()
	if err := v.WriteEntry(entryID, sampleEntry(time.Now())); err != nil {
		t.Fatal(err)
	}

	path, err := LockFilePath(v.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := vaultlock.Acquire(path, 0)
	if err != nil {
		t.Fatalf("acquiring the lock externally: %v", err)
	}
	defer func() { _ = held.Release() }()

	done := make(chan error, 1)
	go func() {
		_, err := v.ReadEntry(entryID, &id)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReadEntry: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadEntry blocked on a write lock held by someone else; reads must never take the write lock")
	}
}
