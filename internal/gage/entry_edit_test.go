package gage

import (
	"errors"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

func TestUpdateRewritesEntryAndCommits(t *testing.T) {
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

	e.Value = "rotated-secret"
	if err := v.Update(entryID, e, &id); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d (exactly one new commit)", after, before+1)
	}

	got, err := v.ReadEntry(entryID, &id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "rotated-secret" {
		t.Errorf("Value = %q, want %q", got.Value, "rotated-secret")
	}
}

func TestRenameChangesOnlyTitleAndBumpsUpdated(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	old := time.Now().Add(-24 * time.Hour)
	e := sampleEntry(old)
	entryID, err := v.Insert(e, false, &id)
	if err != nil {
		t.Fatal(err)
	}

	gotID, err := v.Rename(e.Title, "New Title", false, &id)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if gotID != entryID {
		t.Errorf("Rename returned id %s, want %s", gotID, entryID)
	}

	got, err := v.ReadEntry(entryID, &id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "New Title" {
		t.Errorf("Title = %q, want %q", got.Title, "New Title")
	}
	if got.Value != e.Value {
		t.Errorf("Value changed: got %q, want unchanged %q", got.Value, e.Value)
	}
	if len(got.Fields) != len(e.Fields) {
		t.Errorf("Fields changed: got %v, want unchanged %v", got.Fields, e.Fields)
	}
	if !got.Updated.After(NewTimestamp(old).Time) {
		t.Errorf("Updated = %v, want it bumped past the original %v", got.Updated, old)
	}
	if got.Created != NewTimestamp(old) {
		t.Errorf("Created changed: got %v, want unchanged %v", got.Created, NewTimestamp(old))
	}
}

func TestRenameProducesExactlyOneCommit(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	if _, err := v.Insert(e, false, &id); err != nil {
		t.Fatal(err)
	}
	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := v.Rename(e.Title, "New Title", false, &id); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d", after, before+1)
	}
}

func TestRenameToExistingTitleRejectedUnlessForced(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	a := sampleEntry(time.Now())
	a.Title = "Entry A"
	b := sampleEntry(time.Now())
	b.Title = "Entry B"
	if _, err := v.Insert(a, false, &id); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Insert(b, false, &id); err != nil {
		t.Fatal(err)
	}

	_, err := v.Rename("Entry B", "Entry A", false, &id)
	if !errors.Is(err, ErrDuplicateTitle) {
		t.Errorf("error = %v, want it to wrap ErrDuplicateTitle", err)
	}
	if exitcode.CodeOf(err) != exitcode.Conflict {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(err), exitcode.Conflict)
	}

	// Unrenamed: Entry B is still resolvable by its old title.
	_, gotB, err := v.Resolve("Entry B", &id)
	if err != nil {
		t.Fatalf("Entry B should be unchanged after the rejected rename: %v", err)
	}
	if gotB.Title != "Entry B" {
		t.Errorf("Entry B's title = %q, want unchanged %q", gotB.Title, "Entry B")
	}

	if _, err := v.Rename("Entry B", "Entry A", true, &id); err != nil {
		t.Fatalf("forced rename to a duplicate title: %v", err)
	}
	_, _, err = v.Resolve("Entry A", &id)
	if !errors.Is(err, ErrAmbiguousQuery) {
		t.Errorf("resolving the now-duplicated title: error = %v, want ErrAmbiguousQuery", err)
	}
}

func TestRenameToOwnTitleAlwaysAllowed(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	if _, err := v.Insert(e, false, &id); err != nil {
		t.Fatal(err)
	}

	if _, err := v.Rename(e.Title, e.Title, false, &id); err != nil {
		t.Errorf("renaming to the entry's own current title should never conflict: %v", err)
	}
}

func TestRenameUnknownQueryFails(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = v.Rename("no such entry", "New Title", false, &id)
	if !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("error = %v, want it to wrap ErrEntryNotFound", err)
	}

	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("commit count changed (%d -> %d) on a Rename that found nothing", before, after)
	}
}
