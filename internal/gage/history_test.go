package gage

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestLogReportsOneRevisionPerWrite: an insert and two edits are three
// revisions, newest first, with nothing decrypted in them.
func TestLogReportsOneRevisionPerWrite(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	entryID, err := v.Insert(e, false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	for _, value := range []string{"second", "third"} {
		e.Value = value
		if err := v.Update(entryID, e, &id); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	log, err := v.Log(entryID)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log) != 3 {
		t.Fatalf("Log returned %d revisions, want 3 (insert + two edits)", len(log))
	}
	for i, le := range log {
		if le.Hash == "" {
			t.Errorf("revision %d has no commit hash", i)
		}
		if le.When.IsZero() {
			t.Errorf("revision %d has no timestamp", i)
		}
		if le.Deleted {
			t.Errorf("revision %d is marked deleted; none of these were", i)
		}
	}
	// Newest first.
	for i := 1; i < len(log); i++ {
		if log[i].When.After(log[i-1].When) {
			t.Errorf("Log is not newest-first: revision %d is newer than %d", i, i-1)
		}
	}
}

// TestLogMarksTheDeletingCommit: a removal is a commit too, and a log
// that stopped at the last edit would imply the entry is still there.
func TestLogMarksTheDeletingCommit(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID, err := v.Insert(sampleEntry(time.Now()), false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := v.Remove(entryID.String(), &id); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	log, err := v.Log(entryID)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("Log returned %d revisions, want 2 (insert + delete)", len(log))
	}
	if !log[0].Deleted {
		t.Error("the newest revision of a removed entry is not marked deleted")
	}
	if log[1].Deleted {
		t.Error("the insert is marked deleted")
	}
}

// TestLogOnAnEntryGitHasNeverSeenIsEmptyNotAnError: an id with no
// history is a normal state, not a failure.
func TestLogOnAnEntryGitHasNeverSeenIsEmptyNotAnError(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	log, err := v.Log(uuid.New())
	if err != nil {
		t.Fatalf("Log on an unknown id: %v", err)
	}
	if len(log) != 0 {
		t.Errorf("Log on an unknown id returned %d revisions, want none", len(log))
	}
}

// TestHistoryDecryptsEveryPastRevision is the dangerous tier's core:
// values already rotated away are recoverable from git, which is
// precisely why the command is named the way it is.
func TestHistoryDecryptsEveryPastRevision(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	e.Value = "first-password"
	entryID, err := v.Insert(e, false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	e.Value = "second-password"
	if err := v.Update(entryID, e, &id); err != nil {
		t.Fatalf("Update: %v", err)
	}

	revs, err := v.History(entryID, &id)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("History returned %d revisions, want 2", len(revs))
	}
	if revs[0].Entry.Value != "second-password" {
		t.Errorf("newest revision value = %q, want the current one", revs[0].Entry.Value)
	}
	if revs[1].Entry.Value != "first-password" {
		t.Errorf("older revision value = %q, want the superseded one", revs[1].Entry.Value)
	}
}

// TestHistoryOnADeletedEntryStillWalksIt: removing an entry doesn't
// remove its past, which is the other half of why history --decrypt is
// the tier it is.
func TestHistoryOnADeletedEntryStillWalksIt(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	e := sampleEntry(time.Now())
	e.Value = "gone-but-not-forgotten"
	entryID, err := v.Insert(e, false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := v.Remove(entryID.String(), &id); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	revs, err := v.History(entryID, &id)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("History returned %d revisions, want 2 (insert + delete)", len(revs))
	}
	if !revs[0].Deleted {
		t.Error("the deleting revision is not marked deleted")
	}
	if revs[1].Entry.Value != "gone-but-not-forgotten" {
		t.Errorf("the deleted entry's last value = %q, want it recovered from git", revs[1].Entry.Value)
	}
}

// TestHistoryFailsRatherThanSkippingAnUndecryptableRevision: quietly
// omitting a revision this identity can't open would turn "here is this
// secret's history" into "here is the part of it I could read", with no
// way for the reader to tell which they got.
func TestHistoryFailsRatherThanSkippingAnUndecryptableRevision(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID, err := v.Insert(sampleEntry(time.Now()), false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// A second vault's identity, which was never a recipient of the
	// first vault's entries.
	other, otherID := newEntryTestVault(t, "work", "laptop-1")
	defer func() { _ = otherID.Close() }()
	_ = other

	_, err = v.History(entryID, &otherID)
	if err == nil {
		t.Fatal("History with an identity that can't decrypt succeeded")
	}
	if !strings.Contains(err.Error(), entryID.String()) {
		t.Errorf("error %q does not say which entry failed", err)
	}
}
