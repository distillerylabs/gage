package gage

import "github.com/denmark/gage/internal/gage/exitcode"

// Vault is a single vault's on-disk state: config, recipients, entries. It
// stays a stateless, identity-agnostic operator over ciphertext in both
// invocation modes — Session, not Vault, owns any "how long does this stay
// unlocked" bookkeeping. See "Library architecture" in the design doc.
//
// This is an M0 skeleton: the real on-disk structure arrives in M1, and
// real crypto in M2. What's fixed here is the shape everything after M1
// builds against.
type Vault struct {
	// Name and Path are set once a vault actually has an on-disk
	// location to point at — M1's job. Left exported and empty until
	// then.
	Name string
	Path string
}

// Unlock runs the method-specific unlock flow (passphrase prompt and
// decrypt of the wrapped identity file, a YubiKey touch, whatever the
// configured method needs) and returns an Identity the caller threads
// through every subsequent Vault call. Callers own calling Identity.Close
// when they're done with it — Vault.Unlock never does so itself.
//
// The real implementation lands in M2; until then this always fails, so
// nothing upstream can silently proceed as though it had a usable
// Identity.
func (v *Vault) Unlock(p Prompter) (Identity, error) {
	return Identity{}, exitcode.New(exitcode.Internal, "gage: vault unlock is not implemented yet (arrives in M2)")
}
