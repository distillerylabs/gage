package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/devicename"
	"github.com/denmark/gage/internal/gage/exitcode"
)

// The three approving-side commands. Everything here is rendering,
// gathering and ordering: the decisions live in internal/gage, and this
// file's job is to ask a human the questions the library returns as
// values and to put the answers back in.
//
// The ordering is the part worth reading before changing anything:
// open -> collision check -> render -> confirm -> unlock -> approve. The
// unlock is late on purpose (see "The approver unlocks, and the unlock
// comes late"), which is why `approve` is not wrapped in
// withUnlockedVault the way `recipient add` is.

// maxCodePrompts is a runaway guard on the code prompt, not a retry
// policy — the same distinction unlock.go draws for maxUnlockAttempts.
// The loop ends when the human stops answering, which at a masked prompt
// means a bare Enter.
const maxCodePrompts = 100

func newRecipientPendingCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "pending",
		Short: commandShort("recipient pending"),
		Long: commandShort("recipient pending") + ".\n\n" +
			"Everything shown comes from the request's filename, so this needs no unlock,\n" +
			"no code and no history walk. Device names are deliberately absent: they are\n" +
			"under the seal, and listing them would tell every reader of the vault who is\n" +
			"setting up a new machine before anyone approved anything.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultWithoutUnlocking(app, useFlag)
			if err != nil {
				return err
			}
			pending, err := v.PendingEnrollments()
			if err != nil {
				return err
			}
			writeOut(app.Out, pendingLines(v.Name, pending))
			return nil
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// pendingLines renders the listing: id and expiry, and nothing else
// there is to say without a code.
//
// The expiry is rendered in the approver's own local timezone, since it
// is the one timestamp they act on. Nothing here is a "requested at"
// time: that would have to come from the introducing commit, which is
// both an O(history) lookup and no more trustworthy than the filename.
func pendingLines(vault string, pending []gage.PendingRequest) []string {
	if len(pending) == 0 {
		return []string{fmt.Sprintf("gage: no pending enrollment requests for %q", vault)}
	}

	lines := []string{fmt.Sprintf("gage: %d pending enrollment %s for %q:",
		len(pending), plural(len(pending), "request", "requests"), vault), ""}
	for _, r := range pending {
		lines = append(lines, fmt.Sprintf("  %s   expires %s  (in %s)",
			shortRequestID(r.ID), r.Expires.Local().Format("2006-01-02 15:04 MST"),
			humanDuration(time.Until(r.Expires))))
	}
	return append(lines, "",
		"gage: nothing here can decrypt anything until it is approved.",
		"gage: run `gage recipient approve --code <code>` with a code you received out-of-band.")
}

// shortRequestID is the eight-character form the transcripts use and
// every resolver accepts.
func shortRequestID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func newRecipientApproveCommand(app *App) *cobra.Command {
	var (
		useFlag    string
		deviceFlag string
		codeFlags  []string
	)

	cmd := &cobra.Command{
		Use:   "approve [ID...]",
		Short: commandShort("recipient approve"),
		Long: commandShort("recipient approve") + ".\n\n" +
			"Opens each pending request with the code(s) you were given out-of-band, shows\n" +
			"what each one claims, and — on confirmation — adds them to the recipient list,\n" +
			"re-encrypts every entry so they can read the whole vault, and removes every\n" +
			"approved request, all in one commit.\n\n" +
			"There is no --reencrypt flag: approval always re-encrypts, because a recipient\n" +
			"who can read only part of a vault is a state gage has no use for. The unlock\n" +
			"comes after the confirmation, so a wrong code or a declined prompt costs no\n" +
			"passphrase. --device relabels a request whose claimed name now collides with\n" +
			"an existing recipient; the code authenticated the key, not the label.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Checked before anything is read, opened or asked: this is
			// a rejection of the command line itself, and it should cost
			// neither a code nor a confirmation nor a passphrase. The
			// recipient write validates it again — that one is the
			// authoritative check, but it happens after both.
			if deviceFlag != "" && !devicename.Valid(deviceFlag) {
				return exitcode.Newf(exitcode.Usage, "gage: device name %q is invalid", deviceFlag)
			}
			return runRecipientApprove(app, useFlag, deviceFlag, codeFlags, args)
		},
	}

	addUseFlag(cmd, &useFlag)
	cmd.Flags().StringArrayVar(&codeFlags, "code", nil,
		"an enrollment code you were given out-of-band (repeatable; prompted for if absent)")
	cmd.Flags().StringVar(&deviceFlag, "device", "",
		"record the approved key under this name instead of the one the request claims")
	return cmd
}

// runRecipientApprove is the whole of approve below flag parsing, in the
// order the TDD fixes:
//
//  1. Resolve the scope — every live request, or the ones an ID names.
//  2. Open it with the codes. No identity is held yet.
//  3. The device-name collision pre-check, still holding nothing.
//  4. Render what each request claims, and take the [y/N].
//  5. Unlock, and only now.
//  6. Approve, which runs the full-access pre-flight, takes the lock,
//     fetches, re-verifies and writes.
func runRecipientApprove(app *App, use, device string, codes, ids []string) error {
	v, err := vaultWithoutUnlocking(app, use)
	if err != nil {
		return err
	}

	scope, err := approvalScope(v, ids)
	if err != nil {
		reportAmbiguousRequest(app, err)
		return err
	}

	opened, codes, err := openWithCodes(app, v, scope, codes)
	if err != nil {
		return tooManyPendingAdvice(err)
	}

	// One flag cannot name which of several requests it relabels, so a
	// multi-request run is a usage error that says how to narrow it
	// rather than a guess.
	if device != "" && len(opened) != 1 {
		return exitcode.Newf(exitcode.Usage,
			"gage: --device relabels one request, but this run opened %d; "+
				"pass the ID of the one to relabel (see `gage recipient pending`)", len(opened))
	}

	approvals := make([]gage.Approval, 0, len(opened))
	for _, o := range opened {
		approvals = append(approvals, gage.Approval{Request: o, Label: device})
	}

	// Before the confirmation and before any unlock: it reads the
	// recipient list, which is plaintext.
	if err := v.CheckEnrollmentLabels(approvals); err != nil {
		return err
	}

	ok, err := confirmApproval(app, v, approvals)
	if err != nil {
		// A frontend with nobody to ask is not a frontend that agrees.
		// Letting a device into a vault is exactly the decision a run
		// that was never asked must not make by omission — the same rule
		// "Why there is no --enroll flag" applies on the joining side.
		return exitcode.Wrap(exitcode.LockedOrAuth, fmt.Errorf(
			"gage: there is nobody here to answer whether to admit this device, "+
				"so nothing was approved and the request is still pending: %w", err))
	}
	if !ok {
		return exitcode.New(exitcode.Conflict,
			"gage: nothing was approved, and the request is still pending")
	}

	unlock, done := syncUnlocker(app, v, use)
	defer done()
	ident, err := unlock()
	if err != nil {
		return err
	}

	ctx, cancel := syncContext()
	defer cancel()

	res, err := v.ApproveEnrollments(ctx, approvals, codes, ident)
	if err != nil {
		return err
	}
	writeOut(app.Out, approvalLines(v.Name, res))
	return nil
}

// approvalScope is the difference between the two runs, and it is the
// only difference: with no ID, every live request; with IDs, exactly the
// ones they resolve to.
//
// No flag turns D-ENROLL-SEAL-COST's 32-request bound on or off. The
// broad run can hit it, an ID-scoped run never can, and that falls out
// of what each one asks about rather than out of a parameter — which is
// what makes ErrEnrollmentTooManyPending's "approve by id" advice an
// escape hatch by construction.
func approvalScope(v *gage.Vault, ids []string) ([]gage.PendingRequest, error) {
	if len(ids) == 0 {
		return v.PendingEnrollments()
	}
	scope := make([]gage.PendingRequest, 0, len(ids))
	for _, id := range ids {
		match, err := v.ResolveEnrollment(id)
		if err != nil {
			return nil, err
		}
		scope = append(scope, match)
	}
	return scope, nil
}

// openWithCodes turns the codes a human supplied into opened requests.
//
// Each code is tried on its own, which is what makes "every supplied
// --code must open something" checkable at all: OpenEnrollment reports
// what opened, not which code opened it. A code that opens nothing fails
// the whole run rather than approving the subset that did — a mistyped
// code is far likelier than a deliberately surplus one, and partially
// approving a batch someone confirmed as a batch is the wrong way to be
// helpful.
//
// With no --code the code is prompted for, and cmd/gage owns the retry
// loop around a wrong one — the library returns
// ErrEnrollmentCodeWrong and decides nothing about retrying. The loop
// keys on that error and only on it: every other refusal from the open
// path (wrong vault, malformed, expired, clock skew) exits with its own
// advice, because no amount of retyping a code can fix them.
func openWithCodes(app *App, v *gage.Vault, scope []gage.PendingRequest, codes []string) ([]gage.OpenedRequest, []string, error) {
	if len(codes) == 0 {
		return promptForCode(app, v, scope)
	}

	var opened []gage.OpenedRequest
	for _, code := range codes {
		got, err := v.OpenEnrollment(scope, []string{code})
		if err != nil {
			return nil, nil, err
		}
		opened = append(opened, got...)
	}
	return opened, codes, nil
}

// promptForCode reads a code through the existing Prompter.Value — no
// new Prompter method and no new UnlockKind, per D-ENROLL-PROMPTER — and
// retries a wrong one.
//
// An empty answer ends the loop: at a masked prompt a bare Enter is how
// a human says "cancel", and the alternative is a prompt with no way out
// short of a signal. maxCodePrompts is a runaway guard behind that, for
// a frontend that never stops answering.
func promptForCode(app *App, v *gage.Vault, scope []gage.PendingRequest) ([]gage.OpenedRequest, []string, error) {
	for attempt := 0; attempt < maxCodePrompts; attempt++ {
		code, err := app.Prompter.Value("Enrollment code: ")
		if err != nil {
			return nil, nil, exitcode.Wrap(exitcode.LockedOrAuth, err)
		}
		if strings.TrimSpace(code) == "" {
			return nil, nil, exitcode.New(exitcode.Conflict,
				"gage: no enrollment code given; nothing was approved")
		}

		opened, err := v.OpenEnrollment(scope, []string{code})
		if err == nil {
			// The code travels on with the requests it opened:
			// ApproveEnrollments re-opens each seal under the write lock,
			// and a prompted code that stopped here would leave it with
			// nothing to re-verify against.
			return opened, []string{code}, nil
		}
		if !errors.Is(err, gage.ErrEnrollmentCodeWrong) {
			return nil, nil, err
		}
		app.Prompter.Warn("gage: that code did not open any pending request; check it and try again.")
	}
	return nil, nil, exitcode.Wrap(exitcode.LockedOrAuth, gage.ErrEnrollmentCodeWrong)
}

// confirmApproval renders what each request claims and asks the one
// question this feature adds.
//
// It states the consequence in the terms the approver cares about — the
// device will be able to read everything, including what is already
// there — rather than naming re-encryption, which is how that happens
// and not a thing anyone should have to reason about.
//
// This is deliberately not M10's question, and the two must never be
// collapsed: "should this device be let in" and "do you trust the list
// you are about to encrypt to" are different, and --yes answers only the
// second.
func confirmApproval(app *App, v *gage.Vault, approvals []gage.Approval) (bool, error) {
	ids, err := v.EntryIDs()
	if err != nil {
		return false, err
	}

	lines := []string{fmt.Sprintf("gage: opened %d pending %s.",
		len(approvals), plural(len(approvals), "request", "requests")), ""}
	for _, a := range approvals {
		lines = append(lines,
			fmt.Sprintf("  device:  %s", a.Request.Device),
			fmt.Sprintf("  pubkey:  %s", a.Request.Pubkey),
			fmt.Sprintf("  method:  %s", a.Request.Method),
			fmt.Sprintf("  created: %s  (expires in %s)",
				a.Request.Created.Local().Format("2006-01-02 15:04"),
				humanDuration(time.Until(a.Request.Expires))),
		)
		if a.Label != "" && a.Label != a.Request.Device {
			lines = append(lines, fmt.Sprintf("  recorded as: %s  (--device)", a.Label))
		}
		lines = append(lines, "")
	}
	writeOut(app.Out, lines)

	question := fmt.Sprintf("Add %s as %s of %q? It will be able to read all %d %s, including everything already in the vault.",
		approvedDevices(approvals), plural(len(approvals), "a recipient", "recipients"),
		v.Name, len(ids), plural(len(ids), "entry", "entries"))
	return app.Prompter.Confirm(question)
}

// approvedDevices names what is about to be let in, by the label that
// will actually be recorded.
func approvedDevices(approvals []gage.Approval) string {
	names := make([]string, 0, len(approvals))
	for _, a := range approvals {
		name := a.Request.Device
		if a.Label != "" {
			name = a.Label
		}
		names = append(names, fmt.Sprintf("%q", name))
	}
	return strings.Join(names, ", ")
}

// approvalLines renders what a completed approval did, per request and
// then once for the batch.
//
// It says the commit is local for the same reason `recipient add` does:
// the guarantee re-encryption makes is about HEAD, while publishing it
// is the ordinary post-write push, which warns and proceeds.
func approvalLines(vault string, res gage.ApprovalResult) []string {
	lines := make([]string, 0, len(res.Outcomes)+1)
	added := 0
	for _, o := range res.Outcomes {
		if !o.Added {
			lines = append(lines, fmt.Sprintf(
				"gage: %q is already a recipient of %q, so nothing was granted; its request is cleared.",
				o.Label, vault))
			continue
		}
		added++
		lines = append(lines, fmt.Sprintf("gage: approved %q (%s)", o.Label, o.Request.Pubkey))
	}
	return append(lines, fmt.Sprintf(
		"gage: %d %s added, %d %s re-encrypted, %d %s cleared; committed locally as %s",
		added, plural(added, "recipient", "recipients"),
		res.Reencrypted, plural(res.Reencrypted, "entry", "entries"),
		len(res.Outcomes), plural(len(res.Outcomes), "request", "requests"),
		shortHash(res.Commit)))
}

func newRecipientDenyCommand(app *App) *cobra.Command {
	var useFlag string
	cmd := &cobra.Command{
		Use:   "deny <ID>",
		Short: commandShort("recipient deny"),
		Long: commandShort("recipient deny") + ".\n\n" +
			"Needs no code and no unlock: refusing to grant access requires proving\n" +
			"nothing, it changes no recipient list, and anyone with git write access could\n" +
			"delete the request directly anyway. It still takes the vault write lock and\n" +
			"commits, like any other write — and warns if the push fails, since a deny that\n" +
			"never reached the remote leaves the request live for every other device.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := vaultWithoutUnlocking(app, useFlag)
			if err != nil {
				return err
			}
			if err := v.DenyEnrollment(args[0], app.Prompter); err != nil {
				reportAmbiguousRequest(app, err)
				return err
			}
			writeOut(app.Out, []string{fmt.Sprintf(
				"gage: removed pending request %s from %q. Nothing was granted; nothing to re-encrypt.",
				shortRequestID(args[0]), v.Name)})
			return nil
		},
	}
	addUseFlag(cmd, &useFlag)
	return cmd
}

// reportAmbiguousRequest prints err's candidate requests to stderr if it
// wraps *gage.AmbiguousRequestError, and is a no-op otherwise.
//
// The same shape as reportAmbiguous for entry queries, and for the same
// reason: the library returns the matches as a value rather than printed
// text, and deciding how to show them is this package's job. Both approve
// and deny resolve through one function, so both render one list.
//
// It shows the expiry alongside each id, because in a run that is
// ambiguous the id is by definition not enough to tell them apart, and
// the expiry is the only other thing knowable without a code.
func reportAmbiguousRequest(app *App, err error) {
	var amb *gage.AmbiguousRequestError
	if !errors.As(err, &amb) {
		return
	}
	lines := []string{fmt.Sprintf("gage: %q matches %d pending enrollment requests:", amb.ID, len(amb.Matches))}
	for i, r := range amb.Matches {
		lines = append(lines, fmt.Sprintf("  %d. %s   expires %s",
			i+1, shortRequestID(r.ID), r.Expires.Local().Format("2006-01-02 15:04 MST")))
	}
	writeOut(app.Err, lines)
}

// tooManyPendingAdvice adds the concrete way out to D-ENROLL-SEAL-COST's
// refusal.
//
// The library says the rule — a run with no ID cannot try more than the
// bound — and names "by id" as the recovery, which is as much as a
// library that renders nothing can say. What it cannot know is the exact
// command line, and the whole point of the bound being recoverable in
// place is that the human is told what to type. `gage recipient pending`
// is named alongside it because the ID has to come from somewhere, and
// that listing is unaffected by a stuffed directory: it opens nothing.
func tooManyPendingAdvice(err error) error {
	if !errors.Is(err, gage.ErrEnrollmentTooManyPending) {
		return err
	}
	return exitcode.Newf(exitcode.CodeOf(err),
		"%v; run `gage recipient pending` to find the id, then `gage recipient approve <ID> --code <code>`", err)
}
