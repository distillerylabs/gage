package vaultlock

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// These tests deliberately spawn real, separate OS processes to hold the
// lock rather than goroutines within the test binary. flock(2) locks the
// *open file description*, not the process — two goroutines in one
// process that each open() the path get independent descriptions and
// would contend exactly like two processes would, which makes a
// goroutine-based version of this test pass, but for the wrong reason:
// it wouldn't catch a bug where locking were accidentally scoped to an
// os.File value (e.g. via a package-level mutex substituting for the real
// syscall) rather than the filesystem object gage actually needs
// serialized across independent process invocations — a session in one
// terminal and a one-shot command in another. Real subprocesses are what
// this package exists to serialize, so the tests hold it with real
// subprocesses.
//
// The trampoline below is the same pattern net/http and os/exec use to
// test subprocess behavior: the test binary re-execs itself with a
// sentinel environment variable set, and TestMain intercepts that before
// any real test runs.

const lockHolderEnv = "GAGE_VAULTLOCK_TEST_HOLDER"
const lockHolderPathEnv = "GAGE_VAULTLOCK_TEST_HOLDER_PATH"

func TestMain(m *testing.M) {
	if os.Getenv(lockHolderEnv) == "1" {
		runLockHolder()
		return
	}
	os.Exit(m.Run())
}

// runLockHolder acquires the lock named by lockHolderPathEnv, announces
// success on stdout, then waits for stdin to be closed (the parent
// releasing it deliberately) or to be killed (simulating a crash) before
// releasing and exiting. Using os.Stdin/os.Stdout/fmt.Print here is fine:
// this file is a _test.go file, exactly what M0's library-purity rule
// exempts.
func runLockHolder() {
	path := os.Getenv(lockHolderPathEnv)
	lock, err := Acquire(path, 5*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lockholder: acquire failed:", err)
		os.Exit(1)
	}
	fmt.Println("ACQUIRED")
	// Block until the parent closes our stdin (graceful release) or
	// kills us outright (simulated crash) — either way, this process
	// holding the OS-level lock is what's under test, not this loop.
	buf := make([]byte, 1)
	os.Stdin.Read(buf) //nolint:errcheck
	lock.Release()     //nolint:errcheck
	os.Exit(0)
}

// spawnHolder starts a subprocess that acquires path and blocks until the
// test closes its stdin or kills it. It waits for the subprocess to
// report ACQUIRED before returning, so callers never race the subprocess
// still opening the file. The returned io.Closer is the subprocess's
// stdin: closing it is how a test tells the holder to release gracefully.
func spawnHolder(t *testing.T, path string) (*exec.Cmd, io.Closer, func()) {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	cmd := exec.Command(self, "-test.run=^$")
	cmd.Env = append(os.Environ(), lockHolderEnv+"=1", lockHolderPathEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting lock-holder subprocess: %v", err)
	}

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading ACQUIRED from lock-holder subprocess: %v", err)
	}
	if line != "ACQUIRED\n" {
		t.Fatalf("lock-holder subprocess said %q, want ACQUIRED", line)
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = stdin.Close()
			_ = cmd.Wait()
		})
	}
	return cmd, stdin, release
}

func TestAcquireSerializesAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.lock")

	_, _, release := spawnHolder(t, path)
	defer release()

	// The holder subprocess has the lock; a short-timeout Acquire from
	// this process must observe the hold rather than proceeding
	// concurrently.
	_, err := Acquire(path, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected Acquire to be contended while the subprocess holds the lock")
	}
	var contended *ContendedError
	if !errors.As(err, &contended) {
		t.Fatalf("err = %v (%T), want *ContendedError", err, err)
	}
	if contended.Path != path {
		t.Errorf("ContendedError.Path = %q, want %q", contended.Path, path)
	}
	if contended.Holder.PID == 0 {
		t.Error("ContendedError.Holder.PID = 0, want the subprocess's real PID")
	}
}

func TestReleaseLetsWaiterProceed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.lock")

	_, stdin, release := spawnHolder(t, path)
	defer release()

	// Kick off a blocking Acquire from this process, then release the
	// subprocess's hold shortly after. The blocking Acquire must
	// observe that release and succeed — proving flock's wake-on-release
	// semantics work across processes, not just within one.
	acquired := make(chan error, 1)
	go func() {
		lock, err := Acquire(path, 5*time.Second)
		if err == nil {
			_ = lock.Release()
		}
		acquired <- err
	}()

	time.Sleep(100 * time.Millisecond)

	// Assert the waiter is still blocked *before* releasing. Without
	// this, the test would pass just as well if the lock had never been
	// held at all — the Acquire above would return immediately, the
	// select below would see a nil error, and "releasing lets the waiter
	// proceed" would be reported green by a run that never had a waiter.
	select {
	case err := <-acquired:
		t.Fatalf("Acquire returned (err=%v) while the subprocess still held the lock; nothing was ever blocked", err)
	default:
	}

	if err := stdin.Close(); err != nil {
		t.Fatalf("closing subprocess stdin: %v", err)
	}

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("Acquire after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire never returned after the holder released the lock")
	}
}

func TestKilledHolderReleasesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.lock")

	cmd, _, _ := spawnHolder(t, path)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing lock-holder subprocess: %v", err)
	}
	_ = cmd.Wait() // reap; exit status is expected to be non-zero (killed)

	// No stale lock should survive the kill: a short-timeout Acquire
	// (not a long blocking one) must succeed promptly.
	lock, err := Acquire(path, 2*time.Second)
	if err != nil {
		t.Fatalf("Acquire after killing the holder: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireRoundTripsOnThisPlatform(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.lock")

	lock, err := Acquire(path, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Once released, a fresh Acquire on the same path must succeed
	// immediately (timeout 0: try once, fail fast if still contended).
	lock2, err := Acquire(path, 0)
	if err != nil {
		t.Fatalf("second Acquire after Release: %v", err)
	}
	if err := lock2.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestAcquireZeroTimeoutFailsFastWhenContended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.lock")

	_, _, release := spawnHolder(t, path)
	defer release()

	start := time.Now()
	_, err := Acquire(path, 0)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an immediate contention error")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Acquire with timeout 0 took %s, want near-immediate", elapsed)
	}
}
