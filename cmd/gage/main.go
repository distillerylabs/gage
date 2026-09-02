// Command gage is the CLI frontend over internal/gage: a thin Cobra
// layer that parses flags, calls into the library, and renders the
// result as terminal output, prompts, or an exit code. See the design
// doc's "Library architecture".
package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

func main() {
	// Before anything else, and before any key material can exist: this
	// is the process-wide half of the memory protection described in
	// "Session model" — page-locking protects a key from swap, this
	// protects it from a crash dump. A failure here is reported and the
	// program continues, the same warn-and-proceed posture page-locking
	// takes: a weaker guarantee beats an unusable tool.
	if err := disableCoreDumps(); err != nil {
		fmt.Fprintln(os.Stderr, "gage: could not disable core dumps:", err)
	}

	build := BuildInfo{Version: version, Commit: commit}
	isTerminal := func() bool {
		return term.IsTerminal(int(os.Stdin.Fd()))
	}
	os.Exit(Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, isTerminal, build))
}
