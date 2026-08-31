// Package libpurity enforces one of M0's structural rules: internal/gage
// (the library layer) never references os.Stdin/os.Stdout, and never
// calls fmt.Print*/os.Exit, outside _test.go files. Those are CLI-only
// concerns — see the design doc's "Library architecture" — and the lint
// check exists so a later milestone can't quietly reintroduce one buried
// inside a library method.
package libpurity

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// Violation is one place a non-test file in the scanned tree touched
// something reserved for the CLI layer.
type Violation struct {
	File    string
	Line    int
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.File, v.Line, v.Message)
}

// Check walks every non-test .go file under rootDir and reports every
// violation found. A rootDir that doesn't exist, or contains a file that
// fails to parse as Go source, is reported as an error rather than a
// silently-empty result.
func Check(rootDir string) ([]Violation, error) {
	var violations []Violation
	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		vs, err := checkFile(path)
		if err != nil {
			return err
		}
		violations = append(violations, vs...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return violations, nil
}

// checkSource runs the same check against in-memory source, under a
// synthetic filename, for unit-testing the checker itself without
// touching the real tree.
func checkSource(filename, src string) ([]Violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, fmt.Errorf("libpurity: parsing %s: %w", filename, err)
	}
	return inspect(fset, f, filename), nil
}

func checkFile(path string) ([]Violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("libpurity: parsing %s: %w", path, err)
	}
	return inspect(fset, f, path), nil
}

// forbiddenSelectors names the exact os.* members outside _test.go files
// may never reference — CLI-only concerns per the design doc's
// "Library architecture".
var forbiddenSelectors = map[string]bool{
	"Stdin":  true,
	"Stdout": true,
	"Exit":   true,
}

func inspect(fset *token.FileSet, f *ast.File, path string) []Violation {
	var violations []Violation
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}

		switch {
		case ident.Name == "os" && forbiddenSelectors[sel.Sel.Name]:
			violations = append(violations, Violation{
				File:    path,
				Line:    fset.Position(sel.Pos()).Line,
				Message: fmt.Sprintf("must not reference os.%s outside _test.go files", sel.Sel.Name),
			})
		case ident.Name == "fmt" && strings.HasPrefix(sel.Sel.Name, "Print"):
			violations = append(violations, Violation{
				File:    path,
				Line:    fset.Position(sel.Pos()).Line,
				Message: fmt.Sprintf("must not call fmt.%s outside _test.go files", sel.Sel.Name),
			})
		}
		return true
	})
	return violations
}
