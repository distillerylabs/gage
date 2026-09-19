package gage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/gittest"
	"github.com/distillerylabs/gage/internal/gage/recipients"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
)

// ---------------------------------------------------------------------
// M10 shared test support
//
// The trust cache is per-device local state, so nearly every test below
// is a two-actor story: this device, and whoever changed the recipient
// list behind its back. The second actor is deliberately *not* a second
// gage vault object — it commits straight into the shared working tree
// the way a pulled commit from another machine would arrive, which is
// the only way to produce a change this device has genuinely never
// reviewed.
// ---------------------------------------------------------------------

// trustPrompter is fakePrompter plus a scripted answer to M10's
// recipient-change question and a record of every warning value it was
// handed.
//
// The recorded RecipientChangeWarning is the point: the plan requires
// the library to hand the frontend a typed value (diff, plus whether the
// two recipient files agree) rather than printed text, and a Prompter
// that only returned a bool could not tell the difference.
type trustPrompter struct {
	fakePrompter

	// approve is what ConfirmRecipientChange answers, every time. false
	// stands in for both a human typing n and for every non-interactive
	// frontend, whose answer defaults to no.
	approve bool

	// confirm is what the plain Confirm answers — M9's self-removal
	// question and M10's repair confirmation, which must stay separate
	// from the recipient-change question above.
	confirm bool

	changes  []RecipientChangeWarning
	prompts  []string
	warnings []string
}

func (p *trustPrompter) ConfirmRecipientChange(w RecipientChangeWarning) (bool, error) {
	p.changes = append(p.changes, w)
	return p.approve, nil
}

func (p *trustPrompter) Confirm(prompt string) (bool, error) {
	p.prompts = append(p.prompts, prompt)
	return p.confirm, nil
}

func (p *trustPrompter) Warn(msg string) {
	p.warnings = append(p.warnings, msg)
	p.fakePrompter.Warn(msg)
}

// newTrustPrompter builds a prompter that can actually unlock, since
// every blocking-check test has to reach an encrypting method.
func newTrustPrompter(approve bool) *trustPrompter {
	return &trustPrompter{
		fakePrompter: fakePrompter{passphrases: []string{testPassphrase}},
		approve:      approve,
		confirm:      true,
	}
}

// newTrustKey returns a fresh, real age public key — what an attacker
// appends to .age-recipients, or what a legitimate `recipient add` on
// another device commits.
func newTrustKey(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id.Recipient().String()
}

// commitRoutineRecipientChange appends a recipient to *both* files and
// commits them together — exactly what a legitimate `gage recipient add`
// run on another device leaves behind, and what this device finds after
// a pull. This is the routine case: `verify` still passes afterwards.
func commitRoutineRecipientChange(t *testing.T, v *Vault, device, pubkey string) {
	t.Helper()

	keys, err := recipients.Read(filepath.Join(v.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	if err := recipients.Write(filepath.Join(v.Path, ".age-recipients"), append(keys, pubkey)); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(v.Path, ".gage", "config.toml")
	vc, err := vaultconfig.Read(configPath)
	if err != nil {
		t.Fatal(err)
	}
	vc.Recipients = append(vc.Recipients, vaultconfig.Recipient{Device: device, Pubkey: pubkey})
	if err := vaultconfig.Write(configPath, vc); err != nil {
		t.Fatal(err)
	}

	if _, err := gitrepo.CommitAll(v.Path, "gage: recipient add "+device); err != nil {
		t.Fatal(err)
	}
}

// tamperRecipientsFile appends a key to .age-recipients and to nothing
// else, then commits it — the hand-edit of the file encryption actually
// consults, which diffing config.toml alone would never see. This is the
// mismatched case: `verify` fails afterwards.
func tamperRecipientsFile(t *testing.T, v *Vault, pubkey string) {
	t.Helper()

	path := filepath.Join(v.Path, ".age-recipients")
	keys, err := recipients.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := recipients.Write(path, append(keys, pubkey)); err != nil {
		t.Fatal(err)
	}
	if _, err := gitrepo.CommitAll(v.Path, "add a key"); err != nil {
		t.Fatal(err)
	}
}

// trustCacheOf reads a device's cached record of a vault. It repoints
// the process's XDG roots at that device and leaves them there, like
// every other multi-device helper in this package — see withXDGRoot.
func trustCacheOf(t *testing.T, v *Vault, d testDevice) (trustCache, bool) {
	t.Helper()

	var (
		cache trustCache
		ok    bool
	)
	withXDGRoot(t, d.root, func() {
		var err error
		cache, ok, err = v.loadTrustCache()
		if err != nil {
			t.Fatalf("loading %s's trust cache for %q: %v", d.name, v.Name, err)
		}
	})
	return cache, ok
}

// knownConfigPathFor spells the cache's location out literally rather
// than asking the code under test where it put things: "under
// $GAGE_STATE, keyed by vault id" (A20) is a claim about a path, and a
// test that computed it the same way the implementation does could not
// catch the implementation computing it wrongly.
func knownConfigPathFor(d testDevice, vault string) string {
	return filepath.Join(d.root, "state", "gage", vaultIDForTest(vault), "known-config.toml")
}

// sha256HexOfVaultFile hashes a file in the vault independently of
// whatever the cache did, so "the stored hash is the hash of
// .age-recipients" is checked against the file and not against the
// implementation's own idea of it.
func sha256HexOfVaultFile(t *testing.T, v *Vault, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(v.Path, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// configLineContaining returns the single line of the vault's committed
// .gage/config.toml holding want, trimmed of its line ending — the
// literal text a diff of that file has to carry.
func configLineContaining(t *testing.T, v *Vault, want string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(v.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var found string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if !strings.Contains(line, want) {
			continue
		}
		if found != "" {
			t.Fatalf("%q appears on more than one line of .gage/config.toml; it does not identify one", want)
		}
		found = line
	}
	if found == "" {
		t.Fatalf("no line of .gage/config.toml contains %q:\n%s", want, data)
	}
	return found
}

// ---------------------------------------------------------------------
// Bootstrap and storage
// ---------------------------------------------------------------------

// TestFirstUseWritesTheCacheThenAnUnreviewedChangeWarns is the plan's
// first test: after a device's first successful use of a vault,
// known-config.toml is written to local state; a subsequent unreviewed
// recipient change triggers a diff warning before the next encrypt.
func TestFirstUseWritesTheCacheThenAnUnreviewedChangeWarns(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	if _, ok := trustCacheOf(t, v, laptop); ok {
		t.Fatal("a trust cache exists before the vault has ever been used; the bootstrap is on first *use*")
	}

	// The first successful use. Nothing to warn about — there is no
	// record to compare against yet — so it writes the cache silently.
	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()

	if _, err := os.Stat(knownConfigPathFor(laptop, v.Name)); err != nil {
		t.Fatalf("known-config.toml was not written after the first successful use: %v", err)
	}
	cache, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("no trust cache after the first successful use")
	}
	if cache.RecipientsHash == "" {
		t.Error("the cache recorded no .age-recipients hash on first use")
	}

	// Another device adds a recipient, correctly touching both files.
	commitRoutineRecipientChange(t, v, "phone-1", newTrustKey(t))

	p := newTrustPrompter(true)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("Insert after an approved recipient change: %v", err)
	}

	if len(p.changes) == 0 {
		t.Fatal("the encrypt went ahead without ever asking about the unreviewed recipient change")
	}
	w := p.changes[0]
	if w.Vault != v.Name {
		t.Errorf("warning names vault %q, want %q", w.Vault, v.Name)
	}
	if !strings.Contains(w.Diff, "phone-1") {
		t.Errorf("the diff does not mention the added device:\n%s", w.Diff)
	}
	if w.Mismatched() {
		t.Error("a change touching both files reported as mismatched; this is the routine case")
	}
}

// TestTrustCacheStoresBothHalves is the plan's "the cache stores both
// halves — the verbatim config.toml copy and the .age-recipients content
// hash."
//
// Verbatim matters: the diff shown to a human is a diff of this file
// against the committed one, so anything that normalizes, re-orders, or
// re-serializes it would produce a diff of gage's formatting rather than
// of the change.
func TestTrustCacheStoresBothHalves(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()

	committed, err := os.ReadFile(filepath.Join(v.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cached, err := os.ReadFile(knownConfigPathFor(laptop, v.Name))
	if err != nil {
		t.Fatalf("reading the cached config copy: %v", err)
	}
	if string(cached) != string(committed) {
		t.Errorf("known-config.toml is not a verbatim copy.\ncached:\n%s\ncommitted:\n%s", cached, committed)
	}

	cache, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("no trust cache after the first use")
	}
	if want := sha256HexOfVaultFile(t, v, ".age-recipients"); cache.RecipientsHash != want {
		t.Errorf("cached .age-recipients hash = %q, want %q (sha256 of the committed file)",
			cache.RecipientsHash, want)
	}
}

// TestRecipientsFileEditAloneStillWarns is the plan's "a change to
// .age-recipients alone, with config.toml untouched, still triggers the
// warning" — the case the hash half of the cache exists for. There is no
// config diff to show at all here, so a check built only on the diff
// would sail straight past the file encryption actually reads.
func TestRecipientsFileEditAloneStillWarns(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()

	stray := newTrustKey(t)
	tamperRecipientsFile(t, v, stray)

	p := newTrustPrompter(true)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("Insert after an approved (mismatched) change: %v", err)
	}

	if len(p.changes) == 0 {
		t.Fatal("editing .age-recipients alone produced no warning; the cache's hash half is not being consulted")
	}
	w := p.changes[0]
	if !w.Mismatched() {
		t.Error("a key in .age-recipients but not config.toml did not report as mismatched")
	}
	if !listHas(w.Verification.OnlyInRecipientsFile, stray) {
		t.Errorf("the warning does not name the stray key: %+v", w.Verification)
	}
}

// ---------------------------------------------------------------------
// Where the check hooks
// ---------------------------------------------------------------------

// TestBlockingCheckHooksEveryEncryptingVaultMethod is the plan's "the
// warning fires the same way for a one-shot insert/edit/generate as for
// a session-mode one — the cache check hooks the Vault methods that
// encrypt, not Session.Use, since one-shot mode never calls Session.Use
// at all."
//
// No Session appears anywhere below, on purpose: every one of these
// calls is what a one-shot `gage insert`/`edit`/`rename` makes, and each
// must ask on its own. `generate` is Insert with a generated value (see
// GenerateValue), so it is covered by the Insert case rather than by a
// fourth near-identical block.
func TestBlockingCheckHooksEveryEncryptingVaultMethod(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, v *Vault, id *Identity) error
	}{
		{"Insert", func(t *testing.T, v *Vault, id *Identity) error {
			_, err := v.Insert(sampleEntry(time.Now()), true, id)
			return err
		}},
		{"Update", func(t *testing.T, v *Vault, id *Identity) error {
			ids, err := v.EntryIDs()
			if err != nil {
				return err
			}
			e := sampleEntry(time.Now())
			e.Value = "rewritten"
			return v.Update(ids[0], e, id)
		}},
		{"Rename", func(t *testing.T, v *Vault, id *Identity) error {
			_, err := v.Rename("ProtonMail", "Proton Mail", false, id)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

			// One entry to edit or rename, written before the recipient
			// list moves, so the change under test is the only reason to
			// warn.
			id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
			if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
				t.Fatalf("seeding an entry: %v", err)
			}
			_ = id.Close()

			commitRoutineRecipientChange(t, v, "phone-1", newTrustKey(t))

			p := newTrustPrompter(true)
			id = unlockAsWith(t, v, laptop, p)
			defer func() { _ = id.Close() }()
			if err := tc.write(t, v, &id); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if len(p.changes) == 0 {
				t.Errorf("%s encrypted without asking about the unreviewed recipient change", tc.name)
			}
		})
	}
}

// TestUnlockWarnsOpportunisticallyWithoutBlocking is the plan's
// "non-blocking opportunistic warning fires on a plain gage show/ls in
// one-shot mode when recipients have changed, even though neither
// command encrypts anything."
//
// Two halves, and the second is the one that makes it *opportunistic*:
// the warning arrives, and nothing was asked — a read that stopped to
// ask permission would be a blocking check wired into the wrong place.
func TestUnlockWarnsOpportunisticallyWithoutBlocking(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("seeding an entry: %v", err)
	}
	_ = id.Close()

	commitRoutineRecipientChange(t, v, "phone-1", newTrustKey(t))

	// A plain read: unlock, list, show. Nothing here encrypts.
	p := newTrustPrompter(false)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	if len(p.warnings) == 0 {
		t.Fatal("a vault unlock over a changed recipient list warned about nothing")
	}
	if !strings.Contains(strings.Join(p.warnings, "\n"), "recipient") {
		t.Errorf("the unlock warnings do not mention recipients: %q", p.warnings)
	}
	if len(p.changes) != 0 {
		t.Errorf("the unlock asked for a decision (%d times); the opportunistic check must never block", len(p.changes))
	}

	if _, err := v.List(&id); err != nil {
		t.Fatalf("List after the opportunistic warning: %v", err)
	}
	if _, _, err := v.Resolve("ProtonMail", &id); err != nil {
		t.Fatalf("Resolve after the opportunistic warning: %v", err)
	}

	// A read must not launder an unreviewed list into an approved one.
	cache, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("the trust cache vanished")
	}
	if strings.Contains(string(cache.KnownConfig), "phone-1") {
		t.Error("an opportunistic warning regenerated the cache; only a confirmed change may do that")
	}
}

// TestSyncWarnsOnACleanFastForward is the plan's "gage sync surfaces the
// opportunistic warning even when it resolves via a clean fast-forward
// with no conflicting entry — the case where sync never unlocks any
// identity at all, so the warning can't be riding along on a
// Vault.Unlock call."
//
// The resolver's Unlock is a hard failure here rather than a working
// unlock: a fast-forward that unlocked would silently turn this into a
// re-test of the Unlock hook.
func TestSyncWarnsOnACleanFastForward(t *testing.T) {
	v, id, remote := newSyncVault(t, "personal", "laptop-1")
	_ = id.Close()

	// Another device commits a recipient change and publishes it. The
	// local side has no commits of its own, so this is a clean
	// fast-forward.
	other := gittest.NewDevice(t, remote)
	other.Write(t, ".age-recipients", other.Read(t, ".age-recipients")+newTrustKey(t)+"\n")
	other.Commit(t, "another device edits the recipient list")
	other.Push(t)

	p := newTrustPrompter(false)
	ctx, cancel := context.WithTimeout(context.Background(), RemoteOpTimeout)
	defer cancel()

	report, err := v.SyncResolving(ctx, ConflictResolver{
		Prompter: p,
		Unlock: func() (*Identity, error) {
			t.Fatal("sync unlocked an identity; a clean fast-forward must never need one")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("SyncResolving: %v", err)
	}
	if !report.Pulled {
		t.Fatalf("expected a fast-forward, got %+v", report)
	}
	if len(p.warnings) == 0 {
		t.Error("a sync that fast-forwarded a recipient change in warned about nothing; " +
			"the sync hook is missing, not just riding on Unlock")
	}
}

// ---------------------------------------------------------------------
// The two outcomes
// ---------------------------------------------------------------------

// TestDecliningTheRecipientChangeAbortsTheWrite is the plan's "the
// blocking check *blocks*: declining the prompt aborts the write
// entirely, no commit is produced, and the cache stays at its old value
// so the same warning reappears next time."
func TestDecliningTheRecipientChangeAbortsTheWrite(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()

	before, _ := trustCacheOf(t, v, laptop)
	commitRoutineRecipientChange(t, v, "phone-1", newTrustKey(t))
	commits := commitCount(t, v)
	head := headHash(t, v)

	p := newTrustPrompter(false)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	_, err := v.Insert(sampleEntry(time.Now()), true, &id)
	if !errors.Is(err, ErrRecipientChangeDeclined) {
		t.Fatalf("Insert over a declined recipient change = %v, want ErrRecipientChangeDeclined", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.Conflict {
		t.Errorf("exit code = %d, want Conflict (%d)", got, exitcode.Conflict)
	}

	if got := commitCount(t, v); got != commits {
		t.Errorf("commit count = %d, want %d; a declined write must commit nothing", got, commits)
	}
	if got := headHash(t, v); got != head {
		t.Error("HEAD moved on a declined write")
	}
	if ids, err := v.EntryIDs(); err != nil || len(ids) != 0 {
		t.Errorf("entries after a declined insert = %v (err %v), want none", ids, err)
	}

	after, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("the trust cache vanished on a declined write")
	}
	if string(after.KnownConfig) != string(before.KnownConfig) || after.RecipientsHash != before.RecipientsHash {
		t.Error("a declined change moved the cache; the same warning must reappear next time")
	}

	// And it does reappear.
	p2 := newTrustPrompter(false)
	id2 := unlockAsWith(t, v, laptop, p2)
	defer func() { _ = id2.Close() }()
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id2); !errors.Is(err, ErrRecipientChangeDeclined) {
		t.Fatalf("the second attempt = %v, want the same refusal", err)
	}
	if len(p2.changes) == 0 {
		t.Error("the second attempt was not asked again")
	}
}

// TestConfirmingARoutineChangeRegeneratesBothHalves is the plan's
// "confirming a routine recipient change (files agree) regenerates both
// halves of the cache and stops warning — one acknowledgment per actual
// change, not per command."
func TestConfirmingARoutineChangeRegeneratesBothHalves(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()

	commitRoutineRecipientChange(t, v, "phone-1", newTrustKey(t))

	p := newTrustPrompter(true)
	id = unlockAsWith(t, v, laptop, p)
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	_ = id.Close()
	if len(p.changes) != 1 {
		t.Fatalf("asked %d times about one change, want exactly 1", len(p.changes))
	}

	cache, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("no trust cache after a confirmed change")
	}
	committed, err := os.ReadFile(filepath.Join(v.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(cache.KnownConfig) != string(committed) {
		t.Error("the config half of the cache was not regenerated to the confirmed state")
	}
	if want := sha256HexOfVaultFile(t, v, ".age-recipients"); cache.RecipientsHash != want {
		t.Errorf("the hash half = %q, want %q; both halves regenerate together", cache.RecipientsHash, want)
	}

	// One acknowledgment per actual change: two more writes, no more
	// questions.
	p2 := newTrustPrompter(false)
	id2 := unlockAsWith(t, v, laptop, p2)
	defer func() { _ = id2.Close() }()
	for i := 0; i < 2; i++ {
		e := sampleEntry(time.Now())
		e.Title = "another"
		if _, err := v.Insert(e, true, &id2); err != nil {
			t.Fatalf("a later write was blocked by an already-confirmed change: %v", err)
		}
	}
	if len(p2.changes) != 0 {
		t.Errorf("asked again %d times after confirming; one acknowledgment per change, not per command",
			len(p2.changes))
	}
	if len(p2.warnings) != 0 {
		t.Errorf("still warning after a confirmed change: %q", p2.warnings)
	}
}

// TestConfirmingAMismatchedChangeDoesNotClearTheCache is the plan's
// "confirming a mismatched change (.age-recipients and config.toml
// disagree) does not silently clear the cache — verify keeps failing
// until the inconsistency is actually fixed."
//
// Confirming still proceeds with the write: the human said go ahead, and
// refusing at that point would be the blocking check pretending to be a
// repair tool. What it must not do is buy silence.
func TestConfirmingAMismatchedChangeDoesNotClearTheCache(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()
	before, _ := trustCacheOf(t, v, laptop)

	stray := newTrustKey(t)
	tamperRecipientsFile(t, v, stray)

	p := newTrustPrompter(true)
	id = unlockAsWith(t, v, laptop, p)
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("a confirmed (if mismatched) write was refused: %v", err)
	}
	_ = id.Close()

	after, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("no trust cache after a confirmed mismatch")
	}
	if after.RecipientsHash != before.RecipientsHash {
		t.Error("confirming a mismatch regenerated the .age-recipients hash; " +
			"that is precisely the silence the two-outcome rule withholds")
	}
	if after.MismatchSeen == "" {
		t.Error("the acknowledged mismatch was not recorded at all")
	}

	got, err := v.VerifyRecipients()
	if err != nil {
		t.Fatal(err)
	}
	if got.InSync {
		t.Error("verify passes after confirming past a mismatch; the inconsistency was never fixed")
	}
	if !listHas(got.OnlyInRecipientsFile, stray) {
		t.Errorf("the stray key is gone from verify's report: %+v", got)
	}

	// And the next write asks again, because nothing was resolved.
	p2 := newTrustPrompter(true)
	id2 := unlockAsWith(t, v, laptop, p2)
	defer func() { _ = id2.Close() }()
	e := sampleEntry(time.Now())
	e.Title = "another"
	if _, err := v.Insert(e, true, &id2); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if len(p2.changes) == 0 {
		t.Error("a still-mismatched vault stopped warning after one acknowledgment")
	}
}

// TestTheRecipientWarningIsATypedValueNotPrintedText is the plan's "the
// recipient-change warning is a typed value returned by the library
// (diff content, routine-vs-mismatched flag), not printed text —
// cmd/gage renders it as the [y/N] prompt shown in the design doc."
//
// The library side of that claim is what's checkable here: the frontend
// receives a value carrying the diff, the verification result, and which
// recipients moved. The rendering half is asserted in cmd/gage's own
// tests.
func TestTheRecipientWarningIsATypedValueNotPrintedText(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()

	added := newTrustKey(t)
	commitRoutineRecipientChange(t, v, "phone-1", added)

	p := newTrustPrompter(true)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if len(p.changes) != 1 {
		t.Fatalf("asked %d times, want 1", len(p.changes))
	}
	w := p.changes[0]

	if w.Diff == "" {
		t.Error("the warning carries no diff; the design shows the change, it never just says one happened")
	}
	// The added device's line, as an addition. It is read out of the
	// committed file rather than spelled out here because the quoting is
	// go-toml's to choose (it writes literal strings, 'phone-1', where
	// the design doc's illustration shows "phone-1" — the same TOML
	// value): what this test is about is that the *line naming the new
	// device* appears in the diff, +-prefixed, which is what makes the
	// warning show the change rather than announce one.
	deviceLine := configLineContaining(t, v, "phone-1")
	for _, want := range []string{"known-config.toml", ".gage/config.toml", "+" + deviceLine} {
		if !strings.Contains(w.Diff, want) {
			t.Errorf("the diff is missing %q:\n%s", want, w.Diff)
		}
	}
	if len(w.Added) != 1 || w.Added[0].Pubkey != added || w.Added[0].Device != "phone-1" {
		t.Errorf("Added = %+v, want the one new recipient", w.Added)
	}
	if len(w.Removed) != 0 {
		t.Errorf("Removed = %+v, want none", w.Removed)
	}
	if !w.Verification.InSync {
		t.Error("Verification says the files disagree; they agree in the routine case")
	}
	if w.LastConfirmed.IsZero() {
		t.Error("the warning carries no last-confirmed time; the design's diff header prints it")
	}
}

// TestTrustCacheLivesUnderStateAndIsNeverCommitted is the plan's "the
// cache is written under $GAGE_STATE, never inside the vault, and never
// committed — a git status after a cache regeneration is clean."
func TestTrustCacheLivesUnderStateAndIsNeverCommitted(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()
	commitRoutineRecipientChange(t, v, "phone-1", newTrustKey(t))

	commits := commitCount(t, v)
	id = unlockAsWith(t, v, laptop, newTrustPrompter(true))
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	_ = id.Close()

	if _, err := os.Stat(knownConfigPathFor(laptop, v.Name)); err != nil {
		t.Fatalf("the cache is not at $GAGE_STATE/%s/known-config.toml: %v", v.Name, err)
	}

	// Nothing cache-shaped inside the vault, at any depth.
	err := filepath.Walk(v.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			return nil
		}
		if strings.Contains(info.Name(), "known-config") || info.Name() == "trust.toml" {
			t.Errorf("trust-cache state was written inside the vault: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after a cache regeneration")
	}
	// The insert's own commit, and nothing else.
	if got := commitCount(t, v); got != commits+1 {
		t.Errorf("commit count = %d, want %d; regenerating the cache must commit nothing", got, commits+1)
	}
}

// TestALocalRecipientAddRegeneratesTheCache is the resolved decision
// that a change the operator made *here* needs no second review: without
// it, `gage recipient add` would warn them about their own add on their
// very next write, which is the fastest way to teach someone to stop
// reading the warning.
func TestALocalRecipientAddRegeneratesTheCache(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	p := newTrustPrompter(false)
	id := unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient: %v", err)
	}
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("a write straight after a local recipient add was blocked: %v", err)
	}
	if len(p.changes) != 0 {
		t.Errorf("the operator was asked to review their own recipient add: %+v", p.changes)
	}
}

// ---------------------------------------------------------------------
// Refusing to rebuild a diverged vault (M9's add/remove, revised)
// ---------------------------------------------------------------------

// TestRecipientAddAndRemoveRefuseOverAFailingVerify is the plan's
// "recipient add and recipient remove against a vault whose
// .age-recipients and config.toml disagree are both refused, naming the
// specific differences, and leave both files byte-identical — the stray
// key is still there afterwards for verify to keep reporting, rather
// than having been rebuilt away."
//
// Both verbs: the rebuild that erases the evidence lives in the shared
// tail both paths commit through, so covering only `add` would leave the
// more destructive verb unguarded. Each verb has exactly one form to
// guard — A19 removed `add`'s no-reencrypt case, and M9 refuses a
// removal without --reencrypt outright, since the removed key would
// still open every entry that already exists.
func TestRecipientAddAndRemoveRefuseOverAFailingVerify(t *testing.T) {
	cases := []struct {
		name string
		run  func(v *Vault, id *Identity, phone testDevice) error
	}{
		{"add", func(v *Vault, id *Identity, phone testDevice) error {
			_, err := v.AddRecipient("phone-1", phone.pubkey, id)
			return err
		}},
		{"remove --reencrypt", func(v *Vault, id *Identity, phone testDevice) error {
			_, err := v.RemoveRecipient("phone-1", true, id)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
			phone := newTestDevice(t, "personal", "phone-1")

			id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
			if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
				t.Fatalf("seeding an entry: %v", err)
			}
			// "remove" needs something to remove; the add cases need the
			// name free, so only that one gets it up front.
			if strings.HasPrefix(tc.name, "remove") {
				if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
					t.Fatalf("seeding the recipient to remove: %v", err)
				}
			}
			_ = id.Close()

			stray := newTrustKey(t)
			tamperRecipientsFile(t, v, stray)

			recipientsBefore, err := os.ReadFile(filepath.Join(v.Path, ".age-recipients"))
			if err != nil {
				t.Fatal(err)
			}
			configBefore, err := os.ReadFile(filepath.Join(v.Path, ".gage", "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			commits := commitCount(t, v)
			head := headHash(t, v)
			cacheBefore, _ := trustCacheOf(t, v, laptop)

			p := newTrustPrompter(true)
			id = unlockAsWith(t, v, laptop, p)
			defer func() { _ = id.Close() }()

			err = tc.run(v, &id, phone)
			if !errors.Is(err, ErrRecipientsOutOfSync) {
				t.Fatalf("%s over a diverged vault = %v, want ErrRecipientsOutOfSync", tc.name, err)
			}
			if got := exitcode.CodeOf(err); got != exitcode.Conflict {
				t.Errorf("exit code = %d, want Conflict (%d)", got, exitcode.Conflict)
			}
			// Naming the differences is the whole value of the refusal:
			// "no" without "here is what is wrong" leaves the operator
			// running `verify` to find out what the tool already knew.
			if !strings.Contains(err.Error(), stray) {
				t.Errorf("the refusal does not name the stray key:\n%v", err)
			}

			recipientsAfter, err := os.ReadFile(filepath.Join(v.Path, ".age-recipients"))
			if err != nil {
				t.Fatal(err)
			}
			configAfter, err := os.ReadFile(filepath.Join(v.Path, ".gage", "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(recipientsAfter) != string(recipientsBefore) {
				t.Error(".age-recipients was rewritten by a refused change; the evidence must survive")
			}
			if string(configAfter) != string(configBefore) {
				t.Error(".gage/config.toml was rewritten by a refused change")
			}

			// The stray key is still there for verify to keep reporting.
			got, err := v.VerifyRecipients()
			if err != nil {
				t.Fatal(err)
			}
			if got.InSync || !listHas(got.OnlyInRecipientsFile, stray) {
				t.Errorf("verify no longer reports the divergence after a refusal: %+v", got)
			}

			// Nothing committed, nothing pushed, cache untouched — a
			// mismatch cannot be laundered into an approved state by
			// retrying the add.
			if n := commitCount(t, v); n != commits {
				t.Errorf("commit count = %d, want %d; a refused change commits nothing", n, commits)
			}
			if h := headHash(t, v); h != head {
				t.Error("HEAD moved on a refused recipient change")
			}
			cacheAfter, _ := trustCacheOf(t, v, laptop)
			if cacheAfter.RecipientsHash != cacheBefore.RecipientsHash ||
				string(cacheAfter.KnownConfig) != string(cacheBefore.KnownConfig) {
				t.Error("a refused recipient change moved the trust cache")
			}
		})
	}
}

// TestReencryptRefusalHappensBeforeAnyEntryIsRewritten pins the ordering
// the plan calls out: the verify precondition sits ahead of the
// re-encryption pass, not beside the recipient-file write. Aborting
// after every entry had been rewritten would be its own half-migrated
// state — the exact thing M9's single commit exists to rule out.
func TestReencryptRefusalHappensBeforeAnyEntryIsRewritten(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("seeding an entry: %v", err)
	}
	_ = id.Close()

	tamperRecipientsFile(t, v, newTrustKey(t))

	reencrypted := 0
	v.onReencryptEntry = func(done int) { reencrypted = done }

	id = unlockAsWith(t, v, laptop, newTrustPrompter(true))
	defer func() { _ = id.Close() }()

	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); !errors.Is(err, ErrRecipientsOutOfSync) {
		t.Fatalf("AddRecipient --reencrypt = %v, want ErrRecipientsOutOfSync", err)
	}
	if reencrypted != 0 {
		t.Errorf("%d entries were re-encrypted before the refusal; the precondition must run first", reencrypted)
	}
	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after a refused --reencrypt")
	}
}

// ---------------------------------------------------------------------
// The exit: recipient verify --repair
// ---------------------------------------------------------------------

// TestRepairRewritesTheRecipientsFileFromConfig is the resolved
// sub-decision's happy path: the refusal above has an exit, and the exit
// is a deliberate, visible act rather than a side effect of an unrelated
// add.
func TestRepairRewritesTheRecipientsFileFromConfig(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()

	stray := newTrustKey(t)
	tamperRecipientsFile(t, v, stray)
	commits := commitCount(t, v)

	p := newTrustPrompter(true)
	withXDGRoot(t, laptop.root, func() {
		repair, err := v.RepairRecipients(p)
		if err != nil {
			t.Fatalf("RepairRecipients: %v", err)
		}
		if !listHas(repair.Dropped, stray) {
			t.Errorf("Dropped = %v, want the stray key", repair.Dropped)
		}
		if len(repair.Added) != 0 {
			t.Errorf("Added = %v, want none", repair.Added)
		}
		if repair.Commit == "" {
			t.Error("the repair reported no commit")
		}
	})

	// It asked first, and named what it was about to drop.
	if len(p.prompts) != 1 {
		t.Fatalf("repair asked %d confirmations, want exactly 1", len(p.prompts))
	}
	if !strings.Contains(p.prompts[0], stray) {
		t.Errorf("the confirmation does not name the key it drops: %q", p.prompts[0])
	}

	got, err := v.VerifyRecipients()
	if err != nil {
		t.Fatal(err)
	}
	if !got.InSync {
		t.Errorf("verify still fails after a repair: %+v", got)
	}
	if listHas(readRecipientsFile(t, v), stray) {
		t.Error("the stray key survived the repair")
	}
	if got := commitCount(t, v); got != commits+1 {
		t.Errorf("commit count = %d, want %d (one repair commit)", got, commits+1)
	}
	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("the working tree is dirty after a repair; the rewrite must be committed")
	}
}

// TestRepairDeclinedWritesNothing is the other half of "behind a
// confirmation": a Prompter that answers no — which includes every
// non-interactive one — leaves the divergence exactly where it was.
func TestRepairDeclinedWritesNothing(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()

	stray := newTrustKey(t)
	tamperRecipientsFile(t, v, stray)
	commits := commitCount(t, v)
	before, err := os.ReadFile(filepath.Join(v.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}

	p := newTrustPrompter(true)
	p.confirm = false
	withXDGRoot(t, laptop.root, func() {
		if _, err := v.RepairRecipients(p); err == nil {
			t.Fatal("a declined repair reported success")
		}
	})

	// It has to have *asked* to have been declined: an error that
	// arrives without a question is a different failure wearing this
	// test's result.
	if len(p.prompts) != 1 {
		t.Fatalf("repair asked %d confirmations before refusing, want exactly 1", len(p.prompts))
	}

	after, err := os.ReadFile(filepath.Join(v.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a declined repair rewrote .age-recipients")
	}
	if got := commitCount(t, v); got != commits {
		t.Errorf("commit count = %d, want %d; a declined repair commits nothing", got, commits)
	}
}

// TestRepairDoesNotRegenerateTheTrustCache is the part of the repair
// decision that is easy to get wrong in the convenient direction.
// Repair settles the *inconsistency* between the two files; it says
// nothing about whether the recipient list itself is one this device
// approves. Regenerating the cache here would let a repair launder an
// unreviewed key exactly the way the rebuilding `add` used to.
func TestRepairDoesNotRegenerateTheTrustCache(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()
	before, _ := trustCacheOf(t, v, laptop)

	// A change to *both* files (so it survives the repair) that this
	// device has never reviewed, plus a stray edit to make verify fail.
	commitRoutineRecipientChange(t, v, "phone-1", newTrustKey(t))
	tamperRecipientsFile(t, v, newTrustKey(t))

	p := newTrustPrompter(true)
	withXDGRoot(t, laptop.root, func() {
		if _, err := v.RepairRecipients(p); err != nil {
			t.Fatalf("RepairRecipients: %v", err)
		}
	})

	after, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("the trust cache vanished across a repair")
	}
	if string(after.KnownConfig) != string(before.KnownConfig) {
		t.Error("the repair regenerated the cached config; the recipient change is still unreviewed")
	}

	p2 := newTrustPrompter(true)
	id2 := unlockAsWith(t, v, laptop, p2)
	defer func() { _ = id2.Close() }()
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id2); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if len(p2.changes) == 0 {
		t.Error("the next encrypt after a repair asked nothing; " +
			"repairing the file split is not reviewing the recipient list")
	}
	if !p2.changes[0].Verification.InSync {
		t.Error("the post-repair warning still reports the files as disagreeing")
	}
}

// TestRecipientAddDoesNotLaunderSomeoneElsesChange is the other half of
// the cache-regeneration decision, and the half that is easy to miss:
// "the operator just reviewed that list by typing the command" is true
// of the key *they* named and of nothing else.
//
// A recipient change pulled from another device is unreviewed here even
// when it is perfectly consistent — both files edited, `verify` passing,
// so requireRecipientsInSync has nothing to object to. Regenerating the
// cache at the end of an add would bless that key along with the
// operator's own, silently and permanently. Since A19 it is worse still:
// every existing entry is rewritten to a list nobody here approved
// before the blessing lands, and that now happens on every add rather
// than on an opt-in one — which is why the two cases this test used to
// run are one.
//
// So the blocking check runs here too, and it runs before anything is
// written.
func TestRecipientAddDoesNotLaunderSomeoneElsesChange(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id); err != nil {
		t.Fatalf("seeding an entry: %v", err)
	}
	_ = id.Close()

	// Another device's change, arriving the way a pull delivers one:
	// both files, committed together, `verify` still green.
	stranger := newTrustKey(t)
	commitRoutineRecipientChange(t, v, "stranger", stranger)

	commits := commitCount(t, v)
	cacheBefore, _ := trustCacheOf(t, v, laptop)

	reencrypted := 0
	v.onReencryptEntry = func(done int) { reencrypted = done }
	defer func() { v.onReencryptEntry = nil }()

	p := newTrustPrompter(false)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	_, err := v.AddRecipient("phone-1", phone.pubkey, &id)
	if !errors.Is(err, ErrRecipientChangeDeclined) {
		t.Fatalf("add over an unreviewed change = %v, want ErrRecipientChangeDeclined", err)
	}
	if len(p.changes) != 1 {
		t.Fatalf("asked %d times about the pulled change, want exactly 1", len(p.changes))
	}
	if !strings.Contains(p.changes[0].Diff, "stranger") {
		t.Errorf("the question asked was not about the pulled key:\n%s", p.changes[0].Diff)
	}

	// Declined: nothing written, and in particular nothing re-encrypted.
	// The check has to sit ahead of the rewrite pass, not beside the
	// recipient-file write.
	if reencrypted != 0 {
		t.Errorf("%d entries were re-encrypted before the refusal", reencrypted)
	}
	if n := commitCount(t, v); n != commits {
		t.Errorf("commit count = %d, want %d; a declined add commits nothing", n, commits)
	}
	if listHas(readRecipientsFile(t, v), phone.pubkey) {
		t.Error("the new recipient was added despite the refusal")
	}

	// And above all: the pulled key is still unreviewed.
	cacheAfter, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("the trust cache vanished")
	}
	if string(cacheAfter.KnownConfig) != string(cacheBefore.KnownConfig) {
		t.Error("a declined add regenerated the cache")
	}
	if strings.Contains(string(cacheAfter.KnownConfig), stranger) {
		t.Error("another device's unreviewed key was laundered into the trust cache by a local add")
	}
}

// TestConfirmingLetsTheAddThroughAndRegeneratesOnce is the same story
// answered yes: the operator reviews the pulled change once, their own
// add lands, and the cache ends up covering both — so the very next
// write asks nothing. One acknowledgment per actual change is preserved;
// what the check above adds is that the acknowledgment has to happen.
func TestConfirmingLetsTheAddThroughAndRegeneratesOnce(t *testing.T) {
	v, laptop := newRecipientTestVault(t, "personal", "laptop-1")
	phone := newTestDevice(t, "personal", "phone-1")

	id := unlockAsWith(t, v, laptop, newTrustPrompter(true))
	_ = id.Close()

	stranger := newTrustKey(t)
	commitRoutineRecipientChange(t, v, "stranger", stranger)

	p := newTrustPrompter(true)
	id = unlockAsWith(t, v, laptop, p)
	defer func() { _ = id.Close() }()

	if _, err := v.AddRecipient("phone-1", phone.pubkey, &id); err != nil {
		t.Fatalf("AddRecipient after confirming the pulled change: %v", err)
	}
	if len(p.changes) != 1 {
		t.Fatalf("asked %d times, want exactly 1", len(p.changes))
	}

	// Both keys are now in the cache: the reviewed one and the operator's
	// own, which needed no review.
	cache, ok := trustCacheOf(t, v, laptop)
	if !ok {
		t.Fatal("no trust cache after a confirmed add")
	}
	for _, want := range []string{stranger, phone.pubkey} {
		if !strings.Contains(string(cache.KnownConfig), want) {
			t.Errorf("the regenerated cache is missing %s", want)
		}
	}

	p2 := newTrustPrompter(false)
	id2 := unlockAsWith(t, v, laptop, p2)
	defer func() { _ = id2.Close() }()
	if _, err := v.Insert(sampleEntry(time.Now()), true, &id2); err != nil {
		t.Fatalf("the write after a confirmed add was blocked: %v", err)
	}
	if len(p2.changes) != 0 {
		t.Errorf("asked again after confirming: %d times", len(p2.changes))
	}
}
