// file: core/archtest/noinit.go
// version: 1.0.0
// guid: 05405fb7-8ce9-4818-b74e-b1b61cbd6cf7
// last-edited: 2026-11-05

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// NoInit fails t when a non-test file of a package under dir matching patterns
// (nil: ./...) declares a func init() or a package-level mutable variable
// (03 R5, 06 §6.3 A5). It reads every file of the tag union.
//
// A package-level variable is mutable state unless it is one of:
//   - a sentinel error, `var ErrX = errors.New("…")` (or fmt.Errorf), which 06
//     §6.4.4 allows;
//   - a blank compile-time assertion, `var _ I = (*T)(nil)`.
//
// archtest.TestNoGlobals (R1-13) is the companion AST scan for the process
// globals R5 lists; NoInit stays the init/variable half.
func NoInit(t testing.TB, dir string, patterns ...string) {
	t.Helper()
	pkgs, err := Load(LoadOptions{Dir: dir, Patterns: patternsOrAll(patterns), TagSets: TagSets()})
	if err != nil {
		t.Fatalf("NoInit: %v", err)
	}
	var files []string
	for _, p := range pkgs {
		files = append(files, p.GoFiles...)
	}
	if len(files) == 0 {
		t.Fatalf("NoInit: no non-test Go file under %s %v; the check would be vacuous", dir, patterns)
	}
	findings, err := NoInitFindings(files)
	if err != nil {
		t.Fatalf("NoInit: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d init function(s) or package-level mutable variable(s) (03 R5):\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// NoInitFindings parses files and returns one line per init function or
// package-level mutable variable, sorted.
func NoInitFindings(files []string) ([]string, error) {
	fset := token.NewFileSet()
	var out []string
	for _, f := range uniq(append([]string(nil), files...)) {
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %v", f, err)
		}
		for _, decl := range af.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == "init" {
					out = append(out, fmt.Sprintf("%s: func init(); wire it through the kernel or a constructor instead", fset.Position(d.Pos())))
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if name.Name == "_" || isSentinelError(name.Name, vs, i) {
							continue
						}
						out = append(out, fmt.Sprintf("%s: package-level variable %s; make it a constant, a function returning a fresh value, or a field of an instance",
							fset.Position(name.Pos()), name.Name))
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// isSentinelError reports whether name is `ErrX` or `errX` initialised by an
// errors.New or fmt.Errorf call.
func isSentinelError(name string, vs *ast.ValueSpec, i int) bool {
	if !strings.HasPrefix(name, "Err") && !strings.HasPrefix(name, "err") {
		return false
	}
	if i >= len(vs.Values) {
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
	if !ok {
		return false
	}
	return (pkg.Name == "errors" && sel.Sel.Name == "New") || (pkg.Name == "fmt" && sel.Sel.Name == "Errorf")
}

func patternsOrAll(p []string) []string {
	if len(p) == 0 {
		return []string{"./..."}
	}
	return p
}
