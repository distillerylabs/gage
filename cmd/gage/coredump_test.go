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
// This runs in the test binary's own process, which is the only process
// there is to observe — and it's exactly the same call main() makes.
func TestCoreDumpsAreDisabled(t *testing.T) {
	if err := disableCoreDumps(); err != nil {
		t.Fatalf("disableCoreDumps: %v", err)
	}
	ok, err := coreDumpsDisabled()
	if err != nil {
		t.Fatalf("reading the setting back from the OS: %v", err)
	}
	if !ok {
		t.Errorf("core dumps are still enabled after disableCoreDumps; this platform promises: %s", coreDumpGuarantee)
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
