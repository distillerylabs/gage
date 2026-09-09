package gage

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/denmark/gage/internal/gage/agekey"
	"github.com/denmark/gage/internal/gage/devicename"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/recipients"
	"github.com/denmark/gage/internal/gage/vaultconfig"
)

// ErrLastRecipient is RemoveRecipient refusing to empty a vault's
// recipient list. There is no --force: a vault with no recipients cannot
// be encrypted to at all — encryptRecipients would return nothing and
// the next write would fail — and the --reencrypt that removal requires
// would already have rewritten every entry to a list nobody holds a key
// for, destroying the vault's contents irrecoverably. See the M9 plan's
// resolved decision.
var ErrLastRecipient = errors.New("gage: a vault must keep at least one recipient")

// ErrRecipientExists is AddRecipient refusing a key (or a device name)
// the vault already lists. A list carrying the same key twice makes
// `verify` and M10's trust-cache diff harder to reason about and buys
// nothing.
var ErrRecipientExists = errors.New("gage: this vault already lists that recipient")

// ErrRecipientNotFound is a remove query naming nothing in the recipient
// list. Reported rather than treated as a no-op, so a typo can't read as
// a completed revocation.
var ErrRecipientNotFound = errors.New("gage: no recipient of this vault matches that name or key")

// ErrReencryptRequired is `recipient remove` without --reencrypt.
//
// Removing a key from the list while leaving every existing entry
// encrypted to it revokes nothing at all: the removed device can still
// read every secret it could read a moment earlier. gage refuses rather
// than performing a revocation that only looks like one — see "Recipient
// / access management" in the design doc.
var ErrReencryptRequired = errors.New("gage: removing a recipient requires --reencrypt")

// ErrCannotGrantFullAccess is a grant refused because the acting
// identity cannot itself read the whole vault.
//
// Since A19 every grant re-encrypts every entry, so an actor who is
// only a partial recipient has nothing full to grant. Without this
// check the attempt would still fail — reencryptTo dies on the first
// entry it cannot decrypt — but it would do so mid-write, inside the
// lock, naming an opaque entry UUID. This says what is actually wrong,
// before anything happens. See A19 and "Approval always re-encrypts".
var ErrCannotGrantFullAccess = errors.New(
	"gage: this device cannot read every entry in the vault, so it cannot grant full access")

// VaultRecipient is one entry of a vault's committed recipient list: the
// device name it is labelled with and its age public key.
type VaultRecipient struct{ Device, Pubkey string }

// RecipientVerification is what VerifyRecipients found: whether the two
// recipient-defining files agree, and where they don't.
//
// The differences are returned as data rather than printed, so the same
// value drives a CLI's exit code, a GUI's dialog, and M10's trust-cache
// diff — the pair this milestone keeps in sync is exactly what that
// cache watches.
type RecipientVerification struct {
	InSync               bool
	OnlyInRecipientsFile []string // in .age-recipients, missing from config.toml
	OnlyInConfig         []string // in config.toml, missing from .age-recipients
}

// RecipientChange is what an add or a remove did: which recipient, in
// which commit, and how many entries rode along in it.
type RecipientChange struct {
	Device      string
	Pubkey      string
	Commit      string // the single commit's hash
	Reencrypted int    // always the whole vault: both verbs re-encrypt
}

// recipientsFileName is the vault-relative name of the plain key list a
// stock age CLI can read. It and .gage/config.toml together define who
// can read a vault, and they are written and committed as a pair,
// always — see "On-disk layout" and M10's trust cache.
const recipientsFileName = ".age-recipients"

func (v *Vault) recipientsFilePath() string {
	return filepath.Join(v.Path, recipientsFileName)
}

func (v *Vault) vaultConfigPath() string {
	return filepath.Join(v.Path, ".gage", "config.toml")
}

// Recipients returns the vault's committed recipient list, in
// .gage/config.toml's own order.
//
// It takes no Identity and no Prompter, structurally rather than by
// convention: the recipient list is plaintext control-plane data, and
// reading it must not need a key. That is the same property `verify`
// depends on.
func (v *Vault) Recipients() ([]VaultRecipient, error) {
	vc, err := v.readVaultConfig()
	if err != nil {
		return nil, err
	}
	out := make([]VaultRecipient, 0, len(vc.Recipients))
	for _, r := range vc.Recipients {
		out = append(out, VaultRecipient{Device: r.Device, Pubkey: r.Pubkey})
	}
	return out, nil
}

// VerifyRecipients compares .age-recipients against .gage/config.toml's
// [[recipients]] and reports which keys only one of them knows about.
//
// Like Recipients it takes no Identity and no Prompter — there is no
// parameter to hand one through, which is what makes the design's "needs
// no unlock, safe to run in CI" claim provable rather than merely
// asserted. Both files are plaintext by design; a key in one and not the
// other is the tampering signature M10's trust cache keys off.
func (v *Vault) VerifyRecipients() (RecipientVerification, error) {
	fileKeys, err := v.readRecipientsFile()
	if err != nil {
		return RecipientVerification{}, err
	}
	configured, err := v.Recipients()
	if err != nil {
		return RecipientVerification{}, err
	}

	inConfig := make(map[string]bool, len(configured))
	for _, r := range configured {
		inConfig[r.Pubkey] = true
	}
	inFile := make(map[string]bool, len(fileKeys))
	for _, k := range fileKeys {
		inFile[k] = true
	}

	got := RecipientVerification{}
	for _, k := range fileKeys {
		if !inConfig[k] {
			got.OnlyInRecipientsFile = append(got.OnlyInRecipientsFile, k)
		}
	}
	for _, r := range configured {
		if !inFile[r.Pubkey] {
			got.OnlyInConfig = append(got.OnlyInConfig, r.Pubkey)
		}
	}
	sort.Strings(got.OnlyInRecipientsFile)
	sort.Strings(got.OnlyInConfig)
	got.InSync = len(got.OnlyInRecipientsFile) == 0 && len(got.OnlyInConfig) == 0
	return got, nil
}

// AddRecipient adds a device's public key to this vault's recipient list
// and commits both recipient-defining files together.
//
// Every existing entry is decrypted with ident and rewritten to the new
// list, always (A19). There is no invocation that produces a recipient
// who can read only part of the vault: that is not an access tier
// anyone chose but an artifact of age baking recipients into each file,
// and it is contagious — a partially admitted device can neither repair
// itself nor grant full access to anyone else.
//
// Entries and both recipient files land in the same single commit — one
// commit holding .age-recipients and .gage/config.toml, never one
// without the other, since a recipient change left uncommitted in the
// working tree would be silently discarded by the next write's
// dirty-tree reset, and M10's trust cache diffs exactly that pair. See
// reencryptTo for the ordering that makes an interrupted pass
// invisible.
func (v *Vault) AddRecipient(device, pubkey string, ident *Identity) (RecipientChange, error) {
	if !devicename.Valid(device) {
		return RecipientChange{}, exitcode.Newf(exitcode.Usage, "gage: device name %q is invalid", device)
	}
	if err := agekey.ValidateRecipient(pubkey); err != nil {
		return RecipientChange{}, exitcode.Wrap(exitcode.Usage, err)
	}

	// A19's pre-flight, ahead of the lock and ahead of every question
	// this method asks. An actor who cannot read the whole vault cannot
	// grant it, and finding that out here costs nothing and disturbs
	// nothing — where finding it out inside reencryptTo means a failure
	// mid-write naming an entry UUID. E4 runs the same pass from its own
	// position in `recipient approve`'s sequence, which is why it is a
	// separate callable pass rather than part of the body below.
	//
	// It is only authoritative against a clean working tree, which is
	// why it is gated on one. RequireFullAccess reads entries off disk,
	// and an interrupted `recipient remove <this device> --reencrypt`
	// leaves entries/ holding ciphertext written to the reduced list
	// while HEAD — the state withVaultWrite is about to reset back to —
	// still lists this device and is still entirely readable by it.
	// Refusing on that would send the operator to another device to
	// repair a vault that was never damaged. So on a dirty tree the
	// refusal moves inside the lock, to just after the reset: later
	// than the plan's "before the lock", but still before the first byte
	// is written and still before any confirmation is shown, which is
	// what the ordering is actually for.
	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		return RecipientChange{}, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: checking %q's working tree: %w", v.Name, err))
	}
	if clean {
		if err := v.RequireFullAccess(ident); err != nil {
			return RecipientChange{}, err
		}
	}

	change := RecipientChange{Device: device, Pubkey: pubkey}
	err = v.withVaultWrite(ident.warnTo(), func() error {
		// The pre-flight, if the tree above was dirty. withVaultWrite has
		// now reset it, so this is the first look at the state the
		// re-encryption will actually read — and it runs ahead of
		// everything below for the same reason the pre-lock pass runs
		// ahead of the lock: a refusal here has still disturbed nothing.
		if !clean {
			if err := v.RequireFullAccess(ident); err != nil {
				return err
			}
		}
		// M10's precondition, first: this verb rebuilds .age-recipients
		// from config.toml, so running it over a divergence would erase
		// the stray key and commit the result as an ordinary recipient
		// change. Under the lock so it can't race a concurrent repair,
		// and ahead of everything else — including the re-encryption
		// pass — so a refusal leaves nothing half-migrated.
		if err := v.requireRecipientsInSync(); err != nil {
			return err
		}
		// And M10's blocking check, second. The precondition above only
		// catches a list that disagrees with itself; a change pulled from
		// another device is perfectly consistent and still unreviewed
		// here. Without this, the cache regeneration at the tail of this
		// method would bless that key along with the one the operator
		// actually named — and would do it after rewriting every entry
		// to it. Ordering is the point: it runs before the first byte,
		// so a declined answer leaves nothing to undo, and after
		// requireRecipientsInSync so nobody is asked to approve an
		// operation that is about to be refused.
		if err := v.confirmRecipientTrust(ident.frontend()); err != nil {
			return err
		}

		current, err := v.Recipients()
		if err != nil {
			return err
		}
		for _, r := range current {
			if r.Pubkey == pubkey {
				return exitcode.Wrap(exitcode.Conflict,
					fmt.Errorf("%w: %s is already listed as %q", ErrRecipientExists, pubkey, r.Device))
			}
			if r.Device == device {
				return exitcode.Wrap(exitcode.Conflict,
					fmt.Errorf("%w: %q is already a recipient of this vault", ErrRecipientExists, device))
			}
		}

		updated := append(append([]VaultRecipient{}, current...), VaultRecipient{Device: device, Pubkey: pubkey})

		n, hash, err := v.commitRecipientList(updated, "gage: recipient add "+device+" (reencrypt)", ident)
		if err != nil {
			return err
		}
		change.Reencrypted = n
		change.Commit = hash

		// The operator just reviewed this list by typing the command, so
		// M10's cache is regenerated as the last step. Without it, `gage
		// recipient add` would warn them about their own add at their
		// very next write, which is the fastest way to teach someone to
		// stop reading the warning.
		return v.noteRecipientsReviewed()
	})
	if err != nil {
		return RecipientChange{}, err
	}
	return change, nil
}

// RemoveRecipient removes the recipient query names — a device name or a
// public key, since the design spells the argument "<pubkey-or-name>" —
// and re-encrypts every entry to the reduced list.
//
// reencrypt is required, not optional: see ErrReencryptRequired. Two
// removals are treated specially, and differently, because they differ
// in recoverability — the last recipient is refused outright, and this
// device's own key runs only behind an explicit confirmation naming the
// device that will lose access. See the M9 plan's resolved decision.
func (v *Vault) RemoveRecipient(query string, reencrypt bool, ident *Identity) (RecipientChange, error) {
	// Checked before the lock is taken and before anything is read: this
	// is a rejection of the command line itself, not a discovery about
	// the vault's state, so it should cost nothing and disturb nothing.
	if !reencrypt {
		return RecipientChange{}, exitcode.Wrap(exitcode.Usage, fmt.Errorf(
			"%w: without it the removed key still opens every entry that already exists, "+
				"so the revocation would only look like one", ErrReencryptRequired))
	}

	var change RecipientChange
	err := v.withVaultWrite(ident.warnTo(), func() error {
		// Same M10 precondition as AddRecipient, and first for the same
		// reasons — including before confirmSelfRemoval below, so the
		// operator is not asked to approve an operation that is about to
		// be refused anyway.
		if err := v.requireRecipientsInSync(); err != nil {
			return err
		}
		// M10's blocking check, for the same reason as in AddRecipient and
		// in the same position: this verb also regenerates the cache on
		// success, so it must not become a way to bless a recipient change
		// that arrived from somewhere else.
		if err := v.confirmRecipientTrust(ident.frontend()); err != nil {
			return err
		}

		current, err := v.Recipients()
		if err != nil {
			return err
		}

		idx := -1
		for i, r := range current {
			if r.Device == query || r.Pubkey == query {
				idx = i
				break
			}
		}
		if idx < 0 {
			return exitcode.Wrap(exitcode.NotFound, fmt.Errorf("%w: %q", ErrRecipientNotFound, query))
		}
		removed := current[idx]

		updated := append(append([]VaultRecipient{}, current[:idx]...), current[idx+1:]...)
		if len(updated) == 0 {
			return exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
				"%w: removing %q would leave nothing to encrypt to, and --reencrypt would rewrite "+
					"every entry to a list no key opens", ErrLastRecipient, removed.Device))
		}

		if err := confirmSelfRemoval(removed, v.Name, ident); err != nil {
			return err
		}

		// Said before the work, not after: by the time the commit lands
		// there is nothing left to reconsider, and the point of the
		// warning is that revocation is narrower than it sounds.
		warn(ident.warnTo(),
			"gage: removing %q revokes future access only — anything %s already read can't be unread.",
			removed.Device, removed.Device)

		n, hash, err := v.commitRecipientList(updated, "gage: recipient remove "+removed.Device+" (reencrypt)", ident)
		if err != nil {
			return err
		}
		change = RecipientChange{Device: removed.Device, Pubkey: removed.Pubkey, Commit: hash, Reencrypted: n}
		// As in AddRecipient: a list the operator changed here needs no
		// second review at their next write.
		return v.noteRecipientsReviewed()
	})
	if err != nil {
		return RecipientChange{}, err
	}
	return change, nil
}

// confirmSelfRemoval gates removing the key this vault was unlocked
// with. It is a legitimate operation — decommissioning this laptop from
// a vault other devices still use — and it works, because --reencrypt
// decrypts every entry with the still-valid local identity before
// re-encrypting to the reduced list. But it is also the one way to lock
// yourself out, so it asks first.
//
// A Prompter that answers no aborts with nothing written, and that
// includes every non-interactive Prompter: terminalPrompter.Confirm
// defaults to no, so a scripted run cannot fall into this by accident.
func confirmSelfRemoval(removed VaultRecipient, vault string, ident *Identity) error {
	if removed.Pubkey != ident.Recipient() && removed.Device != ident.Device() {
		return nil
	}
	p := ident.frontend()
	if p == nil {
		return exitcode.Newf(exitcode.LockedOrAuth,
			"gage: removing %q would lock this device out of %q, and there is no way to confirm that here", removed.Device, vault)
	}
	ok, err := p.Confirm(fmt.Sprintf(
		"Remove %q — this device's own key? It will no longer be able to read vault %q.", removed.Device, vault))
	if err != nil {
		return exitcode.Wrap(exitcode.LockedOrAuth, err)
	}
	if !ok {
		return exitcode.Newf(exitcode.Conflict,
			"gage: not removing %q; nothing was written", removed.Device)
	}
	return nil
}

// commitRecipientList is the shared tail of add and remove: re-encrypt
// every entry to the new list, write both recipient-defining files, and
// land all of it in exactly one commit.
//
// It takes no "whether to re-encrypt" parameter, and there is no path
// through it that skips the pass: A19 made it unconditional on add, and
// remove has always required it (ErrReencryptRequired).
//
// The order is the crash-safety property, not an implementation detail.
// Entries are written first, against the new list passed explicitly;
// .age-recipients and .gage/config.toml are written last, immediately
// before the commit. That keeps an interrupted run's dirty surface to
// entries/ — which the next write's reset discards — instead of leaving
// a recipient file that names a key no entry is actually encrypted to.
// Getting it backwards is precisely the half-migrated state this
// milestone exists to rule out.
//
// It runs with the vault write lock already held, by withVaultWrite.
func (v *Vault) commitRecipientList(updated []VaultRecipient, message string, ident *Identity) (reencrypted int, commit string, err error) {
	reencrypted, err = v.reencryptTo(updated, ident)
	if err != nil {
		return 0, "", err
	}

	if err := v.writeRecipientFiles(updated); err != nil {
		return 0, "", err
	}

	hash, err := gitrepo.CommitAll(v.Path, message)
	if err != nil {
		return 0, "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: committing the recipient change: %w", err))
	}
	// A failed push here is a publishing problem, not a half-done
	// migration: the guarantee is about HEAD, and HEAD already has all
	// of it. pushAfterWrite warns and proceeds, as it does after every
	// other write.
	v.pushAfterWrite(ident.warnTo())
	return reencrypted, hash, nil
}

// RequireFullAccess is A19's pre-flight: can ident actually read every
// entry in this vault?
//
// It is exported and standalone because two commands need it at two
// different points in their sequences. `recipient add` is wrapped in
// withUnlockedVault, so it runs this before the write lock and before
// M10's trust-cache confirmation. `recipient approve` shows its own
// confirmation before it unlocks at all, so it can only run this after
// that first question — still before the lock, and still before M10's.
// The guarantee both share is the one that matters: the refusal never
// arrives mid-write on an opaque entry UUID.
//
// It counts only entries ident is *not a recipient of*. A file that
// fails to decrypt for any other reason — a corrupt entry, a truncated
// header — is a different problem with a different fix, and the useful
// answer there is which entry, which is what reencryptTo already says.
// Folding those in would turn "one of your 47 entries is damaged" into
// "you are not allowed to do this", which is both wrong and unactionable.
//
// It takes no Prompter and asks nothing: the answer is a property of the
// vault and the identity, and every human decision this refusal leads to
// belongs to the caller.
func (v *Vault) RequireFullAccess(ident *Identity) error {
	ids, err := v.EntryIDs()
	if err != nil {
		return err
	}

	unreadable := 0
	for _, id := range ids {
		if _, err := v.ReadEntry(id, ident); errors.Is(err, ErrNotARecipient) {
			unreadable++
		}
	}
	if unreadable == 0 {
		return nil
	}

	// The count, and never the ids. Naming them would say nothing a
	// partially admitted device can act on — the entries are opaque to it
	// by construction — and it is precisely the unhelpful failure this
	// check exists to replace.
	return exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
		"%w: %d of %d %s cannot be read by %q; a device that can read all of %q "+
			"has to make this change",
		ErrCannotGrantFullAccess, unreadable, len(ids),
		entryNoun(len(ids)), ident.Device(), v.Name))
}

// entryNoun keeps the count above reading as a sentence rather than as a
// log line.
func entryNoun(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}

// reencryptTo rewrites every entry in the vault against the recipient
// list to, returning how many it rewrote.
//
// Every entry is decrypted first, and all of them must succeed before
// any is written back. A single unreadable entry aborts the whole
// operation with nothing touched and names the entry that failed — an
// abort that happened halfway would leave the vault split across two
// recipient lists, which is the state --reencrypt exists to avoid.
func (v *Vault) reencryptTo(to []VaultRecipient, ident *Identity) (int, error) {
	ids, err := v.EntryIDs()
	if err != nil {
		return 0, err
	}

	entries := make([]Entry, len(ids))
	for i, id := range ids {
		e, err := v.ReadEntry(id, ident)
		if err != nil {
			return 0, fmt.Errorf("gage: re-encrypting entry %s: %w", id, err)
		}
		entries[i] = e
	}

	parsed := make([]Recipient, len(to))
	for i, r := range to {
		p, err := ParseRecipient(r.Pubkey)
		if err != nil {
			return 0, fmt.Errorf("gage: re-encrypting to %q (%s): %w", r.Device, r.Pubkey, err)
		}
		parsed[i] = p
	}

	for i, id := range ids {
		if err := v.writeEntryTo(id, entries[i], parsed); err != nil {
			return 0, fmt.Errorf("gage: re-encrypting entry %s: %w", id, err)
		}
		if v.onReencryptEntry != nil {
			v.onReencryptEntry(i + 1)
		}
	}
	return len(ids), nil
}

// writeRecipientFiles writes .age-recipients and .gage/config.toml from
// one list, in that order and immediately before the caller's commit.
// Neither is ever written without the other.
func (v *Vault) writeRecipientFiles(list []VaultRecipient) error {
	keys := make([]string, len(list))
	for i, r := range list {
		keys[i] = r.Pubkey
	}
	if err := recipients.Write(v.recipientsFilePath(), keys); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}

	vc, err := v.readVaultConfig()
	if err != nil {
		return err
	}
	vc.Recipients = make([]vaultconfig.Recipient, len(list))
	for i, r := range list {
		vc.Recipients[i] = vaultconfig.Recipient{Device: r.Device, Pubkey: r.Pubkey}
	}
	if err := vaultconfig.Write(v.vaultConfigPath(), vc); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	return nil
}

// readVaultConfig parses this vault's committed .gage/config.toml,
// mapping its two typed refusals onto Conflict: both "written by a newer
// gage" and "carries a device name that isn't a safe path component" are
// states a human resolves, not internal failures.
func (v *Vault) readVaultConfig() (vaultconfig.File, error) {
	vc, err := vaultconfig.Read(v.vaultConfigPath())
	if err != nil {
		if errors.Is(err, vaultconfig.ErrUnsupportedFormatVersion) || errors.Is(err, vaultconfig.ErrInvalidDeviceName) {
			return vaultconfig.File{}, exitcode.Wrap(exitcode.Conflict, err)
		}
		return vaultconfig.File{}, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: reading %s: %w", v.vaultConfigPath(), err))
	}
	return vc, nil
}

// readRecipientsFile returns .age-recipients as a list of keys, in file
// order. Nothing here validates them: the file is deliberately a plain
// list a stock age CLI can read, and verify's whole job is to report
// what it actually contains rather than what it should.
func (v *Vault) readRecipientsFile() ([]string, error) {
	keys, err := recipients.Read(v.recipientsFilePath())
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: reading %s: %w", recipientsFileName, err))
	}
	return keys, nil
}
