package gage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/gittest"
	"github.com/denmark/gage/internal/gage/vaultlock"
)

// ---------------------------------------------------------------------
// E1b — the N-recipient write
//
// AddRecipient's lock-held body is now a form that takes a slice, can
// delete paths in the same commit, and lets its caller say what a failed
// push actually cost. E4's `recipient approve` is the second caller;
// AddRecipient is the one-recipient one.
//
// These tests drive the form directly rather than through a command,
// because there is no command on it yet and a milestone that ships an
// untested function has shipped nothing anyone can rely on. Everything
// M9 asserts about `recipient add` stays where it is, untouched — that
// suite is this milestone's primary assertion that nothing observable
// changed.
// ---------------------------------------------------------------------

// addBatch drives the N-recipient form the way ApproveEnrollments will:
// take the vault write lock, then call the lock-held body inside it.
//
// The wrapper lives in the test rather than in the library on purpose —
// "the body assumes the lock is already held" is exactly what lets
// approval fetch and re-verify sealed requests under the same lock,
// so a helper that took the lock for its caller would be testing a
// shape E4 cannot use.
func addBatch(t *testing.T, v *Vault, ident *Identity, add []VaultRecipient, w recipientWrite) (int, string, error) {
	t.Helper()

	var (
		reencrypted int
		commit      string
	)
	err := v.withVaultWrite(ident.warnTo(), func() error {
		var err error
		reencrypted, commit, err = v.addRecipientsLocked(add, w, ident)
		return err
	})
	return reencrypted, commit, err
}

// batchOf turns test devices into the recipient list a batch adds.
func batchOf(devices ...testDevice) []VaultRecipient {
	out := make([]VaultRecipient, 0, len(devices))
	for _, d := range devices {
		out = append(out, VaultRecipient{Device: d.name, Pubkey: d.pubkey})
	}
	return out
}

// commitVaultFile writes a file into the vault and commits it, so a test
// has a tracked path to hand the write as something to delete. Untracked
// would prove nothing: withVaultWrite's reset discards those before the
// write starts.
func commitVaultFile(t *testing.T, v *Vault, path, content string) {
	t.Helper()

	full := filepath.Join(v.Path, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(v.Path, "seeding "+path); err != nil {
		t.Fatal(err)
	}
}

func vaultFileExists(t *testing.T, v *Vault, path string) bool {
	t.Helper()

	_, err := os.Stat(filepath.Join(v.Path, filepath.FromSlash(path)))
	if err == nil {
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return false
}

// TestSeveralRecipientsLandInOneCommit is the form's reason for
// existing: N recipients, one commit, and every one of them able to read
// everything that was in the vault before.
//
// The second half is the A19 property carried over to the batch — an
// approval that admitted two devices and left one of them reading only
// the entries written afterwards would be the partial-recipient state
// A19 exists to make unreachable.
func TestSeveralRecipientsLandInOneCommit(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")
	tablet := newTestDevice(t, "personal", "tablet-1")

	id := unlockAs(t, v, laptop)
	entries := seedEntries(t, v, &id, 3)

	before := commitCount(t, v)
	n, hash, err := addBatch(t, v, &id, batchOf(phone, tablet),
		recipientWrite{message: "gage: recipient approve phone-1, tablet-1"})
	if err != nil {
		t.Fatalf("adding two recipients: %v", err)
	}
	_ = id.Close()

	if got := commitCount(t, v); got != before+1 {
		t.Errorf("commit count = %d, want %d — a batch is one commit however many recipients it carries", got, before+1)
	}
	if n != len(entries) {
		t.Errorf("reencrypted = %d, want %d — a batch rewrites the whole vault, once", n, len(entries))
	}
	if hash != headHash(t, v) {
		t.Errorf("returned commit = %s, want HEAD %s", hash, headHash(t, v))
	}

	paths := commitPaths(t, v, hash)
	if !listHas(paths, ".age-recipients") || !listHas(paths, ".gage/config.toml") {
		t.Errorf("the batch's commit touched %v, want both recipient-defining files", paths)
	}
	for _, entryID := range entries {
		if want := "entries/" + entryID.String() + ".age"; !listHas(paths, want) {
			t.Errorf("the batch's commit does not contain %s (it contains %v)", want, paths)
		}
	}

	for _, d := range []testDevice{phone, tablet} {
		if !listHas(readRecipientsFile(t, v), d.pubkey) {
			t.Errorf(".age-recipients does not list %s", d.name)
		}
		if !listHas(configPubkeys(t, v), d.pubkey) {
			t.Errorf(".gage/config.toml does not list %s", d.name)
		}

		newID := unlockAs(t, v, d)
		for _, entryID := range entries {
			if _, err := v.ReadEntry(entryID, &newID); err != nil {
				t.Errorf("%s cannot read entry %s written before the batch: %v — "+
					"every recipient a batch admits reads the whole vault", d.name, entryID, err)
			}
		}
		_ = newID.Close()
	}
}

// TestOneReencryptionPassForTheBatch pins the count rather than the
// clock. N passes over the same entries reach the same end state, so the
// only thing separating "one pass" from "one per recipient" is how many
// times each entry was rewritten and how many commits landed.
func TestOneReencryptionPassForTheBatch(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")
	tablet := newTestDevice(t, "personal", "tablet-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()
	entries := seedEntries(t, v, &id, 3)

	rewrites := 0
	v.onReencryptEntry = func(int) { rewrites++ }
	defer func() { v.onReencryptEntry = nil }()

	before := commitCount(t, v)
	if _, _, err := addBatch(t, v, &id, batchOf(phone, tablet),
		recipientWrite{message: "gage: recipient approve phone-1, tablet-1"}); err != nil {
		t.Fatalf("adding two recipients: %v", err)
	}

	if rewrites != len(entries) {
		t.Errorf("entries rewritten = %d, want %d — two recipients must not mean two passes over the vault",
			rewrites, len(entries))
	}
	if got := commitCount(t, v); got != before+1 {
		t.Errorf("commit count = %d, want %d", got, before+1)
	}
}

// TestOneTrustQuestionForTheBatch: M10's recipient-change question is
// asked at most once per call, however many recipients ride in it.
//
// The vault is given a genuinely unreviewed change first — the same
// pulled-from-elsewhere state M10's own tests use — because that is the
// only thing that makes the question fire at all.
func TestOneTrustQuestionForTheBatch(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")
	tablet := newTestDevice(t, "personal", "tablet-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	seedEntries(t, v, &id, 2)
	_ = id.Close()

	stranger := newTrustKey(t)
	commitRoutineRecipientChange(t, v, "stranger", stranger)

	p := newTrustPrompter(true)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	if _, _, err := addBatch(t, v, &id, batchOf(phone, tablet),
		recipientWrite{message: "gage: recipient approve phone-1, tablet-1"}); err != nil {
		t.Fatalf("adding two recipients over a reviewed change: %v", err)
	}
	if len(p.changes) != 1 {
		t.Fatalf("asked about the pulled change %d times, want exactly 1 for the whole batch", len(p.changes))
	}

	// And the cache regeneration is once too: the very next write asks
	// nothing, about the pulled key or about either of the two just
	// added.
	p2 := newTrustPrompter(false)
	id2 := unlockAsWith(t, v, laptop, p2)
	defer func() { _ = id2.Close() }()
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id2); err != nil {
		t.Fatalf("the write after a batch was blocked: %v", err)
	}
	if len(p2.changes) != 0 {
		t.Errorf("asked %d times after the batch regenerated the cache, want 0", len(p2.changes))
	}
}

// TestBatchTakesOneLockAndOneDirtyTreeReset covers the two things
// withVaultWrite does, both of which have to happen once for the
// operation rather than once per recipient.
//
// The reset is observed through its warning: it is never silent, so N
// resets would tell someone their leftovers were discarded N times for
// one approval. The lock is observed from inside the pass, at the point
// where an implementation that took a lock per recipient would have
// released it in between.
func TestBatchTakesOneLockAndOneDirtyTreeReset(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")
	tablet := newTestDevice(t, "personal", "tablet-1")

	p := &confirmingPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}, confirm: true}
	id := unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	entries := seedEntries(t, v, &id, 3)
	dirtied := dirtyEntries(t, v, entries[0])

	lockPath, err := LockFilePath(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}

	var observations []error
	v.onReencryptEntry = func(int) {
		lock, err := vaultlock.Acquire(lockPath, 0)
		if err == nil {
			_ = lock.Release()
		}
		observations = append(observations, err)
	}
	defer func() { v.onReencryptEntry = nil }()

	p.warnings = nil
	if _, _, err := addBatch(t, v, &id, batchOf(phone, tablet),
		recipientWrite{message: "gage: recipient approve phone-1, tablet-1"}); err != nil {
		t.Fatalf("adding two recipients over a dirty tree: %v", err)
	}

	resets := 0
	for _, w := range p.warnings {
		if strings.Contains(w, "discarded uncommitted changes") {
			resets++
		}
	}
	if resets != 1 {
		t.Errorf("the dirty-tree reset warned %d times for one batch, want 1: %q", resets, p.warnings)
	}
	if resets == 1 && !strings.Contains(strings.Join(p.warnings, "\n"), dirtied) {
		t.Errorf("the reset warning does not name %s: %q", dirtied, p.warnings)
	}

	if len(observations) == 0 {
		t.Fatal("the per-entry hook never fired; nothing was observed about the lock")
	}
	for i, err := range observations {
		var contended *vaultlock.ContendedError
		if !errors.As(err, &contended) {
			t.Fatalf("acquiring the vault lock during entry %d = %v, want a *vaultlock.ContendedError — "+
				"the batch released the lock partway through", i, err)
		}
	}
}

// TestExtraPathsAreDeletedInTheSameCommit is the second thing E4 needs:
// the approved requests' files disappear in the very commit that admits
// their devices, not in one after it.
//
// Asserted with an ordinary tracked file rather than a pending request,
// so this milestone's tests do not depend on a feature two milestones
// away.
func TestExtraPathsAreDeletedInTheSameCommit(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()
	entries := seedEntries(t, v, &id, 2)

	commitVaultFile(t, v, "pending/one.txt", "a request-shaped file")
	commitVaultFile(t, v, "pending/two.txt", "another one")

	before := commitCount(t, v)
	_, hash, err := addBatch(t, v, &id, batchOf(phone), recipientWrite{
		message:     "gage: recipient approve phone-1",
		deletePaths: []string{"pending/one.txt", "pending/two.txt"},
	})
	if err != nil {
		t.Fatalf("adding a recipient with paths to delete: %v", err)
	}

	if got := commitCount(t, v); got != before+1 {
		t.Errorf("commit count = %d, want %d — the deletions ride in the add's own commit", got, before+1)
	}
	paths := commitPaths(t, v, hash)
	for _, want := range []string{"pending/one.txt", "pending/two.txt", ".age-recipients", ".gage/config.toml"} {
		if !listHas(paths, want) {
			t.Errorf("the commit touched %v, want it to include %s", paths, want)
		}
	}
	for _, entryID := range entries {
		if want := "entries/" + entryID.String() + ".age"; !listHas(paths, want) {
			t.Errorf("the commit does not contain the re-encrypted %s (it contains %v)", want, paths)
		}
	}
	for _, gone := range []string{"pending/one.txt", "pending/two.txt"} {
		if vaultFileExists(t, v, gone) {
			t.Errorf("%s is still on disk after the write that was told to delete it", gone)
		}
	}

	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after the batch; the deletions must be committed, not left staged")
	}
}

// TestFailedPushClauseIsTheCallersToSay covers both halves of the third
// parameter: a caller that supplies a clause is warned with it, and a
// caller that supplies none — every M9 caller — gets M9's own sentence
// byte for byte.
//
// The second half is what keeps this milestone's "nothing observable
// changed" claim true, so it is asserted as an exact string rather than
// as a substring.
func TestFailedPushClauseIsTheCallersToSay(t *testing.T) {
	t.Run("the default is M9's sentence", func(t *testing.T) {
		v, id, _ := newSyncVault(t, "personal", "laptop-1")
		defer func() { _ = id.Close() }()
		phone := newTestDevice(t, "personal", "phone-1")

		v.remoteSyncer = &fakeSyncer{pushErr: unreachableError()}
		p := promptedBy(t, id)
		p.warnings = nil

		if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
			t.Fatalf("AddRecipient: %v", err)
		}

		const want = "gage: the write is committed locally but not pushed: could not reach origin"
		if len(p.warnings) != 1 || p.warnings[0] != want {
			t.Errorf("warnings = %q, want exactly one: %q", p.warnings, want)
		}
	})

	t.Run("a supplied clause is what the human is told", func(t *testing.T) {
		v, id, _ := newSyncVault(t, "personal", "laptop-1")
		defer func() { _ = id.Close() }()
		phone := newTestDevice(t, "personal", "phone-1")

		v.remoteSyncer = &fakeSyncer{pushErr: unreachableError()}
		p := promptedBy(t, id)
		p.warnings = nil

		const clause = "the approval is committed locally but not pushed, so phone-1 is still locked out"
		if _, _, err := addBatch(t, v, &id, batchOf(phone), recipientWrite{
			message:    "gage: recipient approve phone-1",
			pushClause: clause,
		}); err != nil {
			t.Fatalf("adding a recipient with a push clause: %v", err)
		}

		want := "gage: " + clause + ": could not reach origin"
		if len(p.warnings) != 1 || p.warnings[0] != want {
			t.Errorf("warnings = %q, want exactly one: %q", p.warnings, want)
		}
	})
}

// TestDivergedPushIgnoresTheSuppliedClause: the clause describes what
// did not get published, and a diverged push published nothing for a
// different reason that already has its own established sentence.
func TestDivergedPushIgnoresTheSuppliedClause(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	defer func() { _ = id.Close() }()
	phone := newTestDevice(t, "personal", "phone-1")

	if _, err := v.Insert(sampleEntry(time.Now()), false, &id); err != nil {
		t.Fatal(err)
	}

	// Another device changes the same entry file, so this side's
	// re-encryption of it diverges rather than merging cleanly.
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, entryFileName(t, v), "the other device's ciphertext", "conflicting edit")

	p := promptedBy(t, id)
	p.warnings = nil

	const clause = "the approval is committed locally but not pushed"
	if _, _, err := addBatch(t, v, &id, batchOf(phone), recipientWrite{
		message:    "gage: recipient approve phone-1",
		pushClause: clause,
	}); err != nil {
		t.Fatalf("adding a recipient into a divergence: %v", err)
	}

	if len(p.warnings) != 1 {
		t.Fatalf("Warn called %d times on a diverged push, want 1: %q", len(p.warnings), p.warnings)
	}
	got := p.warnings[0]
	if strings.Contains(got, clause) {
		t.Errorf("the diverged push used the caller's clause: %q", got)
	}
	if !strings.Contains(got, "diverged") {
		t.Errorf("warning = %q, want the established divergence sentence", got)
	}
	if got != warningFromConflictingWrite(t) {
		t.Errorf("warning = %q, want the same divergence report every other write produces: %q",
			got, warningFromConflictingWrite(t))
	}
}

// TestADuplicateWithinTheBatchIsRefused is the check M9 could not have
// had: a batch of one has nothing to collide with, so the existing
// duplicate check only ever compared against the committed list.
//
// Both shapes are refused, and neither writes anything — a request list
// carrying the same device twice must not become a recipient list
// carrying it twice.
func TestADuplicateWithinTheBatchIsRefused(t *testing.T) {
	phoneKeyTwice := func(t *testing.T) (string, []VaultRecipient) {
		phone := newTestDevice(t, "personal", "phone-1")
		return phone.pubkey, []VaultRecipient{
			{Device: "phone-1", Pubkey: phone.pubkey},
			{Device: "phone-2", Pubkey: phone.pubkey},
		}
	}
	sameDeviceName := func(t *testing.T) (string, []VaultRecipient) {
		a := newTestDevice(t, "personal", "phone-1")
		b := newTestDevice(t, "personal", "phone-2")
		return a.pubkey, []VaultRecipient{
			{Device: "phone-1", Pubkey: a.pubkey},
			{Device: "phone-1", Pubkey: b.pubkey},
		}
	}

	cases := map[string]func(*testing.T) (string, []VaultRecipient){
		"the same key twice":        phoneKeyTwice,
		"two devices with one name": sameDeviceName,
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
			id := unlockAs(t, v, laptop)
			defer func() { _ = id.Close() }()
			seedEntries(t, v, &id, 2)

			firstKey, batch := build(t)

			before := commitCount(t, v)
			head := headHash(t, v)
			rewrites := 0
			v.onReencryptEntry = func(int) { rewrites++ }
			defer func() { v.onReencryptEntry = nil }()

			_, _, err := addBatch(t, v, &id, batch, recipientWrite{message: "gage: recipient approve"})
			if !errors.Is(err, ErrRecipientExists) {
				t.Fatalf("a batch containing a duplicate = %v, want ErrRecipientExists", err)
			}
			if code := exitcode.CodeOf(err); code != exitcode.Conflict {
				t.Errorf("exit code = %v, want conflict", code)
			}
			if rewrites != 0 {
				t.Errorf("%d entries were re-encrypted before the refusal; the check runs before anything is written", rewrites)
			}
			if got := commitCount(t, v); got != before || headHash(t, v) != head {
				t.Errorf("a refused batch committed something: count %d (want %d)", got, before)
			}
			if listHas(readRecipientsFile(t, v), firstKey) {
				t.Error("the first half of a refused batch was written anyway")
			}
		})
	}
}

// TestABatchMemberAlreadyInTheVaultIsRefused: the pre-existing refusal,
// unchanged, at the level that does the writing.
//
// E4 turns this case into a no-op before it gets here, by dropping the
// already-a-recipient request from the batch. That is a nicety on top;
// this is what makes the check authoritative.
func TestABatchMemberAlreadyInTheVaultIsRefused(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")
	tablet := newTestDevice(t, "personal", "tablet-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()
	seedEntries(t, v, &id, 2)

	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}

	before := commitCount(t, v)
	_, _, err := addBatch(t, v, &id, batchOf(phone, tablet),
		recipientWrite{message: "gage: recipient approve phone-1, tablet-1"})
	if !errors.Is(err, ErrRecipientExists) {
		t.Fatalf("a batch naming an existing recipient = %v, want ErrRecipientExists", err)
	}
	if got := commitCount(t, v); got != before {
		t.Errorf("commit count = %d, want %d — the whole batch is refused, not the part that was new", got, before)
	}
	if listHas(readRecipientsFile(t, v), tablet.pubkey) {
		t.Error("the new half of a refused batch was written anyway")
	}
}

// ---------------------------------------------------------------------
// Atomicity — the property the extraction must not lose
// ---------------------------------------------------------------------

// TestInterruptedBatchLeavesHEADUntouched is M9's central guarantee
// re-asserted on the new shape: a failure partway leaves no commit, no
// recipients, no rewritten entries and no deleted paths.
//
// The paths are the new half. A commit holding the deletions but not the
// recipients — or the reverse — is precisely the half-migrated state the
// single commit exists to rule out.
func TestInterruptedBatchLeavesHEADUntouched(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")
	tablet := newTestDevice(t, "personal", "tablet-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()
	seedEntries(t, v, &id, 4)
	commitVaultFile(t, v, "pending/one.txt", "a request-shaped file")

	beforeHash := headHash(t, v)
	beforeCount := commitCount(t, v)

	v.onReencryptEntry = crashAfter(2)
	crashed := runAndRecoverCrash(t, func() {
		_, _, _ = addBatch(t, v, &id, batchOf(phone, tablet), recipientWrite{
			message:     "gage: recipient approve phone-1, tablet-1",
			deletePaths: []string{"pending/one.txt"},
		})
	})
	v.onReencryptEntry = nil
	if !crashed {
		t.Fatal("the crash seam never fired; the batch finished without being interrupted")
	}

	if got := headHash(t, v); got != beforeHash {
		t.Errorf("HEAD = %s after an interrupted batch, want %s (untouched)", got, beforeHash)
	}
	if got := commitCount(t, v); got != beforeCount {
		t.Errorf("commit count = %d after an interrupted batch, want %d", got, beforeCount)
	}
	for _, d := range []testDevice{phone, tablet} {
		if listHas(readRecipientsFile(t, v), d.pubkey) {
			t.Errorf(".age-recipients names %s after an interrupted batch; the recipient files are written last", d.name)
		}
	}

	// The next write resets the leftovers, and what it commits is its own
	// work only: no recipient change, and the file the interrupted run
	// was going to delete is back.
	if _, err := v.Insert(sampleEntry(time.Now().Add(time.Hour)), true, &id); err != nil {
		t.Fatalf("the write after an interrupted batch: %v", err)
	}
	if !vaultFileExists(t, v, "pending/one.txt") {
		t.Error("the path the interrupted batch was told to delete is gone; nothing it did may survive")
	}
	paths := commitPaths(t, v, headHash(t, v))
	for _, unwanted := range []string{"pending/one.txt", ".age-recipients", ".gage/config.toml"} {
		if listHas(paths, unwanted) {
			t.Errorf("the next write's commit contains %s (it touched %v); the interrupted batch's leftovers were folded in",
				unwanted, paths)
		}
	}
}

// TestABatchWhoseDeletionFailsCommitsNothing: the deletion is part of
// the write, not a tidy-up after it. A path that cannot be removed has
// to abort the whole thing rather than leave a commit that admits the
// recipients and leaves the file behind — which for E4 is an approval
// whose request is still pending on every device that pulls it.
//
// The failure is produced honestly: a non-empty directory is a path
// os.Remove refuses on every platform gage supports, and a
// implementation reaching for RemoveAll instead would take the vault's
// own files with it.
func TestABatchWhoseDeletionFailsCommitsNothing(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()
	seedEntries(t, v, &id, 2)
	commitVaultFile(t, v, "pending/one.txt", "a request-shaped file")

	beforeHash := headHash(t, v)
	beforeCount := commitCount(t, v)

	_, _, err := addBatch(t, v, &id, batchOf(phone), recipientWrite{
		message: "gage: recipient approve phone-1",
		// A directory with a file in it: removable as a path, not as a
		// file.
		deletePaths: []string{"pending"},
	})
	if err == nil {
		t.Fatal("a batch whose deletion could not be performed reported success")
	}
	if !strings.Contains(err.Error(), "pending") {
		t.Errorf("error = %v, want it to name the path it could not delete", err)
	}

	if got := headHash(t, v); got != beforeHash {
		t.Errorf("HEAD = %s after a failed deletion, want %s (untouched)", got, beforeHash)
	}
	if got := commitCount(t, v); got != beforeCount {
		t.Errorf("commit count = %d after a failed deletion, want %d — no commit may be left behind", got, beforeCount)
	}
	if listHas(readRecipientsFile(t, v), phone.pubkey) {
		t.Error(".age-recipients names the recipient after a failed deletion")
	}
	if !vaultFileExists(t, v, "pending/one.txt") {
		t.Error("the file inside the undeletable path was removed anyway")
	}
}

// TestABatchThatFailsAfterDeletingCommitsNothing closes the window the
// two tests above leave open. TestInterruptedBatchLeavesHEADUntouched
// dies during the re-encryption, which is *before* deleteVaultPaths runs
// at all, and TestABatchWhoseDeletionFailsCommitsNothing dies inside it —
// so neither exercises a write that deleted the paths successfully and
// then failed on its way to the commit. Without this, "no paths deleted"
// holds in those tests only because the deletion never happened, which is
// a weaker claim than the one commitRecipientList makes.
//
// What must hold is that the deletion is *uncommitted*, not undone: HEAD
// keeps the file, the working tree does not, and the next write's
// dirty-tree reset restores it rather than folding it into that write's
// own commit. That is the whole reason deleteVaultPaths needs no rollback
// of its own.
//
// The failure is produced through the existing onReencryptEntry seam and
// is a real one: on the last entry, .gage/config.toml is overwritten with
// invalid TOML, so writeRecipientFiles fails in readVaultConfig — after
// the deletion, before the commit.
//
// Corrupting *that* file rather than .age-recipients is the point. A
// .age-recipients replaced by a directory would fail the write too, but it
// also makes the tree unstageable, so "nothing was committed" would be
// the seam's doing rather than the code's. Invalid TOML leaves every path
// in the tree an ordinary file that CommitAll would happily sweep up if
// anything asked it to — and it needs no repair afterwards, because the
// reset under test is what restores it.
func TestABatchThatFailsAfterDeletingCommitsNothing(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()
	entries := seedEntries(t, v, &id, 2)
	commitVaultFile(t, v, "pending/one.txt", "a request-shaped file")

	beforeHash := headHash(t, v)
	beforeCount := commitCount(t, v)

	// Recorded rather than fataled: this runs inside the write, and
	// killing the goroutine from in there would report a broken seam as a
	// broken guarantee.
	var sabotage error
	v.onReencryptEntry = func(done int) {
		if done < len(entries) || sabotage != nil {
			return
		}
		sabotage = os.WriteFile(v.vaultConfigPath(), []byte("this is not toml\x00"), 0o600)
	}
	defer func() { v.onReencryptEntry = nil }()

	_, _, err := addBatch(t, v, &id, batchOf(phone), recipientWrite{
		message:     "gage: recipient approve phone-1",
		deletePaths: []string{"pending/one.txt"},
	})
	v.onReencryptEntry = nil
	if sabotage != nil {
		t.Fatalf("setting up the failure: %v", sabotage)
	}
	if err == nil {
		t.Fatal("a batch that could not write the recipient files reported success")
	}

	// The deletion did happen — otherwise this test proves nothing about
	// the window after it.
	if vaultFileExists(t, v, "pending/one.txt") {
		t.Fatal("the write failed before the deletion; the window after it is still unexercised")
	}
	if got := headHash(t, v); got != beforeHash {
		t.Errorf("HEAD = %s after a failure past the deletion, want %s (untouched)", got, beforeHash)
	}
	if got := commitCount(t, v); got != beforeCount {
		t.Errorf("commit count = %d, want %d — a failure past the deletion may not salvage a commit", got, beforeCount)
	}

	// An ordinary write next. Its reset is what makes the uncommitted
	// deletion harmless, and it also restores the file the seam corrupted.
	if _, err := v.Insert(sampleEntry(time.Now().Add(time.Hour)), true, &id); err != nil {
		t.Fatalf("the write after a failure past the deletion: %v", err)
	}
	if !vaultFileExists(t, v, "pending/one.txt") {
		t.Error("the reset did not restore the path the failed batch deleted; an uncommitted deletion must not survive")
	}
	if listHas(readRecipientsFile(t, v), phone.pubkey) {
		t.Error(".age-recipients still names the recipient the failed batch was adding")
	}
	if listHas(configPubkeys(t, v), phone.pubkey) {
		t.Error(".gage/config.toml names the recipient the failed batch was adding")
	}
	paths := commitPaths(t, v, headHash(t, v))
	for _, unwanted := range []string{"pending/one.txt", ".age-recipients", ".gage/config.toml"} {
		if listHas(paths, unwanted) {
			t.Errorf("the next write's commit contains %s (it touched %v); the failed batch's leftovers were folded in",
				unwanted, paths)
		}
	}
}

// TestADeclinedTrustQuestionAbortsTheWholeBatch: the same nothing as
// M9's declined add — no commit, no partial re-encryption, no cache
// regeneration — now for a call carrying several recipients.
func TestADeclinedTrustQuestionAbortsTheWholeBatch(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")
	tablet := newTestDevice(t, "personal", "tablet-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	seedEntries(t, v, &id, 2)
	_ = id.Close()

	stranger := newTrustKey(t)
	commitRoutineRecipientChange(t, v, "stranger", stranger)
	commitVaultFile(t, v, "pending/one.txt", "a request-shaped file")

	cacheBefore, _ := trustCacheOf(t, v, laptop)
	beforeHash := headHash(t, v)
	beforeCount := commitCount(t, v)

	rewrites := 0
	v.onReencryptEntry = func(int) { rewrites++ }
	defer func() { v.onReencryptEntry = nil }()

	p := newTrustPrompter(false)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	_, _, err := addBatch(t, v, &id, batchOf(phone, tablet), recipientWrite{
		message:     "gage: recipient approve phone-1, tablet-1",
		deletePaths: []string{"pending/one.txt"},
	})
	if !errors.Is(err, ErrRecipientChangeDeclined) {
		t.Fatalf("a batch over an unreviewed change = %v, want ErrRecipientChangeDeclined", err)
	}
	if len(p.changes) != 1 {
		t.Errorf("asked %d times before declining, want 1", len(p.changes))
	}
	if rewrites != 0 {
		t.Errorf("%d entries were re-encrypted before the refusal", rewrites)
	}
	if got := headHash(t, v); got != beforeHash || commitCount(t, v) != beforeCount {
		t.Errorf("a declined batch committed something: HEAD %s, want %s", got, beforeHash)
	}
	if !vaultFileExists(t, v, "pending/one.txt") {
		t.Error("a declined batch deleted the paths it was handed")
	}
	cacheAfter, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("the trust cache vanished")
	}
	if string(cacheAfter.KnownConfig) != string(cacheBefore.KnownConfig) {
		t.Error("a declined batch regenerated the trust cache")
	}
}

// TestAddRecipientIsTheOneRecipientCaller: the shape changed underneath
// it and its own surface did not. M9's suite is the primary assertion
// here; this pins the two things that suite does not state directly —
// the commit message, and that a single add still carries exactly one
// recipient into it.
func TestAddRecipientIsTheOneRecipientCaller(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()
	seedEntries(t, v, &id, 2)

	before, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}

	change, err := v.AddRecipient("phone-1", phone.pubkey, &id)
	if err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}

	message, _, _, err := gitrepo.HeadCommit(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "gage: recipient add phone-1 (reencrypt)"; message != want {
		t.Errorf("commit message = %q, want %q", message, want)
	}

	after, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Errorf("recipients = %d, want %d — one add adds one", len(after), len(before)+1)
	}
	if change.Device != "phone-1" || change.Pubkey != phone.pubkey {
		t.Errorf("RecipientChange = %+v, want it to name the added device", change)
	}
	if change.Commit != headHash(t, v) {
		t.Errorf("RecipientChange.Commit = %s, want HEAD %s", change.Commit, headHash(t, v))
	}
}
