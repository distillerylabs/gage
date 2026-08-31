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

// UnlockRequest is what Vault.Unlock hands a Prompter when it needs a
// human (or a GUI, or a script) to prove this device can decrypt a vault.
type UnlockRequest struct {
	Kind   UnlockKind
	Vault  string
	Device string
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

	// Confirm asks a yes/no question — e.g. the recipient-change warning
	// in "Local trust cache". A false answer means the caller should
	// abort whatever it was about to do.
	Confirm(prompt string) (bool, error)

	// Choose asks which of a CandidateList's entries the caller meant —
	// the session-mode side of an ambiguous query (see "Addressing
	// entries"). Returns the chosen candidate's ID.
	Choose(list CandidateList) (string, error)
}
