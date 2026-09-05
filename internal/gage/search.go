package gage

import (
	"sort"
	"strings"

	"github.com/google/uuid"
)

// SearchResult is one `search`/`grep` match — enough to render a
// candidate-style line without ever carrying the matched secret value or
// field itself. See the M7 plan's "search output never includes a
// matched secret value unless explicitly asked for it."
type SearchResult struct {
	ID          uuid.UUID
	Title       string
	Description string
	// MatchedBody reports the match came from the entry's decrypted
	// value/fields rather than its title/description. It's metadata
	// about *where* the match was, never the matched text itself.
	MatchedBody bool
}

// matchesText reports whether lowerPattern is a case-insensitive
// substring of title or description — the half of search that's always
// index-served in a session (see Index.matchText) and freshly decrypted
// in one-shot mode (see Vault.Search).
func matchesText(title, description, lowerPattern string) bool {
	return strings.Contains(strings.ToLower(title), lowerPattern) ||
		strings.Contains(strings.ToLower(description), lowerPattern)
}

// matchesBody reports whether lowerPattern is a case-insensitive
// substring of e's value or any field value — the half of search that
// always decrypts fresh and is never cached in the index, so a matched
// secret's plaintext exists in memory no longer than an ordinary `show`
// already puts it there. See the M7 plan's "Decisions made."
func matchesBody(e Entry, lowerPattern string) bool {
	if strings.Contains(strings.ToLower(e.Value), lowerPattern) {
		return true
	}
	for _, v := range e.Fields {
		if strings.Contains(strings.ToLower(v), lowerPattern) {
			return true
		}
	}
	return false
}

// Search matches pattern (case-insensitively) against every entry's
// title, description, and decrypted body — one-shot mode's implementation,
// which like every other one-shot read decrypts the whole vault fresh on
// every call. Session's index-backed Search (see Session.Search) is what
// avoids re-decrypting the title/description half on repeat calls.
func (v *Vault) Search(pattern string, ident *Identity) ([]SearchResult, error) {
	entries, err := v.decryptAll(ident)
	if err != nil {
		return nil, err
	}

	lowerPattern := strings.ToLower(pattern)
	out := make([]SearchResult, 0, len(entries))
	for id, e := range entries {
		switch {
		case matchesText(e.Title, e.Description, lowerPattern):
			out = append(out, SearchResult{ID: id, Title: e.Title, Description: e.Description})
		case matchesBody(e, lowerPattern):
			out = append(out, SearchResult{ID: id, Title: e.Title, Description: e.Description, MatchedBody: true})
		}
	}
	sortSearchResults(out)
	return out, nil
}

// sortSearchResults orders results by title then id, the same
// deterministic ordering ls and ambiguousError use.
func sortSearchResults(results []SearchResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Title != results[j].Title {
			return results[i].Title < results[j].Title
		}
		return results[i].ID.String() < results[j].ID.String()
	})
}
