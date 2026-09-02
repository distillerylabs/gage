package gage

// Vault is a single vault's on-disk state: config, recipients, entries. It
// stays a stateless, identity-agnostic operator over ciphertext in both
// invocation modes — Session, not Vault, owns any "how long does this stay
// unlocked" bookkeeping. See "Library architecture" in the design doc.
//
// The on-disk structure arrived in M1 and real crypto in M2; what M0
// fixed is the shape everything after it builds against.
type Vault struct {
	// Name and Path are the vault's registry name and its on-disk
	// location. Set by Create, or by whatever M1+ adds for opening an
	// already-registered vault.
	Name string
	Path string

	// locker is the page-locking seam Unlock and Identity.Close go
	// through. nil means the real memlock-backed implementation, so
	// nothing outside this package's own tests ever sets it — see
	// Locker.
	locker Locker
}

// Unlock lives in unlock.go.
