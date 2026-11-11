// file: core/archtest/guards/runitems_parallelism.go
// version: 1.0.1
// guid: 692ef99e-03a5-44ad-a650-b8a8b0a0c7e1
// last-edited: 2026-11-10

package guards

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/types/typeutil"
)

// RequiredDirective marks an options-struct field that pass-through clause (c)
// of RunItemsDeclareParallelism accepts (05-tasks §5T.3.2).
const RequiredDirective = "//mediacore:required"

// RunItemsDeclareParallelism fails t when, in the packages pkgs names, the
// parallelism argument of a tasks.RunItems, RunSeq or ForEach call, or the
// Parallelism field of a tasks.BatchSpec or FixerSpec literal, is not provably
// declared (03 R7, 05-tasks §5T.3.2; successor of ABO's
// internal/plugins/runitems_concurrency_lint_test.go). It accepts:
//
//	(a) a call to a tasks constructor (CPU, Fixed, Sequential, FromParam), or a
//	    call to a function of the loaded packages whose every return is accepted;
//	(b) a tasks.Parallelism-typed variable, parameter or field whose every
//	    assignment in the loaded packages (initialisers, assignments, composite
//	    literal fields, call arguments) is accepted;
//	(c) a field marked //mediacore:required whose package calls Validate on it.
//
// It flags every tasks.Parallelism{} literal, every zero var (a
// tasks.Parallelism var declared without a value), Sequential("") and
// Fixed(n, "") with an empty or blank reason, and a BatchSpec or FixerSpec
// literal with no Parallelism. Core's tasks package and its sub-packages are
// not checked: they implement the loop.
func RunItemsDeclareParallelism(t testing.TB, pkgs ...string) {
	t.Helper()
	findings, err := parallelismFindings(pkgs)
	if err != nil {
		t.Fatalf("RunItemsDeclareParallelism: %v", err)
		return
	}
	for _, f := range findings {
		t.Errorf("%s", f)
	}
}

type parState struct {
	idx      funcIndex
	assigns  map[*types.Var][]exprIn
	returns  map[*types.Func][]exprIn
	required map[*types.Var]bool
	verdict  map[types.Object]bool
	visiting map[types.Object]bool
}

type exprIn struct {
	e    ast.Expr
	info *types.Info
}

func parallelismFindings(patterns []string) ([]GuardFinding, error) {
	pkgs, err := loadTyped(patterns)
	if err != nil {
		return nil, err
	}
	s := &parState{idx: indexFuncDecls(pkgs), assigns: map[*types.Var][]exprIn{}, returns: map[*types.Func][]exprIn{},
		required: map[*types.Var]bool{}, verdict: map[types.Object]bool{}, visiting: map[types.Object]bool{}}
	var out []GuardFinding
	at := func(p *packages.Package, n ast.Node, format string, a ...any) {
		pos := p.Fset.Position(n.Pos())
		out = append(out, GuardFinding{File: pos.Filename, Line: pos.Line, Msg: fmt.Sprintf(format, a...)})
	}
	isPar := func(t types.Type) bool { return t != nil && isTasksNamed(t, "Parallelism") }
	// Pass 1: assignments, returns, required fields; the direct flags.
	validated := map[*types.Var]bool{}
	marked := map[*types.Var]bool{}
	for _, p := range pkgs {
		info := p.TypesInfo
		checked := !underTasks(p.PkgPath)
		for _, f := range nonTestFiles(p) {
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Field:
					if hasDirective(x.Doc, RequiredDirective) || hasDirective(x.Comment, RequiredDirective) {
						for _, name := range x.Names {
							if v, ok := info.Defs[name].(*types.Var); ok && isPar(v.Type()) {
								marked[v] = true
							}
						}
					}
				case *ast.ValueSpec:
					for i, name := range x.Names {
						v, ok := info.Defs[name].(*types.Var)
						if !ok || !isPar(v.Type()) {
							continue
						}
						switch {
						case len(x.Values) == len(x.Names):
							s.assigns[v] = append(s.assigns[v], exprIn{x.Values[i], info})
						case len(x.Values) == 0 && checked:
							at(p, name, "zero tasks.Parallelism var %s: declare it with CPU, Fixed or Sequential", name.Name)
						}
					}
				case *ast.AssignStmt:
					if len(x.Lhs) != len(x.Rhs) {
						return true
					}
					for i, l := range x.Lhs {
						if v := assignedVar(info, l); v != nil && isPar(v.Type()) {
							s.assigns[v] = append(s.assigns[v], exprIn{x.Rhs[i], info})
						}
					}
				case *ast.CompositeLit:
					t := info.TypeOf(x)
					if t == nil {
						return true
					}
					if isPar(t) && checked {
						at(p, x, "tasks.Parallelism{} is the undeclared zero value: use CPU, Fixed or Sequential")
						return true
					}
					st, ok := t.Underlying().(*types.Struct)
					if !ok {
						return true
					}
					for i, el := range x.Elts {
						var fv *types.Var
						val := el
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if k, ok := kv.Key.(*ast.Ident); ok {
								fv, _ = info.Uses[k].(*types.Var)
							}
							val = kv.Value
						} else if i < st.NumFields() {
							fv = st.Field(i)
						}
						if fv != nil && isPar(fv.Type()) {
							s.assigns[fv.Origin()] = append(s.assigns[fv.Origin()], exprIn{val, info})
						}
					}
				case *ast.CallExpr:
					if fn := funcOf(info, x.Fun); fn != nil && x.Ellipsis == 0 {
						if sig, ok := fn.Type().(*types.Signature); ok {
							for i := 0; i < sig.Params().Len() && i < len(x.Args); i++ {
								if pv := sig.Params().At(i); isPar(pv.Type()) && !(sig.Variadic() && i == sig.Params().Len()-1) {
									s.assigns[pv] = append(s.assigns[pv], exprIn{x.Args[i], info})
								}
							}
						}
					}
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Validate" && isPar(info.TypeOf(sel.X)) {
						if fs, ok := ast.Unparen(sel.X).(*ast.SelectorExpr); ok {
							if v, ok := info.Uses[fs.Sel].(*types.Var); ok && v.IsField() && v.Pkg() == p.Types {
								validated[v.Origin()] = true
							}
						}
					}
					if checked {
						name := tasksFuncName(info, x)
						reasonArg := map[string]int{"Sequential": 0, "Fixed": 1}
						if i, ok := reasonArg[name]; ok && i < len(x.Args) {
							if tv := info.Types[x.Args[i]]; tv.Value != nil && tv.Value.Kind() == constant.String && strings.TrimSpace(constant.StringVal(tv.Value)) == "" {
								at(p, x, "tasks.%s needs a reason", name)
							}
						}
					}
				}
				return true
			})
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fn, ok := info.Defs[fd.Name].(*types.Func)
				if !ok {
					continue
				}
				res := fn.Type().(*types.Signature).Results()
				if res.Len() != 1 || !isPar(res.At(0).Type()) {
					continue
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if _, ok := n.(*ast.FuncLit); ok {
						return false
					}
					if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
						s.returns[fn] = append(s.returns[fn], exprIn{r.Results[0], info})
					}
					return true
				})
			}
		}
	}
	for v := range marked {
		s.required[v] = validated[v]
	}
	// Pass 2: the sites.
	for _, p := range pkgs {
		if underTasks(p.PkgPath) {
			continue
		}
		info := p.TypesInfo
		for _, f := range nonTestFiles(p) {
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					name := tasksFuncName(info, x)
					if (name == "RunItems" || name == "RunSeq" || name == "ForEach") && len(x.Args) > 1 && !s.accepted(x.Args[1], info) {
						at(p, x.Args[1], "tasks.%s parallelism is not provably declared: pass a tasks constructor, a value every assignment of which is one, or a validated //mediacore:required field", name)
					}
				case *ast.CompositeLit:
					t := info.TypeOf(x)
					if t == nil || !(isTasksNamed(t, "BatchSpec") || isTasksNamed(t, "FixerSpec")) {
						return true
					}
					par, ok := keyed(x)["Parallelism"]
					switch {
					case !ok:
						at(p, x, "a BatchSpec or FixerSpec literal with no Parallelism: declare it with CPU, Fixed or Sequential")
					case !s.accepted(par, info):
						at(p, par, "Parallelism is not provably declared: pass a tasks constructor, a value every assignment of which is one, or a validated //mediacore:required field")
					}
				}
				return true
			})
		}
	}
	sortFindings(out)
	return out, nil
}

func hasDirective(cg *ast.CommentGroup, d string) bool {
	if cg == nil {
		return false
	}
	for _, c := range cg.List {
		if c.Text == d || strings.HasPrefix(c.Text, d+" ") {
			return true
		}
	}
	return false
}

// assignedVar returns the variable or field an assignment's left side names.
func assignedVar(info *types.Info, l ast.Expr) *types.Var {
	switch x := ast.Unparen(l).(type) {
	case *ast.Ident:
		v, _ := info.ObjectOf(x).(*types.Var)
		return v
	case *ast.SelectorExpr:
		if v, ok := info.Uses[x.Sel].(*types.Var); ok {
			return v.Origin()
		}
	}
	return nil
}

// accepted reports whether e is a provably declared parallelism.
func (s *parState) accepted(e ast.Expr, info *types.Info) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		switch tasksFuncName(info, x) {
		case "CPU", "Fixed", "Sequential", "FromParam":
			return true
		}
		fn, ok := typeutil.Callee(info, x).(*types.Func)
		if !ok {
			return false
		}
		fn = fn.Origin()
		return s.all(fn, s.returns[fn])
	case *ast.Ident, *ast.SelectorExpr:
		var v *types.Var
		if id, ok := x.(*ast.Ident); ok {
			v, _ = info.Uses[id].(*types.Var)
		} else {
			v, _ = info.Uses[x.(*ast.SelectorExpr).Sel].(*types.Var)
		}
		if v == nil {
			return false
		}
		v = v.Origin()
		if s.required[v] {
			return true
		}
		return s.all(v, s.assigns[v])
	}
	return false
}

// all reports whether obj has at least one recorded expression and every one
// is accepted. A cycle (a value passed back to itself) is assumed accepted
// while it is being decided; its other expressions still decide it.
func (s *parState) all(obj types.Object, es []exprIn) bool {
	if v, done := s.verdict[obj]; done {
		return v
	}
	if s.visiting[obj] {
		return true
	}
	if len(es) == 0 {
		s.verdict[obj] = false
		return false
	}
	s.visiting[obj] = true
	ok := true
	for _, e := range es {
		if !s.accepted(e.e, e.info) {
			ok = false
			break
		}
	}
	delete(s.visiting, obj)
	s.verdict[obj] = ok
	return ok
}
