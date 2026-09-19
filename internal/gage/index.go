package gage

import (
	"sort"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/memlock"
)

// indexArenaMinSize is the smallest arena Index ever allocates. Small
// enough that an empty or near-empty vault doesn't reserve pages for
// nothing, large enough that most vaults never grow past their first
// allocation.
const indexArenaMinSize = 4096

// span is a byte range within an Index's arena.
type span struct {
	off, len int
}

// indexEntry is one vault entry's cached metadata: title and description
// as spans into the Index's arena — never ordinary Go strings, per the
// M7 plan's "Decisions made" — plus the small amount of bookkeeping
// later commands (candidate lists, `search`) render without decrypting
// again. Never a secret value, never a structured field.
type indexEntry struct {
	title, description span
	created, updated   Timestamp
	updatedBy          string
}

// ListEntry is one row of a vault listing: the metadata `ls` prints —
// title, id, dates, updated_by — plus the description the index also
// caches for a later command to use without decrypting again. Produced
// both by Vault.List (one-shot, always freshly decrypted) and by
// Session.List (index-served); the two are byte-identical by
// construction, which is what lets cmd/gage render either with one
// function.
type ListEntry struct {
	ID          uuid.UUID
	Title       string
	Description string
	Created     Timestamp
	Updated     Timestamp
	UpdatedBy   string
}

// sortListEntries fixes ls's order — title first, per the M4 plan's
// output decision, then id to break ties. The tie-break matters because
// -f/--force allows duplicate titles: without it, two entries sharing a
// title would come back in whatever order the map ranged in, and one-shot
// and session mode could print the same vault in different orders.
func sortListEntries(rows []ListEntry) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Title != rows[j].Title {
			return rows[i].Title < rows[j].Title
		}
		return rows[i].ID.String() < rows[j].ID.String()
	})
}

// Index is a session-scoped cache of one vault's decrypted metadata —
// title, description, dates, updated_by — built the first time a session
// needs to list, resolve, or search that vault, and reused for the rest
// of the session. It never holds a secret value or a structured field:
// see the M7 plan's "Decisions made" for why that line is drawn where it
// is, and it never outlives the Identity that built it (Session discards
// it in the same place it closes that Identity).
//
// It belongs to Session, not Vault — Vault stays a stateless,
// identity-agnostic operator over ciphertext. See "Library architecture"
// in the design doc.
//
// The backing store is one memlock.Alloc arena holding every cached
// title/description's bytes, with indexEntry values referencing offsets
// into it, rather than one allocation per entry: Alloc burns up to two
// pages of slack per call to guarantee an unshared page, which is
// pathological for a vault of any size. Ordinary Go strings and maps
// can't hold this material in the first place — they're immutable and
// GC-copied, so they can't be zeroed reliably.
type Index struct {
	entries map[uuid.UUID]indexEntry

	arena     []byte
	used      int
	allocated bool
	locked    bool

	// lockEnabled mirrors the vault's Identity.PageLocked() at the
	// moment this Index was built: if the identity's own key couldn't be
	// page-locked, the same constraint (a restrictive RLIMIT_MEMLOCK, a
	// container, locked-down Windows policy) applies here too, so the
	// arena doesn't even try — and, critically, doesn't warn a second
	// time about a failure the identity's own unlock already reported.
	lockEnabled bool
	locker      Locker
}

// newIndex builds an empty Index over locker, ready for put. lockEnabled
// should be the vault's Identity.PageLocked() at the time of the call —
// see the lockEnabled field doc.
func newIndex(locker Locker, lockEnabled bool) *Index {
	return &Index{
		entries:     map[uuid.UUID]indexEntry{},
		locker:      lockerOrDefault(locker),
		lockEnabled: lockEnabled,
	}
}

// put caches id's current title/description/dates, overwriting whatever
// this Index previously held for id. Used both for a full index build
// and for a session's incremental update after insert/edit/rename.
func (idx *Index) put(id uuid.UUID, e Entry) {
	idx.entries[id] = indexEntry{
		title:       idx.store(e.Title),
		description: idx.store(e.Description),
		created:     e.Created,
		updated:     e.Updated,
		updatedBy:   e.UpdatedBy,
	}
}

// remove drops id from the index — the cache-side half of `rm`. It does
// not compact or zero the bytes id's old title/description occupied in
// the arena: they stay part of the same locked, live allocation until
// the whole Index is discarded, which is what actually bounds their
// lifetime.
func (idx *Index) remove(id uuid.UUID) {
	delete(idx.entries, id)
}

// titles returns every cached id's title, materialized as ordinary Go
// strings for the shared resolveTitleAndUUID algorithm to match against.
// These are transient copies made for one resolution, not new long-lived
// storage — the arena is what the "never an ordinary Go string" rule
// binds, not a match's own working set. See the M7 plan's "Decisions
// made".
func (idx *Index) titles() map[uuid.UUID]string {
	out := make(map[uuid.UUID]string, len(idx.entries))
	for id, e := range idx.entries {
		out[id] = idx.text(e.title)
	}
	return out
}

// list returns every cached entry as a ListEntry, in the same order —
// and carrying the same fields — Vault.List produces one-shot.
func (idx *Index) list() []ListEntry {
	out := make([]ListEntry, 0, len(idx.entries))
	for id, e := range idx.entries {
		out = append(out, ListEntry{
			ID:          id,
			Title:       idx.text(e.title),
			Description: idx.text(e.description),
			Created:     e.created,
			Updated:     e.updated,
			UpdatedBy:   e.updatedBy,
		})
	}
	sortListEntries(out)
	return out
}

// matchText returns every cached entry whose title or description
// contains lowerPattern, case-insensitively — the index-served half of
// `search`/`grep`; the body-text half always decrypts fresh (see
// search.go) and is never cached here.
func (idx *Index) matchText(lowerPattern string) map[uuid.UUID]SearchResult {
	out := map[uuid.UUID]SearchResult{}
	for id, e := range idx.entries {
		title, description := idx.text(e.title), idx.text(e.description)
		if matchesText(title, description, lowerPattern) {
			out[id] = SearchResult{ID: id, Title: title, Description: description}
		}
	}
	return out
}

// text materializes sp as an ordinary Go string — a copy, deliberately:
// a string backed directly by the arena's own bytes (e.g. via
// unsafe.String) would still be "live" after discard zeroes the arena
// out from under it, corrupting a value Go's own rules say is immutable.
func (idx *Index) text(sp span) string {
	if sp.len == 0 {
		return ""
	}
	return string(idx.arena[sp.off : sp.off+sp.len])
}

// store appends s's bytes to the arena, growing it first if needed, and
// returns the span it now occupies. An empty string is never stored —
// span{} already reads back as "" via text, so there's nothing to gain
// by giving it real arena bytes.
func (idx *Index) store(s string) span {
	if s == "" {
		return span{}
	}
	idx.ensureCap(len(s))
	off := idx.used
	copy(idx.arena[off:], s)
	idx.used += len(s)
	return span{off: off, len: len(s)}
}

// ensureCap grows the arena to hold n more bytes if it doesn't already,
// by allocating a fresh, larger memlock.Alloc arena, copying the live
// bytes across, and releasing/zeroing the old one — the "one arena"
// invariant holds at any instant even though a growing index swaps the
// backing allocation out from under itself over its lifetime.
//
// This is a growth, not a rebuild: it never decrypts anything, so it
// doesn't count against the "index update doesn't trigger a full
// rebuild" property incremental put/remove calls are for.
func (idx *Index) ensureCap(n int) {
	if idx.used+n <= len(idx.arena) {
		return
	}

	size := len(idx.arena)
	if size == 0 {
		size = indexArenaMinSize
	}
	for size < idx.used+n {
		size *= 2
	}
	newArena := memlock.Alloc(size)
	copy(newArena, idx.arena[:idx.used])

	locked := false
	if idx.lockEnabled {
		locked = idx.locker.Lock(newArena) == nil
	}
	if idx.allocated && idx.locked {
		_ = idx.locker.Unlock(idx.arena)
	}
	zero(idx.arena)

	idx.arena = newArena
	idx.locked = locked
	idx.allocated = true
}

// lockOK reports whether the arena is protected the way it's supposed to
// be right now: either page-locking was never attempted (nothing
// allocated yet, or the vault's own Identity already failed to page-lock
// its key and there's no point trying the same thing twice), or it was
// attempted and succeeded. False is the one case worth a warning:
// locking was attempted and it failed.
func (idx *Index) lockOK() bool {
	if !idx.lockEnabled || !idx.allocated {
		return true
	}
	return idx.locked
}

// discard releases the arena's page lock (if any) and zeroes it — the
// Index's Close, called from the same code path that closes the vault's
// Identity (Session.lockHeld), so an index never outlives the key that
// built it. Idempotent: calling it again after the arena is already gone
// is a no-op rather than a second, invalid page-unlock.
func (idx *Index) discard() {
	if !idx.allocated {
		idx.entries = nil
		return
	}
	if idx.locked {
		_ = idx.locker.Unlock(idx.arena)
		idx.locked = false
	}
	zero(idx.arena)
	idx.arena = nil
	idx.used = 0
	idx.allocated = false
	idx.entries = nil
}
