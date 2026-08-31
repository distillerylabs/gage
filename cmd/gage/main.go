// Command gage is the CLI frontend over internal/gage: a thin Cobra
// layer that parses flags, calls into the library, and renders the
// result as terminal output, prompts, or an exit code. See the design
// doc's "Library architecture".
package main

import (
	"os"

	"golang.org/x/term"
)

func main() {
	build := BuildInfo{Version: version, Commit: commit}
	isTerminal := func() bool {
		return term.IsTerminal(int(os.Stdin.Fd()))
	}
	os.Exit(Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, isTerminal, build))
}
