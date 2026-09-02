//go:build unix

package main

import "golang.org/x/sys/unix"

// disableCoreDumps sets RLIMIT_CORE to zero, so a crash can't write this
// process's memory — including an unlocked private key — to a core file.
// Both the soft and hard limits go to zero: lowering only the soft limit
// would leave anything in the process able to raise it again, which
// defeats the point.
//
// This is irreversible for the life of the process, which is the intent.
func disableCoreDumps() error {
	return unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
}

// coreDumpsDisabled reads the limit back from the OS. It exists so the
// test asserts what the kernel actually thinks rather than what this
// package believes it asked for.
func coreDumpsDisabled() (bool, error) {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &lim); err != nil {
		return false, err
	}
	return lim.Cur == 0, nil
}

// coreDumpGuarantee describes what this platform actually promises, for
// the one place a user could reasonably ask.
const coreDumpGuarantee = "core dumps are disabled for this process (RLIMIT_CORE = 0)"
