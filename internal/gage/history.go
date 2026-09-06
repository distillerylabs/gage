package gage

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// LogEntry is one commit in which an entry changed: when it happened and
// which commit it was, and nothing else.
//
// There is deliberately no decrypted field here, and no `Message`
// either. The commit message is the entry's UUID and nothing more (M4's
// decision), so rendering it would only repeat the id the caller already
// has — while making it look as though `log` shows message text, which
// is the thing that must stay empty of titles. What `log` reveals is
// exactly "this opaque entry changed at this time", which is what an
// observer with the repository can already see.
type LogEntry struct {
	Hash string
	When time.Time
	// Deleted marks the commit that removed the entry, so a log can show
	// an entry's end rather than stopping at its last edit.
	Deleted bool
}

// Revision is one past state of an entry, decrypted: what `gage history
// --decrypt` walks. This is the design doc's "one meaningfully more
// dangerous tier" — every value here is a secret the vault used to hold,
// including ones that have since been rotated away.
type Revision struct {
	Hash    string
	When    time.Time
	Deleted bool
	// Entry is the decrypted entry at this commit. Meaningless when
	// Deleted is set — there was no entry to decrypt.
	Entry Entry
}

// Log returns the commit history of one entry, newest first, without
// decrypting anything.
//
// It takes an already-resolved id rather than a query because resolving
// a query is itself a decrypt-everything operation (see "Addressing
// entries") — a caller that has an id shouldn't be made to unlock, and
// a caller that only has a title has to resolve it first anyway.
func (v *Vault) Log(id uuid.UUID) ([]LogEntry, error) {
	revs, err := v.entryRevisions(id)
	if err != nil {
		return nil, err
	}
	out := make([]LogEntry, 0, len(revs))
	for _, r := range revs {
		out = append(out, LogEntry{Hash: r.Hash, When: r.When, Deleted: r.Content == nil})
	}
	return out, nil
}

// History returns every past state of one entry, decrypted, newest
// first.
//
// A revision this identity cannot decrypt is a hard failure rather than
// a skipped line: it means the entry was encrypted to a recipient set
// this device was not part of, and quietly omitting it would turn "here
// is this secret's history" into "here is the part of it I could read",
// with no way for the reader to tell which they got.
func (v *Vault) History(id uuid.UUID, ident *Identity) ([]Revision, error) {
	revs, err := v.entryRevisions(id)
	if err != nil {
		return nil, err
	}
	out := make([]Revision, 0, len(revs))
	for _, r := range revs {
		rev := Revision{Hash: r.Hash, When: r.When, Deleted: r.Content == nil}
		if !rev.Deleted {
			e, err := decryptEntry(r.Content, ident)
			if err != nil {
				return nil, fmt.Errorf("gage: decrypting revision %s of %s: %w", shortHash(r.Hash), id, err)
			}
			rev.Entry = e
		}
		out = append(out, rev)
	}
	return out, nil
}

// entryRevisions is the shared walk behind Log and History.
//
// entryFilePath, not filepath.Join: git names paths with forward slashes
// on every platform, so a Windows separator here would match nothing and
// report an entry with a full history as having none.
func (v *Vault) entryRevisions(id uuid.UUID) ([]gitrepo.Revision, error) {
	revs, err := gitrepo.Log(v.Path, entryFilePath(id))
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, err)
	}
	return revs, nil
}

// shortHash abbreviates a commit hash the way git does.
func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
