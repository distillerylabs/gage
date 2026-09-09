package gage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/gittest"
	"github.com/denmark/gage/internal/gage/remoteauth"
	"github.com/denmark/gage/internal/gage/syncerr"
	"github.com/denmark/gage/internal/gage/vaultlock"
)

// D-ENROLL-REMOTE: enroll is the one write in gage that is worthless
// unless it reaches the remote, and both halves of its network behavior
// — fetch first and fail early, push last and fail legibly — follow from
// that. These are the tests under that decision.

// remoteHas reports whether the bare repository at remote holds path at
// its tip: how "the request was actually published" is asserted, from
// the far side rather than from the pushing device's own git state.
func remoteHas(t *testing.T, remote, path string) bool {
	t.Helper()
	d := gittest.NewDevice(t, remote)
	return d.Exists(t, path)
}

// TestEnrollFetchesBeforeItCommits: against a remote that moved ahead,
// the resulting push is a fast-forward rather than a divergence. Without
// the explicit fetch, enroll would commit onto a stale tip — a joining
// device never unlocks, so it passes through none of the hooks that
// carry the automatic catch-up.
func TestEnrollFetchesBeforeItCommits(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	// The remote moves after this device cloned it.
	gittest.NewDevice(t, remote).WriteCommitPush(t, "README", "moved on", "another device wrote this")

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	if err != nil {
		t.Fatalf("Enroll against a remote that moved ahead: %v", err)
	}
	if !req.Published {
		t.Fatal("Published = false, want true")
	}

	// Published means published: the file is on the remote, not merely
	// committed locally.
	name := pendingFileName(req.ID, req.Expires)
	if !remoteHas(t, remote, pendingDirName+"/"+name) {
		t.Errorf("the remote does not hold %s; the push did not land", name)
	}
	// And the catch-up really happened: the other device's commit is in
	// this device's history.
	if !strings.Contains(readFileAt(t, j.vault.Path, "README"), "moved on") {
		t.Error("enroll committed onto a stale tip; the other device's commit is missing")
	}
	ahead, err := gitrepo.AheadCount(j.vault.Path)
	if err != nil {
		t.Fatal(err)
	}
	if ahead != 0 {
		t.Errorf("%d local commits still unpushed after a successful enroll, want 0", ahead)
	}
}

func readFileAt(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

// TestEnrollWithAnUnreachableRemoteFailsBeforeAnythingIsCreated is the
// departure from "warn and proceed", and the assertion is what the
// failure did *not* leave behind: no prompt, no key, no commit. An
// enrollment request that was never published is a private key, a local
// commit and a code nobody can act on — failing before any of that
// exists is kinder than producing all three.
func TestEnrollWithAnUnreachableRemoteFailsBeforeAnythingIsCreated(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	before := headHash(t, j.vault)
	// The one situation a local repository cannot produce: a network
	// that isn't there.
	fake := &fakeSyncer{fetchErr: unreachableError()}
	j.vault.remoteSyncer = fake

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	_, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	assertEnrollmentError(t, err, ErrEnrollmentRemoteUnreachable, exitcode.Unreachable)

	if len(p.requests) != 0 {
		t.Errorf("the prompter was asked %d times, want 0 — the fetch fails before the passphrase prompt",
			len(p.requests))
	}
	if files := identityFilesFor(t, j); len(files) != 0 {
		t.Errorf("an unreachable remote still wrote %v under the identities directory", files)
	}
	if files := pendingFiles(t, j.vault); len(files) != 0 {
		t.Errorf("an unreachable remote still wrote %v into pending/", files)
	}
	if got := headHash(t, j.vault); got != before {
		t.Error("an unreachable remote still moved HEAD")
	}
	if fake.pushes != 0 {
		t.Errorf("pushed %d times after failing to fetch, want 0", fake.pushes)
	}
}

// TestEnrollWithNoRemoteFailsWithItsOwnError: "there is no remote" and
// "the remote could not be reached" have different fixes — configure
// one, versus get on the network — so cmd/gage has to be able to tell
// them apart.
func TestEnrollWithNoRemoteFailsWithItsOwnError(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	_ = laptop

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	_, err := v.Enroll(context.Background(), "phone-1", DefaultEnrollmentTTL, p)
	assertEnrollmentError(t, err, ErrEnrollmentNoRemote, exitcode.Usage)

	if errors.Is(err, ErrEnrollmentRemoteUnreachable) {
		t.Error("a vault with no remote reported ErrEnrollmentRemoteUnreachable; the two must stay distinguishable")
	}
	if len(p.requests) != 0 {
		t.Errorf("the prompter was asked %d times, want 0", len(p.requests))
	}
	if files := pendingFiles(t, v); len(files) != 0 {
		t.Errorf("a vault with no remote still wrote %v into pending/", files)
	}
}

// TestEnrollHoldsTheWriteLockAcrossThePull: the pull mutates the working
// tree, so pulling outside the lock and taking it afterwards would leave
// a window for another gage process to move HEAD between the catch-up
// and the commit. The hook fires immediately after the pull; if the lock
// were not held then, a second acquisition would succeed.
func TestEnrollHoldsTheWriteLockAcrossThePull(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	checked := false
	j.vault.onEnrollPulled = func() {
		checked = true
		lockPath, err := LockFilePath(j.vault.ID)
		if err != nil {
			t.Errorf("resolving the lock path: %v", err)
			return
		}
		held, err := vaultlock.Acquire(lockPath, 0)
		if err == nil {
			_ = held.Release()
			t.Error("the vault lock was free just after enroll's pull; " +
				"the pull and the commit must be one locked read-modify-commit sequence")
			return
		}
		var contended *vaultlock.ContendedError
		if !errors.As(err, &contended) {
			t.Errorf("acquiring the vault lock during enroll = %v, want a *vaultlock.ContendedError", err)
		}
	}

	if _, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if !checked {
		t.Fatal("the post-pull hook never fired, so nothing was asserted")
	}
}

// TestUncontendedEnrollNeverReportsContention is the plain-success test
// that catches enroll reaching the network through Pull/Push/tryPull.
//
// Those take the write lock themselves via underWriteLock, and vaultlock
// is not re-entrant — each acquireVaultLock opens a fresh descriptor, and
// both flock and LockFileEx conflict across descriptions inside one
// process. So an Enroll that called them from inside its own lock would
// block against itself for the full vaultLockTimeout and then fail as
// *contended*, naming this process's own lock. Asserting the elapsed time
// is what makes that surface as a failure rather than as a slow suite.
func TestUncontendedEnrollNeverReportsContention(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	start := time.Now()
	_, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	elapsed := time.Since(start)

	var contended *vaultlock.ContendedError
	if errors.As(err, &contended) {
		t.Fatalf("an uncontended enroll reported lock contention — it is blocking against its own lock, "+
			"which is what calling Pull/Push/tryPull from inside withVaultWrite does: %v", err)
	}
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if elapsed >= vaultLockTimeout/2 {
		t.Errorf("an uncontended enroll took %s, more than half of vaultLockTimeout (%s) — "+
			"it is waiting on a lock it already holds", elapsed, vaultLockTimeout)
	}
}

// authRejectedPush is what a token with read but not write access looks
// like by the time it reaches Vault: classified by syncerr, annotated
// with the host and the command that fixes it exactly as gitrepo's
// annotateAuth does.
func authRejectedPush() error {
	classified := syncerr.ClassifyPush(transport.ErrAuthorizationFailed)
	return fmt.Errorf("%w; %s", classified, remoteauth.AuthHint("github.com"))
}

// TestEnrollPushRejectedForWriteAccessKeepsWhatItMade: a read-scoped
// token clones and fetches fine and cannot push, and predicting that is
// deliberately not available (gage is host-neutral and `auth status`
// makes no network call). So the requirement is legibility and cheap
// recovery: say what happened, name `gage auth login`, and leave the
// identity and the commit in place so the retry is a reuse.
func TestEnrollPushRejectedForWriteAccessKeepsWhatItMade(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	before := headHash(t, j.vault)
	j.vault.remoteSyncer = &fakeSyncer{pushErr: authRejectedPush()}

	_, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}})
	if err == nil {
		t.Fatal("Enroll reported success on a push the remote rejected")
	}
	if !errors.Is(err, syncerr.ErrAuth) {
		t.Errorf("error = %v, want it to carry the classified auth failure", err)
	}
	msg := err.Error()
	for _, want := range []string{"committed locally", "gage auth login"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}

	// What it made survives, which is the whole point: re-running after
	// `gage auth login` reuses the key rather than generating a second.
	if files := identityFilesFor(t, j); len(files) != 1 {
		t.Errorf("identity files = %v, want the one the failed enroll created", files)
	}
	if got := headHash(t, j.vault); got == before {
		t.Error("the local commit is missing; a failed push must not roll the write back")
	}
	if files := pendingFiles(t, j.vault); len(files) != 1 {
		t.Errorf("pending/ holds %v, want the one committed request", files)
	}
}

// TestEnrollAfterAFailedPushReusesTheIdentityAndSucceeds is the retry
// this design promises is cheap. It mints a fresh request and code
// (D-ENROLL-COLLISIONS) — the unpublished one is inert and expires.
func TestEnrollAfterAFailedPushReusesTheIdentityAndSucceeds(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	j.vault.remoteSyncer = &fakeSyncer{pushErr: authRejectedPush()}
	if _, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}}); err == nil {
		t.Fatal("the first enroll was supposed to fail at the push")
	}
	first := pendingFiles(t, j.vault)

	// `gage auth login` happened.
	j.vault.remoteSyncer = nil

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	if err != nil {
		t.Fatalf("re-running after a failed push: %v", err)
	}
	if !req.Published {
		t.Error("Published = false on the retry")
	}
	if got := p.requests[0].Purpose; got != PurposeUnlock {
		t.Errorf("the retry's unlock purpose = %q, want %q — it must reuse the existing key", got, PurposeUnlock)
	}
	if files := identityFilesFor(t, j); len(files) != 1 {
		t.Errorf("identity files = %v, want still exactly one", files)
	}
	if len(first) != 1 || first[0] == pendingFileName(req.ID, req.Expires) {
		t.Errorf("the retry reused the first request (%v); it must mint a fresh request and code", first)
	}
	if !remoteHas(t, remote, pendingDirName+"/"+pendingFileName(req.ID, req.Expires)) {
		t.Error("the retry's request never reached the remote")
	}
}

// TestEnrollRefusesADivergedPull, built the way divergence actually
// happens here: a push that fails leaves a local commit, the remote then
// moves, and the re-run finds two sides that have both advanced.
//
// The refusal must come *before* anything is created, and its message
// must not say "run `gage sync`" — that command resolves entry conflicts
// by decrypting both sides, which is precisely what a joining device
// cannot do.
func TestEnrollRefusesADivergedPull(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	// A read-scoped token: the request is committed locally, unpublished.
	j.vault.remoteSyncer = &fakeSyncer{pushErr: authRejectedPush()}
	if _, err := enrollAs(t, j, DefaultEnrollmentTTL, &fakePrompter{passphrases: []string{testPassphrase}}); err == nil {
		t.Fatal("the first enroll was supposed to fail at the push")
	}
	j.vault.remoteSyncer = nil

	// The remote moves on independently.
	gittest.NewDevice(t, remote).WriteCommitPush(t, "README", "moved on", "another device wrote this")

	beforeHead := headHash(t, j.vault)
	beforePending := pendingFiles(t, j.vault)
	p := &fakePrompter{passphrases: []string{testPassphrase}}
	_, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	assertEnrollmentError(t, err, ErrEnrollmentDiverged, exitcode.Conflict)

	if strings.Contains(err.Error(), "gage sync") {
		t.Errorf("error = %q, want it not to send a device that cannot decrypt to `gage sync`", err)
	}
	if len(p.requests) != 0 {
		t.Errorf("the prompter was asked %d times, want 0 — the refusal precedes the identity step",
			len(p.requests))
	}
	if got := headHash(t, j.vault); got != beforeHead {
		t.Error("a diverged pull still committed something")
	}
	if got := pendingFiles(t, j.vault); len(got) != len(beforePending) {
		t.Errorf("pending/ went from %v to %v; a diverged pull must seal nothing", beforePending, got)
	}
}

// TestDivergenceIsFoundByTheReportRatherThanAnError pins the nil-error
// trap directly: v.pull returns nil and sets SyncReport.Diverged, so an
// implementation that only inspects the error walks past it, commits
// onto the diverged branch and fails at the *push* instead — with the
// standard "origin has diverged — run `gage sync`" advice.
//
// A test that only checked "some error came back" would pass against
// that implementation. This one asserts the shape of the refusal: the
// diverged sentinel, and no commit.
func TestDivergenceIsFoundByTheReportRatherThanAnError(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	// Diverge without going anywhere near enroll: a local commit here,
	// an unrelated one on the remote.
	gittest.NewDevice(t, remote).WriteCommitPush(t, "README", "theirs", "the remote moved")
	if err := os.WriteFile(filepath.Join(j.vault.Path, "LOCAL"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(j.vault.Path, "a local commit"); err != nil {
		t.Fatal(err)
	}

	// The pull the implementation runs: nil error, Diverged set. If this
	// ever stops being true the trap is gone and so is the reason for
	// checking the field.
	report, err := j.vault.Pull(context.Background())
	if err != nil {
		t.Fatalf("v.Pull over a divergence returned %v; this test exists because it returns nil", err)
	}
	if !report.Diverged {
		t.Fatal("v.Pull did not report a divergence, so this fixture is not diverged")
	}

	before := headHash(t, j.vault)
	p := &fakePrompter{passphrases: []string{testPassphrase}}
	_, err = enrollAs(t, j, DefaultEnrollmentTTL, p)
	assertEnrollmentError(t, err, ErrEnrollmentDiverged, exitcode.Conflict)
	if len(p.requests) != 0 {
		t.Errorf("the prompter was asked %d times, want 0", len(p.requests))
	}
	if got := headHash(t, j.vault); got != before {
		t.Error("enroll committed onto a diverged branch")
	}
}

// TestEnrollTakesTheDirtyTreeResetBeforeItPulls is the bullet that fails
// if Enroll is wired to withWriteLock instead of withVaultWrite — and it
// fails for a reason that looks like a remote problem, since a
// fast-forward refuses over a dirty tree with a Conflict.
//
// It matters more here than anywhere else: a joining device has no other
// write to clear a stray with, because every path that runs the reset
// needs an unlock it cannot perform. Without the reset in enroll's own
// preamble, one interrupted enroll wedges the device permanently.
func TestEnrollTakesTheDirtyTreeResetBeforeItPulls(t *testing.T) {
	_, owner, remote := newSyncVault(t, "personal", "laptop-1")
	_ = owner.Close()
	j := newJoiningDevice(t, remote, "personal", "phone-1")

	// The remote moves, so the pull actually has a fast-forward to do —
	// which is what refuses over a dirty tree.
	gittest.NewDevice(t, remote).WriteCommitPush(t, "README", "moved on", "another device wrote this")

	// Exactly what a killed enroll leaves: an untracked file in pending/.
	stray := writePending(t, j.vault, pendingFileName("11111111-1111-4111-8111-111111111111",
		time.Now().Add(time.Hour)), []byte("an interrupted enroll wrote this"))

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	req, err := enrollAs(t, j, DefaultEnrollmentTTL, p)
	if err != nil {
		t.Fatalf("Enroll over a stray from an interrupted run = %v; "+
			"a joining device has no other write to clear one with, so this wedges it permanently", err)
	}
	if !req.Published {
		t.Error("Published = false")
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("the stray survived the reset: %v", err)
	}
	if !anyWarningMentions(p.warnings, "discarded uncommitted changes") {
		t.Errorf("warnings = %v, want the ordinary discard warning naming what it threw away", p.warnings)
	}
}
