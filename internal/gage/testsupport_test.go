package gage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

// testScryptWorkFactor is what this package's own tests unlock at,
// instead of the real, deliberately expensive scryptWorkFactor. Still a
// real scrypt pass — this isn't skipping the KDF, just running it small
// — so every test that unlocks a vault still exercises the actual wrap/
// unwrap code path, at a cost too small to notice rather than the ~1s
// per unlock shippedScryptWorkFactor costs (see its own comment on why
// that number is what it is).
const testScryptWorkFactor = 10

// testEnrollmentWorkFactor is what this package's own tests seal
// enrollment requests at, for the same reason and with the same
// consequences as testScryptWorkFactor: a real scrypt pass, run small.
//
// It is a separate knob rather than a reuse of the one above, which is
// the whole point of enrollment having its own live variable and setter.
// Sharing one would mean the two factors were one factor, and
// TestEnrollmentAndIdentityWorkFactorsAreIndependent exists to catch
// exactly that collapse.
const testEnrollmentWorkFactor = 10

// TestMain lowers both of gage's scrypt work factors for this package's
// entire test binary before any test runs. Every test that unlocks a
// vault — which is most of them — and every test that seals an
// enrollment request goes through this without doing anything itself.
//
// The enrollment half is not a nicety: E2's fixtures alone are 32 seals,
// and later milestones build pending requests through Enroll across most
// of their lists, on three platforms.
//
// Tests keep both shipped factors honest despite it:
// TestScryptWorkFactorIsDeliberate and
// TestEnrollmentScryptWorkFactorIsDeliberate check the constants these
// variables are copies of, TestOnlyTheTestHooksWriteTheWorkFactors checks
// that no non-test code can move either copy, and
// TestShippedWorkFactorReachesARealAgeFile /
// TestShippedEnrollmentWorkFactorReachesARealAgeFile restore the shipped
// values for one encryption each and read them back off the age header.
func TestMain(m *testing.M) {
	restore := SetScryptWorkFactorForTests(testScryptWorkFactor)
	restoreEnrollment := SetEnrollmentWorkFactorForTests(testEnrollmentWorkFactor)
	code := m.Run()
	restoreEnrollment()
	restore()
	os.Exit(code)
}

// fakePrompter satisfies Prompter for tests without ever touching a
// terminal, proving the interface is usable from the library side. It
// answers with passphrases from a scripted list — one per attempt — and
// records every request and warning it was handed, which is how the
// unlock tests assert on the exchange itself rather than only its result.
type fakePrompter struct {
	// passphrases is answered in order, one per Unlock call. When it
	// runs out, Unlock returns errPrompterGaveUp — which is how a test
	// Prompter expresses the same "stop retrying" decision cmd/gage's
	// real retry policy makes.
	passphrases []string
	unlockErr   error

	requests []UnlockRequest
	warnings []string
}

// errPrompterGaveUp stands in for cmd/gage's "that's enough attempts."
// The library has no retry policy of its own; it stops when the Prompter
// stops answering, and this is a Prompter that stops.
var errPrompterGaveUp = errors.New("fake prompter: out of scripted answers")

func (f *fakePrompter) Unlock(req UnlockRequest) (UnlockResponse, error) {
	f.requests = append(f.requests, req)
	if f.unlockErr != nil {
		return UnlockResponse{}, f.unlockErr
	}
	i := len(f.requests) - 1
	if i >= len(f.passphrases) {
		return UnlockResponse{}, errPrompterGaveUp
	}
	return UnlockResponse{Kind: KindPassphrase, Passphrase: f.passphrases[i]}, nil
}

func (f *fakePrompter) Confirm(prompt string) (bool, error) { return true, nil }

// ConfirmDefaultYes answers yes, like Confirm. Nothing in the library
// asks it — clone's offer is cmd/gage's — so this exists to satisfy the
// interface rather than to prove anything.
func (f *fakePrompter) ConfirmDefaultYes(prompt string) (bool, error) { return true, nil }

// ConfirmRecipientChange answers M10's trust-cache question yes, the
// same way Confirm answers yes: a fake that blocked every write over a
// recipient change would fail most of this package's tests for a reason
// none of them are about. Tests that care what was asked use
// trustPrompter (trustcache_test.go), which records the warning.
func (f *fakePrompter) ConfirmRecipientChange(w RecipientChangeWarning) (bool, error) {
	return true, nil
}
func (f *fakePrompter) Choose(list CandidateList) (string, error) { return "", nil }
func (f *fakePrompter) Warn(msg string)                           { f.warnings = append(f.warnings, msg) }
func (f *fakePrompter) Value(prompt string) (string, error)       { return "", nil }

// mismatchedPrompter answers a passphrase request with a different Kind,
// which is what a buggy or mismatched frontend looks like from the
// library's side.
type mismatchedPrompter struct{}

func (mismatchedPrompter) Unlock(req UnlockRequest) (UnlockResponse, error) {
	return UnlockResponse{Kind: "yubikey"}, nil
}
func (mismatchedPrompter) Confirm(prompt string) (bool, error)           { return true, nil }
func (mismatchedPrompter) ConfirmDefaultYes(prompt string) (bool, error) { return true, nil }
func (mismatchedPrompter) ConfirmRecipientChange(w RecipientChangeWarning) (bool, error) {
	return true, nil
}
func (mismatchedPrompter) Choose(list CandidateList) (string, error) { return "", nil }
func (mismatchedPrompter) Warn(msg string)                           {}
func (mismatchedPrompter) Value(prompt string) (string, error)       { return "", nil }

// alwaysLocks is a Locker that succeeds without asking the OS for
// anything.
//
// The Close contract — releases the lock, zeroes the key, does neither
// twice — is about Identity's own logic, not about whether this
// particular machine's kernel will honour an mlock. Wiring the real
// memlock into those tests made them fail on any host that refuses to
// lock pages: a restricted `ulimit -l`, a container, a locked-down
// Windows policy. That is exactly the environment the design says gage
// must keep working in ("a weaker guarantee beats an unusable tool"), so
// a red test suite there would be the tests contradicting the product.
//
// The real memlock is still covered: by its own package's round-trip
// test, and by TestUnlockPageLocksOrWarnsButNeverBoth, which drives the
// default locker and asserts the invariant that holds either way.
type alwaysLocks struct{}

func (alwaysLocks) Lock(b []byte) error   { return nil }
func (alwaysLocks) Unlock(b []byte) error { return nil }

// countingLocker wraps the real Locker so a test can assert how many
// times a page was locked and unlocked — the only way to prove Close is
// idempotent in the sense that matters (it doesn't double-unlock a page),
// as opposed to merely not panicking.
type countingLocker struct {
	inner    Locker
	lockErr  error
	locks    int
	unlocks  int
	unlocked [][]byte
}

func (c *countingLocker) Lock(b []byte) error {
	c.locks++
	if c.lockErr != nil {
		return c.lockErr
	}
	return c.inner.Lock(b)
}

func (c *countingLocker) Unlock(b []byte) error {
	c.unlocks++
	c.unlocked = append(c.unlocked, b)
	if c.lockErr != nil {
		// Nothing was ever locked, so there is nothing to release; a
		// real Locker would report an error for this, which is exactly
		// what Close must not reach.
		return errors.New("countingLocker: asked to unlock a page that was never locked")
	}
	return c.inner.Unlock(b)
}

// isolateXDG points every XDG root at a fresh temp directory for the
// duration of one test, so library tests that read global config or write
// identity files never touch the real machine's config/data.
func isolateXDG(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
}

// vaultIDForTest derives a stable vault id from a vault name, so that
// every device a test sets up for one vault agrees on that vault's id —
// which is what real devices do, since they all read it out of the same
// committed .gage/config.toml.
//
// Derived rather than minted because these helpers register each device
// through its own isolated XDG roots and have nowhere to thread a freshly
// minted id through. It proves nothing about A20 by itself, and is not
// meant to: the property that matters — two different vaults never
// sharing an id, and nothing local being keyed by the local name — is
// asserted directly in paths_test.go and in cmd/gage's keying tests,
// against ids that really were minted independently.
func vaultIDForTest(name string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("gage-test-vault:"+name)).String()
}

// registerVault writes the global-config record Unlock reads this
// device's name, id and method out of, and returns that vault's id —
// which is what every per-vault local path is keyed by (A20), so tests
// need it as much as the code does.
func registerVault(t *testing.T, name, device, method string) string {
	t.Helper()
	dir := configDirForTest(t)
	id := vaultIDForTest(name)
	g := config.Global{
		Current: name,
		Vaults: map[string]config.VaultEntry{
			name: {Path: filepath.Join(t.TempDir(), name), ID: id, Type: TypeGit, Device: device, Method: method},
		},
	}
	if err := config.Write(filepath.Join(dir, "config.toml"), g); err != nil {
		t.Fatal(err)
	}
	return id
}

// configDirForTest resolves and creates $GAGE_CONFIG under the isolated
// roots isolateXDG set up.
func configDirForTest(t *testing.T) string {
	t.Helper()
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
