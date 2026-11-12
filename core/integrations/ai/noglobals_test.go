// file: core/integrations/ai/noglobals_test.go
// version: 1.0.0
// guid: c1d7f3a8-4e92-4b60-9a15-8f2e6b0d3c47
// last-edited: 2026-11-12

package ai_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoGlobals (R5, I5): no non-test file of integrations/ai, its aitest
// package or its openai adapter declares func init() or a package-level var,
// except a sentinel error. HEAD's internal/aidispatch kept its dispatcher, its
// endpoint cooldowns and its usage counters in package-level state, so two
// apps in one process shared a bench; here every one of them is a Router field.
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
	var sawAdapter, sawSuite bool
	for _, f := range files {
		sawAdapter = sawAdapter || strings.HasPrefix(f, "openai"+string(filepath.Separator))
		sawSuite = sawSuite || strings.HasPrefix(f, "aitest"+string(filepath.Separator))
		for _, finding := range globalsIn(t, f, nil) {
			t.Error(finding)
		}
	}
	if !sawAdapter || !sawSuite {
		t.Fatalf("the scan did not reach the adapter (%v) and the suite package (%v)", sawAdapter, sawSuite)
	}
	bad := `package x
import "errors"
var cooldowns = map[string]int{}
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
