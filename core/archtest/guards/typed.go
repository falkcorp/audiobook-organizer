// file: core/archtest/guards/typed.go
// version: 1.0.1
// guid: d05ebb1a-8c11-4fa1-8826-7f6e33b47618
// last-edited: 2026-11-10

// Package guards holds archtest's type-aware guards over core/tasks
// definitions and loops (05-tasks §5T.3.2, §5T.3.6, §5T.8.2): they load
// packages with golang.org/x/tools/go/packages, so they live apart from
// archtest itself, which coretest links into every module's tests; x/tools
// stays out of coretest's graph (TestTasklint_TestSupportOnly). Test-support
// only (03 §3.5 R4): no non-test package imports it.
package guards

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/types/typeutil"
)

// tasksPath is core's tasks package, whose definitions and loops the type-aware
// guards (ResumeContinueCheckpoints, RunItemsDeclareParallelism) read.
const tasksPath = "github.com/falkcorp/mediacore/tasks"

// GuardFinding is one finding of a type-aware guard.
type GuardFinding struct {
	File string // absolute
	Line int
	Msg  string
}

func (f GuardFinding) String() string { return fmt.Sprintf("%s:%d: %s", f.File, f.Line, f.Msg) }

// loadTyped type-checks the packages patterns name, from the working
// directory, with syntax and type information (dependencies from export data).
func loadTyped(patterns []string) ([]*packages.Package, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("no package pattern; the check would be vacuous")
	}
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
		packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo}, patterns...)
	if err != nil {
		return nil, err
	}
	var errs []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			errs = append(errs, e.Error())
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%d package error(s):\n  %s", len(errs), strings.Join(errs, "\n  "))
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no package matched %v; the check would be vacuous", patterns)
	}
	return pkgs, nil
}

// underTasks reports core's tasks package and its sub-packages (the engine
// and its test kit), which the guards never check.
func underTasks(path string) bool { return path == tasksPath || strings.HasPrefix(path, tasksPath+"/") }

// nonTestFiles returns p's non-test syntax trees.
func nonTestFiles(p *packages.Package) []*ast.File {
	var out []*ast.File
	for _, f := range p.Syntax {
		if !strings.HasSuffix(p.Fset.File(f.Pos()).Name(), "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

// tasksFuncName returns the name of the tasks package-level function call
// invokes (generic instantiations resolved to their origin), or "".
func tasksFuncName(info *types.Info, call *ast.CallExpr) string {
	fn, ok := typeutil.Callee(info, call).(*types.Func)
	if !ok {
		return ""
	}
	fn = fn.Origin()
	if fn.Pkg() == nil || fn.Pkg().Path() != tasksPath {
		return ""
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return ""
	}
	return fn.Name()
}

// isTasksNamed reports whether t (or *t) is the tasks type name.
func isTasksNamed(t types.Type, name string) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	o := n.Origin().Obj()
	return o.Pkg() != nil && o.Pkg().Path() == tasksPath && o.Name() == name
}

// funcIndex maps every function and method declared in the loaded packages to
// its declaration and the package holding it.
type funcIndex map[*types.Func]declIn

type declIn struct {
	decl *ast.FuncDecl
	pkg  *packages.Package
}

func indexFuncDecls(pkgs []*packages.Package) funcIndex {
	idx := funcIndex{}
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
					if fn, ok := p.TypesInfo.Defs[fd.Name].(*types.Func); ok {
						idx[fn] = declIn{fd, p}
					}
				}
			}
		}
	}
	return idx
}

// funcOf returns the function an identifier, selector or call names.
func funcOf(info *types.Info, e ast.Expr) *types.Func {
	var obj types.Object
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		obj = info.Uses[x]
	case *ast.SelectorExpr:
		obj = info.Uses[x.Sel]
	case *ast.CallExpr:
		obj = typeutil.Callee(info, x)
	}
	fn, _ := obj.(*types.Func)
	if fn == nil {
		return nil
	}
	return fn.Origin()
}

// keyed returns a composite literal's keyed elements by field name.
func keyed(lit *ast.CompositeLit) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok {
				out[k.Name] = kv.Value
			}
		}
	}
	return out
}

// directives maps file -> line -> the text after prefix on that line.
func directives(fset *token.FileSet, files []*ast.File, prefix string) map[string]map[int]string {
	out := map[string]map[int]string{}
	for _, f := range files {
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				rest, ok := strings.CutPrefix(c.Text, prefix)
				if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
					continue
				}
				pos := fset.Position(c.Pos())
				if out[pos.Filename] == nil {
					out[pos.Filename] = map[int]string{}
				}
				out[pos.Filename][pos.Line] = strings.TrimSpace(rest)
			}
		}
	}
	return out
}

func sortFindings(fs []GuardFinding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		return fs[i].Msg < fs[j].Msg
	})
}
