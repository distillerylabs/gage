package gage

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestIdentityCloseReleasesTheLockAndZeroesTheKey is Close's contract in
// one place: the page lock comes off, the key material is overwritten,
// and the Identity stops working rather than silently continuing to.
func TestIdentityCloseReleasesTheLockAndZeroesTheKey(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")
	counter := &countingLocker{inner: alwaysLocks{}}
	v.locker = counter

	id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}
	if counter.locks != 1 {
		t.Errorf("Lock called %d times during one unlock, want 1", counter.locks)
	}
	if !id.PageLocked() {
		t.Fatal("the unlocked identity is not page-locked")
	}

	// Hold on to the buffer Close is expected to wipe. This is the
	// library's own test, so reaching into the state is the only way to
	// assert zeroing actually happened rather than trusting the comment.
	secret := id.st.secret
	if !bytes.HasPrefix(secret, []byte(identityFileSecretPrefix)) {
		t.Fatalf("held key material = %q, want an %s… line", secret, identityFileSecretPrefix)
	}

	if err := id.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if counter.unlocks != 1 {
		t.Errorf("Unlock (page) called %d times, want 1", counter.unlocks)
	}
	if !id.Closed() {
		t.Error("Closed() = false after Close()")
	}
	if id.PageLocked() {
		t.Error("PageLocked() = true after Close()")
	}
	for i, b := range secret {
		if b != 0 {
			t.Fatalf("key material byte %d is %#x after Close, want the buffer zeroed", i, b)
		}
	}
}

// TestUsingAnIdentityAfterCloseFails: the alternative to failing is a
// confusing wrong-key error or an operation that looks like it worked,
// both of which are worse than a clear refusal.
func TestUsingAnIdentityAfterCloseFails(t *testing.T) {
	v, pubkey := newUnlockableVault(t, "personal", "laptop-1")

	id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := ParseRecipient(pubkey)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := Encrypt([]byte("readable while open"), recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(ct, &id); err != nil {
		t.Fatalf("Decrypt before Close: %v", err)
	}

	if err := id.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(ct, &id)
	if err == nil {
		t.Fatal("expected Decrypt after Close to fail")
	}
	if !errors.Is(err, ErrIdentityClosed) {
		t.Errorf("error = %v, want it to wrap ErrIdentityClosed", err)
	}
	if got != nil {
		t.Errorf("plaintext = %q, want nothing", got)
	}
}

// TestIdentityCloseIsIdempotent covers both halves of the M2 bullet: a
// second Close must not panic, and it must not unlock the page twice —
// which only a counting Locker can prove.
func TestIdentityCloseIsIdempotent(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")
	counter := &countingLocker{inner: alwaysLocks{}}
	v.locker = counter

	id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := id.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
	if counter.unlocks != 1 {
		t.Errorf("three Closes produced %d page unlocks, want exactly 1", counter.unlocks)
	}
	if !id.Closed() {
		t.Error("Closed() = false after Close()")
	}
}

// TestIdentityCopiesShareOneClose: Identity is returned by value, so a
// caller can copy it. Every copy has to see the same key and the same
// Close, or a closed key would still look open through another copy.
func TestIdentityCopiesShareOneClose(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")
	counter := &countingLocker{inner: alwaysLocks{}}
	v.locker = counter

	id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}
	copied := id

	if err := id.Close(); err != nil {
		t.Fatal(err)
	}
	if !copied.Closed() {
		t.Error("a copy of a closed Identity reports Closed() = false")
	}
	if err := copied.Close(); err != nil {
		t.Fatal(err)
	}
	if counter.unlocks != 1 {
		t.Errorf("closing two copies produced %d page unlocks, want 1", counter.unlocks)
	}
}

// TestDefaultLockerIsTheRealMemlockBackedOne pins what production
// actually uses. The contract tests above inject a deterministic Locker
// so they run identically everywhere; this is what stops that
// convenience from quietly becoming "gage never page-locks at all."
func TestDefaultLockerIsTheRealMemlockBackedOne(t *testing.T) {
	if _, ok := lockerOrDefault(nil).(memLocker); !ok {
		t.Errorf("lockerOrDefault(nil) = %T, want the memlock-backed memLocker", lockerOrDefault(nil))
	}
}

// TestUnlockPageLocksOrWarnsButNeverBoth drives the *real* locker — no
// injection — and asserts the invariant that holds on every platform,
// whether or not the OS grants the lock: an unlocked identity either has
// its pages pinned, or the user was told once that they aren't. Never
// both, never neither.
//
// This is how the real memlock integration stays covered without the
// suite depending on a machine's `ulimit -l`.
func TestUnlockPageLocksOrWarnsButNeverBoth(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	id, err := v.Unlock(p)
	if err != nil {
		t.Fatalf("Unlock must succeed whether or not the platform grants a page lock: %v", err)
	}
	defer func() { _ = id.Close() }()

	switch {
	case id.PageLocked() && len(p.warnings) != 0:
		t.Errorf("key material is page-locked but the user was warned anyway: %q", p.warnings)
	case !id.PageLocked() && len(p.warnings) != 1:
		t.Errorf("key material is not page-locked but the user got %d warnings, want exactly 1: %q",
			len(p.warnings), p.warnings)
	}

	// Either way the identity is fully usable — that is the whole point
	// of warn-and-proceed.
	if _, err := id.ageIdentity(); err != nil {
		t.Errorf("the identity is not usable: %v", err)
	}
}

// TestPageLockFailureWarnsOnceAndProceeds is the design's warn-and-proceed
// rule: page-locking fails routinely under a restrictive `ulimit -l` or
// in a container, and refusing to unlock there would make gage unusable
// in exactly the environments people most often run it in. A weaker
// guarantee beats an unusable tool.
func TestPageLockFailureWarnsOnceAndProceeds(t *testing.T) {
	v, pubkey := newUnlockableVault(t, "personal", "laptop-1")
	counter := &countingLocker{inner: memLocker{}, lockErr: errors.New("mlock: cannot allocate memory")}
	v.locker = counter

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	id, err := v.Unlock(p)
	if err != nil {
		t.Fatalf("a page-lock failure aborted the unlock: %v", err)
	}
	defer func() { _ = id.Close() }()

	// The Identity is genuinely usable, not a degraded stub.
	recipient, err := ParseRecipient(pubkey)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := Encrypt([]byte("usable despite an unlocked page"), recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(ct, &id); err != nil {
		t.Errorf("the unlocked-but-unprotected identity cannot decrypt: %v", err)
	}

	if id.PageLocked() {
		t.Error("PageLocked() = true after the lock failed")
	}
	if len(p.warnings) != 1 {
		t.Fatalf("Prompter.Warn called %d times, want exactly 1 per unlock: %q", len(p.warnings), p.warnings)
	}
	if !strings.Contains(p.warnings[0], "cannot allocate memory") {
		t.Errorf("warning %q does not carry the underlying reason", p.warnings[0])
	}
}

// TestPageLockIsAttemptedExactlyOncePerUnlock is the "not once per key or
// once per call" half of the same bullet, asserted on the mechanism
// rather than on the warning text: one Lock call means one possible
// warning, structurally.
func TestPageLockIsAttemptedExactlyOncePerUnlock(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")
	counter := &countingLocker{inner: alwaysLocks{}}
	v.locker = counter

	p := &fakePrompter{passphrases: []string{"wrong", "wrong again", testPassphrase}}
	id, err := v.Unlock(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = id.Close() }()

	// Three passphrase attempts, still one page lock.
	if counter.locks != 1 {
		t.Errorf("Lock called %d times across an unlock with 3 passphrase attempts, want 1", counter.locks)
	}
	if len(p.warnings) != 0 {
		t.Errorf("warnings = %q, want none when locking succeeded", p.warnings)
	}
}

// TestCloseAfterAFailedPageLockDoesNotUnlockAnything: if the lock never
// happened, Close must not try to release it — on some platforms that's
// an error, and an unlocked Identity is a normal, supported state.
func TestCloseAfterAFailedPageLockDoesNotUnlockAnything(t *testing.T) {
	v, _ := newUnlockableVault(t, "personal", "laptop-1")
	counter := &countingLocker{inner: memLocker{}, lockErr: errors.New("nope")}
	v.locker = counter

	id, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Close(); err != nil {
		t.Fatalf("Close after a failed page lock: %v", err)
	}
	if counter.unlocks != 0 {
		t.Errorf("Unlock (page) called %d times after the lock had failed, want 0", counter.unlocks)
	}
}

// TestZeroIdentityIsSafeToCloseAndUnusable covers the value callers get
// alongside an error from Unlock: `defer id.Close()` without inspecting
// the error first has to be safe.
func TestZeroIdentityIsSafeToCloseAndUnusable(t *testing.T) {
	var id Identity
	if err := id.Close(); err != nil {
		t.Fatal(err)
	}
	if err := id.Close(); err != nil {
		t.Fatal(err)
	}
	if !id.Closed() {
		t.Error("Closed() = false after two Close() calls")
	}
	if _, err := id.ageIdentity(); !errors.Is(err, ErrIdentityClosed) {
		t.Errorf("using a zero Identity gave %v, want ErrIdentityClosed", err)
	}
}

func TestNilIdentityCloseDoesNotPanic(t *testing.T) {
	var id *Identity
	if err := id.Close(); err != nil {
		t.Errorf("Close on nil Identity: %v", err)
	}
	if id.Closed() {
		t.Error("Closed() = true on a nil Identity")
	}
}
