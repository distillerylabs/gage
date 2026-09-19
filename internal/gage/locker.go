package gage

import "github.com/distillerylabs/gage/internal/gage/memlock"

// Locker is the seam between Vault.Unlock/Identity.Close and the
// platform's page-locking syscalls. Production and every realistic test
// use the real memlock-backed implementation; a fake returning a
// deterministic failure exists for exactly one test, the one proving a
// page-lock failure warns and proceeds rather than aborting the unlock.
// This is the same injectable-seam shape M8a uses for RemoteSyncer: a
// two-method interface with one real implementation, injected only where
// a real failure would otherwise be unreproducible.
type Locker interface {
	Lock(b []byte) error
	Unlock(b []byte) error
}

// memLocker is the real implementation: a trivial adapter over the
// memlock package, which is where the platform-specific code lives.
type memLocker struct{}

func (memLocker) Lock(b []byte) error   { return memlock.Lock(b) }
func (memLocker) Unlock(b []byte) error { return memlock.Unlock(b) }

// lockerOrDefault resolves a possibly-nil Locker to the real one. A nil
// Locker means "use the real thing," so nothing outside a test ever has
// to set the field, and a Vault built by Create or by a struct literal
// gets real page-locking without opting in.
func lockerOrDefault(l Locker) Locker {
	if l == nil {
		return memLocker{}
	}
	return l
}
