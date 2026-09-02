package memlock

import (
	"bytes"
	"testing"
)

// keySized is the shape of the buffer this package actually protects: an
// age X25519 secret key's bech32 encoding, which is what Identity holds
// page-locked for its lifetime.
const keySized = 74

// KNOWN LIMIT, recorded deliberately: this test cannot distinguish a
// working mlock from a Lock that returns nil without doing anything.
// Neither POSIX nor Windows offers a portable "is this page locked?"
// query — on Linux it can be read out of /proc/self/smaps, but there is
// no macOS or Windows equivalent, so the check would hold on one of the
// three platforms CI runs and give false confidence on the other two.
// What is verified here is that the syscalls are reached, accept a
// key-sized buffer, agree about the empty case, and don't corrupt what
// they protect. The consequence of a silent no-op is bounded: a key that
// could reach swap, which is the same outcome as the warn-and-proceed
// path gage already supports and tests.
//
// TestLockUnlockRoundTrip is the M2 test list's "the memlock package's
// Lock/Unlock round-trip succeeds on the current platform for a
// representative key-sized byte slice."
//
// A failure to lock is only tolerated when the platform itself said no
// for a resource reason — a container's RLIMIT_MEMLOCK of zero, a low
// `ulimit -l`, a Windows working-set quota. Any other error fails the
// test, so "we called mlock wrong" can't hide behind the same skip that
// "this CI runner won't allow locking" legitimately needs.
func TestLockUnlockRoundTrip(t *testing.T) {
	buf := make([]byte, keySized)
	for i := range buf {
		buf[i] = byte(i)
	}
	want := bytes.Clone(buf)

	if err := Lock(buf); err != nil {
		if resourceLimited(err) {
			t.Skipf("this platform refuses to lock pages for a resource reason (%v); "+
				"gage's own answer to that is to warn once and proceed, tested in Vault.Unlock", err)
		}
		t.Fatalf("Lock: %v", err)
	}

	// Locking must not disturb the contents it's protecting.
	if !bytes.Equal(buf, want) {
		t.Errorf("Lock altered the buffer: got %v, want %v", buf, want)
	}

	if err := Unlock(buf); err != nil {
		t.Fatalf("Unlock after a successful Lock: %v", err)
	}
	if !bytes.Equal(buf, want) {
		t.Errorf("Unlock altered the buffer: got %v, want %v", buf, want)
	}
}

// TestEmptySliceIsANoOp pins the documented zero-length contract: the
// platform calls disagree about whether a zero-length range is an error,
// so this package settles it rather than passing the disagreement on to
// Identity.Close.
func TestEmptySliceIsANoOp(t *testing.T) {
	if err := Lock(nil); err != nil {
		t.Errorf("Lock(nil) = %v, want nil", err)
	}
	if err := Unlock(nil); err != nil {
		t.Errorf("Unlock(nil) = %v, want nil", err)
	}
	if err := Lock([]byte{}); err != nil {
		t.Errorf("Lock(empty) = %v, want nil", err)
	}
	if err := Unlock([]byte{}); err != nil {
		t.Errorf("Unlock(empty) = %v, want nil", err)
	}
}
