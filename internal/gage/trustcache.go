package gage

// M10 — local trust cache.
//
// This device's record of the recipient list it last confirmed for each
// vault, and the checks that consult it: a blocking one before anything
// is encrypted, and an opportunistic one on unlock and on sync. See
// doc/implementation/00_gage-cli/plans/gage-cli-design/m10-trust-cache.md and the design doc's "Trust
// boundaries" / "Local trust cache".

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/distillerylabs/gage/internal/gage/atomicfile"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

// ErrRecipientChangeDeclined is an encrypting method refusing to write
// because the operator did not approve a recipient list that changed
// since this device last confirmed one. Nothing is written, nothing is
// committed, and the cache stays where it was, so the same question is
// asked again next time.
var ErrRecipientChangeDeclined = errors.New("gage: the recipient list changed and was not confirmed")

// ErrRecipientsOutOfSync is AddRecipient/RemoveRecipient refusing to run
// against a vault whose .age-recipients and .gage/config.toml disagree.
//
// Both verbs rebuild .age-recipients from config.toml, so running either
// over a divergence would overwrite the stray key out of existence and
// commit the result as an ordinary recipient change — destroying the
// evidence, producing a clean `verify`, and regenerating the cache
// against a list nobody reviewed. The exit is `recipient verify
// --repair`, which performs the same overwrite deliberately and says
// what it drops.
var ErrRecipientsOutOfSync = errors.New("gage: this vault's recipient files disagree")

// RecipientChangeWarning is what the library hands a frontend when the
// committed recipient list no longer matches what this device last
// confirmed: the diff to show, whether the two recipient-defining files
// still agree with each other, and which recipients moved.
//
// It is a value, not printed text, for the same reason CandidateList is:
// cmd/gage renders it as the terminal diff + [y/N] prompt in the design
// doc, a GUI would render it as a dialog, and neither rendering belongs
// in a library that must not touch a terminal.
type RecipientChangeWarning struct {
	// Vault is the vault whose list changed — named because a session
	// holds several, and the warning must say which one it is about.
	Vault string

	// Diff is the unified diff of the cached known-config.toml against
	// the vault's current committed .gage/config.toml. It can be empty:
	// an edit to .age-recipients alone changes nothing in config.toml,
	// and that case is caught by Verification instead.
	Diff string

	// Verification is the current VerifyRecipients result. InSync false
	// is the tampering signature, and it is what makes this a
	// mismatched change rather than a routine one.
	Verification RecipientVerification

	// Added and Removed are the recipients config.toml gained and lost
	// since the cached copy, so a frontend can summarize without
	// parsing Diff.
	Added   []VaultRecipient
	Removed []VaultRecipient

	// LastConfirmed is when this device last confirmed a recipient list
	// for this vault — the date the design doc's diff header carries.
	LastConfirmed time.Time
}

// Mismatched reports whether this is the severe case: .age-recipients
// and config.toml disagree with each other, not merely with the cache.
// Confirming past a mismatched change does not regenerate the cache —
// see "Cache regeneration isn't one-size-fits-all".
func (w RecipientChangeWarning) Mismatched() bool { return !w.Verification.InSync }

// Summary is the one-line form the opportunistic, non-blocking checks
// warn with — on Vault.Unlock and on sync, where there is nothing to
// block and nobody being asked.
//
// It says what to do next rather than only that something happened: the
// warning rides along with a read the operator asked for, so the thing
// it interrupts is not the thing it is about.
func (w RecipientChangeWarning) Summary() string {
	if w.Mismatched() {
		return fmt.Sprintf(
			"gage: %q's recipient list changed since this device last confirmed it, and its two "+
				"recipient files now disagree — run `gage recipient verify`.", w.Vault)
	}
	return fmt.Sprintf(
		"gage: %q's recipient list changed since this device last confirmed it (%s); "+
			"the next write here will ask you to review it.", w.Vault, w.delta())
}

// delta is Summary's parenthetical: what moved, counted rather than
// listed, since a one-line advisory has no room for keys.
func (w RecipientChangeWarning) delta() string {
	switch {
	case len(w.Added) > 0 && len(w.Removed) > 0:
		return fmt.Sprintf("%d added, %d removed", len(w.Added), len(w.Removed))
	case len(w.Added) > 0:
		return fmt.Sprintf("%d added", len(w.Added))
	case len(w.Removed) > 0:
		return fmt.Sprintf("%d removed", len(w.Removed))
	default:
		// config.toml is unchanged, so nothing can be counted from it —
		// the .age-recipients-only edit, which is the case with the
		// least to say and the most to worry about.
		return ".age-recipients was edited on its own"
	}
}

// trustCache is this device's record of the recipient list it last
// confirmed for one vault: a verbatim copy of .gage/config.toml and the
// content hash of .age-recipients.
//
// Both halves matter. The config copy is what gets diffed and shown,
// because device names make a legible warning; the hash is what catches
// someone editing only the file encryption actually reads.
type trustCache struct {
	// KnownConfig is the verbatim bytes of .gage/config.toml as last
	// confirmed. Verbatim, not re-serialized: the diff a human reads has
	// to be a diff of the change, not of gage's formatting.
	KnownConfig []byte

	// RecipientsHash is the hex sha256 of .age-recipients as last
	// confirmed.
	RecipientsHash string

	// ConfirmedAt is when this record was written.
	ConfirmedAt time.Time

	// MismatchSeen records the hex sha256 of an .age-recipients that was
	// acknowledged while it disagreed with config.toml. It is the "gage
	// records that the mismatch was seen" half of the two-outcome rule:
	// it never clears the inconsistency, and neither half above moves.
	MismatchSeen string
}

// trustRecord is trust.toml's on-disk shape — everything in a trustCache
// except the config copy, which is its own file so that a human (and a
// diff) can read it as the TOML it is.
type trustRecord struct {
	RecipientsHash string    `toml:"recipients_hash"`
	ConfirmedAt    time.Time `toml:"confirmed_at"`
	MismatchSeen   string    `toml:"mismatch_seen,omitempty"`
}

const (
	// knownConfigFileName is the verbatim copy's name. It is the file the
	// plan and the design doc both name, and the one a human comparing
	// caches by hand would reach for.
	knownConfigFileName = "known-config.toml"
	// trustFileName holds the hash half plus this record's metadata.
	trustFileName = "trust.toml"
)

// TrustCacheDir is where a vault's trust cache lives:
// $GAGE_STATE/<vault-id>. Under the state root, never inside the vault:
// it is local, uncommitted, never synced, and explicitly not something to
// carry to a new machine.
//
// Keyed by the id for the same reason the identities directory is (A20),
// though the stakes here are lower: an inherited cache produces a
// spurious recipient-change warning rather than suppressing a real one.
// There is still no reason to leave one keying scheme correct and its
// neighbour wrong when the id exists anyway.
func TrustCacheDir(vaultID string) (string, error) {
	if err := checkVaultID(vaultID); err != nil {
		return "", err
	}
	stateDir, err := xdgpaths.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, vaultID), nil
}

// loadTrustCache reads this device's record for the vault. The bool is
// false when no record exists yet — the first-use case, which is
// bootstrapped silently rather than warned about.
//
// Either file missing reads as "no record". The two are written as a
// pair and a half-written pair is not a record this device can honestly
// claim to have confirmed; treating it as a first use re-bootstraps it
// on the next successful use, which is the same weak-but-honest position
// a genuinely new device is in.
func (v *Vault) loadTrustCache() (trustCache, bool, error) {
	dir, err := TrustCacheDir(v.ID)
	if err != nil {
		return trustCache{}, false, err
	}

	// #nosec G304 -- both paths come from TrustCacheDir, which validates
	// the vault id against the traversal rule before joining it.
	known, err := os.ReadFile(filepath.Join(dir, knownConfigFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return trustCache{}, false, nil
		}
		return trustCache{}, false, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: reading the trust cache for %q: %w", v.Name, err))
	}
	// #nosec G304 -- as above.
	data, err := os.ReadFile(filepath.Join(dir, trustFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return trustCache{}, false, nil
		}
		return trustCache{}, false, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: reading the trust cache for %q: %w", v.Name, err))
	}

	var rec trustRecord
	if err := toml.Unmarshal(data, &rec); err != nil {
		return trustCache{}, false, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: parsing %s for %q: %w", trustFileName, v.Name, err))
	}
	return trustCache{
		KnownConfig:    known,
		RecipientsHash: rec.RecipientsHash,
		ConfirmedAt:    rec.ConfirmedAt,
		MismatchSeen:   rec.MismatchSeen,
	}, true, nil
}

// storeTrustCache writes both halves through M0's atomic-write helper.
//
// The config copy goes first. There is no way to make two files land
// together, so the ordering is chosen for which torn state is safer: a
// new config copy beside an old hash reads as a changed recipient list
// and warns, which is the direction a half-finished write should fail
// in.
func (v *Vault) storeTrustCache(c trustCache) error {
	dir, err := TrustCacheDir(v.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: creating the trust cache directory for %q: %w", v.Name, err))
	}

	if err := atomicfile.WriteFile(filepath.Join(dir, knownConfigFileName), c.KnownConfig, 0o600); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: writing %s: %w", knownConfigFileName, err))
	}

	data, err := toml.Marshal(trustRecord{
		RecipientsHash: c.RecipientsHash,
		ConfirmedAt:    c.ConfirmedAt,
		MismatchSeen:   c.MismatchSeen,
	})
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: encoding %s: %w", trustFileName, err))
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, trustFileName), data, 0o600); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: writing %s: %w", trustFileName, err))
	}
	return nil
}

// RemoveTrustCache deletes $GAGE_STATE/<vault-id>/ — what `vault remove`
// does alongside dropping the registration, so that re-adding the vault
// is a first use rather than an inherited approval for a list nobody
// looked at in the interval.
func RemoveTrustCache(vaultID string) error {
	dir, err := TrustCacheDir(vaultID)
	if err != nil {
		return err
	}

	// The files this cache owns, by name, rather than RemoveAll on the
	// directory. That was originally load-bearing: $GAGE_STATE/<vault>
	// was keyed by a name the user chose, and one such name — "locks" —
	// resolves to the directory holding *every* vault's lock file (see
	// LockFilePath), so RemoveAll there would have broken mutual
	// exclusion for a process mid-write. A20's id-keying makes that
	// collision unreachable, since a UUID is never "locks"; removing
	// what this cache owns and nothing else is kept anyway, because a
	// narrow delete needs no such argument to be safe. Anything added to
	// trustCache's on-disk form belongs in this list too.
	for _, name := range []string{knownConfigFileName, trustFileName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return exitcode.Wrap(exitcode.Internal,
				fmt.Errorf("gage: removing the trust cache for %q: %w", vaultID, err))
		}
	}
	// Then the directory itself, which is what "removes
	// $GAGE_STATE/<vault-id>/" means. A non-empty directory fails here
	// and is left alone on purpose, and the cache it held is already
	// gone.
	_ = os.Remove(dir)
	return nil
}

// currentTrustState is what the cache would hold if this device
// confirmed the vault's committed recipient files right now.
//
// The config copy is the file's own bytes and the hash is the
// .age-recipients file's own bytes — neither is re-serialized on the way
// through, so a diff of two of these is a diff of what changed rather
// than of how gage happened to write it.
func (v *Vault) currentTrustState() (trustCache, error) {
	// #nosec G304 -- vaultConfigPath is <vault>/.gage/config.toml, built
	// from the registered vault path rather than from user input.
	config, err := os.ReadFile(v.vaultConfigPath())
	if err != nil {
		return trustCache{}, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: reading %s: %w", v.vaultConfigPath(), err))
	}
	// #nosec G304 -- as above, for <vault>/.age-recipients.
	keys, err := os.ReadFile(v.recipientsFilePath())
	if err != nil {
		return trustCache{}, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: reading %s: %w", recipientsFileName, err))
	}
	sum := sha256.Sum256(keys)
	return trustCache{
		KnownConfig:    config,
		RecipientsHash: hex.EncodeToString(sum[:]),
		ConfirmedAt:    time.Now(),
	}, nil
}

// checkRecipientTrust compares the committed recipient files against
// this device's cached record. A nil warning means nothing to say: the
// files match the record, or there was no record and this use
// bootstrapped one.
//
// Both halves are compared, and either one differing is enough. That is
// the whole reason there are two: a change to .age-recipients alone
// leaves config.toml — and so the diff — completely unchanged, which is
// exactly the edit that matters most.
func (v *Vault) checkRecipientTrust() (*RecipientChangeWarning, error) {
	current, err := v.currentTrustState()
	if err != nil {
		return nil, err
	}

	cache, ok, err := v.loadTrustCache()
	if err != nil {
		return nil, err
	}
	if !ok {
		// First use. The very first recipient list a device sees is
		// trusted on faith — inherent to a trust-on-first-use record,
		// and why this mechanism is a backstop rather than a guarantee.
		return nil, v.storeTrustCache(current)
	}
	if bytes.Equal(cache.KnownConfig, current.KnownConfig) && cache.RecipientsHash == current.RecipientsHash {
		return nil, nil
	}

	w := &RecipientChangeWarning{Vault: v.Name, LastConfirmed: cache.ConfirmedAt}
	if !bytes.Equal(cache.KnownConfig, current.KnownConfig) {
		w.Diff = unifiedDiff(
			fmt.Sprintf("%s (last confirmed %s)", knownConfigFileName, cache.ConfirmedAt.UTC().Format("2006-01-02")),
			".gage/config.toml (current)",
			cache.KnownConfig, current.KnownConfig)
	}

	w.Added, w.Removed, err = v.recipientDelta(cache.KnownConfig)
	if err != nil {
		return nil, err
	}
	w.Verification, err = v.VerifyRecipients()
	if err != nil {
		return nil, err
	}
	return w, nil
}

// recipientDelta reports which recipients the committed config.toml has
// gained and lost since the cached copy, so a frontend can summarize the
// change without parsing a diff.
//
// The cached bytes are parsed with the plain TOML decoder rather than
// through vaultconfig.Read: this is a copy gage itself wrote from a file
// that was already validated, and an old cache that a newer gage would
// reject on format_version must still be *diffable* — refusing to
// describe the change would suppress the warning the refusal is about.
func (v *Vault) recipientDelta(cached []byte) (added, removed []VaultRecipient, err error) {
	var was vaultconfig.File
	if err := toml.Unmarshal(cached, &was); err != nil {
		return nil, nil, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: parsing the cached %s for %q: %w", knownConfigFileName, v.Name, err))
	}
	now, err := v.Recipients()
	if err != nil {
		return nil, nil, err
	}

	before := make(map[string]VaultRecipient, len(was.Recipients))
	for _, r := range was.Recipients {
		before[r.Pubkey] = VaultRecipient{Device: r.Device, Pubkey: r.Pubkey}
	}
	after := make(map[string]bool, len(now))
	for _, r := range now {
		after[r.Pubkey] = true
		if _, had := before[r.Pubkey]; !had {
			added = append(added, r)
		}
	}
	// In config.toml's own order, like Recipients itself, so the two
	// lists read the same way the file does.
	for _, r := range was.Recipients {
		if !after[r.Pubkey] {
			removed = append(removed, VaultRecipient{Device: r.Device, Pubkey: r.Pubkey})
		}
	}
	return added, removed, nil
}

// confirmRecipientTrust is the blocking pre-encrypt check, run under the
// vault lock before anything is written. It asks through the Prompter,
// returns ErrRecipientChangeDeclined on a no, and on a yes applies the
// two-outcome rule: a routine change regenerates both cache halves, a
// mismatched one records only that it was seen.
func (v *Vault) confirmRecipientTrust(p Prompter) error {
	w, err := v.checkRecipientTrust()
	if err != nil || w == nil {
		return err
	}
	// A library caller with nobody to ask is a caller that cannot
	// approve, and an unapproved recipient list is exactly what this
	// check refuses to encrypt to.
	if p == nil {
		return exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
			"%w: %q's recipient list changed and there is no way to confirm it here",
			ErrRecipientChangeDeclined, v.Name))
	}

	ok, err := p.ConfirmRecipientChange(*w)
	if err != nil {
		return exitcode.Wrap(exitcode.Conflict, err)
	}
	if !ok {
		return exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
			"%w: nothing was written to %q", ErrRecipientChangeDeclined, v.Name))
	}

	// The two outcomes, decided here off VerifyRecipients rather than off
	// who answered — which is what makes cmd/gage's --yes structurally
	// unable to buy silence for a mismatch.
	if w.Mismatched() {
		return v.noteRecipientMismatchSeen()
	}
	return v.noteRecipientsReviewed()
}

// warnRecipientTrust is the opportunistic, non-blocking check wired into
// Vault.Unlock and into sync. It never asks, never regenerates the
// cache, and never fails the operation it rides along with.
//
// A failure to read the cache or the recipient files is swallowed on
// purpose: every caller is on a path that has already succeeded, and an
// advisory must not be the thing that turns a working unlock or a
// completed sync into an error. The blocking check before the next
// encrypt reports the same failure where it can actually be acted on.
func (v *Vault) warnRecipientTrust(p Prompter) {
	w, err := v.checkRecipientTrust()
	if err != nil || w == nil {
		return
	}
	warn(p, "%s", w.Summary())
}

// noteRecipientsReviewed regenerates both cache halves against the
// current committed state — what a confirmed routine change does, and
// what a successful local `recipient add`/`remove` does as its last
// step.
//
// MismatchSeen is not carried over: it records an acknowledgment of an
// inconsistency, and both files agreeing is what settles it.
func (v *Vault) noteRecipientsReviewed() error {
	current, err := v.currentTrustState()
	if err != nil {
		return err
	}
	return v.storeTrustCache(current)
}

// noteRecipientMismatchSeen is the other outcome: the operator confirmed
// a change whose two recipient files disagree, so the write proceeds and
// nothing else does. Both cache halves stay exactly where they were —
// `verify` keeps failing, and the next write asks again — and all that
// is recorded is which .age-recipients was acknowledged.
func (v *Vault) noteRecipientMismatchSeen() error {
	cache, ok, err := v.loadTrustCache()
	if err != nil {
		return err
	}
	if !ok {
		// Unreachable in practice: a mismatch is only ever reported
		// against a record that exists. Bootstrapping one here would
		// silently approve the very list the mismatch is about.
		return nil
	}
	current, err := v.currentTrustState()
	if err != nil {
		return err
	}
	cache.MismatchSeen = current.RecipientsHash
	return v.storeTrustCache(cache)
}

// requireRecipientsInSync is AddRecipient's and RemoveRecipient's
// precondition: both verbs rebuild .age-recipients from config.toml, so
// running either over a divergence would overwrite the stray key out of
// existence and commit the result as an ordinary recipient change.
//
// It names the differences rather than only refusing. "No" without "here
// is what is wrong" leaves the operator running `verify` to find out
// what gage already knew, and the differences are the whole content of
// the refusal.
func (v *Vault) requireRecipientsInSync() error {
	got, err := v.VerifyRecipients()
	if err != nil {
		return err
	}
	if got.InSync {
		return nil
	}
	return exitcode.Wrap(exitcode.Conflict, fmt.Errorf("%w:\n%s\n%s",
		ErrRecipientsOutOfSync, recipientDifferenceLines(got),
		"gage: fix this with `gage recipient verify --repair`, "+
			"which rewrites .age-recipients from .gage/config.toml and says what it drops"))
}

// recipientDifferenceLines renders a failing verification the way
// `recipient verify` renders it, so the refusal and the diagnostic verb
// describe the same vault in the same words.
func recipientDifferenceLines(got RecipientVerification) string {
	lines := make([]string, 0, len(got.OnlyInRecipientsFile)+len(got.OnlyInConfig))
	for _, k := range got.OnlyInRecipientsFile {
		lines = append(lines, fmt.Sprintf("  %s is in %s but not in .gage/config.toml", k, recipientsFileName))
	}
	for _, k := range got.OnlyInConfig {
		lines = append(lines, fmt.Sprintf("  %s is in .gage/config.toml but not in %s", k, recipientsFileName))
	}
	return strings.Join(lines, "\n")
}

// RecipientRepair is what `recipient verify --repair` did: the keys it
// dropped from .age-recipients, the keys it added to it, and the commit
// holding the rewrite.
type RecipientRepair struct {
	Dropped []string
	Added   []string
	Commit  string
}

// RepairRecipients rewrites .age-recipients from .gage/config.toml's
// list and commits it — the deliberate, visible form of the overwrite
// AddRecipient used to perform as a side effect.
//
// It confirms first, naming every key it drops and every key it adds; a
// Prompter that answers no writes and commits nothing. It deliberately
// does not regenerate the trust cache: repair settles the inconsistency
// between the two files, not the question of whether the recipient list
// itself is one this device approves.
func (v *Vault) RepairRecipients(p Prompter) (RecipientRepair, error) {
	var repair RecipientRepair
	err := v.withVaultWrite(p, func() error {
		// Re-read under the lock rather than trusting a verification the
		// caller did outside it: what gets rewritten has to be what was
		// just looked at.
		got, err := v.VerifyRecipients()
		if err != nil {
			return err
		}
		if got.InSync {
			// Nothing to repair, so nothing to ask about and nothing to
			// commit. Not an error: `verify --repair` on a healthy vault
			// is a reasonable thing to run.
			return nil
		}
		repair.Dropped = got.OnlyInRecipientsFile
		repair.Added = got.OnlyInConfig

		if p == nil {
			return exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
				"%w, and there is no way to confirm a repair here", ErrRecipientsOutOfSync))
		}
		ok, err := p.Confirm(repairPrompt(v.Name, repair))
		if err != nil {
			return exitcode.Wrap(exitcode.Conflict, err)
		}
		if !ok {
			return exitcode.Newf(exitcode.Conflict,
				"gage: not repairing %q's recipient files; nothing was written", v.Name)
		}

		configured, err := v.Recipients()
		if err != nil {
			return err
		}
		if err := v.writeRecipientFiles(configured); err != nil {
			return err
		}
		hash, err := gitrepo.CommitAll(v.Path, "gage: recipient repair")
		if err != nil {
			return exitcode.Wrap(exitcode.Internal,
				fmt.Errorf("gage: committing the recipient repair: %w", err))
		}
		repair.Commit = hash
		v.pushAfterWrite(p)
		// No noteRecipientsReviewed here, deliberately: see this
		// method's doc comment.
		return nil
	})
	if err != nil {
		return RecipientRepair{}, err
	}
	return repair, nil
}

// repairPrompt is the confirmation, and it names every key on both sides
// because that list is the entire decision: rewriting .age-recipients
// from config.toml is only safe if the operator agrees the keys it
// removes are ones that should not have been there.
func repairPrompt(vault string, repair RecipientRepair) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Rewrite %q's %s from .gage/config.toml?", vault, recipientsFileName))
	for _, k := range repair.Dropped {
		b.WriteString(fmt.Sprintf("\n  drop %s (it can read anything written while it was listed)", k))
	}
	for _, k := range repair.Added {
		b.WriteString(fmt.Sprintf("\n  add  %s (config.toml already lists it)", k))
	}
	return b.String()
}

// ---------------------------------------------------------------------
// Diffing the cached config against the committed one
// ---------------------------------------------------------------------

// diffContextLines is how much unchanged text surrounds each hunk. The
// files being compared are a few dozen lines of TOML, so this is about
// keeping a [[recipients]] header visible above the device line that
// changed under it, not about bounding output size.
const diffContextLines = 3

// unifiedDiff renders a and b as a unified diff, with oldName/newName as
// the ---/+++ header lines.
//
// gage carries its own rather than pulling in a diff library because
// what it diffs is one small, self-written TOML file against another,
// and because the result is read by a human deciding whether to trust a
// recipient list — a dependency whose output shape could change under us
// is a poor fit for a security prompt.
func unifiedDiff(oldName, newName string, a, b []byte) string {
	oldLines := splitDiffLines(a)
	newLines := splitDiffLines(b)
	common := longestCommonSubsequence(oldLines, newLines)

	var out strings.Builder
	out.WriteString(fmt.Sprintf("--- %s\n+++ %s\n", oldName, newName))
	for _, h := range diffHunks(common) {
		out.WriteString(h)
	}
	return strings.TrimRight(out.String(), "\n")
}

// splitDiffLines splits a file into lines without a trailing empty one,
// so a file ending in a newline doesn't diff as having a blank last
// line.
func splitDiffLines(data []byte) []string {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// diffOp is one line of the edit script: kept, removed, or added.
type diffOp struct {
	kind byte // ' ', '-', or '+'
	text string
}

// longestCommonSubsequence returns the edit script turning a into b, as
// the classic LCS table walked backwards. Quadratic, which is the right
// trade for two files of a few dozen lines each.
func longestCommonSubsequence(a, b []string) []diffOp {
	n, m := len(a), len(b)
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			table[i][j] = max(table[i+1][j], table[i][j+1])
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// diffHunks groups an edit script into unified-diff hunks with
// diffContextLines of unchanged text on either side of each run of
// changes.
func diffHunks(ops []diffOp) []string {
	// Which ops are worth printing: every change, plus the context
	// around it.
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == ' ' {
			continue
		}
		for j := max(0, i-diffContextLines); j <= min(len(ops)-1, i+diffContextLines); j++ {
			keep[j] = true
		}
	}

	var (
		hunks              []string
		oldLine, newLine   = 1, 1
		body               strings.Builder
		oldStart, newStart int
		oldCount, newCount int
		open               bool
	)
	flush := func() {
		if !open {
			return
		}
		hunks = append(hunks, fmt.Sprintf("@@ -%d,%d +%d,%d @@\n%s",
			oldStart, oldCount, newStart, newCount, body.String()))
		body.Reset()
		oldCount, newCount, open = 0, 0, false
	}

	for i, op := range ops {
		if !keep[i] {
			flush()
			if op.kind != '+' {
				oldLine++
			}
			if op.kind != '-' {
				newLine++
			}
			continue
		}
		if !open {
			open = true
			oldStart, newStart = oldLine, newLine
		}
		body.WriteString(fmt.Sprintf("%c%s\n", op.kind, op.text))
		if op.kind != '+' {
			oldLine++
			oldCount++
		}
		if op.kind != '-' {
			newLine++
			newCount++
		}
	}
	flush()
	return hunks
}
