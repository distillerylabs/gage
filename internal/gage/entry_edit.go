package gage

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// Update rewrites an existing entry's content in place — same id, fresh
// ciphertext — and commits, under this vault's write lock like every
// other mutating method. It stamps nothing itself: `gage edit` decides
// what "re-stamp updated/updated_by, leave created alone" means and hands
// Update the already-final Entry, so Update stays usable for anything
// that needs to rewrite an entry as-is (M9's --reencrypt, eventually).
//
// ident is not used to write — the entry is encrypted to the vault's
// recipients, not to the writer — but every mutating method takes one, so
// that the automatic push after a write has a frontend to report an
// unreachable remote or a divergence through.
func (v *Vault) Update(id uuid.UUID, e Entry, ident *Identity) error {
	return v.withEncryptingWrite(ident, func() error {
		if err := v.WriteEntry(id, e); err != nil {
			return err
		}
		if _, err := gitrepo.CommitAll(v.Path, id.String()); err != nil {
			return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: committing update of %s: %w", id, err))
		}
		v.pushAfterWrite(ident.warnTo())
		return nil
	})
}

// Rename resolves query and changes only its title, bumping updated and
// updated_by — value and fields are untouched. Like Insert, a title
// collision is rejected unless force is set — see the M5 plan's
// "Decisions made": rename can produce a duplicate title exactly as
// easily as insert can, so it gets the same guard. Renaming to the
// entry's current title is always allowed, force or not, since it
// changes nothing.
func (v *Vault) Rename(query, newTitle string, force bool, ident *Identity) (uuid.UUID, error) {
	var id uuid.UUID
	err := v.withEncryptingWrite(ident, func() error {
		var e Entry
		var err error
		id, e, err = v.Resolve(query, ident)
		if err != nil {
			return err
		}

		if !force && newTitle != e.Title {
			exists, err := v.titleExists(newTitle, ident)
			if err != nil {
				return err
			}
			if exists {
				return exitcode.Wrap(exitcode.Conflict,
					fmt.Errorf("%w: %q", ErrDuplicateTitle, newTitle))
			}
		}

		e.Title = newTitle
		e.Updated = NewTimestamp(time.Now())
		e.UpdatedBy = ident.Device()
		if err := v.WriteEntry(id, e); err != nil {
			return err
		}
		if _, err := gitrepo.CommitAll(v.Path, id.String()); err != nil {
			return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: committing rename of %s: %w", id, err))
		}
		v.pushAfterWrite(ident.warnTo())
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
