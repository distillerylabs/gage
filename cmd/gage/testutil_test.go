package main

import (
	"bytes"
	"strings"
	"testing"
)

// cliResult is one in-process invocation of the CLI via Run — the
// "rootCmd.Execute() in-process with injected IO and a fake Prompter"
// harness the plan calls for. No subcommand in M0 needs a Prompter yet,
// so this harness doesn't wire one; it will grow one alongside the first
// command that does.
type cliResult struct {
	Stdout string
	Stderr string
	Code   int
}

// runCLI runs the CLI in-process against args, with stdin either a fixed
// string (non-TTY) or, via runCLITTY, a forced-true IsTerminal.
func runCLI(t *testing.T, args []string, stdin string) cliResult {
	t.Helper()
	return runCLIWithTerminal(t, args, stdin, false)
}

func runCLIWithTerminal(t *testing.T, args []string, stdin string, isTerminal bool) cliResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &stdout, &stderr, func() bool { return isTerminal }, BuildInfo{Version: "v1.2.3", Commit: "abcdef1"})
	return cliResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}
