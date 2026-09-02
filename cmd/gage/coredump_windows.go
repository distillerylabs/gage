//go:build windows

package main

import "golang.org/x/sys/windows"

// errorModeMask is what gage sets: suppress the Windows Error Reporting
// crash dialog, and the critical-error handler dialog, so an unattended
// crash doesn't sit at a modal box offering to send a dump.
const errorModeMask = windows.SEM_FAILCRITICALERRORS | windows.SEM_NOGPFAULTERRORBOX

// kernel32's GetErrorMode has no x/sys wrapper, so it's resolved here.
// SetErrorMode returns the *previous* mode rather than the current one,
// which makes it unusable for reading back what was set without a
// set-and-restore dance that would race every other goroutine.
var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procGetErrorMode = kernel32.NewProc("GetErrorMode")
)

// disableCoreDumps is Windows' narrower analogue of setrlimit(RLIMIT_CORE,
// 0), and the design doc is explicit that it is not parity: Windows has no
// direct equivalent to a Unix core dump, and a non-elevated process cannot
// guarantee that no crash dump is ever written (WER can be configured
// machine-wide to collect dumps regardless). What gage can do — and does —
// is stop the crash dialog that would otherwise offer to produce one.
// Documented as a gap rather than claimed as a guarantee it can't back.
func disableCoreDumps() error {
	windows.SetErrorMode(errorModeMask)
	return nil
}

// coreDumpsDisabled reads the process error mode back from the OS, so the
// test asserts what Windows actually holds rather than what this package
// believes it set.
func coreDumpsDisabled() (bool, error) {
	if err := procGetErrorMode.Find(); err != nil {
		return false, err
	}
	mode, _, _ := procGetErrorMode.Call()
	return uint32(mode)&errorModeMask == errorModeMask, nil
}

const coreDumpGuarantee = "the Windows Error Reporting crash dialog is suppressed (SetErrorMode); " +
	"unlike POSIX's RLIMIT_CORE this cannot guarantee no crash dump is ever written"
