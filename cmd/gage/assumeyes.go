package main

import (
	"github.com/distillerylabs/gage/internal/gage"
)

// assumeYesPrompter answers M10's recipient-change question with yes,
// without rendering it, and delegates everything else to the prompter it
// wraps. It is what `--yes` is: not a library concept at all, but this
// frontend deciding that in a scripted run there is nobody to show a
// diff to and the answer is already known.
//
// It answers *only* ConfirmRecipientChange. Confirm stays untouched, so
// M9's "remove this device's own key?" — a question about locking a
// device out, not about reviewing a list — is still a real question in a
// script, and still defaults to no.
//
// What it deliberately cannot do is change what confirming *means*. The
// two-outcome rule (a routine change regenerates the trust cache, a
// mismatched one records only that it was seen) is decided library-side
// off VerifyRecipients, so "--yes never launders a mismatch into an
// approval" falls out of where that decision lives rather than out of a
// special case here.
type assumeYesPrompter struct{ gage.Prompter }

// ConfirmRecipientChange is the whole of --yes.
func (p assumeYesPrompter) ConfirmRecipientChange(w gage.RecipientChangeWarning) (bool, error) {
	return true, nil
}

// inner exposes the wrapped prompter, so the few places that need the
// concrete terminalPrompter — the session installing its line reader —
// can still find it through the decorator. See terminalPrompterOf.
func (p assumeYesPrompter) inner() gage.Prompter { return p.Prompter }

// assumeYesConflictPrompter is assumeYesPrompter for a frontend that can
// also resolve entry conflicts.
//
// It exists because "can this frontend answer [l/r/b/s/q]?" is decided
// structurally, by whether the Prompter also implements ConflictPrompter
// (see resolveConflicts). A decorator that always implemented it would
// make every non-interactive frontend look interactive the moment --yes
// was passed; one that never did would break `gage sync --yes` on a real
// terminal. So the wrapper's shape follows the wrapped prompter's.
type assumeYesConflictPrompter struct {
	assumeYesPrompter
	asker gage.ConflictPrompter
}

func (p assumeYesConflictPrompter) ResolveConflict(c gage.EntryConflict) (gage.Resolution, error) {
	return p.asker.ResolveConflict(c)
}

// withAssumeYes wraps p so that ConfirmRecipientChange is answered
// without asking, preserving whether p can resolve conflicts.
func withAssumeYes(p gage.Prompter) gage.Prompter {
	wrapped := assumeYesPrompter{Prompter: p}
	if asker, ok := p.(gage.ConflictPrompter); ok {
		return assumeYesConflictPrompter{assumeYesPrompter: wrapped, asker: asker}
	}
	return wrapped
}

// promptDecorator is anything wrapping another Prompter — today only
// --yes. It exists so that unwrapping is a property of the decorator
// rather than a list of concrete types every caller has to know.
type promptDecorator interface{ inner() gage.Prompter }

// terminalPrompterOf finds the real terminalPrompter behind whatever
// decorators are in front of it, so `gage --yes` in session mode still
// hands its reads to the session's line editor.
func terminalPrompterOf(p gage.Prompter) (*terminalPrompter, bool) {
	for {
		switch v := p.(type) {
		case *terminalPrompter:
			return v, true
		case promptDecorator:
			p = v.inner()
		default:
			return nil, false
		}
	}
}

// scopeAssumeYes bounds a --yes wrap to one command execution, returning
// the function that takes it back off.
//
// NewRootCmd installs the wrap from PersistentPreRunE, which is the
// earliest point the flag has been parsed; this is what keeps that
// installation from outliving the command it was typed on. One App can
// run more than one command tree — that is what a session is — so an
// App whose Prompter stayed wrapped would be answering questions nobody
// passed --yes for.
func scopeAssumeYes(app *App) func() {
	prev := app.Prompter
	return func() { app.Prompter = prev }
}
