package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestMain in-process (via Run, in session_test.go) proves the dispatch
// *decision*, using an injected IsTerminal. This file goes one step
// further for the redirected-stdin half of that bullet: it builds the
// real gage binary and runs it with stdin from an ordinary file — a
// genuine, unfaked non-terminal — proving term.IsTerminal itself, not
// just gage's own dispatch logic around it, does the right thing. There
// is no equivalent real-TTY integration test here: producing an actual
// terminal on stdin needs a pty, which per the plan doesn't arrive until
// M2 (creack/pty); the TTY branch stays dispatch-level only, as the plan
// says it should for M0.
var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func builtGageBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		// Built once per test binary run (not via t.TempDir(), which is
		// per-test and would rebuild for every test that wants it).
		var dir string
		dir, binErr = os.MkdirTemp("", "gage-integration-*")
		if binErr != nil {
			return
		}
		binPath = filepath.Join(dir, "gage")
		cmd := exec.Command("go", "build", "-o", binPath, ".")
		cmd.Dir = "."
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = err
			t.Logf("go build output: %s", out)
		}
	})
	if binErr != nil {
		t.Fatalf("building gage binary: %v", binErr)
	}
	return binPath
}

func TestRealBinaryWithRedirectedStdinPrintsHelp(t *testing.T) {
	bin := builtGageBinary(t)

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	cmd := exec.Command(bin)
	cmd.Stdin = devNull
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gage (redirected stdin) failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "Usage:") {
		t.Errorf("gage with redirected stdin didn't print help:\n%s", out)
	}
}

func TestRealBinaryVersionFlag(t *testing.T) {
	bin := builtGageBinary(t)

	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("gage --version failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "gage") {
		t.Errorf("gage --version output = %q, want it to mention gage", out)
	}
}
