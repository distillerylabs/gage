// Package exitcode defines gage's process exit-code taxonomy and a way for
// library errors to carry one. cmd/gage is the only thing that ever turns a
// Code into an actual os.Exit call; internal/gage never calls os.Exit itself.
package exitcode

import "fmt"

// Code is one of gage's well-known process exit codes. Every command's exit
// status must be one of these — never a bare, unenumerated integer.
type Code int

const (
	// Success means the command did what it was asked.
	Success Code = 0
	// Conflict means an operation found a state a human must resolve:
	// a sync divergence, a recipient-file mismatch (gage recipient
	// verify), a dirty entries/ working tree, etc.
	Conflict Code = 1
	// Usage means the command line itself was invalid: bad flags, an
	// unknown (sub)command, a mutually-exclusive flag combination.
	Usage Code = 2
	// NotFound means a query resolved to no entry.
	NotFound Code = 3
	// Ambiguous means a query resolved to more than one candidate and
	// there was nobody to ask (one-shot mode).
	Ambiguous Code = 4
	// LockedOrAuth means an unlock failed: wrong passphrase, no local
	// identity registered for this device, an expired auth token, etc.
	LockedOrAuth Code = 5
	// Internal means an unexpected error that doesn't fit any of the
	// above categories.
	Internal Code = 6
	// Unreachable means a remote could not be reached at all: DNS
	// failure, connection refused, a timeout. It is deliberately not
	// Conflict: nothing about either side's state is wrong and there is
	// nothing for a human to resolve, so the same command is worth
	// retrying unchanged once the network is back. A script can act on
	// that difference; it could not when the two shared a code.
	//
	// gage's automatic sync paths never surface this — being offline
	// warns once and the triggering operation still succeeds on its own
	// terms (see "Sync model"). Only an explicitly invoked sync verb
	// reaches it, where the human asked to talk to the remote and
	// deserves to know it didn't happen.
	Unreachable Code = 7
)

// String renders the code the way error messages and tests refer to it.
func (c Code) String() string {
	switch c {
	case Success:
		return "success"
	case Conflict:
		return "conflict"
	case Usage:
		return "usage"
	case NotFound:
		return "not-found"
	case Ambiguous:
		return "ambiguous"
	case LockedOrAuth:
		return "locked-or-auth"
	case Internal:
		return "internal"
	case Unreachable:
		return "unreachable"
	default:
		return fmt.Sprintf("code(%d)", int(c))
	}
}

// All lists every defined code, in the order commands most often reach them.
// Tests use this to assert every code is exercised by at least one path.
func All() []Code {
	return []Code{Success, Conflict, Usage, NotFound, Ambiguous, LockedOrAuth, Internal, Unreachable}
}

// codedError pairs an error with the exit code cmd/gage should render it as.
type codedError struct {
	code Code
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

// Code reports the exit code this error was constructed with, satisfying
// the interface CodeOf looks for.
func (e *codedError) Code() Code { return e.code }

// coded is the interface a wrapped error implements to carry an explicit
// exit code up through cmd/gage. Library code never needs to know about
// this type directly — New and Wrap construct it.
type coded interface {
	Code() Code
}

// New builds an error carrying the given exit code.
func New(code Code, msg string) error {
	return &codedError{code: code, err: fmt.Errorf("%s", msg)}
}

// Newf builds an error carrying the given exit code, with fmt.Errorf-style
// formatting.
func Newf(code Code, format string, args ...any) error {
	return &codedError{code: code, err: fmt.Errorf(format, args...)}
}

// Wrap attaches an exit code to an existing error.
func Wrap(code Code, err error) error {
	if err == nil {
		return nil
	}
	return &codedError{code: code, err: err}
}

// CodeOf walks err's chain for an exit code. A nil error maps to Success;
// any error that never opted into a specific code maps to Internal — this
// is what keeps a command from ever surfacing a bare, unenumerated exit
// status by accident.
func CodeOf(err error) Code {
	if err == nil {
		return Success
	}
	for e := err; e != nil; e = unwrap(e) {
		if c, ok := e.(coded); ok {
			return c.Code()
		}
	}
	return Internal
}

// IsCoded reports whether err's chain contains an explicit code from New
// or Wrap. cmd/gage uses this to tell "this error opted into a specific
// exit code" apart from "this fell back to Internal because it never
// opted into one at all" — the latter case covers things like Cobra's own
// usage/parsing rejections, which cmd/gage maps to Usage instead.
func IsCoded(err error) bool {
	for e := err; e != nil; e = unwrap(e) {
		if _, ok := e.(coded); ok {
			return true
		}
	}
	return false
}

func unwrap(err error) error {
	u, ok := err.(interface{ Unwrap() error })
	if !ok {
		return nil
	}
	return u.Unwrap()
}
