package gage

import (
	"strings"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// Field returns one entry from an Entry's `fields` map — what `gage show
// --field NAME` prints instead of `value`, and what `--field NAME --qr`
// scopes the QR code to (see "Notes on show").
//
// It lives here rather than in cmd/gage because "which part of this
// entry did you mean" is a question about an Entry, not about a
// terminal: a GUI showing a field picker wants the same lookup and the
// same not-found answer. The frontend's job is only to render it.
//
// An unknown name is NotFound rather than Usage: the name isn't
// malformed, it just isn't in this entry — and the caller can't see
// inside the entry to check, which is why the error lists the names
// that are there. It lists names only, never values.
func (e Entry) Field(name string) (string, error) {
	if v, ok := e.Fields[name]; ok {
		return v, nil
	}
	if len(e.Fields) == 0 {
		return "", exitcode.Newf(exitcode.NotFound,
			"gage: entry %q has no fields; --field %s cannot be read from it", e.Title, name)
	}
	// Sorted so the message doesn't reshuffle between runs over the same
	// entry — Go map iteration order is randomized, and an error a human
	// is meant to read the alternatives from should not.
	return "", exitcode.Newf(exitcode.NotFound,
		"gage: entry %q has no field %q; it has: %s",
		e.Title, name, strings.Join(sortedKeys(e.Fields), ", "))
}
