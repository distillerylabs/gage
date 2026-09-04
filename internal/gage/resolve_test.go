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

func TestResolveByExactUUID(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID := insertNamed(t, v, &id, "ProtonMail")

	gotID, gotEntry, err := v.Resolve(entryID.String(), &id)
	if err != nil {
		t.Fatalf("Resolve by exact UUID: %v", err)
	}
	if gotID != entryID {
		t.Errorf("resolved id = %s, want %s", gotID, entryID)
	}
	if gotEntry.Title != "ProtonMail" {
		t.Errorf("resolved title = %q, want %q", gotEntry.Title, "ProtonMail")
	}
}

// TestResolveByNonCanonicalUUIDSpellings is what gives the exact-UUID
// stage teeth. For a canonical id the stage is invisible: the substring
// stage right after it would match the same entry anyway, since a
// 32-hex-character query can only be "contained in" the identical
// 32-hex-character id. The spellings below are the ones that actually
// separate the two — uuid.Parse accepts braced and urn:uuid: forms,
// while the substring stage compares raw hex and cannot match either
// (the braces and the "urn:uuid:" literal aren't hex).
//
// Without this test, deleting the exact-UUID stage outright leaves every
// other test green while silently dropping support for both spellings.
func TestResolveByNonCanonicalUUIDSpellings(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID := insertNamed(t, v, &id, "ProtonMail")
	canonical := entryID.String()

	for _, spelling := range []struct{ name, query string }{
		{"braced", "{" + canonical + "}"},
		{"urn", "urn:uuid:" + canonical},
	} {
		t.Run(spelling.name, func(t *testing.T) {
			gotID, gotEntry, err := v.Resolve(spelling.query, &id)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", spelling.query, err)
			}
			if gotID != entryID {
				t.Errorf("resolved id = %s, want %s", gotID, entryID)
			}
			if gotEntry.Title != "ProtonMail" {
				t.Errorf("resolved title = %q, want %q", gotEntry.Title, "ProtonMail")
			}
		})
	}
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

// TestResolveByUUIDMidStringSubstring covers the fourth stage's actual
// scope: a substring, not merely a prefix, of a UUID's canonical string
// form. Uses an explicit id rather than a randomly-generated one so the
// substring under test is guaranteed to exist at a known, non-prefix
// offset.
func TestResolveByUUIDMidStringSubstring(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID := uuid.MustParse("11111111-2222-4333-8444-deadbeef0000")
	e := sampleEntry(time.Now())
	e.Title = "ProtonMail"
	if err := v.WriteEntry(entryID, e); err != nil {
		t.Fatal(err)
	}

	// "deadbeef" sits in the middle of the id, not at the start — a
	// prefix-only matcher would never find this.
	gotID, gotEntry, err := v.Resolve("deadbeef", &id)
	if err != nil {
		t.Fatalf("Resolve by mid-string UUID substring: %v", err)
	}
	if gotID != entryID {
		t.Errorf("resolved id = %s, want %s", gotID, entryID)
	}
	if gotEntry.Title != "ProtonMail" {
		t.Errorf("resolved title = %q, want %q", gotEntry.Title, "ProtonMail")
	}
}

// TestResolveByUUIDSubstringSpanningAHyphen: the substring stage strips
// hyphens from both the query and the id before comparing, so a query
// that spans one of the id's group boundaries still matches.
func TestResolveByUUIDSubstringSpanningAHyphen(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	// Canonical form: 11112222-3333-4444-8555-666677778888. The
	// characters "22223333" span the first hyphen.
	entryID := uuid.MustParse("11112222-3333-4444-8555-666677778888")
	e := sampleEntry(time.Now())
	e.Title = "ProtonMail"
	if err := v.WriteEntry(entryID, e); err != nil {
		t.Fatal(err)
	}

	gotID, _, err := v.Resolve("22223333", &id)
	if err != nil {
		t.Fatalf("Resolve by hyphen-spanning UUID substring: %v", err)
	}
	if gotID != entryID {
		t.Errorf("resolved id = %s, want %s", gotID, entryID)
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

// TestResolveAmbiguousUUIDSubstringListsCandidates is the fourth stage's
// own ambiguity case: two entries whose ids both contain the query as a
// substring, and neither entry's title matches at all — so title-stage
// results are guaranteed empty and the UUID substring stage is what
// actually produces (and must report) the ambiguity.
func TestResolveAmbiguousUUIDSubstringListsCandidates(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	id1 := uuid.MustParse("aaaaaaaa-1111-4111-8111-111111111111")
	id2 := uuid.MustParse("aaaaaaaa-2222-4222-8222-222222222222")
	for _, wid := range []uuid.UUID{id1, id2} {
		e := sampleEntry(time.Now())
		e.Title = "Something Else Entirely"
		if err := v.WriteEntry(wid, e); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := v.Resolve("aaaaaaaa", &id)
	if !errors.Is(err, ErrAmbiguousQuery) {
		t.Fatalf("error = %v, want it to wrap ErrAmbiguousQuery", err)
	}
	var amb *AmbiguousQueryError
	if !errors.As(err, &amb) {
		t.Fatalf("error = %v, want it to carry an *AmbiguousQueryError", err)
	}
	seen := map[string]bool{}
	for _, c := range amb.List.Candidates {
		seen[c.ID] = true
	}
	if !seen[id1.String()] || !seen[id2.String()] {
		t.Errorf("candidates = %+v, want both %s and %s", amb.List.Candidates, id1, id2)
	}
}

// TestResolveExactTitleWinsOverUUIDMatch is the regression test for the
// bug this milestone's original implementation shipped, caught only
// after it reached CI: with UUID matching tried *before* title matching,
// a query that was the exact, literal title of one entry could silently
// resolve to a *different* entry instead, whenever the query happened to
// also be a unique UUID substring elsewhere in the vault — no error, no
// ambiguity, just the wrong secret.
//
// "22" as a title next to some unrelated entry whose id happens to
// contain "22" was enough to trigger it in practice; this test pins that
// exact shape with explicit (non-random) ids so the property is checked
// on every run rather than roughly one run in several.
func TestResolveExactTitleWinsOverUUIDMatch(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	// Titled "Website Password"; its id contains "22" — the string that
	// is also, coincidentally, another entry's exact title.
	idWebsite := uuid.MustParse("22ed1bfd-4b77-4bba-90be-ff40de0eab87")
	eWebsite := sampleEntry(time.Now())
	eWebsite.Title, eWebsite.Value = "Website Password", "correcthorsebattery"
	if err := v.WriteEntry(idWebsite, eWebsite); err != nil {
		t.Fatal(err)
	}

	// Titled exactly "22", with an id that shares no substring with the
	// query beyond what its own title already guarantees.
	idTwentyTwo := uuid.MustParse("5ebca883-6a5f-4e96-acab-766f2fceb2d4")
	eTwentyTwo := sampleEntry(time.Now())
	eTwentyTwo.Title, eTwentyTwo.Value = "22", "some-other-secret"
	if err := v.WriteEntry(idTwentyTwo, eTwentyTwo); err != nil {
		t.Fatal(err)
	}

	gotID, got, err := v.Resolve("22", &id)
	if err != nil {
		t.Fatalf(`Resolve("22"): %v`, err)
	}
	if gotID != idTwentyTwo {
		t.Errorf(`Resolve("22") = %s (title %q, value %q), want the exact-title match %s (title %q) — `+
			`title stages must run, and stop, before UUID matching is ever attempted`,
			gotID, got.Title, got.Value, idTwentyTwo, eTwentyTwo.Title)
	}
	if got.Title != "22" {
		t.Errorf(`Resolve("22") resolved to title %q, want %q`, got.Title, "22")
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
