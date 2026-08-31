package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// exeName returns name with the platform's executable extension, so a
// binary built with `go build -o <explicit path>` (which does not
// auto-append .exe the way `go build -o <dir>` does) can still be found
// by exec.Command on Windows.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

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
	binDir  string
	binErr  error
)

// TestMain removes the once-per-run build directory that
// builtGageBinary creates, so a `go test ./...` doesn't leave a
// gage-integration-* directory behind in the system temp dir on every
// invocation.
func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

func builtGageBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		// Built once per test binary run (not via t.TempDir(), which is
		// per-test and would rebuild for every test that wants it).
		// Cleaned up by TestMain rather than t.Cleanup, since the
		// binary outlives whichever test happened to build it first.
		var dir string
		dir, binErr = os.MkdirTemp("", "gage-integration-*")
		if binErr != nil {
			return
		}
		binDir = dir
		binPath = filepath.Join(dir, exeName("gage"))
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

// TestLinkTimeVersionInjection builds with the same -X ldflags shape the
// Makefile uses and asserts the values actually reach `gage --version`.
//
// This exists because the in-process version tests construct BuildInfo
// directly, which proves the rendering but says nothing about whether
// the link-time injection is wired to the right symbols. During M0 the
// Makefile pointed -X at `<module>/cmd/gage.version` — a plausible-looking
// path that silently does nothing, since the package is `main` — and
// every version test stayed green while `gage --version` reported
// "commit unknown" on every build. A wrong -X path is a no-op, not a
// link error, so nothing but an end-to-end assertion catches it.
func TestLinkTimeVersionInjection(t *testing.T) {
	dir, err := os.MkdirTemp("", "gage-ldflags-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	bin := filepath.Join(dir, exeName("gage"))
	const wantVersion = "v9.9.9-testtag"
	const wantCommit = "cafef00"

	build := exec.Command("go", "build",
		"-ldflags", "-X 'main.version="+wantVersion+"' -X 'main.commit="+wantCommit+"'",
		"-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building with ldflags: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("gage --version failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), wantVersion) {
		t.Errorf("--version output %q does not contain the injected tag %q — the Makefile's -X path is not reaching the version symbol", out, wantVersion)
	}
	if !strings.Contains(string(out), wantCommit) {
		t.Errorf("--version output %q does not contain the injected commit %q", out, wantCommit)
	}
}

// TestMakefileLdflagsTargetTheRealSymbols guards the specific failure
// above from the other direction: it reads the Makefile's LDFLAGS line
// and requires it to name `main.version`/`main.commit`, the symbols
// TestLinkTimeVersionInjection proves work. Without this, someone could
// "fix" a Makefile regression by editing only the test's own -X path and
// leave the shipped build silently unstamped.
func TestMakefileLdflagsTargetTheRealSymbols(t *testing.T) {
	data, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(data)
	for _, want := range []string{"main.version=", "main.commit="} {
		if !strings.Contains(makefile, want) {
			t.Errorf("Makefile LDFLAGS does not set %q; a -X path that doesn't match a real symbol links silently and stamps nothing", want)
		}
	}
}
