package gage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/recipients"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
	"github.com/distillerylabs/gage/internal/gage/vaultlock"
)

// ---------------------------------------------------------------------
// M9 shared test support
//
// Everything below the divider is M9's own test-only scaffolding. It
// builds on M4's newTestDevice/withXDGRoot/unlockAs (entry_test.go)
// rather than duplicating them: what M9 adds is (a) a Prompter that can
// answer Confirm with something other than yes and records what it was
// asked, and (b) vault setups with more than one device.
// ---------------------------------------------------------------------

// confirmingPrompter is fakePrompter plus a scripted Confirm answer and a
// record of every confirmation and warning it was handed.
//
// M9 is the first milestone where the library asks a *question* whose
// "no" is load-bearing — removing this device's own key locks it out of
// the vault, so a Prompter that declines must abort with nothing
// written. fakePrompter answers every Confirm with yes, which is exactly
// the answer that can't prove that.
type confirmingPrompter struct {
	fakePrompter

	// confirm is what Confirm answers, every time. false stands in for
	// both a human saying no and for every non-interactive Prompter,
	// whose Confirm defaults to no (see cmd/gage's terminalPrompter).
	confirm bool

	confirmPrompts []string
}

func (s *confirmingPrompter) Confirm(prompt string) (bool, error) {
	s.confirmPrompts = append(s.confirmPrompts, prompt)
	return s.confirm, nil
}

// unlockAsWith is unlockAs with the Prompter supplied, so a test can
// reach the Confirm and Warn exchanges the write path routes through
// Identity.warnTo.
func unlockAsWith(t *testing.T, v *Vault, d testDevice, p Prompter) Identity {
	t.Helper()
	var id Identity
	withXDGRoot(t, d.root, func() {
		var err error
		id, err = v.Unlock(p)
		if err != nil {
			t.Fatalf("unlocking as %s: %v", d.name, err)
		}
	})
	return id
}

// newRecipientTestVault is newEntryTestVault with the device handed back
// too, since every M9 test needs to switch XDG roots between devices.
func newRecipientTestVault(t *testing.T, vaultName, device string) (*Vault, testDevice) {
	t.Helper()
	d := newTestDevice(t, vaultName, device)

	var v *Vault
	withXDGRoot(t, d.root, func() {
		var err error
		v, err = Create(CreateSpec{
			Name:       vaultName,
			ID:         d.vaultID,
			Path:       filepath.Join(t.TempDir(), vaultName),
			Type:       TypeGit,
			Method:     MethodPassphrase,
			Device:     device,
			Recipients: []string{d.pubkey},
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	return v, d
}

// readRecipientsFile returns the vault's .age-recipients as a list of
// keys — the file a stock age CLI reads, checked as itself rather than
// through any gage-side accessor.
func readRecipientsFile(t *testing.T, v *Vault) []string {
	t.Helper()
	keys, err := recipients.Read(filepath.Join(v.Path, ".age-recipients"))
	if err != nil {
		t.Fatalf("reading .age-recipients: %v", err)
	}
	return keys
}

// readVaultConfigFile returns the vault's committed .gage/config.toml.
func readVaultConfigFile(t *testing.T, v *Vault) vaultconfig.File {
	t.Helper()
	f, err := vaultconfig.Read(filepath.Join(v.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatalf("reading .gage/config.toml: %v", err)
	}
	return f
}

// configPubkeys is readVaultConfigFile reduced to the keys, in order.
func configPubkeys(t *testing.T, v *Vault) []string {
	t.Helper()
	f := readVaultConfigFile(t, v)
	out := make([]string, 0, len(f.Recipients))
	for _, r := range f.Recipients {
		out = append(out, r.Pubkey)
	}
	return out
}

// listHas is the test-side membership check. Named to stay out of the
// way of the package's own `contains`, which vault_create.go already
// defines with the same shape.
func listHas(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// commitPaths lists the vault-relative paths a commit changed against
// its first parent — the only way to assert "exactly one commit, holding
// both sides of the recipient pair" rather than merely "one new commit".
func commitPaths(t *testing.T, v *Vault, hash string) []string {
	t.Helper()

	repo, err := git.PlainOpen(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(plumbing.NewHash(hash))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}

	var parentTree *object.Tree
	if commit.NumParents() > 0 {
		parent, err := commit.Parent(0)
		if err != nil {
			t.Fatal(err)
		}
		parentTree, err = parent.Tree()
		if err != nil {
			t.Fatal(err)
		}
	}

	changes, err := object.DiffTree(parentTree, tree)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(changes))
	for _, c := range changes {
		name := c.To.Name
		if name == "" {
			name = c.From.Name
		}
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths
}

func commitCount(t *testing.T, v *Vault) int {
	t.Helper()
	n, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func headHash(t *testing.T, v *Vault) string {
	t.Helper()
	h, err := gitrepo.HeadHash(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// ---------------------------------------------------------------------
// Recipients
// ---------------------------------------------------------------------

// TestRecipientAddHasNoPartialForm replaces M9's
// "TestRecipientAddWithoutReencryptAffectsOnlyFutureWrites", which
// pinned the behavior A19 removes. There is now no invocation of
// AddRecipient that produces a recipient who can read only part of the
// vault, so the assertion runs the other way: entries written before the
// add and entries written after it are equally readable by the new key.
//
// Asserted from the new recipient's side — a real second identity
// performing a real decrypt — rather than by inspecting the ciphertext's
// header, for the same reason the test it replaces was written that way.
func TestRecipientAddHasNoPartialForm(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id1 := unlockAs(t, v, laptop)
	before, err := v.Insert(sampleEntry(time.Now()), true, &id1)
	if err != nil {
		t.Fatalf("Insert (before the add): %v", err)
	}
	_ = id1.Close()

	phone := newTestDevice(t, "personal", "phone-1")

	id1 = unlockAs(t, v, laptop)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id1); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}
	after, err := v.Insert(sampleEntry(time.Now().Add(time.Minute)), true, &id1)
	if err != nil {
		t.Fatalf("Insert (after the add): %v", err)
	}
	_ = id1.Close()

	id2 := unlockAs(t, v, phone)
	defer func() { _ = id2.Close() }()

	if _, err := v.ReadEntry(after, &id2); err != nil {
		t.Fatalf("the new recipient cannot read an entry written after the add: %v", err)
	}
	if _, err := v.ReadEntry(before, &id2); err != nil {
		t.Fatalf("the new recipient cannot read an entry written before the add: %v — "+
			"A19: no invocation of add may leave a recipient reading only part of the vault", err)
	}
}

// TestRecipientAddCommitsBothFilesTogether is M9's resolved decision
// that an add commits .age-recipients and .gage/config.toml in the same
// single commit — never one without the other, since M10's trust cache
// diffs exactly that pair.
//
// A19 widens rather than narrows it: the re-encrypted entries ride in
// that same commit, so the vault is never split across two recipient
// lists at any commit boundary. The vault is seeded first so
// "alongside every re-encrypted entry" has something to be true of.
func TestRecipientAddCommitsBothFilesTogether(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()
	ids := seedEntries(t, v, &id, 3)

	before := commitCount(t, v)
	change, err := v.AddRecipient("phone-1", phone.pubkey, &id)
	if err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}

	if got := commitCount(t, v); got != before+1 {
		t.Errorf("commit count = %d, want %d (exactly one commit)", got, before+1)
	}
	if change.Reencrypted != len(ids) {
		t.Errorf("Reencrypted = %d, want %d — an add always rewrites the whole vault", change.Reencrypted, len(ids))
	}

	paths := commitPaths(t, v, headHash(t, v))
	if !listHas(paths, ".age-recipients") || !listHas(paths, ".gage/config.toml") {
		t.Errorf("the add's commit touched %v, want both .age-recipients and .gage/config.toml", paths)
	}
	for _, entryID := range ids {
		if want := "entries/" + entryID.String() + ".age"; !listHas(paths, want) {
			t.Errorf("the add's commit does not contain %s (it contains %v); "+
				"the recipient pair and every re-encrypted entry land together", want, paths)
		}
	}

	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after AddRecipient; the change must be committed, not left staged")
	}

	if !listHas(readRecipientsFile(t, v), phone.pubkey) {
		t.Error(".age-recipients does not list the added key")
	}
	if !listHas(configPubkeys(t, v), phone.pubkey) {
		t.Error(".gage/config.toml does not list the added key")
	}
}

// TestRecipientAddMakesHistoryReadable is M9's "makes all pre-existing
// entries decryptable by the new recipient", now unconditional: A19
// removed the flag that used to gate it, so this is what a plain
// `recipient add` does.
func TestRecipientAddMakesHistoryReadable(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id1 := unlockAs(t, v, laptop)
	var existing []struct {
		id    string
		title string
	}
	for i, title := range []string{"ProtonMail", "Bank", "Router"} {
		e := sampleEntry(time.Now().Add(time.Duration(i) * time.Minute))
		e.Title = title
		entryID, err := v.Insert(e, true, &id1)
		if err != nil {
			t.Fatalf("Insert %q: %v", title, err)
		}
		existing = append(existing, struct {
			id    string
			title string
		}{entryID.String(), title})
	}
	_ = id1.Close()

	phone := newTestDevice(t, "personal", "phone-1")

	id1 = unlockAs(t, v, laptop)
	change, err := v.AddRecipient("phone-1", phone.pubkey, &id1)
	if err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}
	_ = id1.Close()

	if change.Reencrypted != len(existing) {
		t.Errorf("Reencrypted = %d, want %d", change.Reencrypted, len(existing))
	}

	id2 := unlockAs(t, v, phone)
	defer func() { _ = id2.Close() }()

	ids, err := v.EntryIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(existing) {
		t.Fatalf("entry count = %d, want %d", len(ids), len(existing))
	}
	for _, entryID := range ids {
		got, err := v.ReadEntry(entryID, &id2)
		if err != nil {
			t.Fatalf("the new recipient cannot read pre-existing entry %s after the add: %v", entryID, err)
		}
		if got.Title == "" {
			t.Errorf("entry %s decrypted to an empty title; re-encryption lost its contents", entryID)
		}
	}
}

// ---------------------------------------------------------------------
// A19's refusal: an actor that cannot read the whole vault
// ---------------------------------------------------------------------

// commitRecipientFiles rewrites both recipient-defining files from list
// and commits them, the way a hand edit followed by a `git commit`
// would. It goes through writeRecipientFiles rather than touching the
// two files separately so the pair can never be left disagreeing, which
// is a different failure (ErrRecipientsOutOfSync) than the one these
// tests are about.
func commitRecipientFiles(t *testing.T, v *Vault, list []VaultRecipient) {
	t.Helper()
	if err := v.writeRecipientFiles(list); err != nil {
		t.Fatalf("writing the hand-edited recipient list: %v", err)
	}
	if _, err := gitrepo.CommitAll(v.Path, "test: hand-edited recipient list"); err != nil {
		t.Fatalf("committing the hand-edited recipient list: %v", err)
	}
}

// hideOneEntryFrom leaves the vault holding one entry that d cannot
// read, while .age-recipients and .gage/config.toml still agree with
// each other and still list d.
//
// This is the state a recipient admitted before A19 lives in, built by
// the only other route into it: a hand-edited recipient list. The list
// is pointed at a single foreign key, one entry is written through the
// ordinary path so it is encrypted to that key alone, and the list is
// then put back. Constructing it this way is deterministic and does not
// depend on shipping the behavior A19 removed.
//
// It deliberately does not restore the trust cache afterwards. The
// round trip through a foreign list leaves M10's cache stale, so a
// vault built here is one where AddRecipient *would* ask for a
// recipient-change confirmation — which is what makes "the refusal
// arrives before that confirmation" an assertion about ordering rather
// than about a prompt that was never going to appear.
func hideOneEntryFrom(t *testing.T, v *Vault, d testDevice) uuid.UUID {
	t.Helper()

	original, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	commitRecipientFiles(t, v, []VaultRecipient{{Device: "foreign", Pubkey: newTrustKey(t)}})

	id := unlockAsWith(t, v, d, newTrustPrompter(true))
	// force, so Insert's duplicate-title check doesn't try to decrypt the
	// entries this vault is in the middle of being made unable to read.
	hidden, err := v.Insert(sampleEntry(time.Now()), true, &id)
	if err != nil {
		t.Fatalf("writing the entry only the foreign key can read: %v", err)
	}
	_ = id.Close()

	commitRecipientFiles(t, v, original)
	return hidden
}

// TestRecipientAddRefusesAnActorThatCannotReadEveryEntry is A19's new
// refusal, and most of what E1a adds rather than removes.
//
// Partial access is contagious: reencryptTo fails on the first entry the
// acting identity cannot read, so without this check a partially
// admitted device's `recipient add` would die mid-write, inside the
// lock, naming an opaque entry UUID. The refusal exists to make that
// legible — and to make it arrive before anything has happened.
func TestRecipientAddRefusesAnActorThatCannotReadEveryEntry(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	seedEntries(t, v, &id, 2)
	_ = id.Close()

	hidden := hideOneEntryFrom(t, v, laptop)

	beforeHash := headHash(t, v)
	beforeCount := commitCount(t, v)
	cacheBefore, hadCache := trustCacheOf(t, v, laptop)

	reencrypted := 0
	v.onReencryptEntry = func(done int) { reencrypted = done }
	defer func() { v.onReencryptEntry = nil }()

	p := newTrustPrompter(true)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	_, err := v.AddRecipient("phone-1", phone.pubkey, &id)
	if !errors.Is(err, ErrCannotGrantFullAccess) {
		t.Fatalf("AddRecipient from a partially admitted device = %v, want ErrCannotGrantFullAccess", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.Conflict {
		t.Errorf("exit code = %d, want Conflict (%d) — this is pre-existing vault damage surfacing, "+
			"not a bad command line", got, exitcode.Conflict)
	}

	// The count is the diagnosis. The UUID is not: naming it is exactly
	// the unhelpful mid-write failure this check exists to replace.
	if !strings.Contains(err.Error(), "1 of 3") {
		t.Errorf("error = %q, want it to say how many of the vault's entries are unreadable", err)
	}
	if strings.Contains(err.Error(), hidden.String()) {
		t.Errorf("error = %q, want it not to name an entry UUID", err)
	}

	// Before the confirmation: the cache is stale here (see
	// hideOneEntryFrom), so an add that got as far as confirmRecipientTrust
	// would have asked.
	if len(p.changes) != 0 {
		t.Errorf("the operator was asked to review a recipient change before being refused: %+v", p.changes)
	}

	// And nothing changed.
	if got := headHash(t, v); got != beforeHash {
		t.Errorf("HEAD = %s after the refusal, want %s (untouched)", got, beforeHash)
	}
	if got := commitCount(t, v); got != beforeCount {
		t.Errorf("commit count = %d after the refusal, want %d (nothing committed)", got, beforeCount)
	}
	if reencrypted != 0 {
		t.Errorf("%d entries were re-encrypted before the refusal; the pre-flight must precede the rewrite pass", reencrypted)
	}
	if listHas(readRecipientsFile(t, v), phone.pubkey) {
		t.Error(".age-recipients gained the new key despite the refusal")
	}
	if listHas(configPubkeys(t, v), phone.pubkey) {
		t.Error(".gage/config.toml gained the new key despite the refusal")
	}
	cacheAfter, stillHasCache := trustCacheOf(t, v, laptop)
	if hadCache != stillHasCache || cacheAfter.RecipientsHash != cacheBefore.RecipientsHash ||
		!bytes.Equal(cacheAfter.KnownConfig, cacheBefore.KnownConfig) {
		t.Error("the trust cache was regenerated by a refused add")
	}
}

// TestFullAccessPreflightRunsBeforeTheVaultLock pins the half of the
// ordering the assertions above cannot see: "before the write lock" is
// not the same claim as "before the confirmation", and only one of them
// is provable from what the Prompter was asked.
//
// The lock is held by something else for the whole call, so an
// AddRecipient that reached withVaultWrite at all would fail as
// contention. Getting ErrCannotGrantFullAccess instead is what proves
// the pre-flight ran outside the lock — which is what E4 depends on,
// since approval places the same pass at a different point.
func TestFullAccessPreflightRunsBeforeTheVaultLock(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	seedEntries(t, v, &id, 1)
	_ = id.Close()

	hideOneEntryFrom(t, v, laptop)

	id = unlockAsWith(t, v, laptop, newTrustPrompter(true))
	defer func() { _ = id.Close() }()

	lockPath, err := LockFilePath(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := vaultlock.Acquire(lockPath, 0)
	if err != nil {
		t.Fatalf("taking the vault lock the add must never reach: %v", err)
	}
	defer func() { _ = held.Release() }()

	_, err = v.AddRecipient("phone-1", phone.pubkey, &id)
	if !errors.Is(err, ErrCannotGrantFullAccess) {
		var contended *vaultlock.ContendedError
		if errors.As(err, &contended) {
			t.Fatalf("AddRecipient took the write lock before the full-access pre-flight ran: %v", err)
		}
		t.Fatalf("AddRecipient = %v, want ErrCannotGrantFullAccess", err)
	}
}

// TestRequireFullAccessIsCallableOnItsOwn is the shape E4 depends on:
// the pre-flight is a pass a caller can run at a point of its own
// choosing, not something buried inside AddRecipient. `recipient
// approve` shows its confirmation before it unlocks, so it has to place
// the same check later in its sequence than `recipient add` does.
func TestRequireFullAccessIsCallableOnItsOwn(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAs(t, v, laptop)
	seedEntries(t, v, &id, 2)
	_ = id.Close()

	id = unlockAs(t, v, laptop)
	if err := v.RequireFullAccess(&id); err != nil {
		t.Fatalf("RequireFullAccess on a vault this device can read entirely = %v, want nil", err)
	}
	_ = id.Close()

	hideOneEntryFrom(t, v, laptop)

	id = unlockAsWith(t, v, laptop, newTrustPrompter(true))
	defer func() { _ = id.Close() }()
	if err := v.RequireFullAccess(&id); !errors.Is(err, ErrCannotGrantFullAccess) {
		t.Fatalf("RequireFullAccess with one unreadable entry = %v, want ErrCannotGrantFullAccess", err)
	}
}

// interruptSelfRemoval leaves the vault in the one state where entries/
// and HEAD disagree about who can read the vault: laptop's own
// `recipient remove --reencrypt`, killed partway through the pass.
//
// HEAD is untouched, so it still lists d and is still entirely readable
// by it. The working tree is not: it holds however many entries the pass
// got through, encrypted to the reduced list that excludes d. This is
// the dirty tree the design doc calls the likeliest one there is, and
// the one withVaultWrite's reset exists to discard.
func interruptSelfRemoval(t *testing.T, v *Vault, d testDevice) {
	t.Helper()

	id := unlockAsWith(t, v, d, newTrustPrompter(true))
	v.onReencryptEntry = crashAfter(1)
	crashed := runAndRecoverCrash(t, func() {
		_, _ = v.RemoveRecipient(d.name, true, &id)
	})
	v.onReencryptEntry = nil
	_ = id.Close()

	if !crashed {
		t.Fatal("the crash seam never fired; the self-removal ran to completion")
	}
	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Fatal("the interrupted self-removal left a clean working tree; " +
			"there is nothing here for the reset to discard")
	}
}

// TestAddIsNotRefusedOverATreeTheResetDiscards is the pre-flight's
// interaction with withVaultWrite's dirty-tree reset.
//
// RequireFullAccess reads entries off disk, and an interrupted
// self-removal is exactly the state where disk and HEAD disagree about
// who can read them: entries/ holds ciphertext written to the reduced
// list, while HEAD still lists this device and is entirely readable by
// it. The reset discards precisely that, so a refusal raised on it would
// send the operator to another device to repair a vault that was never
// damaged — and the identical command succeeds the moment any other
// write happens to reset the tree first, which is the tell that the
// refusal was about the tree rather than about access.
func TestAddIsNotRefusedOverATreeTheResetDiscards(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	seedEntries(t, v, &id, 3)
	// A second recipient, so removing laptop-1's own key is not the
	// last-recipient refusal instead.
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient phone-1: %v", err)
	}
	_ = id.Close()

	interruptSelfRemoval(t, v, laptop)

	third := newTestDevice(t, "personal", "phone-2")
	id = unlockAsWith(t, v, laptop, newTrustPrompter(true))
	defer func() { _ = id.Close() }()

	change, err := v.AddRecipient("phone-2", third.pubkey, &id)
	if errors.Is(err, ErrCannotGrantFullAccess) {
		t.Fatalf("the add was refused over a working tree the reset discards: %v", err)
	}
	if err != nil {
		t.Fatalf("AddRecipient after an interrupted self-removal: %v", err)
	}
	if change.Reencrypted != 3 {
		t.Errorf("Reencrypted = %d, want 3 — the add re-encrypts the entries at HEAD, "+
			"not the ones the reset threw away", change.Reencrypted)
	}
	if !listHas(configPubkeys(t, v), third.pubkey) || !listHas(readRecipientsFile(t, v), third.pubkey) {
		t.Error("the new recipient is missing from one of the two recipient files")
	}
}

// TestAddIsStillRefusedOnADirtyTreeWhenAccessIsGenuinelyPartial is the
// other half, and the one that keeps the fix above from being a way to
// switch the refusal off.
//
// The unreadable entry here is committed, so the reset cannot make it go
// away. Deferring the check past the reset must therefore still reach
// it: what moves is where the refusal is raised, never whether it is.
func TestAddIsStillRefusedOnADirtyTreeWhenAccessIsGenuinelyPartial(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	seedEntries(t, v, &id, 2)
	_ = id.Close()

	hidden := hideOneEntryFrom(t, v, laptop)

	// Dirty the tree with something the reset will discard, so the
	// pre-lock pass is the one that has to stand down — while the
	// committed entry it would have found is still there afterwards.
	stray := filepath.Join(v.Path, "left-behind.txt")
	if err := os.WriteFile(stray, []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if clean, err := gitrepo.IsClean(v.Path); err != nil {
		t.Fatal(err)
	} else if clean {
		t.Fatal("the working tree is clean; this test needs a dirty one")
	}

	beforeHash := headHash(t, v)
	beforeCount := commitCount(t, v)

	p := newTrustPrompter(true)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	_, err := v.AddRecipient("phone-1", phone.pubkey, &id)
	if !errors.Is(err, ErrCannotGrantFullAccess) {
		t.Fatalf("AddRecipient on a dirty tree from a partially admitted device = %v, "+
			"want ErrCannotGrantFullAccess — the reset must not swallow a real refusal", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.Conflict {
		t.Errorf("exit code = %d, want Conflict (%d)", got, exitcode.Conflict)
	}
	if !strings.Contains(err.Error(), "1 of 3") {
		t.Errorf("error = %q, want it to say how many entries are unreadable", err)
	}
	if strings.Contains(err.Error(), hidden.String()) {
		t.Errorf("error = %q, want it not to name an entry UUID", err)
	}

	// Still before the confirmation, and still with nothing written: the
	// refusal moved past the reset, not past the guarantees.
	if len(p.changes) != 0 {
		t.Errorf("the operator was asked to review a recipient change before being refused: %+v", p.changes)
	}
	if got := headHash(t, v); got != beforeHash {
		t.Errorf("HEAD = %s after the refusal, want %s (untouched)", got, beforeHash)
	}
	if got := commitCount(t, v); got != beforeCount {
		t.Errorf("commit count = %d after the refusal, want %d (nothing committed)", got, beforeCount)
	}
	if listHas(configPubkeys(t, v), phone.pubkey) {
		t.Error(".gage/config.toml gained the new key despite the refusal")
	}
}

// TestRecipientRemoveWithoutReencryptIsRejected is the design's "removing
// a recipient REQUIRES --reencrypt (gage refuses to silently leave old
// ciphertext readable by a removed party)": rejected outright, and
// nothing written or committed.
func TestRecipientRemoveWithoutReencryptIsRejected(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}

	before := headHash(t, v)
	_, err := v.RemoveRecipient("phone-1", false, &id)
	_ = id.Close()

	if !errors.Is(err, ErrReencryptRequired) {
		t.Fatalf("RemoveRecipient without --reencrypt = %v, want ErrReencryptRequired", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want Usage (%d) — the command line asked for something gage refuses", got, exitcode.Usage)
	}
	if got := headHash(t, v); got != before {
		t.Error("HEAD moved on a rejected remove")
	}
	if !listHas(readRecipientsFile(t, v), phone.pubkey) {
		t.Error("the recipient was removed from .age-recipients despite the refusal")
	}
}

// TestRecipientRemoveWithReencryptExcludesTheRemovedKey is the second
// half of the same bullet: with --reencrypt it re-encrypts every entry,
// excluding the removed key. Proven from the removed device's side — its
// identity can no longer decrypt anything.
func TestRecipientRemoveWithReencryptExcludesTheRemovedKey(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id1 := unlockAs(t, v, laptop)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id1); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}
	entryID, err := v.Insert(sampleEntry(time.Now()), true, &id1)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	_ = id1.Close()

	// Readable by the phone before the removal — otherwise the assertion
	// after it proves nothing.
	id2 := unlockAs(t, v, phone)
	if _, err := v.ReadEntry(entryID, &id2); err != nil {
		t.Fatalf("phone-1 cannot read the entry before removal: %v", err)
	}
	_ = id2.Close()

	id1 = unlockAs(t, v, laptop)
	change, err := v.RemoveRecipient("phone-1", true, &id1)
	if err != nil {
		t.Fatalf("RemoveRecipient --reencrypt: %v", err)
	}
	if _, err := v.ReadEntry(entryID, &id1); err != nil {
		t.Fatalf("the remaining recipient cannot read the entry after removal: %v", err)
	}
	_ = id1.Close()

	if change.Reencrypted != 1 {
		t.Errorf("Reencrypted = %d, want 1", change.Reencrypted)
	}
	if listHas(readRecipientsFile(t, v), phone.pubkey) {
		t.Error(".age-recipients still lists the removed key")
	}
	if listHas(configPubkeys(t, v), phone.pubkey) {
		t.Error(".gage/config.toml still lists the removed key")
	}

	id2 = unlockAs(t, v, phone)
	defer func() { _ = id2.Close() }()
	if _, err := v.ReadEntry(entryID, &id2); !errors.Is(err, ErrNotARecipient) {
		t.Fatalf("the removed recipient can still decrypt %s (err=%v); --reencrypt must have excluded its key", entryID, err)
	}
}

// TestRecipientRemoveWarnsThatRevocationIsFutureOnly is the plan's
// "prints the 'revokes future access only — anything already read can't
// be unread' warning". The library never prints: it goes through the
// Prompter, which is where cmd/gage renders it.
func TestRecipientRemoveWarnsThatRevocationIsFutureOnly(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	p := &confirmingPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}, confirm: true}
	id := unlockAsWith(t, v, laptop, p)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}
	if _, err := v.RemoveRecipient("phone-1", true, &id); err != nil {
		t.Fatalf("RemoveRecipient: %v", err)
	}
	_ = id.Close()

	joined := strings.Join(p.warnings, "\n")
	if !strings.Contains(joined, "future access") {
		t.Errorf("warnings = %q, want one saying the removal revokes future access only", p.warnings)
	}
	if !strings.Contains(joined, "already read") {
		t.Errorf("warnings = %q, want one saying anything already read can't be unread", p.warnings)
	}
}

// TestRecipientRemoveRefusesTheLastRecipient is the plan's resolved
// decision: a vault with an empty recipient list cannot be encrypted to
// at all, and --reencrypt would have rewritten every entry to a list
// nobody holds a key for. Refused outright, with no --force.
func TestRecipientRemoveRefusesTheLastRecipient(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	p := &confirmingPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}, confirm: true}
	id := unlockAsWith(t, v, laptop, p)
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatal(err)
	}

	before := headHash(t, v)
	_, err := v.RemoveRecipient("laptop-1", true, &id)
	_ = id.Close()

	if !errors.Is(err, ErrLastRecipient) {
		t.Fatalf("removing the only recipient = %v, want ErrLastRecipient", err)
	}
	if got := headHash(t, v); got != before {
		t.Error("HEAD moved on a refused last-recipient removal")
	}
	if keys := readRecipientsFile(t, v); len(keys) != 1 {
		t.Errorf(".age-recipients = %v, want the single recipient left untouched", keys)
	}
}

// TestRecipientRemoveOfThisDeviceNeedsConfirmation is the other half of
// that decision: removing the local device's own key is legitimate
// (decommissioning this laptop) but locks this device out, so it runs
// only behind an explicit Confirm. A Prompter that answers no — which
// includes every non-interactive one — aborts with nothing written.
func TestRecipientRemoveOfThisDeviceNeedsConfirmation(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	declining := &confirmingPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}, confirm: false}
	id := unlockAsWith(t, v, laptop, declining)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}

	before := headHash(t, v)
	_, err := v.RemoveRecipient("laptop-1", true, &id)
	_ = id.Close()

	if err == nil {
		t.Fatal("RemoveRecipient of this device's own key succeeded against a Prompter that declined")
	}
	if len(declining.confirmPrompts) == 0 {
		t.Fatal("no confirmation was asked before removing this device's own key")
	}
	if joined := strings.Join(declining.confirmPrompts, "\n"); !strings.Contains(joined, "laptop-1") {
		t.Errorf("confirmation prompt = %q, want it to name the device being locked out", declining.confirmPrompts)
	}
	if got := headHash(t, v); got != before {
		t.Error("HEAD moved after a declined confirmation")
	}
	if !listHas(readRecipientsFile(t, v), laptop.pubkey) {
		t.Error("this device's key was removed despite the declined confirmation")
	}

	// And with a yes, the same removal goes through: the confirmation is
	// a gate, not a refusal.
	accepting := &confirmingPrompter{fakePrompter: fakePrompter{passphrases: []string{testPassphrase}}, confirm: true}
	id = unlockAsWith(t, v, laptop, accepting)
	if _, err := v.RemoveRecipient("laptop-1", true, &id); err != nil {
		t.Fatalf("RemoveRecipient after a confirmed prompt: %v", err)
	}
	_ = id.Close()
	if listHas(readRecipientsFile(t, v), laptop.pubkey) {
		t.Error("this device's key survived a confirmed removal")
	}
}

// TestRecipientListMatchesConfigExactly is the plan's "`gage recipient
// list` prints every recipient's device name and public key, matching
// .gage/config.toml's [[recipients]] exactly, and reflects an
// add/remove from the same test run."
func TestRecipientListMatchesConfigExactly(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	assertMatchesConfig := func(when string) {
		t.Helper()
		got, err := v.Recipients()
		if err != nil {
			t.Fatalf("Recipients (%s): %v", when, err)
		}
		want := readVaultConfigFile(t, v).Recipients
		if len(got) != len(want) {
			t.Fatalf("Recipients (%s) returned %d entries, config.toml has %d", when, len(got), len(want))
		}
		for i := range want {
			if got[i].Device != want[i].Device || got[i].Pubkey != want[i].Pubkey {
				t.Errorf("Recipients(%s)[%d] = %+v, want %+v (device name and key, in config order)",
					when, i, got[i], want[i])
			}
		}
	}

	assertMatchesConfig("at creation")

	id := unlockAs(t, v, laptop)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}
	assertMatchesConfig("after add")

	list, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("after add, Recipients has %d entries, want 2", len(list))
	}
	if list[1].Device != "phone-1" || list[1].Pubkey != phone.pubkey {
		t.Errorf("added recipient = %+v, want device phone-1 with its own key", list[1])
	}

	if _, err := v.RemoveRecipient("phone-1", true, &id); err != nil {
		t.Fatalf("RemoveRecipient: %v", err)
	}
	_ = id.Close()
	assertMatchesConfig("after remove")

	list, err = v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Device != "laptop-1" {
		t.Errorf("after remove, Recipients = %+v, want only laptop-1", list)
	}
}

// TestRecipientAddRejectsADuplicateKey keeps `recipient add` from
// quietly producing a recipient list with the same key twice, which
// would make `verify` and M10's trust-cache diff harder to reason about
// for no benefit.
func TestRecipientAddRejectsADuplicateKey(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()

	before := headHash(t, v)
	_, err := v.AddRecipient("laptop-again", laptop.pubkey, &id)
	if !errors.Is(err, ErrRecipientExists) {
		t.Fatalf("adding an already-listed key = %v, want ErrRecipientExists", err)
	}
	if got := headHash(t, v); got != before {
		t.Error("HEAD moved on a rejected duplicate add")
	}
}

// TestRecipientRemoveOfAnUnknownRecipientIsRejected: a query naming
// nothing in the list fails rather than silently succeeding as a no-op,
// which would let a typo read as a completed revocation.
func TestRecipientRemoveOfAnUnknownRecipientIsRejected(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()

	_, err := v.RemoveRecipient("no-such-device", true, &id)
	if !errors.Is(err, ErrRecipientNotFound) {
		t.Fatalf("removing an unknown recipient = %v, want ErrRecipientNotFound", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.NotFound {
		t.Errorf("exit code = %d, want NotFound (%d)", got, exitcode.NotFound)
	}
}

// TestRecipientRemoveAcceptsEitherADeviceNameOrAPublicKey: the design
// spells the argument "<pubkey-or-name>", so both spellings must
// address the same recipient.
func TestRecipientRemoveAcceptsEitherADeviceNameOrAPublicKey(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query func(d testDevice) string
	}{
		{"by device name", func(d testDevice) string { return d.name }},
		{"by public key", func(d testDevice) string { return d.pubkey }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
			phone := newTestDevice(t, "personal", "phone-1")

			id := unlockAs(t, v, laptop)
			if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
				t.Fatalf("AddRecipient: %v", err)
			}
			change, err := v.RemoveRecipient(tc.query(phone), true, &id)
			_ = id.Close()
			if err != nil {
				t.Fatalf("RemoveRecipient(%s): %v", tc.name, err)
			}
			if change.Pubkey != phone.pubkey {
				t.Errorf("removed key = %q, want %q", change.Pubkey, phone.pubkey)
			}
			if listHas(readRecipientsFile(t, v), phone.pubkey) {
				t.Error(".age-recipients still lists the removed key")
			}
		})
	}
}

// ---------------------------------------------------------------------
// verify
// ---------------------------------------------------------------------

// TestVerifyReportsInSyncAndTheSpecificDifferences is the plan's
// "`gage recipient verify` exits 0 and reports 'in sync' when
// .age-recipients and config.toml agree; exits 1 and lists the specific
// differences when they don't." The library half: the typed value the
// exit code is derived from carries the differences, both directions.
func TestVerifyReportsInSyncAndTheSpecificDifferences(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop-1")

	got, err := v.VerifyRecipients()
	if err != nil {
		t.Fatalf("VerifyRecipients on a fresh vault: %v", err)
	}
	if !got.InSync {
		t.Fatalf("a freshly created vault reports out of sync: %+v", got)
	}
	if len(got.OnlyInConfig) != 0 || len(got.OnlyInRecipientsFile) != 0 {
		t.Errorf("in-sync verification still listed differences: %+v", got)
	}

	// A key only .age-recipients knows about: the shape of a hand-edited
	// or maliciously-appended recipient file.
	stray := "age1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0savhh7m"
	keys := append(readRecipientsFile(t, v), stray)
	if err := recipients.Write(filepath.Join(v.Path, ".age-recipients"), keys); err != nil {
		t.Fatal(err)
	}

	got, err = v.VerifyRecipients()
	if err != nil {
		t.Fatalf("VerifyRecipients after divergence: %v", err)
	}
	if got.InSync {
		t.Fatal("verification reports in sync while .age-recipients carries a key config.toml doesn't")
	}
	if !listHas(got.OnlyInRecipientsFile, stray) {
		t.Errorf("OnlyInRecipientsFile = %v, want it to name %q", got.OnlyInRecipientsFile, stray)
	}
	if len(got.OnlyInConfig) != 0 {
		t.Errorf("OnlyInConfig = %v, want empty", got.OnlyInConfig)
	}

	// And the mirror image: a key only config.toml knows about.
	f := readVaultConfigFile(t, v)
	f.Recipients = append(f.Recipients, vaultconfig.Recipient{Device: "ghost", Pubkey: stray})
	if err := vaultconfig.Write(filepath.Join(v.Path, ".gage", "config.toml"), f); err != nil {
		t.Fatal(err)
	}
	original := readRecipientsFile(t, v)
	if err := recipients.Write(filepath.Join(v.Path, ".age-recipients"), original[:len(original)-1]); err != nil {
		t.Fatal(err)
	}

	got, err = v.VerifyRecipients()
	if err != nil {
		t.Fatal(err)
	}
	if got.InSync {
		t.Fatal("verification reports in sync while config.toml carries a key .age-recipients doesn't")
	}
	if !listHas(got.OnlyInConfig, stray) {
		t.Errorf("OnlyInConfig = %v, want it to name %q", got.OnlyInConfig, stray)
	}
}

// TestVerifyNeedsNoIdentityFileOrPrompter is the plan's second verify
// bullet, and it is the one that proves the "needs no unlock, safe to
// run in CI" claim rather than asserting it: the vault is verified from
// a device that has no identity file at all — the state right after
// `clone`, before `gage identity add` — through a Prompter that fails
// the test if it is touched.
func TestVerifyNeedsNoIdentityFileOrPrompter(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	// A fresh XDG root with no identity file and no vault registration:
	// this device knows nothing but where the vault directory is.
	stranger := t.TempDir()
	withXDGRoot(t, stranger, func() {
		hasIdentity, err := HasIdentity(v.ID, laptop.name)
		if err != nil {
			t.Fatal(err)
		}
		if hasIdentity {
			t.Fatal("the stranger root unexpectedly has an identity file; the test proves nothing")
		}

		got, err := v.VerifyRecipients()
		if err != nil {
			t.Fatalf("VerifyRecipients without any local identity: %v", err)
		}
		if !got.InSync {
			t.Errorf("verification = %+v, want in sync", got)
		}
	})
}

// TestVerifyReadsNeitherIdentityNorPrompter backs the same claim from
// the other direction: a Vault whose Unlock would fail loudly is never
// consulted, because verify never calls it. The explosive Prompter is
// never handed over at all — there is no parameter to hand it through,
// which is the structural half of the guarantee.
func TestVerifyReadsNeitherIdentityNorPrompter(t *testing.T) {
	v, _ := newRecipientTestVault(t, "personal", "laptop-1")

	// Remove every identity file for this vault, so any Unlock attempt
	// inside VerifyRecipients would fail rather than silently succeed.
	dir, err := IdentitiesDir(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	got, err := v.VerifyRecipients()
	if err != nil {
		t.Fatalf("VerifyRecipients with no identity files present: %v", err)
	}
	if !got.InSync {
		t.Errorf("verification = %+v, want in sync", got)
	}
}

// TestAddRecipientRejectsInvalidDeviceNameOrPubkey is AddRecipient's own
// top-level validation — the pre-flight before anything else runs,
// including the lock. See TestAddRecipientsLockedRevalidatesEveryBatchMember
// for the batch form's re-check of the same two properties.
func TestAddRecipientRejectsInvalidDeviceNameOrPubkey(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()

	t.Run("invalid device name", func(t *testing.T) {
		_, err := v.AddRecipient("not a valid name!", "age1yubikey1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0s9hkmc0", &id)
		if exitcode.CodeOf(err) != exitcode.Usage {
			t.Errorf("code = %v, want Usage", exitcode.CodeOf(err))
		}
	})

	t.Run("invalid pubkey", func(t *testing.T) {
		_, err := v.AddRecipient("phone-1", "not-an-age-key", &id)
		if exitcode.CodeOf(err) != exitcode.Usage {
			t.Errorf("code = %v, want Usage", exitcode.CodeOf(err))
		}
	})
}
