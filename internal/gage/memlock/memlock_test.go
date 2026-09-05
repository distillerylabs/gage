package memlock

import (
	"bytes"
	"os"
	"testing"
	"unsafe"
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

// pageOf reports which OS page b's first byte lives on.
func pageOf(b []byte) uintptr {
	pg := uintptr(os.Getpagesize())
	// #nosec G103 -- read only to bucket the address by page; no pointer
	// is reconstructed from it.
	return uintptr(unsafe.Pointer(&b[0])) &^ (pg - 1)
}

// TestAllocGivesEachBufferItsOwnPage is the guarantee Alloc exists for,
// and the reason it's asserted here rather than left to Lock/Unlock: two
// keys sharing a page share a single, non-reference-counted page lock,
// so the first Unlock silently releases the second key's protection.
// Windows is the only platform that reports it (ERROR_NOT_LOCKED on the
// second unlock) and it reports it as an error on the innocent caller,
// so without this test the invariant would only ever be checked on one
// of three platforms — and then only by accident.
//
// The comparison is against plain make, which is what this replaced:
// that lands two key-sized buffers on the same page essentially always,
// so the assertion below is not a coincidence being pinned.
func TestAllocGivesEachBufferItsOwnPage(t *testing.T) {
	const n = 8
	bufs := make([][]byte, n)
	seen := map[uintptr]int{}
	for i := range bufs {
		bufs[i] = Alloc(keySized)
		if len(bufs[i]) != keySized {
			t.Fatalf("Alloc(%d) returned %d bytes", keySized, len(bufs[i]))
		}
		if prev, ok := seen[pageOf(bufs[i])]; ok {
			t.Errorf("Alloc buffers %d and %d share a page; each locked key must own its pages", prev, i)
		}
		seen[pageOf(bufs[i])] = i
	}

	// A key-sized buffer spans one page, so the whole allocation must
	// also not reach into a following page another Alloc could own.
	if pg := os.Getpagesize(); keySized > pg {
		t.Fatalf("this test assumes a key fits in one page (%d > %d)", keySized, pg)
	}

	// Capacity is capped so an append can't grow the key into the
	// surrounding pages, which are allocated but never locked.
	if got := cap(bufs[0]); got != keySized {
		t.Errorf("cap(Alloc(%d)) = %d, want %d — an append would spill past the locked region", keySized, got, keySized)
	}
}

// TestUnlockingOneBufferLeavesAnotherLocked is the failure the session
// milestone actually hit: two identities held at once, and closing the
// first made closing the second fail. It reproduces at this level with
// two buffers locked and unlocked in sequence, which on Windows returned
// ERROR_NOT_LOCKED for the second before Alloc gave each its own page.
func TestUnlockingOneBufferLeavesAnotherLocked(t *testing.T) {
	first, second := Alloc(keySized), Alloc(keySized)

	if err := Lock(first); err != nil {
		if resourceLimited(err) {
			t.Skipf("this platform refuses to lock pages for a resource reason (%v)", err)
		}
		t.Fatalf("Lock(first): %v", err)
	}
	if err := Lock(second); err != nil {
		t.Fatalf("Lock(second) with another key already locked: %v", err)
	}

	if err := Unlock(first); err != nil {
		t.Fatalf("Unlock(first): %v", err)
	}
	if err := Unlock(second); err != nil {
		t.Errorf("Unlock(second) after unlocking an unrelated key: %v — the two keys shared a page lock", err)
	}
}
