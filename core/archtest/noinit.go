// file: core/archtest/noinit.go
// version: 1.1.0
// guid: 05405fb7-8ce9-4818-b74e-b1b61cbd6cf7
// last-edited: 2026-11-06

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
//   - a blank compile-time assertion, `var _ I = (*T)(nil)`;
//   - a spec-mandated exported value carrying an `//archtest:allow-var <reason>`
//     line in its own doc or line comment, e.g. rbac.Public (05-rbac §3.1). The
//     directive needs a reason, applies to the one spec it annotates (never to a
//     whole var block), and is refused on a map, slice, func, pointer or chan
//     value, which R5 forbids outright.
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
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution|parser.ParseComments)
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
						if hasAllowDirective(vs) {
							if kind := forbiddenKind(vs, i); kind != "" {
								out = append(out, fmt.Sprintf("%s: package-level variable %s is a %s; //archtest:allow-var never exempts a map, slice, func, pointer or chan (03 R5)",
									fset.Position(name.Pos()), name.Name, kind))
							}
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

// allowVarDirective marks a spec-mandated package-level value NoInit accepts.
const allowVarDirective = "//archtest:allow-var "

// hasAllowDirective reports an `//archtest:allow-var <reason>` line, with a
// non-empty reason, in vs's own doc or line comment. The var block's doc is
// deliberately not consulted, so one directive cannot exempt a whole block.
func hasAllowDirective(vs *ast.ValueSpec) bool {
	for _, cg := range []*ast.CommentGroup{vs.Doc, vs.Comment} {
		if cg == nil {
			continue
		}
		for _, c := range cg.List {
			if reason, ok := strings.CutPrefix(c.Text, allowVarDirective); ok && strings.TrimSpace(reason) != "" {
				return true
			}
		}
	}
	return false
}

// forbiddenKind names the R5-forbidden kind of the i-th name of vs as far as
// the syntax shows it (its declared type, or the shape of its initialiser), or
// "" when it is none of map, slice, func, pointer or chan.
func forbiddenKind(vs *ast.ValueSpec, i int) string {
	if k := typeKind(vs.Type); k != "" {
		return k
	}
	if i >= len(vs.Values) {
		return ""
	}
	switch v := vs.Values[i].(type) {
	case *ast.CompositeLit:
		return typeKind(v.Type)
	case *ast.FuncLit:
		return "func"
	case *ast.UnaryExpr:
		if v.Op == token.AND {
			return "pointer"
		}
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok && (id.Name == "make" || id.Name == "new") {
			if id.Name == "new" {
				return "pointer"
			}
			if len(v.Args) > 0 {
				return typeKind(v.Args[0])
			}
		}
	}
	return ""
}

func typeKind(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.MapType:
		return "map"
	case *ast.ArrayType:
		if t.Len == nil {
			return "slice"
		}
	case *ast.FuncType:
		return "func"
	case *ast.StarExpr:
		return "pointer"
	case *ast.ChanType:
		return "chan"
	}
	return ""
}
