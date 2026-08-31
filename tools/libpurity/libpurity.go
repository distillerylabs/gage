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

// importBindings maps each local identifier a file binds to an import
// onto that import's package path — so `import osx "os"` yields
// osx -> os. Resolving through the import list rather than matching the
// literal identifier "os" is what makes the check hold against an
// aliased import, and equally keeps a local variable that happens to be
// named os from being reported as the package.
//
// A dot-import of os or fmt is reported by the caller rather than
// resolved: it puts Exit/Println into file scope unqualified, which this
// selector-based analysis cannot see at all.
func importBindings(f *ast.File) (bindings map[string]string, dotImported []string) {
	bindings = map[string]string{}
	for _, spec := range f.Imports {
		importPath := strings.Trim(spec.Path.Value, `"`)
		if importPath != "os" && importPath != "fmt" {
			continue
		}
		switch {
		case spec.Name == nil:
			bindings[importPath] = importPath
		case spec.Name.Name == ".":
			dotImported = append(dotImported, importPath)
		case spec.Name.Name == "_":
			// Imported for side effects only; nothing can reference it.
		default:
			bindings[spec.Name.Name] = importPath
		}
	}
	return bindings, dotImported
}

func inspect(fset *token.FileSet, f *ast.File, path string) []Violation {
	var violations []Violation

	bindings, dotImported := importBindings(f)
	for _, pkg := range dotImported {
		violations = append(violations, Violation{
			File:    path,
			Line:    fset.Position(f.Pos()).Line,
			Message: fmt.Sprintf("must not dot-import %q outside _test.go files (it hides os.Exit/fmt.Print* from this check)", pkg),
		})
	}

	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		// ident.Obj is non-nil when the identifier resolves to something
		// declared in this file (a variable, a parameter), which means
		// it shadows the import rather than referring to it.
		if ident.Obj != nil {
			return true
		}

		pkg, ok := bindings[ident.Name]
		if !ok {
			return true
		}

		switch {
		case pkg == "os" && forbiddenSelectors[sel.Sel.Name]:
			violations = append(violations, Violation{
				File:    path,
				Line:    fset.Position(sel.Pos()).Line,
				Message: fmt.Sprintf("must not reference os.%s outside _test.go files", sel.Sel.Name),
			})
		case pkg == "fmt" && strings.HasPrefix(sel.Sel.Name, "Print"):
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
