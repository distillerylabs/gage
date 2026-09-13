package gage

// UnlockKind names the identity method an UnlockRequest is asking about.
// This exists so Prompter.Unlock is one generic, method-agnostic call
// rather than a passphrase-specific method — a second Kind (e.g.
// "yubikey") is additive to the interface, not a breaking change. Only
// KindPassphrase exists today; see "Decryption methods are per-device" in
// the design doc.
type UnlockKind string

const (
	KindPassphrase UnlockKind = "passphrase"
)

// UnlockPurpose distinguishes the two directions the same method-agnostic
// exchange runs in: proving possession of an identity that already exists,
// and choosing the secret that will protect a new one. They need the same
// answer type but not the same presentation — a CLI asks the second one
// twice and checks the two match, a GUI shows a strength meter — so the
// distinction is data the Prompter can render on, not two interface
// methods.
type UnlockPurpose string

const (
	// PurposeUnlock is the default: open an identity that already exists.
	PurposeUnlock UnlockPurpose = "unlock"
	// PurposeCreate asks for the secret a brand-new identity will be
	// protected with. There is nothing to check the answer against, so a
	// typo here is unrecoverable — which is exactly why it's marked as a
	// distinct purpose rather than left indistinguishable from an unlock.
	PurposeCreate UnlockPurpose = "create"
)

// UnlockRequest is what Vault.Unlock hands a Prompter when it needs a
// human (or a GUI, or a script) to prove this device can decrypt a vault.
type UnlockRequest struct {
	Kind UnlockKind
	// Purpose says whether this request opens an existing identity or
	// establishes a new one. Every request gage builds sets it
	// explicitly; a Prompter that ignores it entirely still behaves
	// correctly for the unlock case, which is what every request before
	// M2 was.
	Purpose UnlockPurpose
	Vault   string
	Device  string
	// Attempt counts retries within a single Unlock call, starting at 1,
	// so a Prompter can render "wrong passphrase, try again" without the
	// library baking in a retry policy of its own.
	Attempt int
}

// UnlockResponse is a Prompter's answer to an UnlockRequest. Which fields
// are meaningful depends on Kind: a "passphrase" response carries
// Passphrase; a future "yubikey" response would carry nothing but the
// touch signal implied by returning at all.
type UnlockResponse struct {
	Kind       UnlockKind
	Passphrase string
}

// CandidateList is what a query resolver returns instead of a single
// entry when more than one title matches — see "Addressing entries" in
// the design doc. Left as a skeleton here; M5 fills in Candidate's shape.
type CandidateList struct {
	Query      string
	Candidates []Candidate
}

// Candidate is one ambiguous-query match, enough for a caller to render a
// numbered picker without decrypting anything further.
type Candidate struct {
	ID    string
	Title string
}

// Prompter is how internal/gage asks for a human decision without ever
// touching a terminal itself: no fmt.Print, no reading stdin. cmd/gage
// implements one against os.Stdin/os.Stdout; library tests implement one
// against an in-memory fake; a future GUI/TUI would implement one against
// dialogs. See "Library architecture" and "Interactive decisions become
// data, not printed text" in the design doc.
type Prompter interface {
	// Unlock asks for whatever proves this device holds the private key
	// a vault's chosen method expects.
	Unlock(req UnlockRequest) (UnlockResponse, error)

	// Value asks for one line of free-form secret text — e.g. gage
	// insert's value when none of -m/--value-stdin/-e is given. It's
	// masked the same way a passphrase is, but distinct from Unlock:
	// nothing here proves possession of a key, so it carries no
	// vault/device/attempt context and has no retry policy — there's
	// nothing "wrong" to retry.
	Value(prompt string) (string, error)

	// Confirm asks a yes/no question — e.g. M9's "remove this device's
	// own key?". A false answer means the caller should abort whatever
	// it was about to do.
	Confirm(prompt string) (bool, error)

	// ConfirmDefaultYes asks a yes/no question whose default is yes —
	// clone's offer to set up an enrollment request, and nothing else.
	// Every other yes/no in gage stays on Confirm.
	//
	// It is a separate method rather than a flag on Confirm because the
	// default is part of how the question is *rendered*: a CLI shows
	// [Y/n] instead of [y/N], a GUI focuses the affirmative button. The
	// library still decides nothing about presentation; it only says
	// which of the two shapes this question has.
	//
	// The default belongs to the rendering, never to the absence of
	// someone to answer. A frontend with nobody to ask must not return
	// true here — see "Why there is no `--enroll` flag": a script that
	// is never asked must never become a script that always agrees.
	ConfirmDefaultYes(prompt string) (bool, error)

	// ConfirmRecipientChange asks whether to encrypt to a recipient list
	// that has changed since this device last confirmed one — the
	// blocking half of "Local trust cache".
	//
	// It is its own method rather than a Confirm with a formatted string
	// for the same reason Choose is: what the frontend needs is the
	// change itself — the diff, whether the two recipient-defining files
	// still agree, which recipients moved — and a library that pre-
	// rendered that into a sentence would have decided how a GUI shows
	// it. It is also what lets cmd/gage's --yes answer this question and
	// only this one, leaving Confirm's genuinely different questions
	// real in a scripted run.
	//
	// A false answer aborts the write with nothing committed and the
	// cache untouched, so the same question is asked again next time.
	ConfirmRecipientChange(w RecipientChangeWarning) (bool, error)

	// Choose asks which of a CandidateList's entries the caller meant —
	// the session-mode side of an ambiguous query (see "Addressing
	// entries"). Returns the chosen candidate's ID.
	Choose(list CandidateList) (string, error)

	// Warn delivers a non-fatal advisory the caller should surface once:
	// today, only that page-locking this process's key material failed
	// and the unlock is proceeding without it (see "Session model").
	//
	// It returns nothing on purpose. Every caller of Warn is on a path
	// that has already decided to continue — a warning that could fail
	// would give the library a way to turn a degraded-but-working unlock
	// into a failed one, which is the opposite of what this path exists
	// for.
	Warn(msg string)
}
