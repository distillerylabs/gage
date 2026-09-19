package gage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/vaultlock"
)

// errSimulatedCrash is the panic value the crash seam raises. A panic
// rather than an error return, and that is the whole point: a returned
// error would let --reencrypt run its own cleanup and the test would
// then be proving that cleanup works, not that an *interrupted* run is
// invisible. Nothing in the reencrypt path recovers, so the stack
// unwinds with no tidy-up beyond the deferred lock release — which is
// exactly what a kill -9 leaves behind, since the OS releases an flock
// when the process dies.
var errSimulatedCrash = errors.New("simulated crash partway through --reencrypt")

// crashAfter returns a hook for Vault.onReencryptEntry that lets n
// entries be written into the working tree and then dies.
func crashAfter(n int) func(done int) {
	return func(done int) {
		if done >= n {
			panic(errSimulatedCrash)
		}
	}
}

// runAndRecoverCrash runs fn and reports whether it died on the
// simulated crash. Any other panic is re-raised, so a real bug in the
// implementation surfaces as a real failure instead of being swallowed
// as "the crash we asked for."
func runAndRecoverCrash(t *testing.T, fn func()) (crashed bool) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if err, ok := r.(error); ok && errors.Is(err, errSimulatedCrash) {
			crashed = true
			return
		}
		panic(r)
	}()
	fn()
	return false
}

// seedEntries inserts n entries and returns their ids, so an atomicity
// test has more than one thing to be half-way through.
func seedEntries(t *testing.T, v *Vault, ident *Identity, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		e := sampleEntry(time.Now().Add(time.Duration(i) * time.Minute))
		e.Title = "entry-" + string(rune('a'+i))
		id, err := v.Insert(e, true, ident)
		if err != nil {
			t.Fatalf("seeding entry %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// ---------------------------------------------------------------------
// --reencrypt atomicity
// ---------------------------------------------------------------------

// TestInterruptedReencryptLeavesHEADUntouched is the milestone's central
// claim: "A simulated crash/interruption partway through --reencrypt
// leaves HEAD byte-identical to before the command ran — no partial
// commit, no leftover dirty entries/ once gage next starts."
//
// Both halves are asserted: HEAD immediately after the crash, and the
// working tree after the *next* gage write, which is where the leftover
// is supposed to disappear.
func TestInterruptedReencryptLeavesHEADUntouched(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAs(t, v, laptop)
	seedEntries(t, v, &id, 4)
	_ = id.Close()

	phone := newTestDevice(t, "personal", "phone-1")
	id = unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()

	beforeHash := headHash(t, v)
	beforeCount := commitCount(t, v)

	v.onReencryptEntry = crashAfter(2)
	crashed := runAndRecoverCrash(t, func() {
		_, _ = v.AddRecipient("phone-1", phone.pubkey, &id)
	})
	v.onReencryptEntry = nil
	if !crashed {
		t.Fatal("the crash seam never fired; --reencrypt finished without being interrupted")
	}

	if got := headHash(t, v); got != beforeHash {
		t.Errorf("HEAD = %s after an interrupted --reencrypt, want %s (untouched)", got, beforeHash)
	}
	if got := commitCount(t, v); got != beforeCount {
		t.Errorf("commit count = %d after an interrupted --reencrypt, want %d (nothing committed)", got, beforeCount)
	}
	if listHas(readRecipientsFile(t, v), phone.pubkey) {
		t.Error(".age-recipients names the new recipient after an interrupted run; the recipient files must be written last")
	}

	// The interruption is expected to leave the working tree dirty —
	// that is what makes the reset on the next write load-bearing. If it
	// somehow doesn't, the rest of this milestone's dirty-tree tests are
	// testing a state that can't occur.
	dirty, err := gitrepo.DirtyPaths(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) == 0 {
		t.Fatal("the interrupted run left a clean tree; the crash seam fired too early to have written anything")
	}

	// The next gage write is where the leftover goes away.
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("Insert after an interrupted --reencrypt: %v", err)
	}
	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("entries/ is still dirty after the next write; the precondition reset never ran")
	}
}

// TestRetryingReencryptAfterAnInterruptionSucceeds is the plan's
// "Retrying --reencrypt after such an interruption produces the same
// correct end state as an uninterrupted run."
func TestRetryingReencryptAfterAnInterruptionSucceeds(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAs(t, v, laptop)
	ids := seedEntries(t, v, &id, 4)
	_ = id.Close()

	phone := newTestDevice(t, "personal", "phone-1")
	id = unlockAs(t, v, laptop)

	baseCount := commitCount(t, v)

	v.onReencryptEntry = crashAfter(2)
	if !runAndRecoverCrash(t, func() { _, _ = v.AddRecipient("phone-1", phone.pubkey, &id) }) {
		t.Fatal("the crash seam never fired")
	}
	v.onReencryptEntry = nil

	change, err := v.AddRecipient("phone-1", phone.pubkey, &id)
	if err != nil {
		t.Fatalf("retrying --reencrypt after an interruption: %v", err)
	}
	_ = id.Close()

	if change.Reencrypted != len(ids) {
		t.Errorf("Reencrypted = %d on the retry, want %d — the retry must start from scratch, not resume", change.Reencrypted, len(ids))
	}
	// The abandoned attempt contributed nothing to history: exactly one
	// commit total, from the successful run.
	if got := commitCount(t, v); got != baseCount+1 {
		t.Errorf("commit count = %d, want %d (the interrupted attempt committed nothing)", got, baseCount+1)
	}

	id2 := unlockAs(t, v, phone)
	defer func() { _ = id2.Close() }()
	for _, entryID := range ids {
		if _, err := v.ReadEntry(entryID, &id2); err != nil {
			t.Errorf("after the retry, the new recipient still cannot read %s: %v", entryID, err)
		}
	}
}

// TestReencryptAbortsWholeOperationWhenOneEntryFails is the plan's "A
// single entry failing during --reencrypt (simulated decrypt/encrypt
// error) aborts the whole operation before any commit and reports which
// entry failed; no entries are left partially migrated."
//
// The failure is produced honestly — one entry file replaced with
// ciphertext this identity genuinely cannot decrypt — rather than
// through a seam, since a corrupt entry is a state that really occurs.
func TestReencryptAbortsWholeOperationWhenOneEntryFails(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAs(t, v, laptop)
	ids := seedEntries(t, v, &id, 3)
	_ = id.Close()

	// Corrupt the middle entry, and commit the corruption so the vault
	// starts this test with a clean tree — otherwise the precondition
	// reset would undo the setup before --reencrypt ever saw it.
	broken := ids[1]
	if err := os.WriteFile(v.entryPath(broken), []byte("not an age file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(v.Path, "test: corrupt one entry"); err != nil {
		t.Fatal(err)
	}

	phone := newTestDevice(t, "personal", "phone-1")
	id = unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()

	beforeHash := headHash(t, v)
	beforeCount := commitCount(t, v)

	_, err := v.AddRecipient("phone-1", phone.pubkey, &id)
	if err == nil {
		t.Fatal("--reencrypt succeeded with an undecryptable entry in the vault")
	}
	if !strings.Contains(err.Error(), broken.String()) {
		t.Errorf("error = %v, want it to name the entry that failed (%s)", err, broken)
	}

	if got := headHash(t, v); got != beforeHash {
		t.Errorf("HEAD = %s after a failed --reencrypt, want %s", got, beforeHash)
	}
	if got := commitCount(t, v); got != beforeCount {
		t.Errorf("commit count = %d after a failed --reencrypt, want %d (nothing committed)", got, beforeCount)
	}
	if listHas(readRecipientsFile(t, v), phone.pubkey) {
		t.Error(".age-recipients gained the new key even though the operation aborted")
	}
	if listHas(configPubkeys(t, v), phone.pubkey) {
		t.Error(".gage/config.toml gained the new key even though the operation aborted")
	}

	// No entry is left partially migrated: every intact entry is still
	// readable by the original recipient and still unreadable by the one
	// that was never actually added.
	id2 := unlockAs(t, v, phone)
	defer func() { _ = id2.Close() }()
	for _, entryID := range ids {
		if entryID == broken {
			continue
		}
		if _, err := v.ReadEntry(entryID, &id2); !errors.Is(err, ErrNotARecipient) {
			t.Errorf("entry %s is readable by the never-added recipient (err=%v); it was partially migrated", entryID, err)
		}
	}
}

// TestReencryptLandsEverythingInExactlyOneCommit is the plan's
// "--reencrypt lands the recipient-list files (.age-recipients/
// config.toml) and every re-encrypted entry in exactly one commit —
// never a commit containing only one side of that pair."
func TestReencryptLandsEverythingInExactlyOneCommit(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAs(t, v, laptop)
	ids := seedEntries(t, v, &id, 3)

	phone := newTestDevice(t, "personal", "phone-1")
	id2 := unlockAs(t, v, laptop)
	_ = id.Close()
	defer func() { _ = id2.Close() }()

	before := commitCount(t, v)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id2); err != nil {
		t.Fatalf("AddRecipient --reencrypt: %v", err)
	}

	if got := commitCount(t, v); got != before+1 {
		t.Fatalf("commit count = %d, want %d — --reencrypt must produce exactly one commit", got, before+1)
	}

	paths := commitPaths(t, v, headHash(t, v))
	for _, want := range []string{".age-recipients", ".gage/config.toml"} {
		if !listHas(paths, want) {
			t.Errorf("the single commit does not contain %s (it contains %v); the pair must never be split", want, paths)
		}
	}
	for _, entryID := range ids {
		want := "entries/" + entryID.String() + ".age"
		if !listHas(paths, want) {
			t.Errorf("the single commit does not contain re-encrypted entry %s (it contains %v)", want, paths)
		}
	}

	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after a successful --reencrypt")
	}
}

// TestReencryptHoldsTheVaultLockThroughout is the plan's "--reencrypt
// holds the vault lock for its whole duration; a concurrent write
// observes contention rather than interleaving."
//
// The observation is made from inside the run, at the point where an
// interleaving implementation would have released the lock between
// entries.
func TestReencryptHoldsTheVaultLockThroughout(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAs(t, v, laptop)
	seedEntries(t, v, &id, 3)
	_ = id.Close()

	phone := newTestDevice(t, "personal", "phone-1")
	id = unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()

	lockPath, err := LockFilePath(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}

	var observations []error
	v.onReencryptEntry = func(done int) {
		// A second acquire with no wait is what another gage process's
		// write would do first. It must fail as contention every time,
		// at every point in the pass.
		lock, err := vaultlock.Acquire(lockPath, 0)
		if err == nil {
			_ = lock.Release()
		}
		observations = append(observations, err)
	}
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient --reencrypt: %v", err)
	}
	v.onReencryptEntry = nil

	if len(observations) == 0 {
		t.Fatal("the per-entry hook never fired; nothing was observed")
	}
	for i, err := range observations {
		var contended *vaultlock.ContendedError
		if !errors.As(err, &contended) {
			t.Fatalf("acquiring the vault lock during entry %d = %v, want a *vaultlock.ContendedError — "+
				"--reencrypt released the lock partway through", i, err)
		}
	}
}

// ---------------------------------------------------------------------
// Dirty working tree
// ---------------------------------------------------------------------

// dirtyEntries leaves the vault's entries/ directory dirty the way an
// interrupted --reencrypt would: an existing entry file overwritten with
// content that was never committed.
func dirtyEntries(t *testing.T, v *Vault, id uuid.UUID) string {
	t.Helper()
	path := v.entryPath(id)
	if err := os.WriteFile(path, []byte("half-written ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	return "entries/" + id.String() + ".age"
}

// TestDirtyWorkTreeIsResetBeforeAnySubsequentWrite is the plan's "A
// dirty entries/ working tree left by a simulated crash is reset to HEAD
// before any subsequent write (insert/edit/generate/rename/--reencrypt)
// proceeds, rather than being folded into that write's commit."
//
// Every write verb is driven, because the requirement is about the
// precondition running on all of them rather than on whichever one was
// implemented first.
func TestDirtyWorkTreeIsResetBeforeAnySubsequentWrite(t *testing.T) {
	writes := map[string]func(t *testing.T, v *Vault, ident *Identity, existing uuid.UUID) error{
		"insert": func(t *testing.T, v *Vault, ident *Identity, _ uuid.UUID) error {
			_, err := v.Insert(sampleEntry(time.Now()), true, ident)
			return err
		},
		"edit": func(t *testing.T, v *Vault, ident *Identity, existing uuid.UUID) error {
			e := sampleEntry(time.Now())
			e.Title = "edited"
			return v.Update(existing, e, ident)
		},
		"rename": func(t *testing.T, v *Vault, ident *Identity, existing uuid.UUID) error {
			_, err := v.Rename(existing.String(), "renamed", true, ident)
			return err
		},
		"remove": func(t *testing.T, v *Vault, ident *Identity, existing uuid.UUID) error {
			_, err := v.Remove(existing.String(), ident)
			return err
		},
	}

	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
			id := unlockAs(t, v, laptop)
			defer func() { _ = id.Close() }()

			ids := seedEntries(t, v, &id, 2)
			// The victim of the dirtying is an entry the write itself
			// never touches, so any surviving change is unambiguously
			// the stale one being folded in.
			stale := dirtyEntries(t, v, ids[1])
			before := commitCount(t, v)

			if err := write(t, v, &id, ids[0]); err != nil {
				t.Fatalf("%s over a dirty tree: %v", name, err)
			}

			if got := commitCount(t, v); got != before+1 {
				t.Errorf("commit count = %d, want %d — the reset must not produce a commit of its own", got, before+1)
			}
			if paths := commitPaths(t, v, headHash(t, v)); listHas(paths, stale) {
				t.Errorf("%s's commit contains the stale path %s (commit touched %v); "+
					"the dirty tree was folded into the write instead of being reset", name, stale, paths)
			}
			clean, err := gitrepo.IsClean(v.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !clean {
				t.Errorf("the tree is still dirty after %s", name)
			}
			// The reset restored the entry rather than deleting it.
			// Asserted for every verb including remove, which deletes
			// ids[0] and so must leave the dirtied ids[1] readable just
			// like the others.
			if _, err := v.ReadEntry(ids[1], &id); err != nil {
				t.Errorf("the untouched entry %s no longer decrypts after the reset: %v", ids[1], err)
			}
		})
	}
}

// TestDirtyWorkTreeResetIsAlsoRunByReencrypt covers the fifth verb in
// the same list. It is separate because --reencrypt is the operation
// whose own interruption is the expected cause of the dirtiness, so
// "the next --reencrypt cleans up after the last one" is the specific
// loop that has to close.
func TestDirtyWorkTreeResetIsAlsoRunByReencrypt(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAs(t, v, laptop)
	ids := seedEntries(t, v, &id, 2)
	stale := dirtyEntries(t, v, ids[1])

	phone := newTestDevice(t, "personal", "phone-1")
	id2 := unlockAs(t, v, laptop)
	_ = id.Close()
	defer func() { _ = id2.Close() }()

	before := commitCount(t, v)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id2); err != nil {
		t.Fatalf("AddRecipient --reencrypt over a dirty tree: %v", err)
	}

	if got := commitCount(t, v); got != before+1 {
		t.Errorf("commit count = %d, want %d", got, before+1)
	}
	// The stale path is in the commit — every entry is — but it must be
	// there as a freshly re-encrypted entry, not as the half-written
	// bytes the reset was supposed to discard.
	got, err := v.ReadEntry(ids[1], &id2)
	if err != nil {
		t.Fatalf("the entry left dirty is not decryptable after --reencrypt: %v (stale path %s)", err, stale)
	}
	if got.Title == "" {
		t.Error("the entry left dirty decrypted to nothing; the half-written bytes survived the reset")
	}
}

// TestDirtyWorkTreeResetWarnsNamingTheDiscardedPaths is the plan's
// "Resetting a dirty entries/ working tree emits a one-line warning
// naming the discarded paths (via Prompter, a fake in tests) before
// proceeding — the reset is automatic, but never silent, since the 'only
// an interrupted --reencrypt could cause this' assumption isn't provable,
// only likely."
func TestDirtyWorkTreeResetWarnsNamingTheDiscardedPaths(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	p := &confirmingPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}, confirm: true}
	id := unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	ids := seedEntries(t, v, &id, 2)
	stale := dirtyEntries(t, v, ids[1])

	warningsBefore := len(p.warnings)
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("Insert over a dirty tree: %v", err)
	}

	added := p.warnings[warningsBefore:]
	if len(added) == 0 {
		t.Fatal("the reset was silent; a warning must be emitted through the Prompter")
	}
	joined := strings.Join(added, "\n")
	if !strings.Contains(joined, stale) {
		t.Errorf("warnings = %q, want one naming the discarded path %s", added, stale)
	}
	for _, w := range added {
		if strings.Contains(w, "\n") {
			t.Errorf("warning %q spans multiple lines; the plan calls for a one-line warning", w)
		}
	}

	// A clean tree stays silent — a warning on every ordinary write
	// would train people to ignore the one that matters.
	warningsBefore = len(p.warnings)
	if _, err := v.Insert(sampleEntry(time.Now().Add(time.Minute)), true, &id); err != nil {
		t.Fatalf("second Insert: %v", err)
	}
	if got := p.warnings[warningsBefore:]; len(got) != 0 {
		t.Errorf("a write over a clean tree warned anyway: %q", got)
	}
}

// TestInterruptedRecipientWriteLeavesNoHalfMigratedState covers the half
// of the plan's dirty-tree requirement the entry-loop crash seam
// structurally cannot reach: "The reset covers the whole vault working
// tree rather than only entries/ — a crash in the narrow window after
// the recipient files are written but before the commit dirties those
// two as well, and a reset that skipped them would leave exactly the
// half-migrated state this milestone exists to rule out."
//
// onReencryptEntry fires only while entries are being written, so every
// other atomicity test crashes with .age-recipients and config.toml
// still clean. Without this test a reset narrowed to entries/ passes the
// whole suite, and a vault would quietly commit a recipient list naming
// a key no entry is encrypted to.
//
// Both crash shapes are driven, because they fail differently. A
// --reencrypt dies with entries dirty *and* the recipient pair dirty; a
// plain `recipient add` never touches an entry at all, so its window
// leaves the pair as the only dirty thing in the tree — the shape a
// reset that keys off entries/ skips entirely rather than merely
// under-reports.
//
// The state is reproduced through writeRecipientFiles itself — the
// genuine last act before either verb's single commit — rather than by
// hand-writing two files that merely resemble it.
func TestInterruptedRecipientWriteLeavesNoHalfMigratedState(t *testing.T) {
	cases := map[string]struct{ dirtyAnEntry bool }{
		"interrupted reencrypt": {dirtyAnEntry: true},
		"interrupted plain add": {dirtyAnEntry: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

			p := &confirmingPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}, confirm: true}
			id := unlockAsWith(t, v, laptop, p)
			defer func() { _ = id.Close() }()

			ids := seedEntries(t, v, &id, 2)
			phone := newTestDevice(t, "personal", "phone-1")

			mustNotBeCommitted := []string{".age-recipients", ".gage/config.toml"}
			if tc.dirtyAnEntry {
				mustNotBeCommitted = append(mustNotBeCommitted, dirtyEntries(t, v, ids[1]))
			}

			current, err := v.Recipients()
			if err != nil {
				t.Fatal(err)
			}
			halfMigrated := append(append([]VaultRecipient{}, current...),
				VaultRecipient{Device: "phone-1", Pubkey: phone.pubkey})
			if err := v.writeRecipientFiles(halfMigrated); err != nil {
				t.Fatalf("staging the crash window's recipient files: %v", err)
			}

			// The state under test has to actually exist, or everything
			// below passes vacuously.
			if !listHas(readRecipientsFile(t, v), phone.pubkey) {
				t.Fatal("the staged .age-recipients does not name the new key; this test would prove nothing")
			}
			if !listHas(configPubkeys(t, v), phone.pubkey) {
				t.Fatal("the staged config.toml does not name the new key; this test would prove nothing")
			}

			warningsBefore := len(p.warnings)
			before := commitCount(t, v)
			newID, err := v.Insert(sampleEntry(time.Now().Add(time.Hour)), true, &id)
			if err != nil {
				t.Fatalf("Insert over a half-migrated tree: %v", err)
			}

			// Both sides of the pair are back at HEAD.
			if listHas(readRecipientsFile(t, v), phone.pubkey) {
				t.Error(".age-recipients still names the interrupted run's key; the reset skipped it")
			}
			if listHas(configPubkeys(t, v), phone.pubkey) {
				t.Error(".gage/config.toml still names the interrupted run's key; the reset skipped it")
			}

			// And nothing left over rode into the write's own commit,
			// which is the half-migrated state itself: a recipient list
			// naming a key that only the entries this write happened to
			// touch are actually encrypted to.
			if got := commitCount(t, v); got != before+1 {
				t.Errorf("commit count = %d, want %d — the reset must not commit", got, before+1)
			}
			paths := commitPaths(t, v, headHash(t, v))
			for _, unwanted := range mustNotBeCommitted {
				if listHas(paths, unwanted) {
					t.Errorf("the write's commit contains %s (it touched %v); the interrupted run's leftovers were folded in",
						unwanted, paths)
				}
			}

			// The reset ran before the write encrypted anything, not
			// merely before it committed: had .age-recipients still held
			// the staged key at encrypt time, the new entry would be
			// readable by a device this vault never actually added.
			phoneID := unlockAs(t, v, phone)
			defer func() { _ = phoneID.Close() }()
			if _, err := v.ReadEntry(newID, &phoneID); !errors.Is(err, ErrNotARecipient) {
				t.Errorf("the entry written after the reset is readable by the never-added recipient (err=%v); "+
					"the reset ran after the recipient list was read for encryption", err)
			}

			// Never silent, and the warning names both files rather than
			// only the entries the other reset tests cover.
			added := strings.Join(p.warnings[warningsBefore:], "\n")
			for _, want := range []string{".age-recipients", ".gage/config.toml"} {
				if !strings.Contains(added, want) {
					t.Errorf("warnings = %q, want one naming the discarded %s", added, want)
				}
			}

			clean, err := gitrepo.IsClean(v.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !clean {
				t.Error("the tree is still dirty after the write")
			}
		})
	}
}
