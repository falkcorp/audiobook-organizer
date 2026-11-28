// file: core/internal/executor/origin_surface_test.go
// version: 1.0.0
// guid: 2f8e5c17-9a43-4b60-8d1e-6c0b7a3f9e54
// last-edited: 2026-11-28

package executor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// originTypes are the unexported types that carry or stamp a start's origin.
var originTypes = []string{"originKind", "startOrigin", "starter", "stagerHandle", "runsHandle"}

// TestOriginIsUnreachableFromExportedSurface: no exported function, method,
// struct field, interface method, variable or constant of this package names a
// type that carries a start's origin, so nothing outside the package can build,
// pass, read or set one; the only values that carry an origin are the handles
// ScheduleStager and OutboxRuns return as plain interfaces. The request and the
// actor a caller builds have no field that could name one either.
func TestOriginIsUnreachableFromExportedSurface(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var leaks []string
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files++
		mentions := func(n ast.Node) []string {
			var got []string
			ast.Inspect(n, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && slices.Contains(originTypes, id.Name) {
					got = append(got, id.Name)
				}
				return true
			})
			return got
		}
		flag := func(pos token.Pos, what string, n ast.Node) {
			for _, m := range mentions(n) {
				leaks = append(leaks, fset.Position(pos).String()+": "+what+" names "+m)
			}
		}
		for _, d := range af.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv != nil && !recvExported(d.Recv) {
					continue // an exported method of an unexported type is reachable only through an interface
				}
				flag(d.Pos(), "exported func "+d.Name.Name, d.Type)
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						if _, isAlias := s.Type.(*ast.Ident); isAlias || s.Assign.IsValid() {
							flag(s.Pos(), "exported type "+s.Name.Name, s.Type)
							continue
						}
						ast.Inspect(s.Type, func(n ast.Node) bool {
							switch n := n.(type) {
							case *ast.Field:
								exported := len(n.Names) == 0 // embedded
								for _, nm := range n.Names {
									exported = exported || nm.IsExported()
								}
								if exported {
									flag(n.Pos(), "exported member of "+s.Name.Name, n.Type)
								}
							}
							return true
						})
					case *ast.ValueSpec:
						for _, nm := range s.Names {
							if nm.IsExported() {
								flag(s.Pos(), "exported value "+nm.Name, s)
							}
						}
					}
				}
			}
		}
	}
	if files == 0 {
		t.Fatal("scanned no file; the guard would be vacuous")
	}
	if len(leaks) > 0 {
		t.Errorf("the exported surface reaches an origin type:\n  %s", strings.Join(leaks, "\n  "))
	}
	for _, v := range []any{tasks.StartRequest{}, tasks.Actor{}} {
		rt := reflect.TypeOf(v)
		for f := range rt.Fields() {
			if strings.Contains(strings.ToLower(f.Name), "origin") {
				t.Errorf("%s has a field %s that could name an origin", rt, f.Name)
			}
		}
	}
}

func recvExported(r *ast.FieldList) bool {
	for _, f := range r.List {
		t := f.Type
		if s, ok := t.(*ast.StarExpr); ok {
			t = s.X
		}
		if id, ok := t.(*ast.Ident); ok && id.IsExported() {
			return true
		}
	}
	return false
}
