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

// TestEveryDefinedCodeIsProducedBySomeCodePath exercises New/Wrap/CodeOf
// for every entry in the taxonomy, proving the general
// construct-then-render mechanism cmd/gage relies on actually reaches
// every defined code. Concrete commands that produce NotFound/Ambiguous/
// LockedOrAuth/Conflict in practice arrive in later milestones (M2, M4,
// M5, M9 — see the plan's cross-milestone contracts); this test is what
// keeps the taxonomy itself honest in the meantime.
func TestEveryDefinedCodeIsProducedBySomeCodePath(t *testing.T) {
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
