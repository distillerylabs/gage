package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// maxPassphraseAttempts is gage's wrong-passphrase retry policy, and it
// lives here rather than in internal/gage on purpose: the library returns
// a typed error and never decides how many tries a human gets (see
// "Library architecture").
//
// Three, and the retry re-enters through the Prompter rather than looping
// inside one library call with a fixed count. Vault.Unlock re-asks for as
// long as its Prompter keeps answering and stops the moment one returns
// an error, so "three attempts" is expressed by this prompter refusing
// the fourth request. That keeps the count in one place, keeps the
// library free of policy, and means a GUI frontend can choose a different
// number without touching anything below it.
//
// Three because a passphrase is typed blind: one attempt punishes a
// typo, and an unlimited count turns a local lock screen into an
// unrate-limited oracle. There is nothing to lock out here — the identity
// file is on disk and an attacker with it can ignore gage entirely — so
// the number is about human ergonomics, not about slowing an attack down.
// That job belongs to the scrypt work factor.
const maxPassphraseAttempts = 3

// errTooManyAttempts ends the retry loop. It's what this prompter returns
// on the request after the last allowed attempt, which is how cmd/gage's
// policy reaches a library that has none of its own.
var errTooManyAttempts = errors.New("too many incorrect passphrase attempts")

// terminalPrompter is cmd/gage's Prompter: the one place in the program
// that reads a passphrase from a terminal. internal/gage never does —
// it asks, through this interface.
type terminalPrompter struct {
	in io.Reader
	// out carries prompts, warnings, and confirmations rather than
	// stdout, so a `gage show foo > secret.txt` gets only the secret and
	// a human still sees the prompt they're answering.
	out io.Writer

	// br buffers in across calls. It has to persist: a fresh bufio.Reader
	// per call would read ahead and drop whatever followed the line it
	// returned, which in a --stdin script is the next command.
	br *bufio.Reader

	// secretFn/lineFn, when set, replace this prompter's own reads for
	// the duration of a session. A session owns the input stream — its
	// line editor holds the terminal in raw mode and reads stdin from a
	// background goroutine — so a passphrase prompt that reached for
	// os.Stdin itself would be racing it for the same bytes. See
	// lineReader and useSessionReader.
	secretFn func(prompt string) (string, error)
	lineFn   func(prompt string) (string, error)

	// interactive says whether there is a human at the other end of in.
	// Only conflict resolution consults it: every other prompt here has a
	// sensible non-interactive answer (a passphrase can come from a
	// script, a confirmation defaults to no), while "which version of
	// this secret do you want to keep" has none — see ResolveConflict.
	//
	// It is set from App.IsTerminal at construction rather than probed
	// per call, so a test can drive both halves without a pty.
	interactive bool
}

func newTerminalPrompter(in io.Reader, out io.Writer) *terminalPrompter {
	return &terminalPrompter{in: in, out: out}
}

// useSessionReader points this prompter's reads at a running session's
// lineReader, and returns a function restoring the previous behavior.
// The REPL calls it once at startup: from then until the session ends,
// every passphrase, value, confirmation, and candidate choice is read
// through the same object reading command lines.
//
// Writes move with the reads, to out. Prompts live on stderr in one-shot
// mode so `gage show foo > secret.txt` puts only the secret in the file;
// inside a session there is no such redirection, the line editor already
// draws its own prompt on out, and splitting a question from the list it
// refers to across two streams would be the only thing that could go
// wrong here.
func (p *terminalPrompter) useSessionReader(lr lineReader, out io.Writer) func() {
	prevSecret, prevLine, prevOut := p.secretFn, p.lineFn, p.out
	p.secretFn, p.lineFn, p.out = lr.readSecret, lr.readPlain, out
	return func() { p.secretFn, p.lineFn, p.out = prevSecret, prevLine, prevOut }
}

// ask renders a prompt and reads one echoed line — the shared path
// behind Confirm and Choose, so both go through a session's reader when
// one is installed and print-then-read otherwise.
func (p *terminalPrompter) ask(prompt string) (string, error) {
	if p.lineFn != nil {
		return p.lineFn(prompt)
	}
	_, _ = fmt.Fprint(p.out, prompt)
	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Unlock renders an UnlockRequest as a terminal prompt and returns what
// the human typed.
func (p *terminalPrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	if req.Kind != gage.KindPassphrase {
		return gage.UnlockResponse{}, exitcode.Newf(exitcode.Internal,
			"gage: this build cannot satisfy a %q unlock request", req.Kind)
	}
	if req.Purpose == gage.PurposeCreate {
		return p.newPassphrase(req)
	}

	if req.Attempt > maxPassphraseAttempts {
		return gage.UnlockResponse{}, exitcode.Wrap(exitcode.LockedOrAuth, errTooManyAttempts)
	}
	if req.Attempt > 1 {
		_, _ = fmt.Fprintf(p.out, "gage: wrong passphrase (attempt %d of %d)\n", req.Attempt, maxPassphraseAttempts)
	}

	passphrase, err := p.readSecret(fmt.Sprintf("Enter passphrase for vault %q (device %s): ", req.Vault, req.Device))
	if err != nil {
		return gage.UnlockResponse{}, err
	}
	return gage.UnlockResponse{Kind: gage.KindPassphrase, Passphrase: passphrase}, nil
}

// newPassphrase asks for a brand-new identity's passphrase, twice. There
// is nothing to check a new passphrase against, so a typo here would
// otherwise be discovered only at the next unlock — by which point the
// key it protects is unrecoverable.
//
// The second line labels the answer at its point of use: this is the one
// place in gage where two different secrets can end up on one screen a
// few lines apart — this passphrase, which the user chooses and never
// sends, and `identity enroll`'s code, which gage generates and which is
// meant to be sent. The failure mode being designed against is someone
// pasting one into a chat window believing it was the other, so each
// says which it is where it appears. See "Which secret is which".
func (p *terminalPrompter) newPassphrase(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	_, _ = fmt.Fprintf(p.out, "gage: creating identity %q for vault %q.\n", req.Device, req.Vault)
	_, _ = fmt.Fprintln(p.out, "gage: this passphrase protects this device's private key. It cannot be recovered or reset.")
	_, _ = fmt.Fprintln(p.out, "gage: it stays on this device and is never sent to anyone.")

	for attempt := 1; attempt <= maxPassphraseAttempts; attempt++ {
		first, err := p.readSecret("Choose a passphrase: ")
		if err != nil {
			return gage.UnlockResponse{}, err
		}
		if first == "" {
			_, _ = fmt.Fprintln(p.out, "gage: an empty passphrase is not accepted.")
			continue
		}
		second, err := p.readSecret("Confirm passphrase: ")
		if err != nil {
			return gage.UnlockResponse{}, err
		}
		if first == second {
			return gage.UnlockResponse{Kind: gage.KindPassphrase, Passphrase: first}, nil
		}
		_, _ = fmt.Fprintln(p.out, "gage: the two passphrases did not match.")
	}
	return gage.UnlockResponse{}, exitcode.Newf(exitcode.LockedOrAuth,
		"gage: gave up after %d attempts to enter a matching passphrase", maxPassphraseAttempts)
}

// Value reads one masked line of free-form text — gage insert's value
// prompt. Unlike Unlock there's nothing to check the answer against and
// no retry policy: whatever comes back is what gets stored.
func (p *terminalPrompter) Value(prompt string) (string, error) {
	return p.readSecret(prompt)
}

// Confirm asks a yes/no question. A bare Enter means no: every caller of
// Confirm is about to do something a user might not want, so the safe
// answer is the default.
func (p *terminalPrompter) Confirm(prompt string) (bool, error) {
	line, err := p.ask(fmt.Sprintf("%s [y/N] ", prompt))
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// ConfirmDefaultYes asks a yes/no question whose default is yes, and it
// is deliberately the only one in gage: clone's offer to set up an
// enrollment request. A bare Enter accepts.
//
// Confirm's default-no rule is right for every question that is about to
// do something a user might not want. This one is the opposite shape —
// clone has just told the user this device cannot read the vault, and
// the offer is the answer to the problem it reported — so making them
// type `y` to accept advice they just asked for would be the wrong
// default rather than the safe one. Nothing else moves: Confirm still
// renders [y/N] and still means no on Enter.
func (p *terminalPrompter) ConfirmDefaultYes(prompt string) (bool, error) {
	line, err := p.ask(fmt.Sprintf("%s [Y/n] ", prompt))
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// ConfirmRecipientChange renders M10's trust-cache warning as the
// terminal diff + [y/N] prompt from "Local trust cache", and returns
// whether the human approved the recipient list it describes.
//
// The library never prints any of this: it hands over a
// RecipientChangeWarning — the diff, the verification result, which
// recipients moved — and this is the CLI deciding what that looks like,
// exactly as Choose renders a CandidateList. The two cases are rendered
// differently on purpose. A routine change (both recipient files agree)
// gets the diff and a confirmation; a mismatched one gets its own, more
// severe line naming the keys the two files disagree about, because that
// is the tampering signature the cache exists to surface and it must not
// read like an ordinary "someone added a device."
//
// Like every other Confirm here, a bare Enter means no.
func (p *terminalPrompter) ConfirmRecipientChange(w gage.RecipientChangeWarning) (bool, error) {
	_, _ = fmt.Fprintf(p.out, "\u26a0 Recipients for %q changed since you last encrypted here:\n\n", w.Vault)
	if w.Diff != "" {
		_, _ = fmt.Fprintln(p.out, w.Diff)
	}

	if w.Mismatched() {
		_, _ = fmt.Fprintln(p.out, "gage: .age-recipients and .gage/config.toml DISAGREE "+
			"(gage recipient verify fails):")
		for _, k := range w.Verification.OnlyInRecipientsFile {
			_, _ = fmt.Fprintf(p.out, "  %s is in .age-recipients but not in .gage/config.toml\n", k)
		}
		for _, k := range w.Verification.OnlyInConfig {
			_, _ = fmt.Fprintf(p.out, "  %s is in .gage/config.toml but not in .age-recipients\n", k)
		}
		// Said before the question, because it changes what answering
		// yes means: it lets this write through, and nothing else. The
		// vault stays flagged until the two files actually agree.
		_, _ = fmt.Fprintln(p.out, "gage: confirming proceeds with this write; it does not clear the mismatch.")
	} else {
		_, _ = fmt.Fprintln(p.out, "gage: .age-recipients matches .gage/config.toml. (gage recipient verify passes)")
	}

	return p.Confirm(fmt.Sprintf("%s. Proceed and trust this recipient list?",
		recipientChangeCount(w)))
}

// recipientChangeCount is the design doc's "1 recipient added." line.
func recipientChangeCount(w gage.RecipientChangeWarning) string {
	switch {
	case len(w.Added) > 0 && len(w.Removed) > 0:
		return fmt.Sprintf("%d recipient(s) added, %d removed", len(w.Added), len(w.Removed))
	case len(w.Added) > 0:
		return fmt.Sprintf("%d %s added", len(w.Added), plural(len(w.Added), "recipient", "recipients"))
	case len(w.Removed) > 0:
		return fmt.Sprintf("%d %s removed", len(w.Removed), plural(len(w.Removed), "recipient", "recipients"))
	default:
		// No config change at all — the .age-recipients-only edit, which
		// is exactly the case with nothing to count and everything to
		// worry about.
		return "The recipient list changed"
	}
}

// maxChooseAttempts bounds the re-ask loop for a mistyped candidate
// number. Same shape as the passphrase retry policy, and here for the
// same reason: the library hands over a candidate list and has no
// opinion about how many times a human gets to answer it.
const maxChooseAttempts = 3

// Choose renders an ambiguous query's candidate list as the numbered
// picker from "Addressing entries & the metadata index" and returns the
// id of whichever one was picked.
//
// This is the session half of ambiguity: one-shot mode prints the same
// list and fails (see printCandidates), because there's nobody to ask.
// The list itself is the library's — a CandidateList value, not printed
// text — and this is the CLI deciding to render it as a prompt.
func (p *terminalPrompter) Choose(list gage.CandidateList) (string, error) {
	if len(list.Candidates) == 0 {
		return "", exitcode.New(exitcode.Internal, "gage: asked to choose from an empty candidate list")
	}

	lines := []string{fmt.Sprintf("Multiple entries match %q:", list.Query)}
	for i, c := range list.Candidates {
		lines = append(lines, fmt.Sprintf("  %d. %s (%s)", i+1, c.Title, shortEntryID(c.ID)))
	}
	writeOut(p.out, lines)

	for attempt := 1; attempt <= maxChooseAttempts; attempt++ {
		answer, err := p.ask(fmt.Sprintf("Which one? [1-%d]: ", len(list.Candidates)))
		if err != nil {
			return "", err
		}
		n, err := strconv.Atoi(strings.TrimSpace(answer))
		if err == nil && n >= 1 && n <= len(list.Candidates) {
			return list.Candidates[n-1].ID, nil
		}
		_, _ = fmt.Fprintf(p.out, "gage: enter a number between 1 and %d.\n", len(list.Candidates))
	}
	return "", exitcode.Newf(exitcode.Ambiguous,
		"gage: no entry chosen after %d attempts", maxChooseAttempts)
}

// maxConflictAttempts bounds the re-ask loop for an answer that isn't
// one of the five keys. Same shape and same reason as maxChooseAttempts,
// but there is no default to fall back to when it runs out: gage refuses
// rather than picking a side, which ends the sync with nothing applied.
const maxConflictAttempts = 5

// ResolveConflict renders the [l/r/b/s/q] menu from "Resolving an entry
// conflict" and returns what the human chose. The EntryConflict arrives
// as a value with both sides already decrypted; this is the CLI deciding
// how to show it, the same split Choose follows.
//
// The two secret *values* are deliberately not printed. The choice is
// made on provenance — who wrote each version, and when — which is what
// the design doc's prompt shows, and which is all it takes to decide.
// Printing both passwords onto a shared screen to answer a question
// about which one to keep would be its own small disclosure, repeated
// once per conflict.
//
// Without a human at the other end it refuses rather than choosing: see
// the M8b plan's non-interactive decision.
func (p *terminalPrompter) ResolveConflict(c gage.EntryConflict) (gage.Resolution, error) {
	if !p.interactive {
		return 0, exitcode.Wrap(exitcode.Conflict, gage.ErrNotInteractive)
	}

	writeOut(p.out, []string{
		fmt.Sprintf("Entry conflicts: %q", conflictTitle(c)),
		"",
		"  local    " + conflictProvenance(c.Local),
		"  remote   " + conflictProvenance(c.Remote),
		"",
		"  [l] keep local     [r] keep remote",
		"  [b] keep both      [s] skip this entry     [q] abort sync",
	})

	for attempt := 1; attempt <= maxConflictAttempts; attempt++ {
		answer, err := p.ask("Which? [l/r/b/s/q]: ")
		if err != nil {
			return 0, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "l":
			return gage.KeepLocal, nil
		case "r":
			return gage.KeepRemote, nil
		case "b":
			return gage.KeepBoth, nil
		case "s":
			return gage.SkipConflict, nil
		case "q":
			return gage.AbortSync, nil
		}
		// No default and no guessing: an unrecognized keystroke is how a
		// secret gets discarded by someone who meant to hit the key next
		// to it.
		_, _ = fmt.Fprintln(p.out, "gage: answer l, r, b, s or q.")
	}
	return 0, exitcode.Newf(exitcode.Conflict,
		"gage: no version chosen after %d attempts", maxConflictAttempts)
}

// conflictTitle is what to call the entry being resolved: whichever side
// still has one, since a delete/modify conflict has only the survivor to
// name it by.
func conflictTitle(c gage.EntryConflict) string {
	if c.Local.Present {
		return c.Local.Entry.Title
	}
	return c.Remote.Entry.Title
}

// conflictProvenance renders one side's line of the prompt — when it was
// written and by which device, or that this side removed the entry
// altogether, which is the only thing a delete/modify conflict has to
// say about the side that deleted it.
func conflictProvenance(side gage.ConflictSide) string {
	if !side.Present {
		return "deleted this entry"
	}
	return fmt.Sprintf("updated %s  by %s", conflictWhen(side.Entry.Updated), lsField(side.Entry.UpdatedBy))
}

// conflictWhen renders a timestamp the way the design doc's prompt shows
// it: the day and the clock time, in UTC like every other date gage
// prints (see lsDate), so two devices in different zones describe the
// same edit the same way.
func conflictWhen(t gage.Timestamp) string {
	if t.IsZero() {
		return lsMissing
	}
	return t.UTC().Format(conflictDateLayout)
}

// conflictDateLayout carries the clock time that lsDateLayout leaves
// off: two edits to one entry are routinely made on the same day, and
// the date alone would then distinguish nothing.
const conflictDateLayout = "2006-01-02 15:04"

// Warn prints a non-fatal advisory. It returns nothing, matching the
// interface: the caller has already decided to continue, and a failed
// write to a terminal must not be able to turn a working unlock into a
// failed one.
func (p *terminalPrompter) Warn(msg string) {
	// The error is deliberately dropped: if warning the user fails there
	// is nowhere left to report that, and the operation being warned
	// about is one gage has already decided to complete.
	_, _ = fmt.Fprintln(p.out, msg)
}

// readSecret prompts and reads one secret without echoing it. On a real
// terminal that's x/term putting the tty into no-echo mode; with stdin
// piped or redirected — a --stdin script, a test — there is no echo to
// suppress and the line is read normally.
func (p *terminalPrompter) readSecret(prompt string) (string, error) {
	// Inside a session the line editor owns the terminal, so the read
	// goes through it rather than through this prompter's own stdin
	// handling. See useSessionReader.
	if p.secretFn != nil {
		return p.secretFn(prompt)
	}

	_, _ = fmt.Fprint(p.out, prompt)

	if f, ok := p.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		secret, err := term.ReadPassword(int(f.Fd()))
		// The terminal swallowed the user's Enter along with the echo,
		// so the cursor is still on the prompt line; move it down before
		// anything else prints.
		_, _ = fmt.Fprintln(p.out)
		if err != nil {
			return "", exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: reading passphrase: %w", err))
		}
		return string(secret), nil
	}

	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	// Piped input isn't echoed either, so without this the next prompt
	// would run on from this one on the same line.
	_, _ = fmt.Fprintln(p.out)
	return strings.TrimRight(line, "\r\n"), nil
}

func (p *terminalPrompter) readLine() (string, error) {
	if p.br == nil {
		p.br = bufio.NewReader(p.in)
	}
	line, err := p.br.ReadString('\n')
	if err != nil {
		// A final line without a trailing newline is still an answer;
		// only an EOF with nothing at all is a failure to answer.
		if errors.Is(err, io.EOF) && line != "" {
			return line, nil
		}
		return "", exitcode.Wrap(exitcode.LockedOrAuth, fmt.Errorf("gage: reading input: %w", err))
	}
	return line, nil
}
