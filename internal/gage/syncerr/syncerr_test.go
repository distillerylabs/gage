package syncerr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

func TestClassifyPushSortsTheThreeBuckets(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
		code exitcode.Code
	}{
		{
			name: "connection refused is unreachable",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")},
			want: ErrUnreachable,
			code: exitcode.Unreachable,
		},
		{
			name: "DNS failure is unreachable",
			err:  &net.DNSError{Err: "no such host", Name: "github.com"},
			want: ErrUnreachable,
			code: exitcode.Unreachable,
		},
		{
			name: "a timeout is unreachable",
			err:  fmt.Errorf("fetching: %w", context.DeadlineExceeded),
			want: ErrUnreachable,
			code: exitcode.Unreachable,
		},
		{
			// A cancelled context is a network operation that did not
			// happen, not a statement about either side's history —
			// without this it would fall to the divergence bucket by
			// elimination and report "origin has diverged" for a ctrl-C.
			name: "a cancelled context is unreachable",
			err:  fmt.Errorf("pushing: %w", context.Canceled),
			want: ErrUnreachable,
			code: exitcode.Unreachable,
		},
		{
			name: "authentication required is auth",
			err:  fmt.Errorf("wrapped: %w", transport.ErrAuthenticationRequired),
			want: ErrAuth,
			code: exitcode.LockedOrAuth,
		},
		{
			name: "authorization failed is auth",
			err:  fmt.Errorf("wrapped: %w", transport.ErrAuthorizationFailed),
			want: ErrAuth,
			code: exitcode.LockedOrAuth,
		},
		{
			// Most hosts answer "not found" for a private repository the
			// caller can't see, so it is folded in with auth rather than
			// treated as a vanished repository.
			name: "repository not found is auth",
			err:  fmt.Errorf("wrapped: %w", transport.ErrRepositoryNotFound),
			want: ErrAuth,
			code: exitcode.LockedOrAuth,
		},
		{
			name: "anything else is divergence",
			err:  errors.New("non-fast-forward update: refs/heads/master"),
			want: ErrDiverged,
			code: exitcode.Conflict,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPush(tc.err)
			if !errors.Is(got, tc.want) {
				t.Errorf("ClassifyPush(%v) = %v, want it to match %v", tc.err, got, tc.want)
			}
			if code := exitcode.CodeOf(got); code != tc.code {
				t.Errorf("exit code = %v, want %v", code, tc.code)
			}
		})
	}
}

// TestClassifyPushCatchesGoGitsRealRejection is the regression test for
// the reason this package classifies by elimination at all.
//
// go-git's Remote.Push rejects a non-fast-forward with a bare
// fmt.Errorf that does not wrap the exported git.ErrNonFastForwardUpdate
// sentinel — so the "obvious" implementation, errors.Is against that
// sentinel, silently never fires on a real push. This test pins both
// halves: that the sentinel genuinely doesn't match, and that gage
// classifies the error correctly anyway.
//
// If a future go-git starts wrapping the sentinel, the first assertion
// fails and points at open-questions.md, where the simplification this
// would allow is recorded.
func TestClassifyPushCatchesGoGitsRealRejection(t *testing.T) {
	// Verbatim the error go-git's checkFastForwardUpdate produces.
	rejection := fmt.Errorf("non-fast-forward update: %s", "refs/heads/master")

	if errors.Is(rejection, git.ErrNonFastForwardUpdate) {
		t.Error("go-git's push rejection now wraps ErrNonFastForwardUpdate; " +
			"the elimination-based classification in ClassifyPush can be simplified — " +
			"see open-questions.md")
	}
	if !errors.Is(ClassifyPush(rejection), ErrDiverged) {
		t.Error("go-git's real push rejection did not classify as divergence")
	}
}

// TestClassifyFetchNeverInventsADivergence is the asymmetry between the
// two classifiers: a fetch only ever adds objects, so it cannot be
// rejected for divergence, and an unrecognized failure must not be
// relabelled as one.
func TestClassifyFetchNeverInventsADivergence(t *testing.T) {
	broken := errors.New("object not found")

	got := ClassifyFetch(broken)
	if errors.Is(got, ErrDiverged) {
		t.Errorf("ClassifyFetch(%v) = %v, want it left alone rather than called a divergence", broken, got)
	}
	if !errors.Is(got, broken) {
		t.Errorf("ClassifyFetch dropped the original error: %v", got)
	}

	// The two buckets a fetch can legitimately land in still work.
	if !errors.Is(ClassifyFetch(&net.DNSError{Err: "no such host"}), ErrUnreachable) {
		t.Error("a fetch against an unresolvable host did not classify as unreachable")
	}
	if !errors.Is(ClassifyFetch(fmt.Errorf("x: %w", transport.ErrAuthenticationRequired)), ErrAuth) {
		t.Error("a fetch refused for credentials did not classify as auth")
	}
}

func TestClassifyPassesNilThrough(t *testing.T) {
	if err := ClassifyPush(nil); err != nil {
		t.Errorf("ClassifyPush(nil) = %v, want nil", err)
	}
	if err := ClassifyFetch(nil); err != nil {
		t.Errorf("ClassifyFetch(nil) = %v, want nil", err)
	}
}

// TestOfflineAndDivergedDoNotShareAnExitCode is the whole reason
// Unreachable is its own code: the two situations need different things
// from whoever is watching. "Retry when the network is back" and "a human
// has to reconcile two histories" being indistinguishable to a script
// would have undone the classification everywhere except the message
// text.
func TestOfflineAndDivergedDoNotShareAnExitCode(t *testing.T) {
	offline := ClassifyPush(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")})
	diverged := ClassifyPush(errors.New("non-fast-forward update: refs/heads/master"))

	offlineCode := exitcode.CodeOf(offline)
	divergedCode := exitcode.CodeOf(diverged)
	if offlineCode == divergedCode {
		t.Fatalf("offline and diverged both exit %v; a caller cannot tell them apart", offlineCode)
	}
	if offlineCode != exitcode.Unreachable {
		t.Errorf("offline exit code = %v, want unreachable", offlineCode)
	}
	if divergedCode != exitcode.Conflict {
		t.Errorf("diverged exit code = %v, want conflict", divergedCode)
	}
}

// TestClassifyIsIdempotentOnAnAlreadyClassifiedUnreachable is
// isUnreachable's first branch, distinct from every case above: an error
// that already wraps ErrUnreachable, rather than a raw network error
// isUnreachable's type-shape checks would recognize on their own.
// Classifying it again has to stay Unreachable rather than falling
// through to the divergence bucket by elimination — the same concern
// TestClassifyPushCatchesGoGitsRealRejection's doc comment states for
// the divergence side.
func TestClassifyIsIdempotentOnAnAlreadyClassifiedUnreachable(t *testing.T) {
	already := fmt.Errorf("gage: retrying: %w", ErrUnreachable)

	for name, got := range map[string]error{
		"push":  ClassifyPush(already),
		"fetch": ClassifyFetch(already),
	} {
		if !errors.Is(got, ErrUnreachable) {
			t.Errorf("Classify%s(already-unreachable) = %v, want it to still match ErrUnreachable", name, got)
		}
		if code := exitcode.CodeOf(got); code != exitcode.Unreachable {
			t.Errorf("Classify%s(already-unreachable) exit code = %v, want Unreachable", name, code)
		}
	}
}
