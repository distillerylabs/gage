package gage

import (
	"fmt"

	"filippo.io/age"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// Identity wraps one device's unlocked private key material for one vault.
// It is a parameter, never internal state: Vault's CRUD methods take an
// already-produced Identity explicitly rather than unlocking themselves.
// The only thing that produces one is Vault.Unlock.
//
// Identity is returned and passed by value, but everything mutable about
// it lives behind the state pointer, so a copy shares one set of key
// material and one Close. Without that, closing a copy would leave
// another copy's view of the same key looking open — the exact
// silently-succeeds-after-Close failure Close's contract rules out.
type Identity struct {
	// device is this identity's device name within its vault, recorded
	// so callers (e.g. an entry's updated_by stamp) don't need to thread
	// it separately. Unexported: nothing outside this package should
	// construct an Identity by hand.
	device string
	st     *identityState
}

// identityState is the part of an Identity that Close mutates and copies
// must share.
type identityState struct {
	// secret is the private key as an "AGE-SECRET-KEY-1..." bech32 line:
	// the buffer this package owns end to end, and therefore the one it
	// can page-lock and zero. Held for the Identity's lifetime so there
	// is something concrete for the page lock to protect.
	secret []byte

	// ident is the parsed key age actually decrypts with. age's
	// X25519Identity keeps its own copy of the scalar in ordinary,
	// garbage-collected memory, and exposes no way to hand it a
	// caller-owned buffer — so that copy is neither page-locked nor
	// zeroable. That's a real, bounded gap: it's recorded here rather
	// than papered over, the same way the design records Windows'
	// narrower core-dump guarantee instead of claiming parity.
	ident *age.X25519Identity

	// recipient is the public half, safe to expose and to write into
	// .age-recipients.
	recipient string

	// prompter is the frontend that unlocked this identity, kept so the
	// write path can surface a sync warning without every mutating
	// method growing a Prompter parameter of its own. It is only ever
	// used for advisories — nothing here decides anything — and an
	// Identity always has one, since Vault.Unlock is the only thing that
	// produces one and it is always given a Prompter.
	prompter Prompter

	locker Locker
	// pageLocked records whether secret is actually locked, so Close
	// unlocks exactly what was locked — unlocking a page that was never
	// locked is an error on some platforms, and the whole point of the
	// warn-and-proceed path is that an unlocked Identity is a normal,
	// supported state.
	pageLocked bool
	closed     bool
}

// Device reports the identity name this Identity was unlocked as.
func (i *Identity) Device() string { return i.device }

// Closed reports whether Close has already run.
func (i *Identity) Closed() bool {
	if i == nil || i.st == nil {
		return false
	}
	return i.st.closed
}

// Recipient returns the public key matching this identity's private key —
// the "age1..." string that goes into .age-recipients and
// .gage/config.toml. Empty on a closed or zero Identity.
func (i *Identity) Recipient() string {
	if i == nil || i.st == nil {
		return ""
	}
	return i.st.recipient
}

// PageLocked reports whether this Identity's key material is actually
// pinned in memory. False is a supported state, not a failure: see
// Vault.Unlock's warn-and-proceed path.
func (i *Identity) PageLocked() bool {
	if i == nil || i.st == nil {
		return false
	}
	return i.st.pageLocked
}

// warnTo returns the Prompter sync advisories for this identity's
// operations should go to, or nil if there is none to warn through.
func (i *Identity) warnTo() Prompter {
	if i == nil || i.st == nil {
		return nil
	}
	return i.st.prompter
}

// ageIdentity hands out the key for a decrypt, refusing once Close has
// run. This is the single choke point that makes "using the Identity
// after Close() fails instead of silently succeeding" true for every
// operation, present and future, rather than a check each caller has to
// remember.
func (i *Identity) ageIdentity() (age.Identity, error) {
	if i == nil || i.st == nil {
		return nil, exitcode.Wrap(exitcode.LockedOrAuth,
			fmt.Errorf("%w: no identity was unlocked", ErrIdentityClosed))
	}
	if i.st.closed {
		return nil, exitcode.Wrap(exitcode.LockedOrAuth, ErrIdentityClosed)
	}
	return i.st.ident, nil
}

// Close releases the page lock on this Identity's private key and zeroes
// it. It's the one place responsible for both, invoked from exactly two
// trigger points: a one-shot command's handler after its single Vault
// call, and Session's Lock/idle timeout/process exit. Calling Close more
// than once is safe and does not unlock the page twice.
func (i *Identity) Close() error {
	if i == nil {
		return nil
	}
	// A zero Identity — the value returned alongside an error from
	// Unlock — has no state to share, so Close allocates one just to
	// record that it ran. Callers legitimately defer Close on the result
	// of a failed Unlock without inspecting it first.
	if i.st == nil {
		i.st = &identityState{closed: true}
		return nil
	}
	if i.st.closed {
		return nil
	}
	i.st.closed = true
	i.st.ident = nil

	var unlockErr error
	if i.st.pageLocked {
		unlockErr = i.st.locker.Unlock(i.st.secret)
		i.st.pageLocked = false
	}
	// Zeroing happens whether or not the page unlock succeeded: leaving
	// key material in memory because a munlock failed would be strictly
	// worse than the failure itself.
	zero(i.st.secret)
	i.st.secret = nil

	if unlockErr != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: releasing the page lock on %q's key: %w", i.device, unlockErr))
	}
	return nil
}

// frontend returns the Prompter this Identity was unlocked through — the
// same one warnTo hands advisories to, named for the case where the
// library asks a *question* rather than only reporting something.
//
// M9's `recipient remove` is the first of those: removing this device's
// own key is legitimate but locks the device out, so it runs only behind
// an explicit Confirm. Routing it through the Identity keeps the design
// rule intact that human interaction rides the Identity rather than
// growing a Prompter parameter on every mutating method.
func (i *Identity) frontend() Prompter { return i.warnTo() }
