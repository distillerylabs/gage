package libpurity

import (
	"testing"
)

func TestCleanSourceHasNoViolations(t *testing.T) {
	src := `package gage

import "fmt"

func Format(v int) string {
	return fmt.Sprintf("%d", v)
}
`
	violations, err := checkSource("clean.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Errorf("clean source flagged: %v", violations)
	}
}

func TestCatchesOsStdin(t *testing.T) {
	src := `package gage

import (
	"bufio"
	"os"
)

func readLine() string {
	s := bufio.NewScanner(os.Stdin)
	s.Scan()
	return s.Text()
}
`
	violations, err := checkSource("bad_stdin.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want exactly 1", violations)
	}
	if violations[0].Line != 9 {
		t.Errorf("Line = %d, want 9", violations[0].Line)
	}
}

func TestCatchesOsStdout(t *testing.T) {
	src := `package gage

import (
	"fmt"
	"os"
)

func warn() {
	fmt.Fprintln(os.Stdout, "warning")
}
`
	violations, err := checkSource("bad_stdout.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want exactly 1", violations)
	}
}

func TestCatchesOsExit(t *testing.T) {
	src := `package gage

import "os"

func fail() {
	os.Exit(1)
}
`
	violations, err := checkSource("bad_exit.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want exactly 1", violations)
	}
}

func TestCatchesFmtPrintFamily(t *testing.T) {
	for _, call := range []string{"Print", "Println", "Printf"} {
		src := "package gage\n\nimport \"fmt\"\n\nfunc noisy() {\n\tfmt." + call + "(\"x\")\n}\n"
		violations, err := checkSource("bad_"+call+".go", src)
		if err != nil {
			t.Fatal(err)
		}
		if len(violations) != 1 {
			t.Errorf("fmt.%s: violations = %v, want exactly 1", call, violations)
		}
	}
}

func TestFprintfIsAllowed(t *testing.T) {
	src := `package gage

import (
	"fmt"
	"io"
)

func write(w io.Writer) {
	fmt.Fprintf(w, "ok")
}
`
	violations, err := checkSource("fine.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Errorf("fmt.Fprintf flagged: %v", violations)
	}
}

func TestOsGetenvIsAllowed(t *testing.T) {
	src := `package gage

import "os"

func home() string {
	return os.Getenv("HOME")
}
`
	violations, err := checkSource("fine_getenv.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Errorf("os.Getenv flagged: %v", violations)
	}
}

// TestAliasedImportsDoNotEvadeTheCheck: the rule this tool enforces is
// architectural, so the check has to hold against the spelling as well
// as the intent. Matching the literal identifier "os" (the obvious
// implementation) misses `import osx "os"` entirely, which would let the
// library layer call os.Exit under an alias with lint still green.
func TestAliasedImportsDoNotEvadeTheCheck(t *testing.T) {
	src := `package gage

import (
	osx "os"
	format "fmt"
)

func fail() {
	format.Println("leaking to stdout")
	osx.Exit(1)
}
`
	violations, err := checkSource("aliased.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 2 {
		t.Errorf("aliased imports evaded the check: got %d violations, want 2: %v", len(violations), violations)
	}
}

func TestDotImportIsReported(t *testing.T) {
	src := `package gage

import . "os"

func fail() {
	Exit(1)
}
`
	violations, err := checkSource("dot.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 {
		t.Error("dot-import of os not reported; it hides os.Exit from selector analysis entirely")
	}
}

// TestLocalIdentifierNamedOsIsNotAPackageReference is the other
// direction: resolving through the import list must not turn a local
// variable that happens to be named os into a false violation.
func TestLocalIdentifierNamedOsIsNotAPackageReference(t *testing.T) {
	src := `package gage

type opts struct{ Stdin string }

func run() string {
	os := opts{Stdin: "x"}
	return os.Stdin
}
`
	violations, err := checkSource("shadow.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Errorf("local variable named os reported as a package reference: %v", violations)
	}
}

func TestUnparseableSourceIsAnError(t *testing.T) {
	if _, err := checkSource("broken.go", "this is not go source {{{"); err == nil {
		t.Fatal("expected a parse error")
	}
}

// TestRealLibraryTreeIsClean is the actual M0 lint check: internal/gage
// itself, right now, must have zero violations. This is what CI's `make
// lint` runs (via cmd/libpurity), and it's included here too so `go test
// ./...` catches a regression even for someone who forgets to run lint.
func TestRealLibraryTreeIsClean(t *testing.T) {
	violations, err := Check("../../internal/gage")
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Errorf("internal/gage has library-purity violations:\n%v", violations)
	}
}

func TestCheckOnMissingDirIsAnError(t *testing.T) {
	if _, err := Check("does-not-exist-anywhere"); err == nil {
		t.Fatal("expected an error for a nonexistent root directory")
	}
}
