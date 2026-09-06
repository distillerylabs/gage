package gage

import (
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

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/recipients"
	"github.com/denmark/gage/internal/gage/vaultconfig"
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

// TestRecipientAddWithoutReencryptAffectsOnlyFutureWrites is the plan's
// "`gage recipient add` (no --reencrypt) affects only future writes —
// entries that existed before the add remain undecryptable by the new
// recipient's key."
//
// This is the property that makes --reencrypt worth having at all, so it
// is asserted from the new recipient's side (a real second identity
// failing a real decrypt) rather than by inspecting the ciphertext's
// header.
func TestRecipientAddWithoutReencryptAffectsOnlyFutureWrites(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id1 := unlockAs(t, v, laptop)
	before, err := v.Insert(sampleEntry(time.Now()), true, &id1)
	if err != nil {
		t.Fatalf("Insert (before the add): %v", err)
	}
	_ = id1.Close()

	phone := newTestDevice(t, "personal", "phone-1")

	id1 = unlockAs(t, v, laptop)
	if _, err := v.AddRecipient("phone-1", phone.pubkey, false, &id1); err != nil {
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
	_, err = v.ReadEntry(before, &id2)
	if !errors.Is(err, ErrNotARecipient) {
		t.Fatalf("reading a pre-existing entry as the new recipient = %v, want ErrNotARecipient — "+
			"an add without --reencrypt must not retroactively grant access", err)
	}
}

// TestRecipientAddWithoutReencryptCommitsBothFilesTogether is the
// decision resolved in the plan: an add with no --reencrypt still
// commits, and commits .age-recipients and .gage/config.toml in the same
// single commit — never one without the other. M10's trust cache diffs
// exactly this pair.
func TestRecipientAddWithoutReencryptCommitsBothFilesTogether(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAs(t, v, laptop)
	defer func() { _ = id.Close() }()

	before := commitCount(t, v)
	change, err := v.AddRecipient("phone-1", phone.pubkey, false, &id)
	if err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}

	if got := commitCount(t, v); got != before+1 {
		t.Errorf("commit count = %d, want %d (exactly one commit)", got, before+1)
	}
	if change.Reencrypted != 0 {
		t.Errorf("Reencrypted = %d, want 0 without --reencrypt", change.Reencrypted)
	}

	paths := commitPaths(t, v, headHash(t, v))
	if !listHas(paths, ".age-recipients") || !listHas(paths, ".gage/config.toml") {
		t.Errorf("the add's commit touched %v, want both .age-recipients and .gage/config.toml", paths)
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

// TestRecipientAddWithReencryptMakesHistoryReadable is the plan's
// "`gage recipient add --reencrypt` makes all pre-existing entries
// decryptable by the new recipient."
func TestRecipientAddWithReencryptMakesHistoryReadable(t *testing.T) {
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
	change, err := v.AddRecipient("phone-1", phone.pubkey, true, &id1)
	if err != nil {
		t.Fatalf("AddRecipient --reencrypt: %v", err)
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
			t.Fatalf("the new recipient cannot read pre-existing entry %s after --reencrypt: %v", entryID, err)
		}
		if got.Title == "" {
			t.Errorf("entry %s decrypted to an empty title; re-encryption lost its contents", entryID)
		}
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
	if _, err := v.AddRecipient("phone-1", phone.pubkey, false, &id); err != nil {
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
	if _, err := v.AddRecipient("phone-1", phone.pubkey, false, &id1); err != nil {
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
	if _, err := v.AddRecipient("phone-1", phone.pubkey, false, &id); err != nil {
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
	if _, err := v.AddRecipient("phone-1", phone.pubkey, false, &id); err != nil {
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
	if _, err := v.AddRecipient("phone-1", phone.pubkey, false, &id); err != nil {
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
	_, err := v.AddRecipient("laptop-again", laptop.pubkey, false, &id)
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
			if _, err := v.AddRecipient("phone-1", phone.pubkey, false, &id); err != nil {
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
		hasIdentity, err := HasIdentity(v.Name, laptop.name)
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
	dir, err := IdentitiesDir(v.Name)
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
