package gage

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// insertNamed is resolve_test.go's own small helper: sampleEntry with a
// different title, inserted with force so a test can build a vault with
// several distinctly-titled entries without repeating the boilerplate.
func insertNamed(t *testing.T, v *Vault, id *Identity, title string) uuid.UUID {
	t.Helper()
	e := sampleEntry(time.Now())
	e.Title = title
	entryID, err := v.Insert(e, true, id)
	if err != nil {
		t.Fatalf("inserting %q: %v", title, err)
	}
	return entryID
}

func TestResolveByUUIDPrefix(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID := insertNamed(t, v, &id, "ProtonMail")

	full := entryID.String()
	prefix := full[:8]

	gotID, gotEntry, err := v.Resolve(prefix, &id)
	if err != nil {
		t.Fatalf("Resolve by UUID prefix: %v", err)
	}
	if gotID.String() != full {
		t.Errorf("resolved id = %s, want %s", gotID, full)
	}
	if gotEntry.Title != "ProtonMail" {
		t.Errorf("resolved title = %q, want %q", gotEntry.Title, "ProtonMail")
	}
}

func TestResolveExactTitleWinsOverSubstringOfAnotherTitle(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	// "AWS" is an exact match for one entry and also a substring of the
	// other's title — exact must win, not be reported as ambiguous with
	// the substring match.
	awsID := insertNamed(t, v, &id, "AWS")
	insertNamed(t, v, &id, "AWS root account")

	gotID, gotEntry, err := v.Resolve("AWS", &id)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotID.String() != awsID.String() {
		t.Errorf("resolved id = %s, want the exact-title match %s", gotID, awsID)
	}
	if gotEntry.Title != "AWS" {
		t.Errorf("resolved title = %q, want %q", gotEntry.Title, "AWS")
	}
}

func TestResolveUniqueSubstringMatch(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	wantID := insertNamed(t, v, &id, "AWS root account")
	insertNamed(t, v, &id, "GitHub")

	gotID, gotEntry, err := v.Resolve("root", &id)
	if err != nil {
		t.Fatalf("Resolve by substring: %v", err)
	}
	if gotID.String() != wantID.String() {
		t.Errorf("resolved id = %s, want %s", gotID, wantID)
	}
	if gotEntry.Title != "AWS root account" {
		t.Errorf("resolved title = %q, want %q", gotEntry.Title, "AWS root account")
	}
}

func TestResolveSubstringMatchIsCaseInsensitive(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	wantID := insertNamed(t, v, &id, "ProtonMail")

	gotID, _, err := v.Resolve("protonmail", &id)
	if err != nil {
		t.Fatalf("Resolve with a lowercase query against a mixed-case title: %v", err)
	}
	if gotID.String() != wantID.String() {
		t.Errorf("resolved id = %s, want %s", gotID, wantID)
	}
}

func TestResolveAmbiguousSubstringListsCandidatesAsAValue(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	id1 := insertNamed(t, v, &id, "AWS root account")
	id2 := insertNamed(t, v, &id, "AWS IAM backup admin")

	_, _, err := v.Resolve("aws", &id)
	if !errors.Is(err, ErrAmbiguousQuery) {
		t.Fatalf("error = %v, want it to wrap ErrAmbiguousQuery", err)
	}
	if exitcode.CodeOf(err) != exitcode.Ambiguous {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(err), exitcode.Ambiguous)
	}

	var amb *AmbiguousQueryError
	if !errors.As(err, &amb) {
		t.Fatalf("error = %v, want it to carry an *AmbiguousQueryError with the candidate list", err)
	}
	if amb.List.Query != "aws" {
		t.Errorf("List.Query = %q, want %q", amb.List.Query, "aws")
	}
	if len(amb.List.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2: %+v", len(amb.List.Candidates), amb.List.Candidates)
	}
	seen := map[string]bool{}
	for _, c := range amb.List.Candidates {
		seen[c.ID] = true
	}
	if !seen[id1.String()] || !seen[id2.String()] {
		t.Errorf("candidates = %+v, want both %s and %s", amb.List.Candidates, id1, id2)
	}
}

func TestResolveNotFoundAndAmbiguousAreDistinguishable(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	insertNamed(t, v, &id, "AWS root account")
	insertNamed(t, v, &id, "AWS IAM backup admin")

	_, _, notFoundErr := v.Resolve("no such thing at all", &id)
	if !errors.Is(notFoundErr, ErrEntryNotFound) {
		t.Errorf("error = %v, want it to wrap ErrEntryNotFound", notFoundErr)
	}
	if exitcode.CodeOf(notFoundErr) != exitcode.NotFound {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(notFoundErr), exitcode.NotFound)
	}

	_, _, ambiguousErr := v.Resolve("aws", &id)
	if !errors.Is(ambiguousErr, ErrAmbiguousQuery) {
		t.Errorf("error = %v, want it to wrap ErrAmbiguousQuery", ambiguousErr)
	}
	if exitcode.CodeOf(ambiguousErr) != exitcode.Ambiguous {
		t.Errorf("exit code = %v, want %v", exitcode.CodeOf(ambiguousErr), exitcode.Ambiguous)
	}

	if exitcode.CodeOf(notFoundErr) == exitcode.CodeOf(ambiguousErr) {
		t.Error("not-found and ambiguous produced the same exit code")
	}
	if errors.Is(notFoundErr, ErrAmbiguousQuery) || errors.Is(ambiguousErr, ErrEntryNotFound) {
		t.Error("not-found and ambiguous errors are cross-matching each other's sentinel")
	}
}
