package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage"
)

// testPassphrase is what the fake prompter answers every passphrase
// request with, so a CLI test's vault is genuinely unlockable afterwards
// without each test restating it.
const testPassphrase = "correct horse battery staple"

// fakePrompter answers the library's requests without a terminal, and
// records them so a test can assert on the exchange rather than only on
// its result. This is the harness the M0 plan described as growing "a
// Prompter alongside the first command that needs one" — `gage init`,
// which generates this device's identity, is that command.
type fakePrompter struct {
	passphrases []string
	calls       int

	requests []gage.UnlockRequest
	warnings []string

	// values is answered in order, one per Value call, standing in for a
	// human typing gage insert's value at a masked prompt. valuePrompts
	// records what each call was asked, so a test can assert insert
	// actually went through the Prompter rather than reading stdin.
	values       []string
	valueCalls   int
	valuePrompts []string
}

func (f *fakePrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	f.requests = append(f.requests, req)
	f.calls++
	i := f.calls - 1
	if i >= len(f.passphrases) {
		i = len(f.passphrases) - 1
	}
	return gage.UnlockResponse{Kind: gage.KindPassphrase, Passphrase: f.passphrases[i]}, nil
}

func (f *fakePrompter) Confirm(prompt string) (bool, error)            { return true, nil }
func (f *fakePrompter) Choose(list gage.CandidateList) (string, error) { return "", nil }
func (f *fakePrompter) Warn(msg string)                                { f.warnings = append(f.warnings, msg) }

func (f *fakePrompter) Value(prompt string) (string, error) {
	f.valuePrompts = append(f.valuePrompts, prompt)
	f.valueCalls++
	i := f.valueCalls - 1
	if i >= len(f.values) {
		i = len(f.values) - 1
	}
	if i < 0 {
		return "", nil
	}
	return f.values[i], nil
}

// cliResult is one in-process invocation of the CLI via runApp — the
// "rootCmd.Execute() in-process with injected IO and a fake Prompter"
// harness the plan calls for.
type cliResult struct {
	Stdout string
	Stderr string
	Code   int
}

// runCLI runs the CLI in-process against args, with stdin a fixed string
// (non-TTY) and a fake Prompter answering with testPassphrase.
func runCLI(t *testing.T, args []string, stdin string) cliResult {
	t.Helper()
	res, _ := runCLIWithPrompter(t, args, stdin, false, &fakePrompter{passphrases: []string{testPassphrase}})
	return res
}

func runCLIWithTerminal(t *testing.T, args []string, stdin string, isTerminal bool) cliResult {
	t.Helper()
	res, _ := runCLIWithPrompter(t, args, stdin, isTerminal, &fakePrompter{passphrases: []string{testPassphrase}})
	return res
}

// runCLIWithPrompter is the full form: it returns the prompter too, so a
// test can assert what the command asked a human for.
func runCLIWithPrompter(t *testing.T, args []string, stdin string, isTerminal bool, p gage.Prompter) (cliResult, gage.Prompter) {
	t.Helper()
	return runCLIWithPrompterAndStdin(t, args, strings.NewReader(stdin), isTerminal, p)
}

// runCLIWithPrompterAndStdin is runCLIWithPrompter with stdin supplied as
// an arbitrary reader rather than a string, so a test can hand the CLI a
// reader that records — or refuses — being read from.
func runCLIWithPrompterAndStdin(t *testing.T, args []string, stdin io.Reader, isTerminal bool, p gage.Prompter) (cliResult, gage.Prompter) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := &App{
		Out:        &stdout,
		Err:        &stderr,
		In:         stdin,
		Build:      BuildInfo{Version: "v1.2.3", Commit: "abcdef1"},
		IsTerminal: func() bool { return isTerminal },
		Prompter:   p,
	}
	code := runApp(app, args)
	return cliResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}, p
}
