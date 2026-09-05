package gage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/syncerr"
	"github.com/denmark/gage/internal/gage/vaultlock"
)

// RemoteOpTimeout bounds every network operation gage starts on its own —
// the fetch on unlock and the push after a write. It exists because those
// are the paths a human never asked for: waiting out a full TCP timeout
// before being told "continuing with the local copy" would make being
// offline feel like being broken, which is the failure mode "Sync model"
// specifically rules out.
//
// It's exported so a frontend driving Pull/Push/Sync directly bounds them
// the same way rather than inventing its own number: a hung push is no
// better when you asked for it.
const RemoteOpTimeout = 30 * time.Second

// mergeCommitMessage is what an automatic merge commits under. Like every
// other commit message gage writes it names no entry and carries no
// plaintext — see the M4 plan's commit-message confidentiality rule.
const mergeCommitMessage = "gage: merge origin"

// The two files whose divergence is more serious than an entry's, because
// they define who can read the vault at all. See "On-disk layout" and
// Q-SYNC-CONFLICT.
const (
	recipientsFile  = ".age-recipients"
	vaultConfigFile = ".gage/config.toml"
)

// RemoteSyncer is the seam between a Vault's sync logic and go-git: the
// two operations that actually touch the network.
//
// It exists so one test can inject a deterministic unreachable-style
// failure. Everything else about sync — comparing, fast-forwarding,
// merging, classifying what diverged — is local git work that runs
// against real repositories in tests, so it deliberately isn't behind
// this interface.
//
// Implementations return the classified sentinels from syncerr and
// nothing else: no raw go-git or transport error reaches a caller.
type RemoteSyncer interface {
	Fetch(ctx context.Context) error
	Push(ctx context.Context) error
}

// gitSyncer is the production RemoteSyncer: go-git against the vault's
// own origin.
type gitSyncer struct{ dir string }

func (g gitSyncer) Fetch(ctx context.Context) error {
	_, err := gitrepo.Fetch(ctx, g.dir)
	return err
}

func (g gitSyncer) Push(ctx context.Context) error {
	_, err := gitrepo.Push(ctx, g.dir)
	return err
}

// syncer returns the RemoteSyncer this vault's sync logic should use —
// the injected one in tests, the real go-git-backed one otherwise.
func (v *Vault) syncer() RemoteSyncer {
	if v.remoteSyncer != nil {
		return v.remoteSyncer
	}
	return gitSyncer{dir: v.Path}
}

// ConflictKind classifies what a divergence conflicted on, since the two
// cases want different words in front of a human. M8b's resolution
// consumes the same classification.
type ConflictKind int

const (
	// ConflictNone means nothing conflicted.
	ConflictNone ConflictKind = iota
	// ConflictEntries means one or more entries were changed on both
	// sides — the ordinary case, resolved through `gage sync`.
	ConflictEntries
	// ConflictRecipients means a recipient-defining file diverged. More
	// serious than an entry conflict: the merged result would be an
	// access list neither device wrote, which is exactly what
	// .gitattributes' `-merge` exists to prevent.
	ConflictRecipients
)

// SyncReport is what one sync attempt actually did. A zero report means
// "nothing to do" — a vault with no remote produces one.
type SyncReport struct {
	// Pulled is true when a fast-forward moved the local branch.
	Pulled bool
	// Merged is true when a divergence was reconciled into a merge
	// commit.
	Merged bool
	// Pushed is true when commits were published to the remote.
	Pushed bool
	// Diverged is true when the two sides have each moved on. It stays
	// true even when the divergence was then merged — it says what was
	// found, not what was left.
	Diverged bool
	// Conflicts lists the vault-relative paths that could not be merged.
	Conflicts []string
	// Ahead is how many local commits the remote lacks, as of the report.
	Ahead int
}

// ConflictKind classifies this report's conflicts, with a recipient-file
// conflict outranking an entry conflict when both are present.
func (r SyncReport) ConflictKind() ConflictKind {
	kind := ConflictNone
	for _, path := range r.Conflicts {
		if path == recipientsFile || path == vaultConfigFile {
			return ConflictRecipients
		}
		kind = ConflictEntries
	}
	return kind
}

// Pull fetches and fast-forwards, and never does anything else: a
// divergence is reported, not merged, per "Sync model"'s
// fast-forward-only rule.
func (v *Vault) Pull(ctx context.Context) (SyncReport, error) {
	return v.underWriteLock(func() (SyncReport, error) { return v.pull(ctx) })
}

// Push publishes local commits, reconciling a divergence when — and only
// when — the two sides touched different files. See push for what that
// reconciliation does and doesn't do.
func (v *Vault) Push(ctx context.Context) (SyncReport, error) {
	return v.underWriteLock(func() (SyncReport, error) { return v.push(ctx) })
}

// Sync is the manual catch-up-and-publish verb: pull what's there, then
// push what isn't. push is what merges a divergence, so this adds no
// resolution of its own — in M8a a genuine conflict is reported, and
// resolving it interactively is M8b's job.
//
// Both halves run under one lock acquisition rather than two, so nothing
// can land in the remote between catching up and publishing.
func (v *Vault) Sync(ctx context.Context) (SyncReport, error) {
	return v.underWriteLock(func() (SyncReport, error) {
		report, err := v.pull(ctx)
		if err != nil {
			return report, err
		}

		pushed, err := v.push(ctx)
		// Pull's findings survive into the combined report: a fast-forward
		// that happened before a failing push still happened, and a caller
		// rendering the result should say so.
		pushed.Pulled = pushed.Pulled || report.Pulled
		pushed.Diverged = pushed.Diverged || report.Diverged
		return pushed, err
	})
}

// underWriteLock runs a sync operation holding this vault's write lock.
//
// Sync is not a read: a fast-forward resets the working tree and a merge
// rewrites files in it, so it needs the same exclusion a write does or it
// could land on top of another process's in-flight insert. The automatic
// paths call the unlocked pull/push directly instead — pushAfterWrite
// already runs inside the write's own lock, and acquiring it twice would
// deadlock.
func (v *Vault) underWriteLock(fn func() (SyncReport, error)) (SyncReport, error) {
	var (
		report SyncReport
		inner  error
	)
	if err := v.withWriteLock(func() error {
		report, inner = fn()
		return nil
	}); err != nil {
		return SyncReport{}, err
	}
	return report, inner
}

// tryPull is Pull for the automatic path: it runs only if this vault's
// write lock is free right now, and reports busy rather than waiting.
//
// The wait is what makes the difference. Before M8a a read took no lock
// at all; wiring the catch-up into every unlock would otherwise make
// `gage show` sit behind an unrelated `gage insert` for the full
// vaultLockTimeout and then warn — a slower, noisier read in exchange for
// nothing, since whoever holds the lock is mid-write and will push when
// it finishes. Being one sync late is the better trade. Manual
// sync/pull/push still wait: there the human asked.
func (v *Vault) tryPull(ctx context.Context) (report SyncReport, busy bool, err error) {
	var inner error
	lockErr := v.withWriteLockTimeout(0, func() error {
		report, inner = v.pull(ctx)
		return nil
	})
	var contended *vaultlock.ContendedError
	switch {
	case errors.As(lockErr, &contended):
		return SyncReport{}, true, nil
	case lockErr != nil:
		return SyncReport{}, false, lockErr
	}
	return report, false, inner
}

// pull is Pull's body, with this vault's write lock already held.
func (v *Vault) pull(ctx context.Context) (SyncReport, error) {
	var report SyncReport

	hasRemote, err := v.hasRemote()
	if err != nil || !hasRemote {
		return report, err
	}
	if err := v.syncer().Fetch(ctx); err != nil {
		return report, err
	}
	return v.fastForward(report)
}

// fastForward advances the local branch when the fetch left a
// fast-forward available, and records what it found either way. Shared by
// Pull and by Push's post-rejection recovery so the two can't drift.
func (v *Vault) fastForward(report SyncReport) (SyncReport, error) {
	state, err := gitrepo.Compare(v.Path)
	if err != nil {
		return report, syncStateError(err)
	}

	switch state {
	case gitrepo.RemoteBehind:
		moved, err := gitrepo.FastForward(v.Path)
		if err != nil {
			return report, syncStateError(err)
		}
		report.Pulled = moved
		if moved {
			v.notePulled()
		}
	case gitrepo.RemoteDiverged:
		report.Diverged = true
	}

	report.Ahead, err = gitrepo.AheadCount(v.Path)
	if err != nil {
		return report, syncStateError(err)
	}
	return report, nil
}

// syncStateError attaches the right exit code to a failure from the local
// git layer.
//
// A working tree something outside gage left uncommitted changes in is a
// state a human must resolve — Conflict, and phrased as gage's own
// message rather than as an internal fault, because it is neither
// unexpected nor a bug. Everything else really is Internal.
func syncStateError(err error) error {
	if errors.Is(err, gitrepo.ErrDirtyWorkTree) {
		return exitcode.Wrap(exitcode.Conflict, fmt.Errorf("gage: %w", err))
	}
	return exitcode.Wrap(exitcode.Internal, err)
}

// push is Push's body, with this vault's write lock already held.
//
// A rejected push is where divergence is actually discovered, so this is
// where it gets classified: fetch, then merge. Disjoint changes merge and
// push with nothing to ask a human (git handles separate files on its
// own, and gage's merge is file-level for exactly that reason). A real
// conflict stops here with the paths named and *nothing changed* —
// detecting is M8a's whole contract; resolving is `gage sync`'s.
func (v *Vault) push(ctx context.Context) (SyncReport, error) {
	var report SyncReport

	hasRemote, err := v.hasRemote()
	if err != nil || !hasRemote {
		return report, err
	}

	err = v.syncer().Push(ctx)
	if err == nil {
		report.Pushed = true
		return report, nil
	}
	if !errors.Is(err, syncerr.ErrDiverged) {
		return report, err
	}

	report.Diverged = true
	if err := v.syncer().Fetch(ctx); err != nil {
		return report, err
	}

	state, err := gitrepo.Compare(v.Path)
	if err != nil {
		return report, syncStateError(err)
	}
	switch state {
	case gitrepo.RemoteBehind:
		// The rejection was a race: whatever the remote had is now
		// something this side can simply fast-forward onto.
		report, err = v.fastForward(report)
		if err != nil {
			return report, err
		}
	case gitrepo.RemoteDiverged:
		result, err := gitrepo.MergeRemote(v.Path, mergeCommitMessage)
		if err != nil {
			return report, syncStateError(err)
		}
		if len(result.Conflicts) > 0 {
			report.Conflicts = result.Conflicts
			// A failure to count is not worth losing the conflict report
			// over: the conflict is the thing the human has to act on,
			// and Summary reads correctly with a zero count.
			report.Ahead, _ = gitrepo.AheadCount(v.Path)
			return report, v.divergedError(report)
		}
		report.Merged = true
		// Counted here rather than only after a successful push, so a
		// merge whose follow-up push then fails still reports how much is
		// pending — the merge commit included.
		report.Ahead, err = gitrepo.AheadCount(v.Path)
		if err != nil {
			return report, syncStateError(err)
		}
		// The merge brought in whatever the other device wrote, so a
		// session's cached metadata is stale for the same reason a pull
		// makes it stale.
		v.notePulled()
	}

	if err := v.syncer().Push(ctx); err != nil {
		return report, err
	}
	report.Pushed = true
	report.Ahead = 0
	return report, nil
}

// divergedError is the typed refusal a real conflict comes back as,
// carrying the exit code the taxonomy already reserves for "a state a
// human must resolve".
func (v *Vault) divergedError(report SyncReport) error {
	return exitcode.Wrap(exitcode.Conflict, &divergenceRefusal{report: report})
}

// divergenceRefusal reports a divergence in the report's own words while
// still matching syncerr.ErrDiverged under errors.Is.
//
// Wrapping the sentinel with %w would prepend its text to the summary,
// and the two say the same thing — a human would read "the remote has
// diverged from this vault: origin has diverged — ...". Matching and
// phrasing are separable, so they are separated here.
type divergenceRefusal struct{ report SyncReport }

func (e *divergenceRefusal) Error() string { return e.report.Summary() }
func (e *divergenceRefusal) Unwrap() error { return syncerr.ErrDiverged }

// Summary renders a report's divergence the way the design doc's own
// example does — "origin has diverged — 2 local commits pending" — plus
// what conflicted, so one line says both what happened and what to do.
func (r SyncReport) Summary() string {
	switch r.ConflictKind() {
	case ConflictRecipients:
		return fmt.Sprintf(
			"origin has diverged on the files that define who can read this vault (%v) — "+
				"%d local commit(s) pending. These are never merged automatically: "+
				"review both sides and run `gage sync`",
			r.Conflicts, r.Ahead)
	case ConflictEntries:
		return fmt.Sprintf(
			"origin has diverged — %d local commit(s) pending, and %d entr(y/ies) changed on both sides; run `gage sync`",
			r.Ahead, len(r.Conflicts))
	default:
		return fmt.Sprintf("origin has diverged — %d local commit(s) pending; run `gage sync`", r.Ahead)
	}
}

// hasRemote reports whether this vault has an origin to sync with at all.
// A local-only vault is a first-class state, not an error: `gage init`
// without --remote produces one, and every sync path is a no-op there.
func (v *Vault) hasRemote() (bool, error) {
	has, err := gitrepo.HasRemote(v.Path)
	if err != nil {
		return false, exitcode.Wrap(exitcode.Internal, err)
	}
	return has, nil
}

// notePulled tells whoever is caching this vault's decrypted metadata
// that entries/ just changed underneath them. It's called from the two
// places that can move the working tree — a fast-forward and a merge —
// rather than from each of their callers, so no caller can forget.
func (v *Vault) notePulled() {
	if v.onPull != nil {
		v.onPull()
	}
}

// syncOnUnlock is the automatic fetch + fast-forward-only pull that
// happens on every vault unlock, in both invocation modes.
//
// It never fails an unlock. Being offline warns once and proceeds with
// the local copy — refusing to show a password because the network is
// down is a worse failure than showing a possibly-stale one — and a
// divergence warns too, since the human is trying to read a secret right
// now and the thing that genuinely needs their attention (an unpushed
// write) will say so at write time. Every other failure warns for the
// same reason: nothing about reaching a remote should stand between a
// human and their own local vault.
func (v *Vault) syncOnUnlock(p Prompter) {
	ctx, cancel := context.WithTimeout(context.Background(), RemoteOpTimeout)
	defer cancel()

	report, busy, err := v.tryPull(ctx)
	switch {
	case busy:
		// Another gage process is mid-write and will push when it
		// finishes. Silent on purpose: nothing is wrong, nothing is
		// stale that the next unlock won't pick up, and a warning here
		// would fire on ordinary concurrency.
	case err != nil:
		warn(p, "gage: %s; continuing with the local copy of %q", offlineOrError(err), v.Name)
	case report.Diverged:
		warn(p, "gage: %s", report.Summary())
	}
}

// pushAfterWrite is the automatic push that follows every write, run
// under the same vault lock the write itself holds.
//
// Like syncOnUnlock it never turns into a failure: the commit is already
// durable in the local object store, which is the whole point of the
// local-durability/network-risk split. What it does do is report the two
// situations differently — "your commit is safe but unsent" for a network
// problem, and the design doc's own "origin has diverged — N local
// commits pending, run `gage sync`" for a divergence — because those need
// different things from the human, or in the offline case nothing at all.
func (v *Vault) pushAfterWrite(p Prompter) {
	ctx, cancel := context.WithTimeout(context.Background(), RemoteOpTimeout)
	defer cancel()

	// The unlocked form: this already runs inside the write's own lock
	// (see underWriteLock), which is exactly what "pushed under the same
	// vault lock the write holds" means.
	report, err := v.push(ctx)
	switch {
	case err != nil && errors.Is(err, syncerr.ErrDiverged):
		warn(p, "gage: %s", report.Summary())
	case err != nil:
		warn(p, "gage: the write is committed locally but not pushed: %s", offlineOrError(err))
	}
}

// offlineOrError renders a sync failure as a clause that reads correctly
// mid-sentence: the offline case as the expected, unalarming thing it is,
// and anything else as its own message minus the "gage: " every sentinel
// carries — the surrounding warning already opens with one, and two in a
// row ("gage: gage: ...") is how that leaks into what a human reads.
func offlineOrError(err error) string {
	if errors.Is(err, syncerr.ErrUnreachable) {
		return "could not reach origin"
	}
	return strings.TrimPrefix(err.Error(), "gage: ")
}

// warn sends one advisory through the Prompter, if there is one. A nil
// Prompter is normal: a library caller that never supplied one has
// nowhere to show a warning, and a sync warning must not be the thing
// that turns that into a failure.
func warn(p Prompter, format string, args ...any) {
	if p == nil {
		return
	}
	p.Warn(fmt.Sprintf(format, args...))
}
