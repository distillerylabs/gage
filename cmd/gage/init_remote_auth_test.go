package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/gittest"
	"github.com/distillerylabs/gage/internal/gage/remoteauth"
)

// TestInitSolicitsATokenWhenNoneIsStored proves `gage init --remote`
// collapses what used to be three commands (init, which failed; auth
// login; push) into one: given an HTTPS-shaped host with no token yet,
// it asks for one on the spot, using the same prompt `gage auth login`
// does, and stores what it's given before ever attempting the push.
func TestInitSolicitsATokenWhenNoneIsStored(t *testing.T) {
	isolateXDG(t)
	recorder := gittest.InstallRecordingTransport(t, errors.New("recorded; not connecting"))

	const host = "git.example.com"
	res, p := runCLIWithPrompter(t, []string{"init", "personal", "--remote", gittest.URL(host, "me/vault.git"), "--no-recovery-key"}, "",
		false, &fakePrompter{passphrases: []string{testPassphrase}, values: []string{"tok_from_prompt"}})

	// The recording transport always fails the connection, so init
	// reports the publish as failed; what matters here is what it did on
	// the way there, not that this particular push succeeded.
	if res.Code == 0 {
		t.Fatalf("init against the recording transport unexpectedly succeeded")
	}

	fp, ok := p.(*fakePrompter)
	if !ok {
		t.Fatalf("prompter = %T, want *fakePrompter", p)
	}
	if len(fp.valuePrompts) != 1 || !strings.Contains(fp.valuePrompts[0], host) {
		t.Fatalf("prompts = %v, want exactly one naming %q", fp.valuePrompts, host)
	}

	stored, err := remoteauth.Load(host)
	if err != nil {
		t.Fatalf("Load after init: %v", err)
	}
	if stored != "tok_from_prompt" {
		t.Errorf("stored token = %q, want the one typed at the prompt", stored)
	}

	auth, ok := recorder.PushAuth()
	if !ok {
		t.Fatal("the push never reached the transport")
	}
	if auth == nil {
		t.Error("push presented no auth after a token was just solicited and stored")
	}
}

// TestInitSkipsTheTokenWhenTheAnswerIsBlank: not every remote needs a
// token (a public repository, one ssh-agent already handles), and init
// has no way to know which case it's in without asking — so leaving the
// prompt blank has to fall back to the pre-existing anonymous attempt
// rather than being treated as an error.
func TestInitSkipsTheTokenWhenTheAnswerIsBlank(t *testing.T) {
	isolateXDG(t)
	recorder := gittest.InstallRecordingTransport(t, errors.New("recorded; not connecting"))

	const host = "git.example.com"
	res, _ := runCLIWithPrompter(t, []string{"init", "personal", "--remote", gittest.URL(host, "me/vault.git"), "--no-recovery-key"}, "",
		false, &fakePrompter{passphrases: []string{testPassphrase}, values: []string{""}})

	if res.Code == 0 {
		t.Fatalf("init against the recording transport unexpectedly succeeded")
	}

	if _, err := remoteauth.Load(host); !errors.Is(err, remoteauth.ErrNoToken) {
		t.Errorf("Load after a blank answer = %v, want ErrNoToken", err)
	}

	auth, ok := recorder.PushAuth()
	if !ok {
		t.Fatal("the push never reached the transport")
	}
	if auth != nil {
		t.Errorf("push presented %v after a blank answer, want an anonymous attempt", auth)
	}
}

// TestInitDoesNotPromptWhenATokenAlreadyExists: a host already configured
// via a prior `gage auth login` (or a prior init) needs no repeat
// question — the whole point is asking exactly once per host.
func TestInitDoesNotPromptWhenATokenAlreadyExists(t *testing.T) {
	isolateXDG(t)
	gittest.InstallRecordingTransport(t, errors.New("recorded; not connecting"))

	const host = "git.example.com"
	if err := remoteauth.Store(host, "tok_existing"); err != nil {
		t.Fatal(err)
	}

	res, p := runCLIWithPrompter(t, []string{"init", "personal", "--remote", gittest.URL(host, "me/vault.git"), "--no-recovery-key"}, "",
		false, &fakePrompter{passphrases: []string{testPassphrase}})

	if res.Code == 0 {
		t.Fatalf("init against the recording transport unexpectedly succeeded")
	}

	fp, ok := p.(*fakePrompter)
	if !ok {
		t.Fatalf("prompter = %T, want *fakePrompter", p)
	}
	if len(fp.valuePrompts) != 0 {
		t.Errorf("prompts = %v, want none: a token for %q was already stored", fp.valuePrompts, host)
	}
}

// TestInitDoesNotPromptForALocalRemote: a local path (what every other
// init-with-remote test in this package uses) has no host to hold a
// token, so it must never be asked about one.
func TestInitDoesNotPromptForALocalRemote(t *testing.T) {
	isolateXDG(t)

	remote := gittest.NewBareRemote(t)
	res, p := runCLIWithPrompter(t, []string{"init", "personal", "--remote", remote, "--no-recovery-key"}, "",
		false, &fakePrompter{passphrases: []string{testPassphrase}})

	if res.Code != 0 {
		t.Fatalf("init exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}

	fp, ok := p.(*fakePrompter)
	if !ok {
		t.Fatalf("prompter = %T, want *fakePrompter", p)
	}
	if len(fp.valuePrompts) != 0 {
		t.Errorf("prompts = %v, want none for a local remote", fp.valuePrompts)
	}
}
