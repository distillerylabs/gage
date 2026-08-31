package gage

// Identity wraps one device's unlocked private key material for one vault.
// It is a parameter, never internal state: Vault's CRUD methods take an
// already-produced Identity explicitly rather than unlocking themselves.
// The only thing that produces one is Vault.Unlock.
//
// This is a skeleton — no real key material exists until M2 wires up age
// and page-locking. Deciding the shape now (rather than in M2) is what
// lets every later CRUD method take Identity as an explicit parameter
// instead of unlocking internally.
type Identity struct {
	// device is this identity's device name within its vault, recorded
	// so callers (e.g. an entry's updated_by stamp) don't need to thread
	// it separately. Unexported: nothing outside this package should
	// construct an Identity by hand.
	device string
	closed bool
}

// Device reports the identity name this Identity was unlocked as.
func (i *Identity) Device() string { return i.device }

// Closed reports whether Close has already run.
func (i *Identity) Closed() bool { return i.closed }

// Close releases whatever this Identity is holding. From M2 on this is
// the one place responsible for releasing the page lock on, and zeroing,
// the private key — invoked from exactly two trigger points: a one-shot
// command's handler after its single Vault call, and Session's Lock/idle
// timeout/process exit. Calling Close more than once is safe.
func (i *Identity) Close() error {
	if i == nil {
		return nil
	}
	i.closed = true
	return nil
}
