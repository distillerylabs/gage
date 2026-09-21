package gage

// Device enrollment, joining side: create or reuse this device's
// identity, seal a request to a freshly generated code, publish it, and
// hand the code back for cmd/gage to display.
//
// The ordering here is D-ENROLL-REMOTE's, and every step's position is
// load-bearing rather than incidental — see the comments on Enroll.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/distillerylabs/gage/internal/gage/atomicfile"
	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/devicename"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/syncerr"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

var (
	// ErrEnrollmentNoRemote is a vault with no remote to publish to. Kept
	// distinct from ErrEnrollmentRemoteUnreachable because the fixes
	// differ — configure one, versus get on the network — and cmd/gage
	// decides which to say. See D-ENROLL-REMOTE.
	ErrEnrollmentNoRemote = errors.New("gage: this vault has no remote to publish an enrollment request to")

	// ErrEnrollmentRemoteUnreachable is a remote that exists but could
	// not be reached.
	//
	// This is the one place gage departs from "warn and proceed" over a
	// network failure, and deliberately: a read still delivers value
	// offline, while an enrollment request that was never published is a
	// private key, a local commit, and a code nobody can act on. Failing
	// before any of that is created is kinder than producing all three
	// and reporting that the useful part didn't happen.
	ErrEnrollmentRemoteUnreachable = errors.New("gage: could not reach this vault's remote, so the enrollment request was not published")

	// ErrEnrollmentDiverged is a reachable remote whose history has
	// diverged from this vault's, so the pull was not a fast-forward.
	//
	// Separate from both its neighbours because the advice a joining
	// device can act on is unique to it: the standard "run `gage sync`"
	// is wrong for a device that cannot decrypt anything, and the local
	// commit in the way is an unpublished pending/ file, which is inert
	// and safe to discard.
	//
	// Note that v.pull reports this condition with a *nil* error and
	// SyncReport.Diverged set, so it has to be checked for rather than
	// caught.
	ErrEnrollmentDiverged = errors.New("gage: this vault's local and remote histories have diverged, so the enrollment request was not published")
)

// enrollmentCommitMessage is what a published request commits under.
// Like every other commit message gage writes it names no device and
// carries no plaintext — the filename scheme exists so that a reader of
// the repository learns nothing about who is enrolling, and a commit
// message that named the device would hand it straight back.
const enrollmentCommitMessage = "gage: enrollment request"

// enrollmentClockSkewThreshold is how far behind HEAD's committer
// timestamp this machine's clock has to be before enroll says so.
//
// A slow clock seals an expiry that is already in the past, which gage
// cannot detect from its own clock — by that clock the expiry is always
// in the future, which is the whole problem. A freshly fetched vault
// carries timestamps other devices wrote, so being behind them is the
// available signal. An hour is comfortably past ordinary timezone-free
// drift and well short of the TTL, so a warning means something.
const enrollmentClockSkewThreshold = time.Hour

// Enroll publishes an enrollment request for this device: it creates a
// local identity if one does not already exist for this vault (reusing
// it if it does), seals the device name and public key to a freshly
// generated code, commits the result to .gage/pending/, pushes, and
// returns the request with that code on it.
//
// It takes a context because it is the one write in gage that blocks on
// the network and *fails* when it cannot reach it. Every other exported
// method that touches a remote and depends on the result takes one; the
// writes that push opportunistically do not, because pushAfterWrite
// warns and proceeds and so has nothing a caller would want to cancel.
// Enroll's fetch is a hard precondition, which puts it on the first
// list.
//
// The order is D-ENROLL-REMOTE's, and each position fixes a specific
// failure:
//
//  0. Validate the device name and the TTL, which need no vault state
//     and no network. Both are usage errors, and getting them out of the
//     way first is what makes "a bad --ttl writes no identity" true.
//  1. Remote configured? Else ErrEnrollmentNoRemote — before the lock,
//     since it needs nothing from the vault either.
//  2. Take the write lock and hold it through the push. A fast-forward
//     pull mutates the working tree, so pulling outside the lock and
//     taking it afterwards would leave a window for another gage process
//     to move HEAD between the catch-up and the commit.
//  3. Discard an unexpectedly dirty working tree — withVaultWrite's
//     reset, not a step invented here. See enrollUnderLock.
//  4. Fetch + fast-forward. Unreachable and diverged both fail here,
//     before anything is generated, prompted for, or written.
//  5. Already-a-recipient check, then the device-name check, both
//     against the list as of the pull just performed. Neither costs a
//     prompt, because both run before step 6.
//  6. Create or reuse the identity — the only step that prompts.
//  7. Seal, write into pending/, commit.
//  8. Push, then release the lock.
func (v *Vault) Enroll(ctx context.Context, device string, ttl time.Duration, p Prompter) (EnrollmentRequest, error) {
	if err := checkDeviceLabelFree(device); err != nil {
		return EnrollmentRequest{}, err
	}
	if !devicename.Valid(device) {
		return EnrollmentRequest{}, exitcode.Newf(exitcode.Usage, "gage: device name %q is invalid", device)
	}
	if err := validateEnrollmentTTL(ttl); err != nil {
		return EnrollmentRequest{}, err
	}

	hasRemote, err := v.hasRemote()
	if err != nil {
		return EnrollmentRequest{}, err
	}
	if !hasRemote {
		return EnrollmentRequest{}, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("%w: set one with `gage git set-remote`", ErrEnrollmentNoRemote))
	}

	var req EnrollmentRequest
	// withVaultWrite rather than the bare withWriteLock: it is lock
	// *plus* resetDirtyWorkTree, and enroll needs the reset for a reason
	// specific to the device running it. gitrepo.FastForward refuses over
	// a dirty tree, so a stray pending/ file from an interrupted enroll
	// would make the *next* enroll fail at step 4 — and a joining device
	// has no other write to clear it with, since every path that runs the
	// reset needs an unlock it cannot perform. Without the reset here,
	// one interrupted enroll wedges the device permanently.
	err = v.withVaultWrite(p, func() error {
		var err error
		req, err = v.enrollUnderLock(ctx, device, ttl, p)
		return err
	})
	if err != nil {
		return EnrollmentRequest{}, err
	}
	return req, nil
}

// enrollUnderLock is steps 4 through 8, with the write lock held and the
// working tree already reset.
func (v *Vault) enrollUnderLock(ctx context.Context, device string, ttl time.Duration, p Prompter) (EnrollmentRequest, error) {
	report, err := v.pull(ctx)
	if err != nil {
		return EnrollmentRequest{}, enrollFetchError(err)
	}
	// The nil-error trap: v.pull reports a divergence by setting this
	// field, not by returning an error. An implementation that only
	// inspects err walks straight past it, commits onto the diverged
	// branch, and fails at the push with the standard "run `gage sync`"
	// advice — which is exactly wrong for a device that cannot decrypt.
	if report.Diverged {
		return EnrollmentRequest{}, exitcode.Wrap(exitcode.Conflict,
			fmt.Errorf("%w: nothing was created, so there is nothing to reconcile here; "+
				"if a previous enroll left an unpublished commit behind it is an inert pending/ file — "+
				"discard it and enroll again", ErrEnrollmentDiverged))
	}
	if v.onEnrollPulled != nil {
		v.onEnrollPulled()
	}
	v.warnOnClockSkew(p)

	recipients, err := v.Recipients()
	if err != nil {
		return EnrollmentRequest{}, err
	}

	// The pubkey question before the name question, because its answer
	// makes the name question moot and its "yes" is the one that must not
	// be reported as a name conflict. A device that ran `identity add`,
	// had its key added manually, and then enrolls would otherwise be
	// told to pass --device — advice that mints a second identity and
	// publishes a request for access it already has (D-ENROLL-COLLISIONS).
	//
	// It is answerable without an unlock, which is the only reason it can
	// come first: global config records this device's public key for this
	// vault. When it records none — an older entry, a hand-edited one, or
	// a clone that has not enrolled yet — the check is skipped and the
	// name check below is the only one left, exactly as before.
	if mine := v.recordedPubkey(); mine != "" {
		for _, r := range recipients {
			if r.Pubkey == mine {
				// A success that does no work: nothing generated, nothing
				// sealed, nothing committed, nothing pushed. It mirrors
				// approval's answer to the same duplicate.
				return EnrollmentRequest{Published: false}, nil
			}
		}
	}

	// Keys, not names — so this fires only for a *different* recipient
	// that happens to share the label, which is the case --device fixes.
	for _, r := range recipients {
		if r.Device == device {
			return EnrollmentRequest{}, exitcode.Wrap(exitcode.Conflict,
				fmt.Errorf("%w: %q already names a recipient of vault %q", ErrDeviceNameTaken, device, v.Name))
		}
	}

	// The same CreateIdentity call `identity add` makes, with the same
	// create-or-reuse behavior. Not forked: enroll *is* identity add plus
	// publishing, and the reuse path's unlock is what guarantees the
	// published public key matches the private key this device holds.
	pubkey, err := CreateIdentity(v.ID, v.Name, device, p)
	if err != nil {
		return EnrollmentRequest{}, err
	}

	// MethodPassphrase rather than whatever global config records: the
	// payload's method describes how this device stores its key, and
	// CreateIdentity has just written a passphrase-wrapped file. A build
	// that can produce another kind of identity is the build that changes
	// this line.
	req, sealed, err := v.sealEnrollment(device, pubkey, MethodPassphrase, ttl)
	if err != nil {
		return EnrollmentRequest{}, err
	}
	if err := v.writePendingRequest(req, sealed); err != nil {
		return EnrollmentRequest{}, err
	}
	if _, err := gitrepo.CommitAll(v.Path, enrollmentCommitMessage); err != nil {
		return EnrollmentRequest{}, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: committing the enrollment request: %w", err))
	}

	// The unlocked form, like pushAfterWrite's: this already runs inside
	// the write's own lock, and vaultlock is not re-entrant — v.Push
	// would take the lock again through underWriteLock and block against
	// this very process for vaultLockTimeout before failing as contended.
	//
	// Unlike every other write, a failed push here is an error rather
	// than a warning: an unpublished request accomplishes nothing. The
	// commit and the identity stay put, because re-running is cheap
	// precisely because they do.
	if _, err := v.push(ctx); err != nil {
		return EnrollmentRequest{}, exitcode.Wrap(exitcode.CodeOf(err),
			fmt.Errorf("gage: the identity was created and the enrollment request committed locally, "+
				"but publishing it failed: %w", err))
	}

	req.Published = true
	return req, nil
}

// validateEnrollmentTTL is sealEnrollment's duration check, hoisted so
// Enroll can run it before anything exists to undo.
//
// sealEnrollment still performs it — it is the only function that takes
// a duration and a GUI could call it directly — so this is a second,
// earlier reading of the same rule rather than a move.
func validateEnrollmentTTL(ttl time.Duration) error {
	switch {
	case ttl <= 0:
		return exitcode.Newf(exitcode.Usage,
			"gage: an enrollment request's lifetime must be positive, not %s", ttl)
	case ttl > MaxEnrollmentTTL:
		return exitcode.Newf(exitcode.Usage,
			"gage: an enrollment request may live at most %s, not %s", MaxEnrollmentTTL, ttl)
	}
	return nil
}

// enrollFetchError maps a failed catch-up onto enroll's own vocabulary.
//
// Only the unreachable case is renamed. An auth failure already says
// which host and what to run (see gitrepo's annotateAuth), and a broken
// repository is neither of enroll's two remote errors — restating either
// as "could not reach the remote" would send someone to fix the wrong
// thing.
func enrollFetchError(err error) error {
	if !errors.Is(err, syncerr.ErrUnreachable) {
		return err
	}
	return exitcode.Wrap(exitcode.Unreachable,
		fmt.Errorf("%w: nothing was created, so nothing is left half-done; try again once the remote is reachable "+
			"(%s)", ErrEnrollmentRemoteUnreachable, offlineOrError(err)))
}

// writePendingRequest lays a sealed request into pending/ under the
// filename gage gives it, creating the directory on first use.
//
// Lazily created rather than laid out by `gage init`: git tracks no empty
// directories, so its absence is the normal state and means "no pending
// requests" — including for every vault that predates this feature.
func (v *Vault) writePendingRequest(req EnrollmentRequest, sealed []byte) error {
	dir := v.pendingDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: creating %s: %w", pendingDirName, err))
	}
	path := filepath.Join(dir, pendingFileName(req.ID, req.Expires))
	if err := atomicfile.WriteFile(path, sealed, 0o600); err != nil {
		return exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: writing the enrollment request: %w", err))
	}
	return nil
}

// recordedPubkey returns this device's public key for this vault as
// global config records it, or "" when it records none.
//
// Reading global config from the library is precedented — unlock.go's
// localIdentityRecord does it, and for the same reason: which key *this
// machine* holds is local state, not vault state. Writing it stays in
// cmd/gage, which is the split `identity add` already uses.
//
// Every failure is "" rather than an error. The caller's fallback for a
// missing pubkey is the device-name check, which is exactly the right
// thing to do when the file is unreadable too.
func (v *Vault) recordedPubkey() string {
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		return ""
	}
	g, err := config.Read(filepath.Join(dir, "config.toml"))
	if err != nil {
		return ""
	}
	return g.Vaults[v.Name].Pubkey
}

// warnOnClockSkew says so when this machine's clock is meaningfully
// behind the timestamp on the tip it just fetched.
//
// Warn and proceed, the same posture as an unreachable network or a
// failed page-lock: refusing to enroll over a heuristic would be worse
// than publishing a request that might expire early. Every failure to
// read the timestamp is silent for the same reason — a heuristic that
// could fail the command would be worse than the skew it detects.
func (v *Vault) warnOnClockSkew(p Prompter) {
	head, err := gitrepo.HeadCommitterTime(v.Path)
	if err != nil || head.IsZero() {
		return
	}
	behind := time.Until(head)
	if behind < enrollmentClockSkewThreshold {
		return
	}
	warn(p, "gage: this machine's clock is about %s behind the latest commit in %q, "+
		"so this request may expire sooner than its lifetime suggests — check the clock if the approver "+
		"sees an already-expired request", behind.Round(time.Minute), v.Name)
}
