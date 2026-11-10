// file: core/tasks/noglobals_test.go
// version: 1.0.0
// guid: 4339aade-dc3c-4590-8544-56fbf5a31cbb
// last-edited: 2026-11-10

package tasks_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoGlobals_Tasks is R5's AST guard for this package: no non-test file of
// core/tasks or its sub-packages declares func init() or a package-level var,
// except a sentinel error (`ErrX`/`errX` = errors.New or fmt.Errorf) and a
// re-exported sentinel (`ErrX = otherpkg.ErrX`). The CPU budget, pause gate,
// ref-lock table and counters are executor or loop fields.
func TestNoGlobals_Tasks(t *testing.T) {
	var files []string
	for _, pattern := range []string{"*.go", "*/*.go"} {
		m, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range m {
			if !strings.HasSuffix(f, "_test.go") {
				files = append(files, f)
			}
		}
	}
	if len(files) < 5 {
		t.Fatalf("found %d non-test files; the scan would be vacuous", len(files))
	}
	for _, f := range files {
		for _, finding := range globalsIn(t, f, nil) {
			t.Error(finding)
		}
	}

	// Control: the scan flags each forbidden shape.
	bad := `package x
import ("errors"; "regexp")
var m = map[string]int{}
var re = regexp.MustCompile("a")
var ErrNotSentinel = 42
var n int
var errOK = errors.New("ok")
func init() {}
`
	if got := globalsIn(t, "control.go", bad); len(got) != 5 {
		t.Fatalf("control: %d findings, want 5 (map, regexp, non-error Err, int, init): %v", len(got), got)
	}
}

func globalsIn(t *testing.T, name string, src any) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == "init" {
				out = append(out, fmt.Sprintf("%s: func init()", fset.Position(d.Pos())))
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if id.Name == "_" || sentinel(id.Name, vs, i) {
						continue
					}
					out = append(out, fmt.Sprintf("%s: package-level var %s", fset.Position(id.Pos()), id.Name))
				}
			}
		}
	}
	return out
}

func sentinel(name string, vs *ast.ValueSpec, i int) bool {
	if !strings.HasPrefix(name, "Err") && !strings.HasPrefix(name, "err") || i >= len(vs.Values) {
		return false
	}
	switch v := vs.Values[i].(type) {
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && ((pkg.Name == "errors" && sel.Sel.Name == "New") || (pkg.Name == "fmt" && sel.Sel.Name == "Errorf"))
	case *ast.SelectorExpr: // a re-exported sentinel, e.g. lifecycle.ErrShutdown
		_, ok := v.X.(*ast.Ident)
		return ok && strings.HasPrefix(v.Sel.Name, "Err")
	}
	return false
}
