// Package syncerr classifies the errors a remote fetch or push can fail
// with into the three buckets gage actually behaves differently on:
// unreachable, auth, and diverged. It exists so that neither Vault's sync
// logic nor cmd/gage ever inspects a raw go-git or transport error — they
// match on the sentinels here with errors.Is, and the classification lives
// in exactly one place.
//
// See the M8a plan's "Push failure vs. divergence" for why the split is
// drawn here and what each bucket means for the user.
package syncerr

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/denmark/gage/internal/gage/exitcode"
)

var (
	// ErrUnreachable means the remote could not be reached at all: DNS
	// failure, connection refused, a timeout. It is never the human's
	// fault and never means anything about the state of either side's
	// history, so gage's automatic sync paths warn once and proceed with
	// the local copy rather than failing (see "Sync model" in the design
	// doc). Only an explicitly invoked sync verb surfaces it as an error,
	// which is why it carries an exit code at all — exitcode.Unreachable,
	// deliberately its own code rather than Conflict, so that "retry this
	// later" and "a human has to resolve something" stay distinguishable
	// to a script and not only to a reader of the message.
	ErrUnreachable = errors.New("gage: could not reach the remote")

	// ErrAuth means the remote refused access — bad or expired
	// credentials, or a repository reported as missing, which most hosts
	// return for a private repository the caller can't see and which is
	// therefore folded in here rather than treated as a distinct case.
	//
	// The wording stays broader than "your token was rejected" precisely
	// because of that folding: the same classification covers a local
	// path that isn't there, where there are no credentials in play at
	// all. What to actually do about it is remoteauth.AuthHint's job,
	// which knows whether a host is involved.
	ErrAuth = errors.New("gage: the remote refused access")

	// ErrDiverged means the remote has commits this vault doesn't, so a
	// push can't fast-forward. It says nothing yet about *what* diverged
	// — that classification is Vault's, after it fetches and compares.
	ErrDiverged = errors.New("gage: the remote has diverged from this vault")
)

// ClassifyPush maps a failed push onto one of the three sentinels.
//
// Unreachable and auth are detected positively; everything left over is
// reported as divergence. That inversion is deliberate, and it is not
// merely a convenience:
//
// go-git's Remote.Push rejects a non-fast-forward update with a bare
// fmt.Errorf("non-fast-forward update: %s", ...) — see checkFastForwardUpdate
// in go-git's remote.go — which does *not* wrap the exported
// git.ErrNonFastForwardUpdate sentinel. That sentinel is only ever
// returned by Worktree.Pull's fast-forward-only path. Matching on it here
// with errors.Is would compile, read correctly, and silently never fire,
// turning every real divergence into an unclassified internal error. The
// error's message text is no better a thing to match on: it isn't API.
//
// Classifying by elimination sidesteps both, and it is sound for gage
// specifically because a vault's remote is a dedicated, hook-free,
// LFS-free repository — there is no other reason for a server to reject
// one of these pushes. See open-questions.md ("go-git's Push doesn't wrap
// ErrNonFastForwardUpdate"); if that gap is ever fixed upstream, this can
// become a direct errors.Is check.
func ClassifyPush(err error) error {
	if err == nil {
		return nil
	}
	if classified := classifyTransport(err); classified != nil {
		return classified
	}
	return exitcode.Wrap(exitcode.Conflict, fmt.Errorf("%w: %v", ErrDiverged, err))
}

// ClassifyFetch maps a failed fetch onto the unreachable or auth
// sentinel, and leaves anything else alone.
//
// Unlike a push, a fetch is never rejected for divergence — it only ever
// adds objects and moves remote-tracking refs — so an unrecognized fetch
// failure is a genuinely broken repository or transport, not a
// divergence, and is passed through rather than mislabeled as one.
func ClassifyFetch(err error) error {
	if err == nil {
		return nil
	}
	if classified := classifyTransport(err); classified != nil {
		return classified
	}
	return err
}

// classifyTransport returns the unreachable or auth sentinel for err, or
// nil if err is neither — the half of classification that is identical
// for fetch and push.
func classifyTransport(err error) error {
	if isUnreachable(err) {
		return exitcode.Wrap(exitcode.Unreachable, fmt.Errorf("%w: %v", ErrUnreachable, err))
	}
	if isAuth(err) {
		return exitcode.Wrap(exitcode.LockedOrAuth, fmt.Errorf("%w: %v", ErrAuth, err))
	}
	return nil
}

// isUnreachable reports whether err is a network-level failure. go-git
// has no sentinel for this — the error comes straight from the transport,
// so this matches on the standard library's own network error shapes
// instead.
func isUnreachable(err error) bool {
	if errors.Is(err, ErrUnreachable) {
		return true
	}
	// Both context errors, not just the deadline: gage's own
	// RemoteOpTimeout produces DeadlineExceeded, and a frontend that
	// cancels an in-flight sync produces Canceled. Neither says anything
	// about either side's history, and leaving Canceled out would send it
	// to the divergence bucket by elimination — turning "you pressed
	// ctrl-C" into "origin has diverged".
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// isAuth reports whether err is the remote refusing these credentials.
// go-git's HTTP transport wraps all three of these with %w, so errors.Is
// is reliable here in a way it is not for the non-fast-forward case.
func isAuth(err error) bool {
	return errors.Is(err, ErrAuth) ||
		errors.Is(err, transport.ErrAuthenticationRequired) ||
		errors.Is(err, transport.ErrAuthorizationFailed) ||
		errors.Is(err, transport.ErrRepositoryNotFound)
}
