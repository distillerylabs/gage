package main

import (
	"testing"

	"github.com/denmark/gage/internal/gage"
)

// recordingDefaultYes is a bare Prompter that records every
// ConfirmDefaultYes it is handed and answers with a scripted value. It is
// what the wrapper prompters are wrapped *around* below, so a test can
// tell "the wrapper delegated" apart from "the wrapper answered".
type recordingDefaultYes struct {
	gage.Prompter
	answer  bool
	prompts []string
}

func (p *recordingDefaultYes) ConfirmDefaultYes(prompt string) (bool, error) {
	p.prompts = append(p.prompts, prompt)
	return p.answer, nil
}

// TestNoNonInteractivePrompterAnswersConfirmDefaultYes covers the
// wrappers directly rather than only through `clone`, because every one
// of them *embeds* gage.Prompter and so gains the new method by
// promotion the moment it is added to the interface — with no compile
// error to prompt anyone to think about it.
//
// The failure being ruled out is a wrapper that returns true because the
// question's default is yes, which would silently turn "a script is
// never asked" into "a script always agrees" — the one outcome "Why
// there is no --enroll flag" spends a section ruling out. Delegation is
// the rule: a default-yes question is never answered yes without a
// human, and the default belongs to the *rendering* of the question,
// not to the absence of someone to answer it.
func TestNoNonInteractivePrompterAnswersConfirmDefaultYes(t *testing.T) {
	// --yes answers ConfirmRecipientChange and nothing else, so it must
	// not reach a question it was never meant to answer.
	t.Run("assumeYesPrompter", func(t *testing.T) {
		inner := &recordingDefaultYes{answer: false}
		assertDelegatesDefaultYes(t, withAssumeYes(inner), inner)
	})
	t.Run("refusingPrompter", func(t *testing.T) {
		inner := &recordingDefaultYes{answer: false}
		assertDelegatesDefaultYes(t, refusingPrompter{Prompter: inner, create: inner}, inner)
	})
	t.Run("noCreatePrompter", func(t *testing.T) {
		inner := &recordingDefaultYes{answer: false}
		assertDelegatesDefaultYes(t, noCreatePrompter{Prompter: inner}, inner)
	})
	t.Run("envPrompter", func(t *testing.T) {
		inner := &recordingDefaultYes{answer: false}
		assertDelegatesDefaultYes(t, &envPrompter{Prompter: inner, create: inner, passphrase: "x"}, inner)
	})
}

// assertDelegatesDefaultYes checks that wrapper neither answers a
// default-yes question itself nor swallows it: the inner prompter is
// asked, and the answer that comes back is the one the inner gave.
func assertDelegatesDefaultYes(t *testing.T, wrapper gage.Prompter, inner *recordingDefaultYes) {
	t.Helper()
	got, err := wrapper.ConfirmDefaultYes("set up an enrollment request now?")
	if err != nil {
		t.Fatalf("ConfirmDefaultYes: %v", err)
	}
	if got {
		t.Errorf("%T answered a default-yes question with yes; it must delegate", wrapper)
	}
	if len(inner.prompts) != 1 {
		t.Errorf("%T asked the wrapped prompter %d times, want 1", wrapper, len(inner.prompts))
	}
}
