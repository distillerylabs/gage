package gage

// Device enrollment, approving side: the types an approval is expressed
// in, the collision pre-check that runs before anyone is asked anything,
// and ApproveEnrollments itself.
//
// The ordering here is the TDD's "The approver unlocks, and the unlock
// comes late" plus "Approval fetches before it commits", and — as on the
// joining side — every step's position is load-bearing rather than
// incidental. See the comments on ApproveEnrollments.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
)

// ErrEnrollmentNameTaken is a request whose device name — the one it was
// sealed with — now labels a *different* recipient of this vault.
//
// Recoverable by the approver alone, via --device: the key was
// authenticated, the label was not. It is deliberately a second error
// for a condition ErrRecipientExists also describes, because the two sit
// in different places and say different things. This one is the UX path,
// reached in every ordinary run and naming the flag that fixes it;
// ErrRecipientExists is the race, reached only when another writer took
// the name between this check and the write lock, and it belongs to
// `recipient add`'s vocabulary rather than enrollment's. See the TDD's
// "Two different errors describe one device-name collision".
var ErrEnrollmentNameTaken = errors.New("gage: another recipient of this vault already uses this request's device name")

// Approval pairs an opened request with the label the approver chose for
// it. An empty Label means "use what the request claims"; a non-empty
// one overrides it, which is how a device-name collision gets resolved
// without a new round trip (D-ENROLL-COLLISIONS).
//
// The label rides here rather than being written onto OpenedRequest,
// because every field on that type is authenticated and a label the
// approver typed is not.
type Approval struct {
	Request OpenedRequest
	Label   string
}

// label is the name this approval will actually record.
func (a Approval) label() string {
	if a.Label != "" {
		return a.Label
	}
	return a.Request.Device
}

// ApprovalOutcome is what happened to one request.
type ApprovalOutcome struct {
	Request OpenedRequest
	// Label is the name actually recorded in the recipient list.
	Label string
	// Added is false when this pubkey was already a recipient — a
	// success that did no work, mirroring EnrollmentRequest.Published on
	// the joining side of the same duplicate.
	Added bool
}

// ApprovalResult covers the whole batch, which shares one re-encryption
// pass and one commit.
//
// RecipientChange is deliberately not reused: it names exactly one
// device, and a batch resolves N requests under one commit, each of
// which may have been relabeled or turned out to be a no-op. The shared
// facts live here once, because they are properties of the batch rather
// than of any one device.
type ApprovalResult struct {
	Outcomes    []ApprovalOutcome
	Reencrypted int
	Commit      string
}

// CheckEnrollmentLabels is the device-name collision pre-check: does any
// approval's effective label already belong to a *different* recipient?
//
// It holds no Identity and reads only the plaintext recipient list,
// which is exactly what lets cmd/gage run it in the one position that
// makes it useful — after the open, before the render, before the
// confirmation and before any unlock. It lives here rather than in
// cmd/gage so a second frontend inherits the check instead of
// reimplementing it; the TDD's "cmd/gage's pre-check" describes where in
// the sequence it runs, not which package holds it.
//
// It is UX, not correctness. The authoritative duplicate check runs
// inside the shared recipient write, under the lock, and returns
// ErrRecipientExists — only that one can be trusted against a concurrent
// writer. This one exists so the ordinary case names --device instead.
//
// A request whose *pubkey* is already listed is not a collision: it is
// the already-a-recipient no-op, and reporting it as a name clash would
// advise --device, which mints a second label for a key that already has
// access. The key question comes first here for the same reason it does
// in Enroll.
func (v *Vault) CheckEnrollmentLabels(approvals []Approval) error {
	if len(approvals) == 0 {
		return nil
	}
	current, err := v.Recipients()
	if err != nil {
		return err
	}

	for _, a := range approvals {
		if recipientHoldingKey(current, a.Request.Pubkey) != "" {
			continue
		}
		label := a.label()
		for _, r := range current {
			if r.Device == label {
				return exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
					"%w: %q already labels the key %s in vault %q; approve with --device NAME to record this "+
						"request under a different name", ErrEnrollmentNameTaken, label, r.Pubkey, v.Name))
			}
		}
	}
	return nil
}

// recipientHoldingKey returns the device name listing pubkey, or "" when
// no recipient holds it.
func recipientHoldingKey(list []VaultRecipient, pubkey string) string {
	for _, r := range list {
		if r.Pubkey == pubkey {
			return r.Device
		}
	}
	return ""
}

// denyCommitMessage is what a refusal commits under. Like every other
// commit message gage writes it names no device: the filename scheme
// exists so a reader of the repository learns nothing about who was
// trying to join, and that holds just as much for someone who was turned
// away.
const denyCommitMessage = "gage: enrollment request denied"

// deniedPushClause is what an unpublished deny says it cost.
//
// The generic "committed locally but not pushed" is exactly wrong here:
// the consequence is not that a write is late, it is that the request is
// still live for every other device holding the code, and a silent
// failed refusal is indistinguishable from a successful one.
const deniedPushClause = "the request was removed locally but the removal was not pushed, " +
	"so it is still pending for every other device"

// DenyEnrollment removes one pending request without granting anything.
//
// It takes a Prompter despite holding no Identity, which is a deliberate
// exception to this codebase's "human interaction rides the Identity"
// rule rather than an erosion of it. That rule exists so methods which
// *already* take an Identity do not redundantly take a Prompter too;
// deny is the first mutating method that legitimately holds none,
// because refusing to grant access requires proving nothing. Without a
// Prompter, a deny whose push fails reports success and lets the request
// reappear on every other device. See the TDD's "Library surface".
//
// It needs no code and no unlock for the same reason: anyone with git
// write access could delete the file directly, so demanding proof would
// buy nothing and cost the one path that is available to someone who
// cannot decrypt the vault. It is still a write — the lock, the
// dirty-tree reset, the commit and the opportunistic push are all the
// ordinary ones.
//
// Resolution happens inside the lock, after the reset, so the request it
// deletes is the one present in the state it is about to commit.
func (v *Vault) DenyEnrollment(id string, p Prompter) error {
	return v.withVaultWrite(p, func() error {
		req, err := v.ResolveEnrollment(id)
		if err != nil {
			return err
		}
		if err := v.deleteVaultPaths([]string{pendingVaultPath(req)}); err != nil {
			return err
		}
		// deny is already writing and already holds the lock, which is
		// the whole of the TDD's "pruning rides the next write" rule.
		// In this commit, never a second one.
		if _, err := v.prunePendingEnrollments(); err != nil {
			return err
		}
		if _, err := gitrepo.CommitAll(v.Path, denyCommitMessage); err != nil {
			return exitcode.Wrap(exitcode.Internal,
				fmt.Errorf("gage: committing the denied enrollment request: %w", err))
		}
		v.pushAfterWriteSaying(p, deniedPushClause)
		return nil
	})
}

// pendingVaultPath is a pending request's path as the git side names it:
// vault-relative, forward-slashed.
//
// It takes the request rather than its id and expiry, because the
// filename's epoch is unauthenticated and need not agree with the sealed
// one. Rebuilding the name from the sealed copy would produce a path
// that names no file, and the deletion would silently do nothing —
// leaving the approved request in the commit for every other device to
// find.
func pendingVaultPath(req PendingRequest) string {
	return pendingDirName + "/" + filepath.Base(req.Path)
}

// approvalPushClause is what an unpublished approval says it cost.
//
// The generic "the write is committed locally but not pushed" is much
// too thin here: approval's local-versus-remote split is the widest in
// the tool. Locally the vault is re-encrypted, the device is listed and
// its request is gone; on the remote none of that happened, the request
// is still pending, and — the part worth saying out loud — the joining
// device is still locked out with no way to tell, because it was told to
// run `gage sync` and `gage sync` will report nothing wrong.
const approvalPushClause = "the approval is committed locally but not pushed, so it has not taken effect " +
	"for anyone else: the request is still pending on the remote and the joining device is still locked out — " +
	"run `gage push` to finish it"

// clearedPushClause is approvalPushClause for the approval that granted
// nothing, because the device was already a recipient. Nothing is
// locked out and nothing is half-applied; the only casualty is that the
// stale request is still sitting in the remote's pending/.
const clearedPushClause = "the request was cleared locally but the removal was not pushed, " +
	"so it is still pending for every other device"

// ApproveEnrollments admits the devices behind a batch of opened
// requests: it adds each one as a recipient, re-encrypts every entry so
// they can read the whole vault, and removes every approved request's
// file — all in one commit.
//
// It takes a context because it fetches before it writes, and that fetch
// is a hard precondition whose failure changes the outcome — the same
// test that puts Enroll, Pull, Push and Sync on that list. It takes the
// codes because it re-opens each sealed request under the write lock and
// writes what *that* open says rather than what was displayed a moment
// earlier; nothing else in reach carries a code, and OpenEnrollment does
// not report which code opened which request.
//
// The order, and what each position is for:
//
//  0. The full-access pre-flight, before the lock. An actor who cannot
//     read the whole vault cannot grant it, and A19 left no flag to skip
//     the re-encryption that would otherwise discover this mid-write on
//     an opaque entry UUID. It runs here rather than earlier because it
//     is a decryption pass and needs the unlocked identity — which is
//     the documented cost of the late unlock, not an oversight. See "The
//     approver unlocks, and the unlock comes late".
//  1. Take the write lock, with withVaultWrite's dirty-tree reset.
//  2. Fetch and fast-forward, inside that lock, via the unexported
//     v.pull. An approval rewrites every entry, so one made onto a stale
//     tip diverges in *every* entry rather than in one.
//  3. Re-verify each request against the file on disk, under the lock.
//  4. Drop the requests whose key is already a recipient — a success
//     that does no work, not an error.
//  5. Add, re-encrypt, clear and commit, through E1b's N-recipient form
//     so the whole batch is one lock, one trust question, one pass and
//     one commit. It does not call AddRecipient: that is a complete
//     write of exactly one recipient, and calling it N times would take
//     N locks, ask N trust questions and produce N commits.
func (v *Vault) ApproveEnrollments(ctx context.Context, approvals []Approval, codes []string, ident *Identity) (ApprovalResult, error) {
	if len(approvals) == 0 {
		return ApprovalResult{}, exitcode.New(exitcode.Usage, "gage: no enrollment requests to approve")
	}

	// Gated on a clean working tree exactly as AddRecipient's is, and
	// for the same reason: the pre-flight reads entries off disk, so on
	// a tree left dirty by an interrupted re-encryption it would judge
	// ciphertext the lock's reset is about to discard and report a
	// device that can read all of HEAD as one that cannot. On a dirty
	// tree it moves inside the lock, to just after the reset — later
	// than "before the lock", but still before the first byte and still
	// before M10's prompt, which is what the ordering is actually for.
	clean, err := gitrepo.IsClean(v.Path)
	if err != nil {
		return ApprovalResult{}, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: checking %q's working tree: %w", v.Name, err))
	}
	if clean {
		if err := v.RequireFullAccess(ident); err != nil {
			return ApprovalResult{}, err
		}
	}

	var result ApprovalResult
	err = v.withVaultWrite(ident.warnTo(), func() error {
		if !clean {
			if err := v.RequireFullAccess(ident); err != nil {
				return err
			}
		}
		if err := v.catchUpBeforeApproving(ctx, ident.warnTo()); err != nil {
			return err
		}

		verified, err := v.reverifyApprovals(approvals, codes)
		if err != nil {
			return err
		}
		result, err = v.writeApprovals(verified, ident)
		return err
	})
	if err != nil {
		return ApprovalResult{}, err
	}
	return result, nil
}

// catchUpBeforeApproving fetches and fast-forwards with the write lock
// already held, and decides which failures stop the approval.
//
// It reaches the network through the unexported v.pull, never Pull:
// Pull takes the lock itself through underWriteLock and vaultlock is not
// re-entrant, so calling it here would block against this very process
// for vaultLockTimeout and then report contention against its own lock.
// The failure looks like an unrelated concurrency bug, which is why it
// is called out here as well as on the joining side.
//
// A divergence refuses, with the *ordinary* advice and no new error: an
// approver holds a key and can decrypt, so `gage sync` is exactly the
// command that resolves this for them. ErrEnrollmentDiverged exists
// because that advice is wrong for a device with no key, which is not
// the situation here. Note the nil-error trap — v.pull reports
// divergence by setting SyncReport.Diverged, not by returning an error,
// so it is checked for rather than caught.
//
// An unreachable remote warns and proceeds, unlike enroll. That is the
// genuine asymmetry rather than an inconsistency: an unpublished
// enrollment request accomplishes nothing, so enroll refuses, while an
// approval that lands locally is real work — the vault is re-encrypted
// and the recipient is listed — and refusing it because the network is
// down would break "warn and proceed" in the direction it exists to
// prevent. The failed-push warning tells the human the rest.
func (v *Vault) catchUpBeforeApproving(ctx context.Context, p Prompter) error {
	report, err := v.pull(ctx)
	if err != nil {
		warn(p, "gage: %s; approving against the local copy of %q", offlineOrError(err), v.Name)
		return nil
	}
	if report.Diverged {
		return exitcode.Wrap(exitcode.Conflict, fmt.Errorf(
			"gage: %s — nothing was approved, and the request is still pending", report.Summary()))
	}
	return nil
}

// reverifyApprovals re-reads each request from disk and re-opens its
// seal, with the write lock held, returning the approvals as the
// authoritative copy states them.
//
// This is the whole of "what gets written is what was verified under the
// lock, not what was displayed". OpenEnrollment ran outside the lock and
// before a human answered a question, so between then and now the file
// could have been swapped, or the request could have been approved or
// denied by another device — a window the fetch above makes ordinary
// rather than rare. Both outcomes are the same fact from the caller's
// side: the request named is no longer the pending request that was
// approved, so nothing is written and ErrEnrollmentNoSuchRequest says
// so, with a message naming the likely cause rather than implying the id
// was mistyped.
//
// A payload that opens but no longer matches what the human was shown is
// refused for the same reason, rather than being silently written: the
// consent was given for a particular device and key.
func (v *Vault) reverifyApprovals(approvals []Approval, codes []string) ([]verifiedApproval, error) {
	valid := make([]string, 0, len(codes))
	for _, c := range codes {
		if normalized, ok := normalizeEnrollmentCode(c); ok {
			valid = append(valid, normalized)
		}
	}

	now := time.Now()
	out := make([]verifiedApproval, 0, len(approvals))
	for _, a := range approvals {
		req, found := v.findPendingByID(a.Request.ID)
		if !found {
			return nil, resolvedElsewhere(a.Request.ID)
		}
		sealed, ok := readPendingFile(req.Path)
		if !ok {
			return nil, resolvedElsewhere(a.Request.ID)
		}

		fresh, opened, err := v.openOneOf(req, sealed, valid, now)
		if err != nil {
			return nil, err
		}
		if !opened || fresh != a.Request {
			return nil, resolvedElsewhere(a.Request.ID)
		}
		out = append(out, verifiedApproval{
			approval: Approval{Request: fresh, Label: a.Label},
			file:     req,
		})
	}
	return out, nil
}

// verifiedApproval is one approval as the copy under the lock states it,
// paired with the file it was verified against.
//
// The file travels with it because that is the path the commit has to
// delete, and it cannot be rebuilt from the request: the filename's
// epoch is a hint, not an authenticated field. See pendingVaultPath.
type verifiedApproval struct {
	approval Approval
	file     PendingRequest
}

// openOneOf tries each code against one sealed request, returning the
// first that opens. A refusal from a code that *did* open — expired,
// wrong vault, malformed — is returned as itself, since the code worked
// and re-trying another cannot change the answer.
func (v *Vault) openOneOf(req PendingRequest, sealed []byte, codes []string, now time.Time) (OpenedRequest, bool, error) {
	for _, code := range codes {
		out, didOpen, err := v.openSealedRequest(req, sealed, code, now)
		if !didOpen {
			continue
		}
		if err != nil {
			return OpenedRequest{}, false, err
		}
		return out, true, nil
	}
	return OpenedRequest{}, false, nil
}

// resolvedElsewhere is the refusal for a request that is no longer the
// one that was approved.
//
// It reuses ErrEnrollmentNoSuchRequest rather than minting a sibling,
// because that is exactly what has happened: the request is no longer
// pending. What the message adds is the likely cause, so it does not
// read as "you mistyped the id" — which is what the same error means
// when a human types one.
func resolvedElsewhere(id string) error {
	return exitcode.Wrap(exitcode.NotFound, fmt.Errorf(
		"%w: %s was still pending when it was opened and is not now — another device most likely "+
			"approved or denied it in the meantime. Nothing was changed here: if it was approved "+
			"elsewhere the device already has access, and if it was denied it should not be given any",
		ErrEnrollmentNoSuchRequest, id))
}

// findPendingByID locates a request's file by the UUID portion of its
// name, without the liveness filter PendingEnrollments applies.
//
// Unfiltered on purpose: expiry is judged by the *sealed* copy, which is
// the authoritative one, and filtering here would report a request that
// expired between the open and the lock as missing rather than as
// expired. Those have different fixes and different messages.
func (v *Vault) findPendingByID(id string) (PendingRequest, bool) {
	dirEntries, err := os.ReadDir(v.pendingDir())
	if err != nil {
		return PendingRequest{}, false
	}
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		name, expires, ok := parsePendingFileName(de.Name())
		if !ok || name != id {
			continue
		}
		return PendingRequest{
			ID:      name,
			Path:    filepath.Join(v.pendingDir(), de.Name()),
			Expires: expires,
		}, true
	}
	return PendingRequest{}, false
}

// writeApprovals is the write itself, with the lock held and every
// request re-verified: split the batch into the devices that need adding
// and the ones that already have access, then commit exactly once.
func (v *Vault) writeApprovals(approvals []verifiedApproval, ident *Identity) (ApprovalResult, error) {
	current, err := v.Recipients()
	if err != nil {
		return ApprovalResult{}, err
	}

	var (
		add      []VaultRecipient
		clear    []string
		outcomes = make([]ApprovalOutcome, 0, len(approvals))
		labels   = make([]string, 0, len(approvals))
	)
	for _, verified := range approvals {
		a := verified.approval
		clear = append(clear, pendingVaultPath(verified.file))

		// The already-a-recipient duplicate, filtered here rather than
		// left to the shared body — which returns ErrRecipientExists for
		// it, correctly, because for `recipient add` it *is* an error.
		// For approval it is a success that does no work: the device
		// already has what it asked for, so the request is cleared and
		// nothing is re-encrypted. This is not the authoritative
		// duplicate check; that one stays inside the shared body, under
		// this same lock, and still catches a writer who took the name
		// or the key in the window.
		if existing := recipientHoldingKey(current, a.Request.Pubkey); existing != "" {
			outcomes = append(outcomes, ApprovalOutcome{Request: a.Request, Label: existing, Added: false})
			continue
		}
		add = append(add, VaultRecipient{Device: a.label(), Pubkey: a.Request.Pubkey})
		labels = append(labels, a.label())
		outcomes = append(outcomes, ApprovalOutcome{Request: a.Request, Label: a.label(), Added: true})
	}

	if len(add) == 0 {
		commit, err := v.clearApprovedRequests(clear, ident)
		if err != nil {
			return ApprovalResult{}, err
		}
		return ApprovalResult{Outcomes: outcomes, Commit: commit}, nil
	}

	reencrypted, commit, err := v.addRecipientsLocked(add, recipientWrite{
		message:     "gage: recipient approve " + strings.Join(labels, ", ") + " (reencrypt)",
		deletePaths: clear,
		pushClause:  approvalPushClause,
	}, ident)
	if err != nil {
		return ApprovalResult{}, err
	}
	return ApprovalResult{Outcomes: outcomes, Reencrypted: reencrypted, Commit: commit}, nil
}

// clearApprovedRequests is the write for a batch that granted nothing,
// because every device in it was already a recipient.
//
// It deliberately does not go through the shared recipient write. That
// body re-encrypts every entry, asks M10's trust question and
// regenerates the trust cache — all three of which are about a recipient
// list that changed, and none of which happened here. What is left is
// the part that still has to happen: the request files are cleared, in
// one commit, under the same lock, with the same pruning every other
// write performs.
func (v *Vault) clearApprovedRequests(clear []string, ident *Identity) (string, error) {
	if err := v.deleteVaultPaths(clear); err != nil {
		return "", err
	}
	if _, err := v.prunePendingEnrollments(); err != nil {
		return "", err
	}
	hash, err := gitrepo.CommitAll(v.Path, "gage: enrollment request cleared (already a recipient)")
	if err != nil {
		return "", exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: committing the cleared enrollment request: %w", err))
	}
	v.pushAfterWriteSaying(ident.warnTo(), clearedPushClause)
	return hash, nil
}
