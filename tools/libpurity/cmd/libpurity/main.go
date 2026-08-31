// Command libpurity is make lint's library-purity check: it fails if
// internal/gage references os.Stdin/os.Stdout, or calls
// fmt.Print*/os.Exit, outside _test.go files. This is a build tool, not
// part of gage itself, so it's free to use os.Exit and fmt.Print* as
// every ordinary CLI does.
package main

import (
	"fmt"
	"os"

	"github.com/denmark/gage/tools/libpurity"
)

func main() {
	root := "internal/gage"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}

	violations, err := libpurity.Check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "libpurity:", err)
		os.Exit(2)
	}
	if len(violations) == 0 {
		fmt.Printf("libpurity: %s is clean\n", root)
		return
	}

	fmt.Fprintf(os.Stderr, "libpurity: %d violation(s) in %s:\n", len(violations), root)
	for _, v := range violations {
		fmt.Fprintln(os.Stderr, " ", v)
	}
	os.Exit(1)
}
