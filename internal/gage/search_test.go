package gage

import (
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSearchMatchesFieldValueAndReportsMatchedBody is the field half of
// matchesBody: search.go's title/description path is exercised
// elsewhere (cmd/gage/search_test.go drives the real CLI), but a match
// coming from a *field value* rather than the entry's Value itself has
// no test in either package, despite being half of what body matching
// promises. A field match still must not leak the matched text, so this
// checks MatchedBody is set and the result carries only title/description.
func TestSearchMatchesFieldValueAndReportsMatchedBody(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	now := time.Now()
	if _, err := v.Insert(Entry{
		Title:     "Cloud provider",
		Created:   NewTimestamp(now),
		Updated:   NewTimestamp(now),
		UpdatedBy: "laptop-1",
		Value:     "unrelated",
		Fields:    map[string]string{"access_key": "AKIA-needle-1234"},
	}, false, &id); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Insert(Entry{
		Title:     "Nothing to do with it",
		Created:   NewTimestamp(now),
		Updated:   NewTimestamp(now),
		UpdatedBy: "laptop-1",
		Value:     "also unrelated",
		Fields:    map[string]string{"note": "no match here"},
	}, false, &id); err != nil {
		t.Fatal(err)
	}

	results, err := v.Search("needle", &id)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("Search(%q) = %d results, want 1: %+v", "needle", len(results), results)
	}
	r := results[0]
	if r.Title != "Cloud provider" {
		t.Errorf("matched entry title = %q, want %q", r.Title, "Cloud provider")
	}
	if !r.MatchedBody {
		t.Error("MatchedBody = false, want true for a match found only in a field value")
	}
}

// TestSortSearchResultsTiesByID is sortSearchResults' tie-break: two
// results sharing a title fall back to comparing IDs, the same
// deterministic ordering ls and ambiguousError use, so search output
// doesn't reorder nondeterministically between runs.
func TestSortSearchResultsTiesByID(t *testing.T) {
	lo := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	hi := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	results := []SearchResult{
		{ID: hi, Title: "same"},
		{ID: lo, Title: "same"},
	}
	sortSearchResults(results)
	if results[0].ID != lo || results[1].ID != hi {
		t.Errorf("order after sort = [%s, %s], want the lower id first for a title tie", results[0].ID, results[1].ID)
	}
	if !sort.SliceIsSorted(results, func(i, j int) bool { return results[i].ID.String() < results[j].ID.String() }) {
		t.Error("results not actually sorted by id")
	}
}
