package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

func unlockReq(attempt int) gage.UnlockRequest {
	return gage.UnlockRequest{
		Kind:    gage.KindPassphrase,
		Purpose: gage.PurposeUnlock,
		Vault:   "personal",
		Device:  "laptop-1",
		Attempt: attempt,
	}
}

// TestRetryPolicyLivesInThePrompter is the M2 decision made observable:
// the library keeps re-asking for as long as its Prompter answers, so
// "three attempts" is expressed by this prompter refusing the fourth
// request rather than by a count inside internal/gage.
func TestRetryPolicyLivesInThePrompter(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("a\nb\nc\nd\n"), &out)

	for attempt := 1; attempt <= maxPassphraseAttempts; attempt++ {
		resp, err := p.Unlock(unlockReq(attempt))
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if resp.Passphrase == "" {
			t.Fatalf("attempt %d returned an empty passphrase", attempt)
		}
	}

	_, err := p.Unlock(unlockReq(maxPassphraseAttempts + 1))
	if err == nil {
		t.Fatalf("attempt %d was answered; the policy allows %d", maxPassphraseAttempts+1, maxPassphraseAttempts)
	}
	if !errors.Is(err, errTooManyAttempts) {
		t.Errorf("error = %v, want it to wrap errTooManyAttempts", err)
	}
	if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
}

// TestRetryPromptTellsTheUserWhichAttemptThisIs: the Attempt field is
// what lets a prompter say "wrong passphrase, try again" without the
// library owning any of that policy.
func TestRetryPromptTellsTheUserWhichAttemptThisIs(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("first\nsecond\n"), &out)

	if _, err := p.Unlock(unlockReq(1)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "wrong passphrase") {
		t.Errorf("the first attempt was announced as a retry:\n%s", out.String())
	}

	out.Reset()
	if _, err := p.Unlock(unlockReq(2)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wrong passphrase") {
		t.Errorf("a retry doesn't tell the user the last attempt was wrong:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "2 of 3") {
		t.Errorf("a retry doesn't say how many attempts are left:\n%s", out.String())
	}
}

// TestNewPassphraseIsConfirmed: there is nothing to check a brand-new
// passphrase against, so a typo would only surface at the next unlock —
// by which point the key it protects is unrecoverable.
func TestNewPassphraseIsConfirmed(t *testing.T) {
	createReq := gage.UnlockRequest{
		Kind:    gage.KindPassphrase,
		Purpose: gage.PurposeCreate,
		Vault:   "personal",
		Device:  "laptop-1",
		Attempt: 1,
	}

	t.Run("matching entries are accepted", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("s3cret\ns3cret\n"), &out)
		resp, err := p.Unlock(createReq)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Passphrase != "s3cret" {
			t.Errorf("passphrase = %q, want %q", resp.Passphrase, "s3cret")
		}
		if !strings.Contains(out.String(), "Confirm passphrase") {
			t.Errorf("the passphrase was never confirmed:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "cannot be recovered") {
			t.Errorf("the user isn't told the passphrase is unrecoverable:\n%s", out.String())
		}
	})

	t.Run("a mismatch is re-asked, not accepted", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("typo\ntypa\ns3cret\ns3cret\n"), &out)
		resp, err := p.Unlock(createReq)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Passphrase != "s3cret" {
			t.Errorf("passphrase = %q, want the confirmed one (%q)", resp.Passphrase, "s3cret")
		}
		if !strings.Contains(out.String(), "did not match") {
			t.Errorf("the mismatch wasn't reported:\n%s", out.String())
		}
	})

	t.Run("repeated mismatches give up rather than looping", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("a\nb\nc\nd\ne\nf\n"), &out)
		if _, err := p.Unlock(createReq); err == nil {
			t.Fatal("expected repeated mismatches to fail")
		} else if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
			t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
		}
	})
}

// TestPromptsGoToTheErrorStreamNotStdout: a `gage show foo > secret.txt`
// must put only the secret in the file, while the human still sees the
// prompt they're answering.
func TestPromptsGoToTheErrorStreamNotStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &App{Out: &stdout, Err: &stderr, In: strings.NewReader("")}
	app.Prompter = newTerminalPrompter(strings.NewReader("s3cret\ns3cret\n"), app.Err)

	if _, err := app.Prompter.Unlock(gage.UnlockRequest{
		Kind: gage.KindPassphrase, Purpose: gage.PurposeCreate, Vault: "personal", Device: "laptop-1", Attempt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Errorf("a prompt reached stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Choose a passphrase") {
		t.Errorf("the prompt didn't reach stderr: %q", stderr.String())
	}
}

func TestWarnIsSurfacedAndReturnsNothing(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader(""), &out)
	p.Warn("gage: could not lock key material into memory")
	if !strings.Contains(out.String(), "could not lock key material") {
		t.Errorf("the warning wasn't surfaced: %q", out.String())
	}
}

func TestConfirmDefaultsToNo(t *testing.T) {
	for input, want := range map[string]bool{
		"y\n":       true,
		"Y\n":       true,
		"yes\n":     true,
		"n\n":       false,
		"\n":        false,
		"garbage\n": false,
	} {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			var out bytes.Buffer
			p := newTerminalPrompter(strings.NewReader(input), &out)
			got, err := p.Confirm("do the thing?")
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("Confirm(%q) = %v, want %v", input, got, want)
			}
		})
	}
}

// TestPrompterRefusesAMethodItCannotSatisfy: a second UnlockKind is
// additive to the interface, so this build has to say plainly that it
// can't satisfy one rather than returning an empty response that would
// read as an empty passphrase.
func TestPrompterRefusesAMethodItCannotSatisfy(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("whatever\n"), &out)
	resp, err := p.Unlock(gage.UnlockRequest{Kind: "yubikey", Vault: "personal", Device: "laptop-1", Attempt: 1})
	if err == nil {
		t.Fatal("expected an unsupported unlock kind to be refused")
	}
	if resp.Passphrase != "" {
		t.Errorf("a refused request still produced a passphrase: %q", resp.Passphrase)
	}
	if !strings.Contains(err.Error(), "yubikey") {
		t.Errorf("error %v doesn't name the kind it can't satisfy", err)
	}
}

// TestReadingBufferedInputDoesNotSwallowFollowingLines: the prompter
// buffers stdin across calls, which it must, or a --stdin script would
// lose whatever followed the passphrase it just read.
func TestReadingBufferedInputDoesNotSwallowFollowingLines(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("first\nsecond\n"), &out)

	one, err := p.Unlock(unlockReq(1))
	if err != nil {
		t.Fatal(err)
	}
	two, err := p.Unlock(unlockReq(2))
	if err != nil {
		t.Fatal(err)
	}
	if one.Passphrase != "first" || two.Passphrase != "second" {
		t.Errorf("read %q then %q, want \"first\" then \"second\"", one.Passphrase, two.Passphrase)
	}
}

func TestReadingPastEndOfInputFails(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader(""), &out)
	if _, err := p.Unlock(unlockReq(1)); err == nil {
		t.Fatal("expected reading a passphrase from empty input to fail")
	} else if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
}

// TestConfirmDefaultYesDefaultsToYes is the one Prompter question in
// gage whose bare Enter means yes — clone's offer to enroll. Both halves
// are pinned, the answer and the rendering: a ConfirmDefaultYes
// accidentally wired to Confirm still compiles and still passes a
// yes-answering test, and would silently make Enter mean no.
func TestConfirmDefaultYesDefaultsToYes(t *testing.T) {
	for input, want := range map[string]bool{
		"y\n":       true,
		"Y\n":       true,
		"yes\n":     true,
		"\n":        true,
		"n\n":       false,
		"N\n":       false,
		"no\n":      false,
		"garbage\n": false,
	} {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			var out bytes.Buffer
			p := newTerminalPrompter(strings.NewReader(input), &out)
			got, err := p.ConfirmDefaultYes("do the thing?")
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("ConfirmDefaultYes(%q) = %v, want %v", input, got, want)
			}
			if !strings.Contains(out.String(), "[Y/n]") {
				t.Errorf("prompt rendered as %q, want it to show [Y/n]", out.String())
			}
		})
	}
}

// TestConfirmStillRendersDefaultNo is the other side of the same pin:
// the new method must have been *added*, not made by changing the old
// one. An empty answer to Confirm is still no, and it still renders
// [y/N].
func TestConfirmStillRendersDefaultNo(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("\n"), &out)
	got, err := p.Confirm("do the thing?")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("a bare Enter answered Confirm with yes")
	}
	if !strings.Contains(out.String(), "[y/N]") {
		t.Errorf("prompt rendered as %q, want it to show [y/N]", out.String())
	}
}

// TestNewPassphraseRejectsAnEmptyOne: an empty passphrase protects
// nothing, so it is re-asked rather than accepted. The re-ask matters as
// much as the refusal — failing outright would lose the identity the
// prompt is in the middle of creating.
func TestNewPassphraseRejectsAnEmptyOne(t *testing.T) {
	createReq := gage.UnlockRequest{
		Kind:    gage.KindPassphrase,
		Purpose: gage.PurposeCreate,
		Vault:   "personal",
		Device:  "laptop-1",
		Attempt: 1,
	}

	var out bytes.Buffer
	// An empty first answer, then a real one entered twice.
	p := newTerminalPrompter(strings.NewReader("\ns3cret\ns3cret\n"), &out)

	resp, err := p.Unlock(createReq)
	if err != nil {
		t.Fatalf("an empty passphrase ended the prompt instead of re-asking: %v", err)
	}
	if resp.Passphrase != "s3cret" {
		t.Errorf("passphrase = %q, want the one actually entered (%q)", resp.Passphrase, "s3cret")
	}
	if !strings.Contains(out.String(), "an empty passphrase is not accepted") {
		t.Errorf("the refusal wasn't reported:\n%s", out.String())
	}
}

// TestRecipientChangeCountSummarizesEveryShape is the design doc's
// "1 recipient added." line, over all four shapes the switch has —
// including the one with nothing to count, which is the
// .age-recipients-only edit and the case that matters most.
func TestRecipientChangeCountSummarizesEveryShape(t *testing.T) {
	r := func(n int) []gage.VaultRecipient {
		out := make([]gage.VaultRecipient, n)
		for i := range out {
			out[i] = gage.VaultRecipient{Device: "d", Pubkey: "k"}
		}
		return out
	}

	cases := []struct {
		name string
		w    gage.RecipientChangeWarning
		want string
	}{
		{"one added", gage.RecipientChangeWarning{Added: r(1)}, "1 recipient added"},
		{"two added", gage.RecipientChangeWarning{Added: r(2)}, "2 recipients added"},
		{"one removed", gage.RecipientChangeWarning{Removed: r(1)}, "1 recipient removed"},
		{"two removed", gage.RecipientChangeWarning{Removed: r(2)}, "2 recipients removed"},
		{"added and removed", gage.RecipientChangeWarning{Added: r(1), Removed: r(2)}, "1 recipient(s) added, 2 removed"},
		{"neither", gage.RecipientChangeWarning{}, "The recipient list changed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := recipientChangeCount(c.w); got != c.want {
				t.Errorf("recipientChangeCount() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestConfirmRecipientChangeNamesBothDirectionsOfAMismatch: when the two
// recipient files disagree, the question has to say *how* — a key only
// .age-recipients knows about, and a key only config.toml knows about,
// are different problems and the prompt lists both.
func TestConfirmRecipientChangeNamesBothDirectionsOfAMismatch(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("n\n"), &out)

	ok, err := p.ConfirmRecipientChange(gage.RecipientChangeWarning{
		Vault: "personal",
		Verification: gage.RecipientVerification{
			InSync:               false,
			OnlyInRecipientsFile: []string{"age1onlyinrecipientsfile"},
			OnlyInConfig:         []string{"age1onlyinconfig"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a bare `n` was taken as approval")
	}
	for _, want := range []string{
		"age1onlyinrecipientsfile",
		"age1onlyinconfig",
		"DISAGREE",
		"does not clear the mismatch",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the question doesn't mention %q:\n%s", want, out.String())
		}
	}
}

// TestChooseOnAnEmptyCandidateListIsInternal: an empty list is a library
// bug, not a human's problem — there is nothing to render and nothing to
// pick, so it must not print an empty picker and wait.
func TestChooseOnAnEmptyCandidateListIsInternal(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader(""), &out)

	_, err := p.Choose(gage.CandidateList{Query: "anything"})
	if err == nil {
		t.Fatal("an empty candidate list was accepted")
	}
	if exitcode.CodeOf(err) != exitcode.Internal {
		t.Errorf("CodeOf(err) = %v, want Internal", exitcode.CodeOf(err))
	}
}

// TestChooseRetriesThenGivesUp: a number outside the list and a
// non-numeric answer are both re-asked with the range spelled out, and
// the loop ends in Ambiguous rather than picking something. Picking on
// the user's behalf here would hand them a different secret than the one
// they meant.
func TestChooseRetriesThenGivesUp(t *testing.T) {
	list := gage.CandidateList{
		Query: "mail",
		Candidates: []gage.Candidate{
			{ID: "11111111-1111-4111-8111-111111111111", Title: "Personal mail"},
			{ID: "22222222-2222-4222-8222-222222222222", Title: "Work mail"},
		},
	}

	t.Run("an out-of-range answer is re-asked, then a valid one wins", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("9\nnot-a-number\n2\n"), &out)

		got, err := p.Choose(list)
		if err != nil {
			t.Fatalf("a valid third answer was not accepted: %v", err)
		}
		if got != list.Candidates[1].ID {
			t.Errorf("chose %q, want %q", got, list.Candidates[1].ID)
		}
		if n := strings.Count(out.String(), "enter a number between 1 and 2"); n != 2 {
			t.Errorf("the range was spelled out %d times, want 2 (once per bad answer):\n%s", n, out.String())
		}
	})

	t.Run("nothing but bad answers gives up", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader(strings.Repeat("0\n", maxChooseAttempts)), &out)

		_, err := p.Choose(list)
		if err == nil {
			t.Fatal("the picker accepted an answer it never got")
		}
		if exitcode.CodeOf(err) != exitcode.Ambiguous {
			t.Errorf("CodeOf(err) = %v, want Ambiguous", exitcode.CodeOf(err))
		}
	})
}

// TestResolveConflictNeverGuesses is the "no default and no guessing"
// rule made into a test that fails if a default is ever added: an
// unrecognized keystroke is how a secret gets discarded by someone who
// meant to hit the key next to it, so the loop refuses rather than
// picking a side.
func TestResolveConflictNeverGuesses(t *testing.T) {
	conflict := gage.EntryConflict{
		Path:   "entries/11111111-1111-4111-8111-111111111111.age",
		Local:  gage.ConflictSide{Present: true, Entry: gage.Entry{Title: "Mail", UpdatedBy: "laptop-1"}},
		Remote: gage.ConflictSide{Present: true, Entry: gage.Entry{Title: "Mail", UpdatedBy: "phone-1"}},
	}

	var out bytes.Buffer
	// "x" is next to nothing in particular; the point is that it is not
	// one of l/r/b/s/q, repeated until the prompter gives up.
	p := newTerminalPrompter(strings.NewReader(strings.Repeat("x\n", maxConflictAttempts)), &out)
	// Without a human at the other end ResolveConflict refuses before it
	// asks anything, which is a different path (and a different test);
	// this one is about the refusal *after* asking.
	p.interactive = true

	_, err := p.ResolveConflict(conflict)
	if err == nil {
		t.Fatal("an unrecognized answer was accepted as a resolution")
	}
	if exitcode.CodeOf(err) != exitcode.Conflict {
		t.Errorf("CodeOf(err) = %v, want Conflict", exitcode.CodeOf(err))
	}
	if n := strings.Count(out.String(), "answer l, r, b, s or q"); n != maxConflictAttempts {
		t.Errorf("re-asked %d times, want %d:\n%s", n, maxConflictAttempts, out.String())
	}
}
