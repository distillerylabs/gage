package gage

import (
	"testing"

	"github.com/denmark/gage/internal/gage/exitcode"
)

// fakePrompter satisfies Prompter for tests without ever touching a
// terminal, proving the interface is usable from the library side.
type fakePrompter struct {
	unlockResp UnlockResponse
	unlockErr  error
}

func (f *fakePrompter) Unlock(req UnlockRequest) (UnlockResponse, error) {
	return f.unlockResp, f.unlockErr
}
func (f *fakePrompter) Confirm(prompt string) (bool, error)       { return true, nil }
func (f *fakePrompter) Choose(list CandidateList) (string, error) { return "", nil }

func TestVaultUnlockSignatureAndNotYetImplemented(t *testing.T) {
	v := &Vault{Name: "personal"}
	p := &fakePrompter{unlockResp: UnlockResponse{Kind: KindPassphrase, Passphrase: "hunter2"}}

	id, err := v.Unlock(p)
	if err == nil {
		t.Fatal("expected Unlock to fail until M2 implements real crypto")
	}
	if exitcode.CodeOf(err) != exitcode.Internal {
		t.Errorf("CodeOf(err) = %v, want Internal", exitcode.CodeOf(err))
	}

	// The Identity value is still usable as a parameter even on the
	// not-implemented path — Close must be safe to call.
	if err := id.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if !id.Closed() {
		t.Error("Closed() = false after Close()")
	}
}

func TestIdentityCloseIsIdempotent(t *testing.T) {
	var id Identity
	if err := id.Close(); err != nil {
		t.Fatal(err)
	}
	if err := id.Close(); err != nil {
		t.Fatal(err)
	}
	if !id.Closed() {
		t.Error("Closed() = false after two Close() calls")
	}
}

func TestNilIdentityCloseDoesNotPanic(t *testing.T) {
	var id *Identity
	if err := id.Close(); err != nil {
		t.Errorf("Close on nil Identity: %v", err)
	}
}
