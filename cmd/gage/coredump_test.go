package main

import (
	"os"
	"strings"
	"testing"
)

// TestCoreDumpsAreDisabled asserts the effect on the current platform by
// reading the setting back from the OS, not by trusting that the call was
// made: on Linux/macOS RLIMIT_CORE must read as 0, on Windows the process
// error mode must carry the crash-dialog suppression bits.
//
// It first *undoes* the suppression, and that step is the entire point.
// Core dumps are already off by default on macOS and under most shells'
// `ulimit -c 0`, so a test that simply called disableCoreDumps and read
// the limit back would pass against an implementation that did nothing —
// which is exactly what it did before this was added.
//
// The undo, the assertion, and the irrevocability check are one test
// rather than three on purpose. Lowering RLIMIT_CORE's *hard* limit is
// permanent for the life of a process, so any test that lowered it first
// would leave the others unable to set up, silently skipping instead of
// checking. One test means one order, whatever `go test -shuffle` does.
//
// What each platform actually guarantees differs, so the parts that are
// POSIX-specific sit behind coreDumpSuppressionIsIrrevocable rather than
// being assumed universal.
//
// This runs in the test binary's own process, which is the only process
// there is to observe, and it's exactly the same call main() makes.
func TestCoreDumpsAreDisabled(t *testing.T) {
	if !undoCoreDumpSuppression(t) {
		t.Skip("core dumps are already irrevocably disabled for this process; " +
			"nothing for disableCoreDumps to change, so this proves nothing either way")
	}
	// Guard the guard: if undoing didn't take, everything below is
	// vacuous again and should say so rather than quietly pass.
	if off, err := coreDumpsDisabled(); err != nil {
		t.Fatal(err)
	} else if off {
		t.Fatal("core dumps still read as disabled after undoing the suppression; this test would prove nothing")
	}

	if err := disableCoreDumps(); err != nil {
		t.Fatalf("disableCoreDumps: %v", err)
	}
	ok, err := coreDumpsDisabled()
	if err != nil {
		t.Fatalf("reading the setting back from the OS: %v", err)
	}
	if !ok {
		t.Fatalf("core dumps are still enabled after disableCoreDumps; this platform promises: %s", coreDumpGuarantee)
	}

	// Everything above holds on every platform. Irrevocability does not:
	// on POSIX, lowering only the soft limit would leave anything in the
	// process free to raise it again, and disableCoreDumps claims to
	// lower the hard limit too — but Windows has no equivalent one-way
	// form at all. Asserting it everywhere would be asserting a fiction
	// on Windows, which is exactly what this test did on its first CI
	// run.
	if !coreDumpSuppressionIsIrrevocable {
		t.Logf("this platform's suppression is revocable by design, so irrevocability is not asserted; it promises only: %s",
			coreDumpGuarantee)
		return
	}
	if !hardLimitIsZero(t) {
		t.Error("the hard limit was left above zero: the process can simply re-enable core dumps")
	}
	if undoCoreDumpSuppression(t) {
		t.Error("core-dump suppression could be undone after disableCoreDumps")
	}
}

// TestDisablingCoreDumpsIsIdempotent: main() calls it once, but a test
// binary calls it too, and lowering an already-zero limit must not start
// failing on the second call.
func TestDisablingCoreDumpsIsIdempotent(t *testing.T) {
	for i := range 3 {
		if err := disableCoreDumps(); err != nil {
			t.Fatalf("disableCoreDumps call %d: %v", i+1, err)
		}
	}
}

// TestMainDisablesCoreDumpsAtStartup guards the wiring rather than the
// mechanism. The function above can be correct and still never be called,
// and a missing call is silent — the same failure shape as the Makefile's
// -X path, which is why it gets the same source-level assertion.
//
// It also pins the *position*: the call has to come before anything that
// could hold key material, which for a program whose whole job is key
// material means first.
func TestMainDisablesCoreDumpsAtStartup(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	if !strings.Contains(src, "disableCoreDumps()") {
		t.Fatal("main.go never calls disableCoreDumps(); core dumps would stay enabled in the shipped binary")
	}
	callAt := strings.Index(src, "disableCoreDumps()")
	runAt := strings.Index(src, "Run(os.Args")
	if runAt < 0 {
		t.Fatal("main.go no longer calls Run(os.Args...); this guard needs updating")
	}
	if callAt > runAt {
		t.Error("main.go disables core dumps after starting the command tree; it must happen before any key material can exist")
	}
}
