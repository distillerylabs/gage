package gage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/syncerr"
)

// ErrNotInteractive is what resolution fails with when there is nobody to
// ask: a Prompter that cannot resolve a conflict (a script, a --stdin
// session, CI), or none at all. gage never picks a side on its own — a
// silent choice here is the last-write-wins outcome the sync model exists
// to refuse. See the M8b plan's non-interactive decision.
var ErrNotInteractive = errors.New("gage: resolving an entry conflict needs someone to ask")

// ConflictSide is one device's version of a conflicted entry, already
// decrypted.
type ConflictSide struct {
	// Present is false when this side deleted the entry — the
	// delete/modify case, where Entry carries nothing.
	Present bool
	// Entry is this side's decrypted content, including the `updated` and
	// `updated_by` the prompt shows to justify a choice.
	Entry Entry
}

// EntryConflict is one entry both sides changed, handed to a frontend as
// data rather than as printed text — see "Interactive decisions become
// data, not printed text". cmd/gage renders the design doc's
// [l/r/b/s/q] menu from this; a GUI would render a dialog.
type EntryConflict struct {
	// ID is the entry's id, which both sides share — `keep both` is what
	// introduces a second one.
	ID uuid.UUID
	// Path is the vault-relative path that conflicted, as the git side
	// named it.
	Path string

	Local  ConflictSide
	Remote ConflictSide
}

// Resolution is a frontend's answer to one EntryConflict.
type Resolution int

const (
	// KeepLocal keeps this device's version and discards the remote's.
	KeepLocal Resolution = iota
	// KeepRemote replaces this device's version with the remote's.
	KeepRemote
	// KeepBoth keeps this device's version where it is and writes the
	// remote's as a new entry under a fresh id, preserving its own
	// created/updated/updated_by. Nothing is destroyed, which is why this
	// option exists at all.
	KeepBoth
	// SkipConflict leaves this entry conflicted and moves to the next.
	// The sync then ends without applying anything or pushing: a skip is
	// not a silent "keep local".
	SkipConflict
	// AbortSync stops asking and restores the pre-sync state exactly.
	AbortSync
)

// ConflictPrompter is the half of a frontend that can resolve an entry
// conflict. It is deliberately separate from Prompter: a frontend with
// nobody to ask implements Prompter fully and this not at all, which
// makes the non-interactive refusal structural rather than a flag every
// caller has to remember to set.
type ConflictPrompter interface {
	ResolveConflict(c EntryConflict) (Resolution, error)
}

// ConflictResolver is what a sync needs in order to *resolve* a
// divergence rather than merely report one.
type ConflictResolver struct {
	// Prompter is asked once per conflicting entry. It must also satisfy
	// ConflictPrompter; one that doesn't is the non-interactive case.
	Prompter Prompter
	// Unlock supplies the identity resolution decrypts with. It is called
	// at most once, and only when a conflict is actually reached — a
	// fast-forward and a disjoint merge decrypt nothing and must not
	// prompt for a key. A session hands back its cached Identity here;
	// one-shot mode unlocks on the spot.
	Unlock func() (*Identity, error)
}

// SyncResolving is `gage sync` with interactive conflict resolution: pull,
// merge, and for each entry both sides changed, present both decrypted
// versions and apply the answer — landing the result as a real two-parent
// merge commit.
//
// It holds this vault's write lock for the whole run, including the time
// a human spends answering. See the M8b plan's locking decision: the
// concurrency this lock exists for is one person's several terminals, so
// "the other pane waits while I finish resolving" is the expected
// behavior — and taking the lock per applied choice instead would let a
// write land between two conflicts and move the local head out from
// under the merge being assembled.
func (v *Vault) SyncResolving(ctx context.Context, r ConflictResolver) (SyncReport, error) {
	return v.underWriteLock(func() (SyncReport, error) { return v.syncResolving(ctx, r) })
}

// syncResolving is SyncResolving's body, with the write lock already
// held.
//
// It opens as Sync does, and for the same reason: everything resolution
// might have to do is *first* something the automatic paths can do
// alone. A fast-forward, and a divergence whose two sides touched
// different entries, decrypt nothing and reach none of the code below —
// which is what keeps a passphrase prompt off the syncs that don't need
// one.
func (v *Vault) syncResolving(ctx context.Context, r ConflictResolver) (SyncReport, error) {
	pulled, err := v.pull(ctx)
	if err != nil {
		return pulled, err
	}

	report, err := v.push(ctx)
	report.Pulled = report.Pulled || pulled.Pulled
	report.Diverged = report.Diverged || pulled.Diverged
	if err == nil {
		return report, err
	}

	// Only a divergence push couldn't reconcile is ours. A recipient-file
	// conflict is refused exactly as M8a refuses it: those files define
	// who can read the vault at all, the merged result would be an access
	// list neither device wrote, and they resolve through M10's
	// trust-cache confirmation rather than through an [l/r/b] menu.
	if !errors.Is(err, syncerr.ErrDiverged) || report.ConflictKind() != ConflictEntries {
		return report, err
	}
	return v.resolveConflicts(ctx, r, report)
}

// resolveConflicts is the interactive half: ask about every conflicting
// entry, then apply every answer at once.
//
// "Then" is load-bearing. Nothing is written until every conflict has a
// real choice, which is what makes `skip` and `abort` all-or-nothing for
// free — there is no partial state to undo, because there is no partial
// state. It is also what a partial resolution would cost: a skipped
// entry left at its local version *inside a merge commit* would mark the
// remote's history as incorporated, the divergence would be silently
// gone, and `skip` would have quietly become `keep local` — the one
// outcome this milestone exists to prevent.
func (v *Vault) resolveConflicts(ctx context.Context, r ConflictResolver, report SyncReport) (SyncReport, error) {
	// Structural, not a flag: a frontend with nobody to ask implements
	// Prompter and not ConflictPrompter, and lands here. Checked before
	// unlocking, so a script isn't asked for a passphrase it will only
	// be refused with.
	asker, ok := r.Prompter.(ConflictPrompter)
	if !ok {
		return report, exitcode.Wrap(exitcode.Conflict, ErrNotInteractive)
	}
	if r.Unlock == nil {
		return report, exitcode.New(exitcode.Internal,
			"gage: resolving a conflict needs a way to unlock the vault")
	}

	merge, err := gitrepo.PrepareMerge(v.Path)
	if err != nil {
		return report, syncStateError(err)
	}
	conflicts := merge.Conflicts()

	// Every path is checked for being an entry before anything is
	// unlocked. A conflict on something that isn't an entry has no
	// [l/r/b] answer and is refused either way — asking for a passphrase
	// first would be asking for a key that is then never used to decrypt
	// anything, which is exactly what the lazy unlock exists to avoid.
	ids := make([]uuid.UUID, len(conflicts))
	for i, path := range conflicts {
		if ids[i], err = entryIDFromPath(path); err != nil {
			return report, err
		}
	}

	// The lazy unlock, at the one point plaintext is genuinely needed: a
	// session hands back its cached Identity here and nobody is
	// re-prompted, and one-shot mode asks for a passphrase now rather
	// than at the top of every sync.
	ident, err := r.Unlock()
	if err != nil {
		return report, err
	}

	choices := make(map[string]gitrepo.MergeSide, len(conflicts))
	adds := make(map[string][]byte)
	for i, path := range conflicts {
		c, err := v.conflictAt(merge, ids[i], path, ident)
		if err != nil {
			return report, err
		}
		answer, err := asker.ResolveConflict(c)
		if err != nil {
			return report, err
		}

		switch answer {
		case KeepLocal:
			choices[path] = gitrepo.LocalSide
		case KeepRemote:
			choices[path] = gitrepo.RemoteSide
		case KeepBoth:
			// This device's version stays exactly where it is, at its own
			// id; the other device's becomes a second entry. The two then
			// share a title, which M5's ambiguous-query resolver already
			// knows how to present — that is the whole reason keeping
			// both is viable rather than a way to make a vault confusing.
			choices[path] = gitrepo.LocalSide
			if err := v.keepBoth(c, adds); err != nil {
				return report, err
			}
		case SkipConflict:
			report.Skipped++
		case AbortSync:
			// Abort stops asking, where skip goes on to the next
			// question. Neither writes anything, and an abort reports no
			// skips: quitting is one decision about the whole
			// resolution, not a skip of each remaining entry — most of
			// which were never even presented.
			report.Aborted = true
			report.Skipped = 0
			return report, abortedRefusal()
		default:
			return report, exitcode.Newf(exitcode.Internal,
				"gage: a frontend answered a conflict with an unknown resolution (%d)", answer)
		}
	}

	if report.Skipped > 0 {
		return report, skippedRefusal(report.Skipped, len(conflicts))
	}
	return v.applyResolution(ctx, merge, report, choices, adds)
}

// keepBoth records the losing version of c as a brand-new entry under a
// fresh id, to be created as part of the merge commit.
//
// It re-encrypts rather than copying the remote's ciphertext across:
// both are legal, but re-encrypting normalizes the new file to *this*
// vault's current recipient list, so a version the other device wrote
// before a recipient change stays readable here.
//
// The Entry is written through verbatim, which is the point. Its
// created/updated/updated_by are the losing side's own — restamping them
// as this device, now, would discard exactly the provenance the conflict
// prompt just showed to justify the choice.
func (v *Vault) keepBoth(c EntryConflict, adds map[string][]byte) error {
	// Nothing to keep when the other side's answer was a deletion: there
	// is one version, and it is already staying put.
	if !c.Remote.Present {
		return nil
	}

	ciphertext, err := v.encryptEntry(c.Remote.Entry)
	if err != nil {
		return err
	}
	adds[entryFilePath(NewEntryID())] = ciphertext
	return nil
}

// applyResolution writes every answer, commits the result as a real
// merge, and publishes it.
func (v *Vault) applyResolution(ctx context.Context, merge *gitrepo.PendingMerge, report SyncReport,
	choices map[string]gitrepo.MergeSide, adds map[string][]byte) (SyncReport, error) {
	resolved := len(choices)
	if _, err := merge.Commit(resolutionMessage(resolved), choices, adds); err != nil {
		return report, syncStateError(err)
	}
	report.Merged = true
	report.Resolved = resolved

	// The merge moved entries/ underneath anyone caching this vault's
	// decrypted metadata — an entry replaced by `keep remote`, another
	// added by `keep both` — for the same reason a pull does. Without
	// this the next `ls` in a session would still show the pre-resolution
	// vault.
	v.notePulled()

	// Counted before the push rather than only after a successful one, so
	// a resolution whose publish then fails still reports how much is
	// pending — the merge commit included.
	var err error
	if report.Ahead, err = gitrepo.AheadCount(v.Path); err != nil {
		return report, syncStateError(err)
	}
	// A fresh window for the publish. The context a sync starts with
	// bounds gage's *network* work (RemoteOpTimeout), and the questions
	// above sat in the middle of it — a deadline generous for a fetch is
	// meaningless against a human weighing up two versions of a
	// password. Reusing an expired one would fail to push a merge that is
	// already committed, for no reason but that someone thought about the
	// answer.
	pushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RemoteOpTimeout)
	defer cancel()
	if err := v.syncer().Push(pushCtx); err != nil {
		return report, err
	}
	report.Pushed = true
	report.Ahead = 0
	return report, nil
}

// conflictAt decrypts both sides of one conflicting path and packages
// them as the value a frontend renders its prompt from.
//
// Both sides come out of git objects rather than the working tree: the
// remote's version was never written to disk (a conflicted merge writes
// nothing), and reading the local one from the tree instead would be a
// second source of truth for what "local" means.
func (v *Vault) conflictAt(merge *gitrepo.PendingMerge, id uuid.UUID, path string,
	ident *Identity) (EntryConflict, error) {
	local, err := conflictSide(merge, gitrepo.LocalSide, path, ident)
	if err != nil {
		return EntryConflict{}, err
	}
	remote, err := conflictSide(merge, gitrepo.RemoteSide, path, ident)
	if err != nil {
		return EntryConflict{}, err
	}
	return EntryConflict{ID: id, Path: path, Local: local, Remote: remote}, nil
}

// conflictSide decrypts one side's version of a path. A side that
// doesn't have the path at all deleted it, which is the delete/modify
// case — an absent side, not an error.
func conflictSide(merge *gitrepo.PendingMerge, side gitrepo.MergeSide, path string,
	ident *Identity) (ConflictSide, error) {
	ciphertext, present, err := merge.Content(side, path)
	if err != nil {
		return ConflictSide{}, exitcode.Wrap(exitcode.Internal, err)
	}
	if !present {
		return ConflictSide{}, nil
	}
	e, err := decryptEntry(ciphertext, ident)
	if err != nil {
		return ConflictSide{}, err
	}
	return ConflictSide{Present: true, Entry: e}, nil
}

// entryFilePath is an entry's path as the git side names it — always
// slash-separated, since that is what a git tree stores regardless of
// platform.
func entryFilePath(id uuid.UUID) string {
	return entriesDirName + "/" + id.String() + entryFileExt
}

// entryIDFromPath recovers an entry's id from that path.
//
// A conflicting path that isn't an entry has no [l/r/b] answer that
// means anything — the two files that define who can read the vault are
// already refused before this point, and anything else in a vault is
// gage's own structure rather than a secret with two versions. So it is
// reported rather than guessed at.
func entryIDFromPath(path string) (uuid.UUID, error) {
	if stem, ok := strings.CutPrefix(path, entriesDirName+"/"); ok {
		if stem, ok := strings.CutSuffix(stem, entryFileExt); ok {
			if id, err := uuid.Parse(stem); err == nil {
				return id, nil
			}
		}
	}
	return uuid.Nil, exitcode.Newf(exitcode.Conflict,
		"gage: %q changed on both sides and is not an entry, so there is no version of it to choose; "+
			"the vault is an ordinary git repository", path)
}

// resolutionMessage is what a resolved merge commits under. Like every
// other commit message gage writes it names no entry and carries no
// plaintext — see the M4 plan's commit-message confidentiality rule,
// which merges inherit rather than escape. The count is the most it can
// say: how much was decided, never what.
func resolutionMessage(resolved int) string {
	return fmt.Sprintf("%s, resolving %s", mergeCommitMessage, entryCount(resolved))
}

// entryCount renders a number of entries as English, since these strings
// are read by people rather than parsed.
func entryCount(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

// skippedRefusal is how a resolution that left something unanswered
// reports itself: nothing applied, nothing pushed, and the divergence
// still there to come back to.
func skippedRefusal(skipped, total int) error {
	return exitcode.Wrap(exitcode.Conflict, &resolutionRefusal{
		msg: fmt.Sprintf(
			"gage: %d of %d conflicting entries left unresolved, so nothing was applied and "+
				"nothing was pushed; run `gage sync` again to finish", skipped, total),
	})
}

// abortedRefusal is the same for quitting outright.
func abortedRefusal() error {
	return exitcode.Wrap(exitcode.Conflict, &resolutionRefusal{
		msg: "gage: resolution aborted; nothing was changed and nothing was pushed",
	})
}

// resolutionRefusal reports an unfinished resolution in its own words
// while still matching syncerr.ErrDiverged, because the divergence it
// declined to resolve is still there — a caller asking "is this vault
// still diverged from origin" gets the same answer it would have got
// before anyone was asked anything.
//
// Separate from divergenceRefusal for the same reason that one exists:
// matching a sentinel and phrasing a message are different jobs, and
// wrapping with %w would prepend one to the other.
type resolutionRefusal struct{ msg string }

func (e *resolutionRefusal) Error() string { return e.msg }
func (e *resolutionRefusal) Unwrap() error { return syncerr.ErrDiverged }
