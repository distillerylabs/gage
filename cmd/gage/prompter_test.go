package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
)

func unlockReq(attempt int) gage.UnlockRequest {
	return gage.UnlockRequest{
		Kind:    gage.KindPassphrase,
		Purpose: gage.PurposeUnlock,
		Vault:   "personal",
		Device:  "laptop-1",
		Attempt: attempt,
	}
}

// TestRetryPolicyLivesInThePrompter is the M2 decision made observable:
// the library keeps re-asking for as long as its Prompter answers, so
// "three attempts" is expressed by this prompter refusing the fourth
// request rather than by a count inside internal/gage.
func TestRetryPolicyLivesInThePrompter(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("a\nb\nc\nd\n"), &out)

	for attempt := 1; attempt <= maxPassphraseAttempts; attempt++ {
		resp, err := p.Unlock(unlockReq(attempt))
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if resp.Passphrase == "" {
			t.Fatalf("attempt %d returned an empty passphrase", attempt)
		}
	}

	_, err := p.Unlock(unlockReq(maxPassphraseAttempts + 1))
	if err == nil {
		t.Fatalf("attempt %d was answered; the policy allows %d", maxPassphraseAttempts+1, maxPassphraseAttempts)
	}
	if !errors.Is(err, errTooManyAttempts) {
		t.Errorf("error = %v, want it to wrap errTooManyAttempts", err)
	}
	if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
}

// TestRetryPromptTellsTheUserWhichAttemptThisIs: the Attempt field is
// what lets a prompter say "wrong passphrase, try again" without the
// library owning any of that policy.
func TestRetryPromptTellsTheUserWhichAttemptThisIs(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("first\nsecond\n"), &out)

	if _, err := p.Unlock(unlockReq(1)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "wrong passphrase") {
		t.Errorf("the first attempt was announced as a retry:\n%s", out.String())
	}

	out.Reset()
	if _, err := p.Unlock(unlockReq(2)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wrong passphrase") {
		t.Errorf("a retry doesn't tell the user the last attempt was wrong:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "2 of 3") {
		t.Errorf("a retry doesn't say how many attempts are left:\n%s", out.String())
	}
}

// TestNewPassphraseIsConfirmed: there is nothing to check a brand-new
// passphrase against, so a typo would only surface at the next unlock —
// by which point the key it protects is unrecoverable.
func TestNewPassphraseIsConfirmed(t *testing.T) {
	createReq := gage.UnlockRequest{
		Kind:    gage.KindPassphrase,
		Purpose: gage.PurposeCreate,
		Vault:   "personal",
		Device:  "laptop-1",
		Attempt: 1,
	}

	t.Run("matching entries are accepted", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("s3cret\ns3cret\n"), &out)
		resp, err := p.Unlock(createReq)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Passphrase != "s3cret" {
			t.Errorf("passphrase = %q, want %q", resp.Passphrase, "s3cret")
		}
		if !strings.Contains(out.String(), "Confirm passphrase") {
			t.Errorf("the passphrase was never confirmed:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "cannot be recovered") {
			t.Errorf("the user isn't told the passphrase is unrecoverable:\n%s", out.String())
		}
	})

	t.Run("a mismatch is re-asked, not accepted", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("typo\ntypa\ns3cret\ns3cret\n"), &out)
		resp, err := p.Unlock(createReq)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Passphrase != "s3cret" {
			t.Errorf("passphrase = %q, want the confirmed one (%q)", resp.Passphrase, "s3cret")
		}
		if !strings.Contains(out.String(), "did not match") {
			t.Errorf("the mismatch wasn't reported:\n%s", out.String())
		}
	})

	t.Run("repeated mismatches give up rather than looping", func(t *testing.T) {
		var out bytes.Buffer
		p := newTerminalPrompter(strings.NewReader("a\nb\nc\nd\ne\nf\n"), &out)
		if _, err := p.Unlock(createReq); err == nil {
			t.Fatal("expected repeated mismatches to fail")
		} else if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
			t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
		}
	})
}

// TestPromptsGoToTheErrorStreamNotStdout: a `gage show foo > secret.txt`
// must put only the secret in the file, while the human still sees the
// prompt they're answering.
func TestPromptsGoToTheErrorStreamNotStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &App{Out: &stdout, Err: &stderr, In: strings.NewReader("")}
	app.Prompter = newTerminalPrompter(strings.NewReader("s3cret\ns3cret\n"), app.Err)

	if _, err := app.Prompter.Unlock(gage.UnlockRequest{
		Kind: gage.KindPassphrase, Purpose: gage.PurposeCreate, Vault: "personal", Device: "laptop-1", Attempt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Errorf("a prompt reached stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Choose a passphrase") {
		t.Errorf("the prompt didn't reach stderr: %q", stderr.String())
	}
}

func TestWarnIsSurfacedAndReturnsNothing(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader(""), &out)
	p.Warn("gage: could not lock key material into memory")
	if !strings.Contains(out.String(), "could not lock key material") {
		t.Errorf("the warning wasn't surfaced: %q", out.String())
	}
}

func TestConfirmDefaultsToNo(t *testing.T) {
	for input, want := range map[string]bool{
		"y\n":       true,
		"Y\n":       true,
		"yes\n":     true,
		"n\n":       false,
		"\n":        false,
		"garbage\n": false,
	} {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			var out bytes.Buffer
			p := newTerminalPrompter(strings.NewReader(input), &out)
			got, err := p.Confirm("do the thing?")
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("Confirm(%q) = %v, want %v", input, got, want)
			}
		})
	}
}

// TestPrompterRefusesAMethodItCannotSatisfy: a second UnlockKind is
// additive to the interface, so this build has to say plainly that it
// can't satisfy one rather than returning an empty response that would
// read as an empty passphrase.
func TestPrompterRefusesAMethodItCannotSatisfy(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("whatever\n"), &out)
	resp, err := p.Unlock(gage.UnlockRequest{Kind: "yubikey", Vault: "personal", Device: "laptop-1", Attempt: 1})
	if err == nil {
		t.Fatal("expected an unsupported unlock kind to be refused")
	}
	if resp.Passphrase != "" {
		t.Errorf("a refused request still produced a passphrase: %q", resp.Passphrase)
	}
	if !strings.Contains(err.Error(), "yubikey") {
		t.Errorf("error %v doesn't name the kind it can't satisfy", err)
	}
}

// TestReadingBufferedInputDoesNotSwallowFollowingLines: the prompter
// buffers stdin across calls, which it must, or a --stdin script would
// lose whatever followed the passphrase it just read.
func TestReadingBufferedInputDoesNotSwallowFollowingLines(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader("first\nsecond\n"), &out)

	one, err := p.Unlock(unlockReq(1))
	if err != nil {
		t.Fatal(err)
	}
	two, err := p.Unlock(unlockReq(2))
	if err != nil {
		t.Fatal(err)
	}
	if one.Passphrase != "first" || two.Passphrase != "second" {
		t.Errorf("read %q then %q, want \"first\" then \"second\"", one.Passphrase, two.Passphrase)
	}
}

func TestReadingPastEndOfInputFails(t *testing.T) {
	var out bytes.Buffer
	p := newTerminalPrompter(strings.NewReader(""), &out)
	if _, err := p.Unlock(unlockReq(1)); err == nil {
		t.Fatal("expected reading a passphrase from empty input to fail")
	} else if exitcode.CodeOf(err) != exitcode.LockedOrAuth {
		t.Errorf("CodeOf(err) = %v, want LockedOrAuth", exitcode.CodeOf(err))
	}
}
