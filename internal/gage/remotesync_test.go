package gage

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/gittest"
	"github.com/denmark/gage/internal/gage/syncerr"
	"github.com/denmark/gage/internal/gage/vaultlock"
)

// fakeSyncer is the injected RemoteSyncer the offline tests use. Every
// other sync test runs against a real bare repository through the real
// go-git-backed syncer — this exists only for the one situation a local
// repo can't produce, which is a network that isn't there.
type fakeSyncer struct {
	fetchErr error
	pushErr  error

	fetches int
	pushes  int
}

func (f *fakeSyncer) Fetch(ctx context.Context) error {
	f.fetches++
	return f.fetchErr
}

func (f *fakeSyncer) Push(ctx context.Context) error {
	f.pushes++
	return f.pushErr
}

// unreachableError is what gage's own classifier makes of a refused
// connection.
//
// The offline tests inject exactly this rather than a sentinel invented
// for the test, which is the point: it proves the warn-and-proceed path
// is reachable through the same classification a real network failure
// goes through, instead of only through an error the test knew to build.
func unreachableError() error {
	return syncerr.ClassifyFetch(&net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connect: connection refused"),
	})
}

// newSyncVault builds a real vault whose origin is a fresh bare
// repository, with its initial commit already published — the starting
// point every sync test needs, and the state a real `gage init --remote`
// followed by a first push leaves behind.
func newSyncVault(t *testing.T, name, device string) (*Vault, Identity, string) {
	t.Helper()

	remote := gittest.NewBareRemote(t)
	d := newTestDevice(t, name, device)

	var v *Vault
	withXDGRoot(t, d.root, func() {
		var err error
		v, err = Create(CreateSpec{
			Name:       name,
			Path:       filepath.Join(t.TempDir(), name),
			Type:       TypeGit,
			Method:     MethodPassphrase,
			Device:     device,
			Recipients: []string{d.pubkey},
			Remote:     remote,
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	if _, err := gitrepo.Push(context.Background(), v.Path); err != nil {
		t.Fatalf("publishing the initial commit: %v", err)
	}

	id := unlockAs(t, v, d)
	return v, id, remote
}

// promptedBy returns the fake Prompter an identity's write path reports
// sync warnings through.
func promptedBy(t *testing.T, id Identity) *fakePrompter {
	t.Helper()

	p, ok := id.warnTo().(*fakePrompter)
	if !ok {
		t.Fatalf("the identity warns through %T, want *fakePrompter", id.warnTo())
	}
	return p
}

// entryFileName is the vault-relative path of an entry, as the git side
// sees it.
func entryFileName(t *testing.T, v *Vault) string {
	t.Helper()

	ids, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected exactly one entry, got %d", len(ids))
	}
	return "entries/" + ids[0].String() + ".age"
}

func TestUnlockFastForwardsFromTheRemote(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	// Another device publishes something this vault doesn't have yet.
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "notes.txt", "from the other device", "another device's commit")

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	report, err := v.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !report.Pulled {
		t.Error("Pull reported no fast-forward, want one")
	}

	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d", after, before+1)
	}
	if _, err := os.Stat(filepath.Join(v.Path, "notes.txt")); err != nil {
		t.Errorf("the pulled file is not in the working tree: %v", err)
	}
}

// TestUnlockPullsWithoutASessionCoversTheOneShotPath proves the automatic
// catch-up is tied to Vault.Unlock — the hook point both invocation modes
// share — and not to the session's `use` verb. A one-shot command's
// implicit unlock is the same call, so this is that path.
func TestUnlockPullsWithoutASessionCoversTheOneShotPath(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	_ = id.Close()

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "notes.txt", "from the other device", "another device's commit")

	// A fresh unlock, exactly as a one-shot `gage show` performs one.
	reunlocked, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer func() { _ = reunlocked.Close() }()

	if _, err := os.Stat(filepath.Join(v.Path, "notes.txt")); err != nil {
		t.Errorf("unlock did not fast-forward the vault: %v", err)
	}
}

func TestUnlockWarnsOnceAndProceedsWhenTheRemoteIsUnreachable(t *testing.T) {
	v, id, _ := newSyncVault(t, "personal", "laptop-1")
	_ = id.Close()

	v.remoteSyncer = &fakeSyncer{fetchErr: unreachableError()}

	p := &fakePrompter{passphrases: []string{testPassphrase}}
	reunlocked, err := v.Unlock(p)
	if err != nil {
		t.Fatalf("Unlock failed while offline, want warn-and-proceed: %v", err)
	}
	defer func() { _ = reunlocked.Close() }()

	if len(p.warnings) != 1 {
		t.Fatalf("Warn called %d times, want exactly 1: %q", len(p.warnings), p.warnings)
	}
	if !strings.Contains(p.warnings[0], "could not reach origin") {
		t.Errorf("warning = %q, want it to say the remote could not be reached", p.warnings[0])
	}
}

// TestTheInjectedOfflineErrorIsWhatVaultTreatsAsUnreachable pins the
// fake's error to the real classification, so the offline tests can't
// quietly stop exercising the path they exist for.
func TestTheInjectedOfflineErrorIsWhatVaultTreatsAsUnreachable(t *testing.T) {
	err := unreachableError()
	if !errors.Is(err, syncerr.ErrUnreachable) {
		t.Fatalf("the injected offline error does not classify as unreachable: %v", err)
	}
	if errors.Is(err, syncerr.ErrDiverged) || errors.Is(err, syncerr.ErrAuth) {
		t.Errorf("the injected offline error also classifies as diverged/auth: %v", err)
	}
}

func TestPullThatBringsNothingReportsNoChange(t *testing.T) {
	v, id, _ := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	report, err := v.Pull(context.Background())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if report.Pulled {
		t.Error("Pull reported a fast-forward with nothing on the remote to fetch")
	}
	if report.Diverged {
		t.Error("Pull reported divergence on an in-sync vault")
	}
}

func TestWritePushesToTheRemote(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	if _, err := v.Insert(sampleEntry(time.Now()), false, &id); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if warnings := promptedBy(t, id).warnings; len(warnings) != 0 {
		t.Errorf("a write with nothing diverged warned, want no user-visible friction: %q", warnings)
	}

	// The other device sees the write without anyone pushing by hand.
	other := gittest.NewDevice(t, remote)
	if !other.Exists(t, entryFileName(t, v)) {
		t.Error("the inserted entry never reached the remote")
	}
}

func TestOfflinePushKeepsTheCommitAndRetriesLater(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	offline := &fakeSyncer{pushErr: syncerr.ClassifyPush(&net.OpError{
		Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused"),
	})}
	v.remoteSyncer = offline

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Insert(sampleEntry(time.Now()), false, &id); err != nil {
		t.Fatalf("Insert failed while offline, want the write to succeed anyway: %v", err)
	}

	// Nothing lost: the commit is in the local object store regardless of
	// the network.
	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("commit count = %d, want %d — the write's commit should exist offline", after, before+1)
	}

	warnings := promptedBy(t, id).warnings
	if len(warnings) != 1 {
		t.Fatalf("Warn called %d times, want 1: %q", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "committed locally but not pushed") {
		t.Errorf("offline warning = %q, want it to say the commit is safe but unsent", warnings[0])
	}

	// Back on the network, the next push carries the pending commit.
	v.remoteSyncer = nil
	report, err := v.Push(context.Background())
	if err != nil {
		t.Fatalf("Push after coming back online: %v", err)
	}
	if !report.Pushed {
		t.Error("the retry pushed nothing")
	}
	other := gittest.NewDevice(t, remote)
	if !other.Exists(t, entryFileName(t, v)) {
		t.Error("the retried push did not reach the remote")
	}
}

// TestDisjointDivergenceMergesAndPushes is the case git can settle on its
// own: two devices, two different files. It must complete with no prompt
// and no unlock.
func TestDisjointDivergenceMergesAndPushes(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "notes.txt", "the other device's file", "another device's commit")

	// A local write now diverges: this side has a commit the remote
	// doesn't, and vice versa.
	if _, err := v.Insert(sampleEntry(time.Now()), false, &id); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	warnings := promptedBy(t, id).warnings
	if len(warnings) != 0 {
		t.Errorf("a mergeable divergence warned, want it handled silently: %q", warnings)
	}

	// Both sides' work is present locally and on the remote.
	if _, err := os.Stat(filepath.Join(v.Path, "notes.txt")); err != nil {
		t.Errorf("the other device's file is missing locally after the merge: %v", err)
	}
	fresh := gittest.NewDevice(t, remote)
	if !fresh.Exists(t, "notes.txt") {
		t.Error("the other device's file vanished from the remote")
	}
	if !fresh.Exists(t, entryFileName(t, v)) {
		t.Error("the local entry never reached the remote")
	}
}

// TestConflictingDivergenceIsDetectedAndLeftAlone is M8a's contract in
// one test: a real conflict is noticed, classified, reported — and not
// resolved, not merged, not silently anything.
func TestConflictingDivergenceIsDetectedAndLeftAlone(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID, err := v.Insert(sampleEntry(time.Now()), false, &id)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	entryPath := entryFileName(t, v)

	// The other device rewrites the same entry file and publishes it.
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, entryPath, "ciphertext written by the other device", "the other device's edit")

	// This device edits the same entry, which commits locally and then
	// finds the remote has moved.
	e := sampleEntry(time.Now())
	e.Value = "rotated-locally"
	if err := v.Update(entryID, e, &id); err != nil {
		t.Fatalf("Update: %v", err)
	}

	headBefore, err := gitrepo.HeadHash(v.Path)
	if err != nil {
		t.Fatal(err)
	}

	report, err := v.Push(context.Background())
	if err == nil {
		t.Fatal("Push succeeded on a conflicting divergence, want it reported")
	}
	if !errors.Is(err, syncerr.ErrDiverged) {
		t.Errorf("Push error = %v, want it to classify as diverged", err)
	}
	if report.ConflictKind() != ConflictEntries {
		t.Errorf("ConflictKind = %v, want ConflictEntries", report.ConflictKind())
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0] != entryPath {
		t.Errorf("Conflicts = %v, want [%s]", report.Conflicts, entryPath)
	}

	// Nothing was resolved: no merge commit, and the local version is
	// still the one this device wrote.
	headAfter, err := gitrepo.HeadHash(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if headAfter != headBefore {
		t.Errorf("HEAD moved (%s -> %s) on a conflicting divergence; M8a detects, it never resolves",
			headBefore, headAfter)
	}
	got, err := v.ReadEntry(entryID, &id)
	if err != nil {
		t.Fatalf("the local entry is no longer readable after a detected conflict: %v", err)
	}
	if got.Value != "rotated-locally" {
		t.Errorf("local value = %q, want the local edit to be untouched", got.Value)
	}
}

// TestDivergenceIsReportedDifferentlyFromBeingOffline is the distinction
// the whole classification exists for: same failed push, two situations,
// two messages.
func TestDivergenceIsReportedDifferentlyFromBeingOffline(t *testing.T) {
	divergedWarning := warningFromConflictingWrite(t)
	offlineWarning := warningFromOfflineWrite(t)

	if divergedWarning == offlineWarning {
		t.Fatalf("a diverged push and an offline push produced the same message: %q", divergedWarning)
	}
	if !strings.Contains(divergedWarning, "diverged") {
		t.Errorf("divergence warning = %q, want it to name the divergence", divergedWarning)
	}
	if !strings.Contains(divergedWarning, "gage sync") {
		t.Errorf("divergence warning = %q, want it to point at `gage sync`", divergedWarning)
	}
	if strings.Contains(offlineWarning, "diverged") {
		t.Errorf("offline warning = %q, want it not to claim a divergence", offlineWarning)
	}
	if !strings.Contains(offlineWarning, "could not reach origin") {
		t.Errorf("offline warning = %q, want it to say the remote was unreachable", offlineWarning)
	}
}

// warningFromConflictingWrite performs a write whose push is rejected for
// a real conflict, and returns what the human was told.
func warningFromConflictingWrite(t *testing.T) string {
	t.Helper()

	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	entryID, err := v.Insert(sampleEntry(time.Now()), false, &id)
	if err != nil {
		t.Fatal(err)
	}
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, entryFileName(t, v), "the other device's ciphertext", "conflicting edit")

	p := promptedBy(t, id)
	p.warnings = nil

	e := sampleEntry(time.Now())
	e.Value = "rotated-locally"
	if err := v.Update(entryID, e, &id); err != nil {
		t.Fatal(err)
	}
	if len(p.warnings) != 1 {
		t.Fatalf("Warn called %d times on a conflicting write, want 1: %q", len(p.warnings), p.warnings)
	}
	return p.warnings[0]
}

// warningFromOfflineWrite performs a write whose push can't reach the
// remote, and returns what the human was told.
func warningFromOfflineWrite(t *testing.T) string {
	t.Helper()

	v, id, _ := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	v.remoteSyncer = &fakeSyncer{pushErr: syncerr.ClassifyPush(&net.OpError{
		Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused"),
	})}

	p := promptedBy(t, id)
	p.warnings = nil

	if _, err := v.Insert(sampleEntry(time.Now()), false, &id); err != nil {
		t.Fatal(err)
	}
	if len(p.warnings) != 1 {
		t.Fatalf("Warn called %d times on an offline write, want 1: %q", len(p.warnings), p.warnings)
	}
	return p.warnings[0]
}

// TestRecipientFileDivergenceConflictsRatherThanUnioning is the
// security-relevant test of this milestone.
//
// Two devices each adding a *different* recipient produce two different
// added lines, which an ordinary line-level merge would combine happily —
// leaving an access list neither device wrote and no unreviewed change
// for M10's trust cache to catch. It has to conflict instead.
func TestRecipientFileDivergenceConflictsRatherThanUnioning(t *testing.T) {
	for _, path := range []string{".age-recipients", ".gage/config.toml"} {
		t.Run(path, func(t *testing.T) {
			v, id, remote := newSyncVault(t, "personal", "laptop-1")
			defer func() { _ = id.Close() }()

			original := readVaultFile(t, v, path)

			other := gittest.NewDevice(t, remote)
			other.WriteCommitPush(t, path, original+"\n# added by the other device\n", "the other device's recipient change")

			// This device adds a different line to the same file.
			writeVaultFile(t, v, path, original+"\n# added by this device\n")
			if _, err := gitrepo.CommitAll(v.Path, "this device's recipient change"); err != nil {
				t.Fatal(err)
			}

			report, err := v.Push(context.Background())
			if err == nil {
				t.Fatal("Push merged a recipient-file divergence, want a conflict")
			}
			if !errors.Is(err, syncerr.ErrDiverged) {
				t.Errorf("Push error = %v, want it to classify as diverged", err)
			}
			if report.ConflictKind() != ConflictRecipients {
				t.Errorf("ConflictKind = %v, want ConflictRecipients — a recipient file is the more severe case",
					report.ConflictKind())
			}

			// The local file is exactly what this device wrote: no union,
			// no other device's line, nothing merged in.
			after := readVaultFile(t, v, path)
			if strings.Contains(after, "added by the other device") {
				t.Errorf("%s gained the other device's line without anyone reviewing it:\n%s", path, after)
			}
			if !strings.Contains(after, "added by this device") {
				t.Errorf("%s lost this device's own change:\n%s", path, after)
			}
		})
	}
}

// TestInitWritesGitattributesForBothRecipientFiles asserts the committed
// file M1 writes still says what the merge rules depend on. gage's own
// merge enforces the rule independently (see gitrepo.MergeRemote), but
// this file is what binds the real `git` a human may run inside the
// vault, so it has to stay correct too.
func TestInitWritesGitattributesForBothRecipientFiles(t *testing.T) {
	v, id, _ := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	content := readVaultFile(t, v, ".gitattributes")
	for _, want := range []string{".age-recipients -merge", ".gage/config.toml -merge"} {
		if !strings.Contains(content, want) {
			t.Errorf(".gitattributes is missing %q:\n%s", want, content)
		}
	}
}

// TestSyncTakesTheWriteLock: a sync is not a read. A fast-forward resets
// the working tree and a merge rewrites files in it, so it needs the same
// exclusion a write does — otherwise it can land on top of another
// process's in-flight insert.
//
// Follows TestInsertBlocksOnAConcurrentlyHeldWriteLock's shape: hold the
// lock externally, then assert the operation is still waiting rather than
// having gone ahead without it.
func TestSyncTakesTheWriteLock(t *testing.T) {
	v, id, _ := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	path, err := LockFilePath(v.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := vaultlock.Acquire(path, 0)
	if err != nil {
		t.Fatalf("acquiring the lock externally: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := v.Pull(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("Pull returned (err=%v) while the write lock was held elsewhere; it never waited for it", err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("Pull after the lock was released: %v", err)
	}
}

// TestWriteDoesNotDeadlockOnItsOwnPush is the other side of that lock:
// the automatic push runs *inside* the write's own lock, so it must use
// the unlocked form. Taking the lock again would leave every write
// waiting on itself until the timeout.
func TestWriteDoesNotDeadlockOnItsOwnPush(t *testing.T) {
	v, id, _ := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	done := make(chan error, 1)
	go func() {
		_, err := v.Insert(sampleEntry(time.Now()), false, &id)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a write with a remote never completed; the push inside it is waiting on the lock the write already holds")
	}
}

func TestSyncOnAVaultWithNoRemoteDoesNothing(t *testing.T) {
	v, id := newEntryTestVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()

	report, err := v.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync on a local-only vault: %v", err)
	}
	if report.Pulled || report.Pushed || report.Merged || report.Diverged {
		t.Errorf("a local-only vault reported sync activity: %+v", report)
	}
}

func readVaultFile(t *testing.T, v *Vault, path string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(v.Path, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeVaultFile(t *testing.T, v *Vault, path, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(v.Path, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
