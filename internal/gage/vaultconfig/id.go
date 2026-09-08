package vaultconfig

import "github.com/google/uuid"

// The vault id: minting it, and validating it as untrusted input.
//
// It lives beside the file it is read from because it is part of that
// file's schema, and because every other package that needs it — the
// path builders in internal/gage, and cmd/gage's `init` — already sits
// above this one. Putting it in internal/gage instead is not an option:
// Read validates the id before returning, and this package cannot import
// the one that imports it.
//
// The id is the structural twin of a recipient's device name: both are
// filename components arriving from this committed, git-writable file,
// and both are validated on the way in (Read) and again on the way out
// (IdentityFilePath, TrustCacheDir, LockFilePath). See A20 and
// Q-DEVICE-NAME. The device name keeps its own package only because its
// normalization is a larger, self-contained thing; the rule the two are
// held to is the same one.

// NewID mints a fresh vault id: a random UUIDv4, assigned rather than
// derived.
//
// Assigned is the whole point. A20 records why the obvious derived
// alternative — hashing the origin URL — cannot work: one remote has
// many spellings, `git set-remote` legitimately changes it, and a
// local-only vault has no origin at all. An assigned id is stable by
// construction and independent of transport.
//
// It is `gage init` that calls this, before it creates this device's
// identity — the identity file is filed under the id, so the id has to
// exist first. Create then records the id it was handed rather than
// minting a second one.
func NewID() string { return uuid.NewString() }

// ValidID reports whether id is a vault id gage will build a path from:
// the canonical lowercase 8-4-4-4-12 form of a non-nil UUID.
//
// It is deliberately stricter than uuid.Parse, which also accepts the
// urn:uuid: prefix, a braced form, and an undashed 32-hex string. Those
// all denote the same UUID but spell it differently, and a vault with
// several legal spellings of its id is a vault with several identities
// directories — the collision this id exists to make unreachable. The
// strictness is also what makes the safety property trivially true:
// every accepted string is 36 characters of hex and hyphens, so it can
// never traverse, never contain a separator on any platform gage builds
// for, and never be empty.
//
// The nil UUID is refused because it is what a zero value and a
// half-written config both produce, and neither is a vault that was
// ever assigned an id.
func ValidID(id string) bool {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return false
	}
	if parsed == uuid.Nil {
		return false
	}
	// Round-trip rather than a regexp: uuid's own String is the
	// definition of the canonical form, so this cannot drift from it.
	return parsed.String() == id
}
