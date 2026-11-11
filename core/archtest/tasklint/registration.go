// file: core/archtest/tasklint/registration.go
// version: 1.3.0
// guid: 5baf56b2-c55e-4af1-af93-7a5c497dbcef
// last-edited: 2026-11-11

package tasklint

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/types/typeutil"
)

// The unregistered-kind rule (05-tasks §5T.8.4) over a set of packages: Run
// checks the whole loaded program at once, so a kind one package builds and
// another registers (core's kernel registering an owner package's kinds, a
// module's wiring package registering its plugins') counts as registered. The
// standalone analyzer applies it to one package at a time (Analyzer's doc).
//
// A kind site is a tasks.Task, tasks.Batch or tasks.Fixer call, or a call of a
// kind helper: a function or method returning tasks.Definition whose body
// makes a kind site itself (found to a fixed point, so helpers of helpers
// count). Every kind site must lie inside what the registration roots reach.
// The roots are the arguments of each Registrar.Tasks call and the build
// argument of each Registrar.DerivedIndex call (05-tasks §5T.2), and, with
// ListFuncs, each list function some loaded package calls.
//
// Reachability follows package-level funcs, methods and vars across packages
// and the assignments of function-local variables; reaching a function covers
// its whole body, so a list built with make and index assignment, an adder
// closure or a table of rows (the F5 conversions' shapes) passes. A site
// inside a helper that has call sites is not checked by coverage: each of
// those call sites is, so a helper shared by a registered and an orphan
// caller reports the orphan call. A helper nobody calls is checked in its own
// body, unless it carries ConstructorDirective: an exported kind constructor
// other modules call (datamigrate.DerivedBuildTask), whose call sites there
// are the kind sites. Config.Constructors lists the functions allowed to
// carry the directive.
//
// Coverage cannot see a kind thrown away inside a covered body, so a site
// that is clearly discarded is reported wherever it is, a helper's or a
// constructor's body included: a bare expression statement, an assignment to
// _ (in its result's position, for a multi-value call), or a local variable
// whose only reads are assignments to _ (Go refuses a local never read at
// all). A kind kept in a parameter, a struct field, a map or a channel is not
// judged discarded.

// ConstructorDirective, in an exported kind helper's doc comment with a
// reason, marks it a constructor for other modules: with no call site in the
// loaded program, its body is not checked by coverage. It never hides a call
// site, nor a kind its body discards. Only the functions in
// Config.Constructors may carry it.
const ConstructorDirective = "//tasklint:constructor"

// unit is one package's non-test files and type information.
type unit struct {
	path  string
	files []*ast.File
	info  *types.Info
}

// nodeAt is a syntax node with the unit whose type information resolves it.
type nodeAt struct {
	u *unit
	n ast.Node
}

// kindFinding is one unregistered kind site.
type kindFinding struct {
	pos token.Pos
	msg string
}

type registration struct {
	cfg   *Config
	units []*unit
	whole bool // the units are the whole loaded program, not one package
	// decls maps the key of each package-level func, method and var (objKey)
	// to its declaration: the *ast.FuncDecl, the var's own initializer, or
	// the *ast.ValueSpec when one call initializes several vars.
	decls map[string]nodeAt
	// funcs holds the *ast.FuncDecl of each func and method key.
	funcs map[string]*ast.FuncDecl
	// locals maps each function-local variable to the expressions assigned
	// to it, so a registration that passes a local reaches what was put in.
	locals map[types.Object][]nodeAt
	// helpers are the kind helpers' keys; called, those with a call site
	// outside their own body.
	helpers, called map[string]bool
	// constructors maps each func carrying ConstructorDirective to its
	// reason.
	constructors map[string]string
}

// unregisteredKinds returns every kind site of units outside what the
// registration roots reach. whole says units are the whole program, which
// only changes the message.
func unregisteredKinds(cfg *Config, units []*unit, whole bool) []kindFinding {
	r := &registration{cfg: cfg, units: units, whole: whole, decls: map[string]nodeAt{},
		funcs: map[string]*ast.FuncDecl{}, locals: map[types.Object][]nodeAt{}, helpers: map[string]bool{}, called: map[string]bool{},
		constructors: map[string]string{}}
	for _, u := range units {
		for _, f := range u.files {
			r.index(u, f)
		}
	}
	r.findHelpers()
	return r.report(r.reach(r.roots()))
}

// objKey names a package-level func, method or var by path and name
// ("path.Name", "path.Type.Method"); "" for anything else (a local, a field,
// an interface method). go/packages shares one *types.Package between the
// packages of one Load, so object identity would also match today; the key
// keeps the pass correct if a unit's imports come from export data instead
// (another loader, or a dependency type-checked apart), where the object one
// package uses is not the object its declaring package defines.
func objKey(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Func:
		o = o.Origin()
		if o.Pkg() == nil {
			return ""
		}
		sig, _ := o.Type().(*types.Signature)
		if sig == nil || sig.Recv() == nil {
			return o.Pkg().Path() + "." + o.Name()
		}
		t := sig.Recv().Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		n, ok := types.Unalias(t).(*types.Named)
		if !ok {
			return ""
		}
		return o.Pkg().Path() + "." + n.Obj().Name() + "." + o.Name()
	case *types.Var:
		if o.Pkg() == nil || o.IsField() || o.Parent() != o.Pkg().Scope() {
			return ""
		}
		return o.Pkg().Path() + "." + o.Name()
	}
	return ""
}

func (r *registration) index(u *unit, f *ast.File) {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if k := objKey(u.info.Defs[d.Name]); k != "" {
				r.decls[k] = nodeAt{u, d}
				r.funcs[k] = d
				if reason, ok := constructorReason(d); ok {
					r.constructors[k] = reason
				}
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					k := objKey(u.info.Defs[n])
					switch {
					case k == "":
					case len(vs.Values) == len(vs.Names):
						r.decls[k] = nodeAt{u, vs.Values[i]} // var a, b = x, y: each its own
					default:
						r.decls[k] = nodeAt{u, vs}
					}
				}
			}
		}
	}
	local := func(id *ast.Ident) types.Object {
		obj := u.info.ObjectOf(id)
		if obj == nil || obj.Pkg() == nil || obj.Parent() == obj.Pkg().Scope() {
			return nil
		}
		return obj
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				id, ok := l.(*ast.Ident)
				if !ok {
					continue
				}
				obj := local(id)
				if obj == nil {
					continue
				}
				if len(n.Rhs) == len(n.Lhs) {
					r.locals[obj] = append(r.locals[obj], nodeAt{u, n.Rhs[i]})
				} else {
					for _, x := range n.Rhs {
						r.locals[obj] = append(r.locals[obj], nodeAt{u, x})
					}
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				obj := local(id)
				switch {
				case obj == nil:
				case len(n.Values) == len(n.Names):
					r.locals[obj] = append(r.locals[obj], nodeAt{u, n.Values[i]})
				default:
					for _, x := range n.Values {
						r.locals[obj] = append(r.locals[obj], nodeAt{u, x})
					}
				}
			}
		}
		return true
	})
}

// constructorReason returns the reason of fd's ConstructorDirective.
func constructorReason(fd *ast.FuncDecl) (string, bool) {
	if fd.Doc == nil {
		return "", false
	}
	for _, c := range fd.Doc.List {
		if rest, ok := strings.CutPrefix(c.Text, ConstructorDirective); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// isDefinition reports tasks.Definition.
func (r *registration) isDefinition(t types.Type) bool {
	n, ok := namedIn(t, r.cfg.TasksPath)
	return ok && n.Obj().Name() == "Definition"
}

// returnsDefinition reports a signature with a tasks.Definition result.
func (r *registration) returnsDefinition(u *unit, fd *ast.FuncDecl) bool {
	fn, ok := u.info.Defs[fd.Name].(*types.Func)
	if !ok {
		return false
	}
	res := fn.Type().(*types.Signature).Results()
	for v := range res.Variables() {
		if r.isDefinition(v.Type()) {
			return true
		}
	}
	return false
}

// siteKey returns "ctor" for a constructor call, the helper's key for a
// helper call, or "".
func (r *registration) siteKey(u *unit, call *ast.CallExpr) string {
	fn, ok := typeutil.Callee(u.info, call).(*types.Func)
	if !ok {
		return ""
	}
	if isConstructor(tasksFuncName(fn, r.cfg.TasksPath)) {
		return "ctor"
	}
	if k := objKey(fn); r.helpers[k] {
		return k
	}
	return ""
}

// findHelpers marks each func returning tasks.Definition whose body makes a
// kind site (a constructor or a helper call other than itself) until nothing
// changes, then records which helpers have a call site outside their body.
func (r *registration) findHelpers() {
	keys := make([]string, 0, len(r.funcs))
	for k, fd := range r.funcs {
		if fd.Body != nil && r.returnsDefinition(r.decls[k].u, fd) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for changed := true; changed; {
		changed = false
		for _, k := range keys {
			if r.helpers[k] {
				continue
			}
			u := r.decls[k].u
			ast.Inspect(r.funcs[k].Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && !r.helpers[k] {
					if s := r.siteKey(u, call); s != "" && s != k {
						r.helpers[k] = true
						changed = true
					}
				}
				return !r.helpers[k]
			})
		}
	}
	r.eachDecl(func(u *unit, d ast.Decl, dk string) {
		ast.Inspect(d, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if s := r.siteKey(u, call); s != "ctor" && s != "" && s != dk {
					r.called[s] = true
				}
			}
			return true
		})
	})
}

// eachDecl calls fn for every top-level declaration with its func key ("" for
// a GenDecl).
func (r *registration) eachDecl(fn func(u *unit, d ast.Decl, dk string)) {
	for _, u := range r.units {
		for _, f := range u.files {
			for _, d := range f.Decls {
				dk := ""
				if fd, ok := d.(*ast.FuncDecl); ok {
					dk = objKey(u.info.Defs[fd.Name])
				}
				fn(u, d, dk)
			}
		}
	}
}

// roots returns the registration roots: the tasks.Definition arguments of
// each Registrar.Tasks call (a ...tasks.Definition spread included) and of
// each Registrar.DerivedIndex call (its build), named so that a Registrar
// method added later registers nothing until this rule says so; and, with
// ListFuncs, every list function some loaded package calls (a list function
// nothing calls registers nothing).
func (r *registration) roots() []nodeAt {
	var q []nodeAt
	lists := map[string]bool{}
	r.eachDecl(func(u *unit, d ast.Decl, dk string) {
		if fd, ok := d.(*ast.FuncDecl); ok && r.cfg.ListFuncs && isListFunc(u.info, r.cfg.TasksPath, fd) {
			lists[dk] = true
		}
		ast.Inspect(d, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if recv, name := methodIn(u.info, call, r.cfg.RegistrarPath); recv == "Registrar" && (name == "Tasks" || name == "DerivedIndex") {
				for _, a := range call.Args {
					t := u.info.TypeOf(a)
					if sl, ok := t.(*types.Slice); ok && call.Ellipsis.IsValid() {
						t = sl.Elem()
					}
					if t != nil && r.isDefinition(t) {
						q = append(q, nodeAt{u, a})
					}
				}
			}
			return true
		})
	})
	if len(lists) == 0 {
		return q
	}
	called := map[string]bool{}
	r.eachDecl(func(u *unit, d ast.Decl, dk string) {
		ast.Inspect(d, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if k := objKey(u.info.Uses[id]); lists[k] && k != dk {
					called[k] = true
				}
			}
			return true
		})
	})
	keys := make([]string, 0, len(called))
	for k := range called {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		q = append(q, r.decls[k])
	}
	return q
}

// reach returns every node the roots reach.
func (r *registration) reach(queue []nodeAt) []ast.Node {
	seenKey := map[string]bool{}
	seenLocal := map[types.Object]bool{}
	var reached []ast.Node
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		reached = append(reached, at.n)
		ast.Inspect(at.n, func(x ast.Node) bool {
			id, ok := x.(*ast.Ident)
			if !ok {
				return true
			}
			obj := at.u.info.Uses[id]
			if obj == nil {
				return true
			}
			if k := objKey(obj); k != "" {
				if d, ok := r.decls[k]; ok && !seenKey[k] {
					seenKey[k] = true
					queue = append(queue, d)
				}
			} else if rhs, ok := r.locals[obj]; ok && !seenLocal[obj] {
				seenLocal[obj] = true
				queue = append(queue, rhs...)
			}
			return true
		})
	}
	return reached
}

func within(nodes []ast.Node, p token.Pos) bool {
	for _, n := range nodes {
		if p >= n.Pos() && p < n.End() {
			return true
		}
	}
	return false
}

func (r *registration) report(reached []ast.Node) []kindFinding {
	covered := func(p token.Pos) bool { return within(reached, p) }
	why := "no Registrar call in this package"
	if r.whole {
		why = "no Registrar call in any loaded package"
	}
	if r.cfg.ListFuncs {
		why += " and no called list function returning []tasks.Definition"
	}
	var out []kindFinding
	allowed := map[string]bool{}
	for _, k := range r.cfg.Constructors {
		allowed[k] = true
	}
	keys := make([]string, 0, len(r.constructors))
	for k := range r.constructors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fd := r.funcs[k]
		switch {
		case !r.helpers[k] || !fd.Name.IsExported():
			out = append(out, kindFinding{pos: fd.Pos(), msg: fmt.Sprintf("%s on %s: only an exported kind helper (a func returning tasks.Definition that builds one) is a constructor; delete the directive", ConstructorDirective, fd.Name.Name)})
		case r.constructors[k] == "":
			out = append(out, kindFinding{pos: fd.Pos(), msg: fmt.Sprintf("%s on %s needs the reason: who calls it and which Registrar method takes the result", ConstructorDirective, fd.Name.Name)})
		case !allowed[k]:
			out = append(out, kindFinding{pos: fd.Pos(), msg: fmt.Sprintf("%s on %s, which Config.Constructors (Options.Constructors) does not list as %q; a new constructor is a reviewed addition to that list", ConstructorDirective, fd.Name.Name, k)})
		}
	}
	if r.whole {
		for _, k := range r.cfg.Constructors {
			if _, ok := r.constructors[k]; !ok {
				out = append(out, kindFinding{pos: token.NoPos, msg: fmt.Sprintf("Constructors lists %s, which no loaded function carrying %s matches; delete the entry", k, ConstructorDirective)})
			}
		}
	}
	reads := r.localReads()
	r.eachDecl(func(u *unit, d ast.Decl, dk string) {
		valid := r.constructors[dk] != "" && allowed[dk] && r.funcs[dk] != nil && r.funcs[dk].Name.IsExported()
		template := r.helpers[dk] && (r.called[dk] || valid) // its call sites carry coverage
		discards := r.discards(u, d, reads)
		ast.Inspect(d, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			s := r.siteKey(u, call)
			if s == "" || s == dk {
				return true
			}
			var what string
			if s == "ctor" {
				fn, _ := typeutil.Callee(u.info, call).(*types.Func)
				what = "tasks." + fn.Name() + " definition"
			} else {
				what = "definition built by " + r.funcs[s].Name.Name
			}
			switch {
			case discards[call]:
				out = append(out, kindFinding{pos: call.Pos(), msg: fmt.Sprintf(
					"%s in %s is built and discarded (a bare statement, an assignment to _, or a local read only by _ =): register it or delete it",
					what, declName(d))})
			case template || covered(call.Pos()):
			default:
				out = append(out, kindFinding{pos: call.Pos(), msg: fmt.Sprintf(
					"%s in %s is not registered: %s reaches it; pass it to Registrar.Tasks (or Registrar.DerivedIndex), directly or through a function the call reaches",
					what, declName(d), why)})
			}
			return true
		})
	})
	return out
}

// isBlank reports the blank identifier.
func isBlank(e ast.Expr) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	return ok && id.Name == "_"
}

// localReads counts, for each function-local variable, the identifier uses
// that read it: every use except the left side of an assignment and the
// right side of an assignment to _ (`_ = v` keeps the compiler quiet and
// reads nothing). A named result counts as read: a bare return reads it.
func (r *registration) localReads() map[types.Object]int {
	reads := map[types.Object]int{}
	for _, u := range r.units {
		for _, f := range u.files {
			notRead := map[*ast.Ident]bool{}
			ast.Inspect(f, func(n ast.Node) bool {
				if ft, ok := n.(*ast.FuncType); ok && ft.Results != nil {
					// A named result is read by every bare return.
					for _, fl := range ft.Results.List {
						for _, id := range fl.Names {
							if obj := u.info.Defs[id]; obj != nil {
								reads[obj]++
							}
						}
					}
				}
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for i, l := range as.Lhs {
					if id, ok := ast.Unparen(l).(*ast.Ident); ok {
						notRead[id] = true
					}
					if isBlank(l) && len(as.Rhs) == len(as.Lhs) {
						if id, ok := ast.Unparen(as.Rhs[i]).(*ast.Ident); ok {
							notRead[id] = true
						}
					}
				}
				return true
			})
			ast.Inspect(f, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || notRead[id] {
					return true
				}
				if obj, ok := u.info.Uses[id].(*types.Var); ok && obj.Pkg() != nil && obj.Parent() != obj.Pkg().Scope() {
					reads[obj]++
				}
				return true
			})
		}
	}
	return reads
}

// discards returns the calls in d whose definition is clearly thrown away:
// a bare expression statement, a value assigned to _, or a value stored in a
// local variable nothing reads (reads, localReads).
func (r *registration) discards(u *unit, d ast.Decl, reads map[types.Object]int) map[*ast.CallExpr]bool {
	out := map[*ast.CallExpr]bool{}
	dropped := func(lhs ast.Expr) bool {
		if isBlank(lhs) {
			return true
		}
		id, ok := ast.Unparen(lhs).(*ast.Ident)
		if !ok {
			return false
		}
		v, ok := u.info.ObjectOf(id).(*types.Var)
		return ok && v.Pkg() != nil && v.Parent() != v.Pkg().Scope() && reads[v] == 0
	}
	// pair marks rhs when it is a call and its definition lands in lhs; a
	// multi-value call lands its tasks.Definition result in that position.
	pair := func(lhs []ast.Expr, rhs []ast.Expr) {
		if len(lhs) == len(rhs) {
			for i, x := range rhs {
				if call, ok := ast.Unparen(x).(*ast.CallExpr); ok && dropped(lhs[i]) {
					out[call] = true
				}
			}
			return
		}
		if len(rhs) != 1 {
			return
		}
		call, ok := ast.Unparen(rhs[0]).(*ast.CallExpr)
		if !ok {
			return
		}
		tup, ok := u.info.TypeOf(call).(*types.Tuple)
		if !ok {
			return
		}
		for i := range tup.Len() {
			if i < len(lhs) && r.isDefinition(tup.At(i).Type()) && dropped(lhs[i]) {
				out[call] = true
			}
		}
	}
	ast.Inspect(d, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ExprStmt:
			if call, ok := ast.Unparen(n.X).(*ast.CallExpr); ok {
				out[call] = true
			}
		case *ast.AssignStmt:
			pair(n.Lhs, n.Rhs)
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, len(n.Names))
			for i, id := range n.Names {
				lhs[i] = id
			}
			pair(lhs, n.Values)
		}
		return true
	})
	return out
}
