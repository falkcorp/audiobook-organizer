// file: core/integrations/providerhttp/noglobals_test.go
// version: 1.0.0
// guid: 03cadb63-54c2-42a4-95f8-cd1c314ed83f
// last-edited: 2026-11-11

package providerhttp_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoGlobals (R5, I5): no non-test file of providerhttp or its test-support
// package declares func init() or a package-level var, except a sentinel
// error. HEAD's limiters, clients, overrides, defaultThrottles and
// dailyquota.registry maps have no counterpart: they are Registry fields.
func TestNoGlobals(t *testing.T) {
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
	if len(files) < 15 {
		t.Fatalf("found %d non-test files; the scan would be vacuous", len(files))
	}
	for _, f := range files {
		for _, finding := range globalsIn(t, f, nil) {
			t.Error(finding)
		}
	}
	bad := `package x
import "errors"
var limiters = map[string]int{}
var n int
var ErrNot = 42
var errOK = errors.New("ok")
func init() {}
`
	if got := globalsIn(t, "control.go", bad); len(got) != 4 {
		t.Fatalf("control: %d findings, want 4 (map, int, non-error Err, init): %v", len(got), got)
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
	call, ok := vs.Values[i].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && ((pkg.Name == "errors" && sel.Sel.Name == "New") || (pkg.Name == "fmt" && sel.Sel.Name == "Errorf"))
}
