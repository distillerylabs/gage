//go:build windows

package memlock

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// lock/unlock are VirtualLock/VirtualUnlock. Unlike mlock they take an
// address and a length rather than a slice, so the address is taken from
// the slice's first element — safe because Lock/Unlock have already
// rejected the empty case, and because the pointer is used only for the
// duration of the syscall.
func lock(b []byte) error {
	// #nosec G103 -- taking the address of a live, non-empty slice's
	// backing array to hand to a syscall that needs an address; the
	// pointer does not outlive the call.
	return windows.VirtualLock(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
}

func unlock(b []byte) error {
	// #nosec G103 -- see lock.
	return windows.VirtualUnlock(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
}

// resourceLimited reports whether err is Windows saying the process's
// working-set quota won't allow another locked page, rather than a
// genuine bug. See the unix file for why this exists.
func resourceLimited(err error) bool {
	return errors.Is(err, windows.ERROR_WORKING_SET_QUOTA) ||
		errors.Is(err, windows.ERROR_NOT_ENOUGH_MEMORY) ||
		errors.Is(err, windows.ERROR_NOT_ENOUGH_QUOTA)
}
