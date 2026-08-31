package exitcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestNilErrorIsSuccess(t *testing.T) {
	if got := CodeOf(nil); got != Success {
		t.Errorf("CodeOf(nil) = %v, want Success", got)
	}
}

func TestBareErrorIsInternal(t *testing.T) {
	if got := CodeOf(errors.New("boom")); got != Internal {
		t.Errorf("CodeOf(bare error) = %v, want Internal — no command may surface an unenumerated exit status", got)
	}
}

// TestEveryDefinedCodeRoundTripsThroughConstructors exercises
// New/Wrap/CodeOf for every entry in the taxonomy.
//
// Note carefully what this does *not* establish. The M0 checklist asks
// that "every defined exit code is produced by at least one code path,"
// and this test does not show that: it hands each code to New and checks
// CodeOf hands the same one back, which is a round-trip of the
// constructor, not evidence that any command ever reaches NotFound,
// Ambiguous, LockedOrAuth, or Conflict. It cannot be otherwise in M0,
// because no command that could produce those exists yet — the
// commands that will are M2's, M4's, M5's, and M9's. Naming this test
// after the checklist bullet would have made a milestone with four
// unreachable codes look fully covered.
//
// What is genuinely verifiable now lives in cmd/gage's
// TestNoBareUnenumeratedExitCode: every code the CLI can currently emit
// is in the taxonomy. The converse — every code in the taxonomy is
// emitted by something — becomes checkable as those milestones land, and
// is tracked as such rather than claimed here.
func TestEveryDefinedCodeRoundTripsThroughConstructors(t *testing.T) {
	for _, code := range All() {
		code := code
		t.Run(code.String(), func(t *testing.T) {
			err := New(code, "example failure")
			if got := CodeOf(err); got != code {
				t.Errorf("CodeOf(New(%v, ...)) = %v, want %v", code, got, code)
			}

			wrapped := Wrap(code, errors.New("underlying"))
			if got := CodeOf(wrapped); got != code {
				t.Errorf("CodeOf(Wrap(%v, ...)) = %v, want %v", code, got, code)
			}

			// Wrapping again with a plain fmt.Errorf %w must not lose
			// the code — CodeOf walks the chain.
			outer := fmt.Errorf("context: %w", err)
			if got := CodeOf(outer); got != code {
				t.Errorf("CodeOf(fmt.Errorf(%%w, New(%v, ...))) = %v, want %v", code, got, code)
			}
		})
	}
}

func TestSuccessIsZero(t *testing.T) {
	if Success != 0 {
		t.Errorf("Success = %d, want 0", Success)
	}
}

func TestAllCodesAreDistinct(t *testing.T) {
	seen := map[Code]bool{}
	for _, code := range All() {
		if seen[code] {
			t.Errorf("duplicate code %v in All()", code)
		}
		seen[code] = true
	}
}

func TestWrapNilIsNil(t *testing.T) {
	if err := Wrap(Internal, nil); err != nil {
		t.Errorf("Wrap(_, nil) = %v, want nil", err)
	}
}
