// file: tools/fixerstores/main.go
// version: 1.1.0
// guid: 4e7c1a93-d2b8-4f60-8a15-6c3b9e0d7f24
// last-edited: 2026-11-12

// Command fixerstores answers one question about the repairs fixers of
// internal/plugins/maintenance: which of the plugin's store accessors can a
// fixer's entry methods reach other than through the shell's FixerStores?
//
// A fixer that runs under the native shell must read and write only the stores
// the shell bound to its context (FixerStores). The plugin's own deps
// (ServerDeps) hand out the same accessors, and a fixer that calls one of them
// reads HEAD's store instead of the shell's. The tripwire tests in
// stores_fixer_test.go catch that for the paths they execute; this pass is the
// static complement that looks at the paths a test does not execute.
//
// How: it loads the maintenance package and everything it imports, builds SSA
// and a call graph with variable type analysis (VTA, seeded by CHA), and walks
// from the roots: the Plan, Replan, Apply, BeginApply, AfterApply, Meta,
// RequiredPermissions and PlanOnly methods of every concrete type that
// implements repairs.Fixer. The walk follows every function of this module
// (not only the maintenance package) and every wrapper the compiler made.
// Every reached, hand-written function of the module is searched for
//
//   - a call (or method value) of one of the FixerStores accessors on anything
//     that is not a "store family" interface (FixerStores, its two halves, or
//     an interface made only of accessors, such as a narrow parameter type): a
//     store sink;
//   - a conversion into a store-family interface of a value that is not one:
//     ServerDeps handed over as the stores (a ChangeInterface or type
//     assertion), or a maintenance type made into FixerStores (pluginFixerStores
//     in fixerStores, the fallback for callers without the shell). Narrowing
//     the signature of a shared body to a store-family parameter does not make
//     the body safe; it moves the question to whoever converts, and this is the
//     check of that conversion;
//   - a call of any other ServerDeps method: a dep, which is not a store but is
//     still reached through the plugin's captured deps and not the shell.
//
// A func value or an interface value that a call hands out is followed
// directly, for every call (static, interface or dynamic): a function literal
// or named function becomes an edge to its body; a maintenance type made into
// an interface for a function outside the module contributes its methods; and
// the funcs stored in a struct literal passed by value, or in the slice a
// variadic call builds (registry.RunItemsOptions: Label, CheckpointFn,
// CheckpointStateFn), are followed as if they had been passed bare. A func
// argument that is a parameter of the caller is the caller's caller's business
// and is followed there. Any other func argument, or func stored in such a
// literal (loaded from a field, returned by a call), cannot be followed and is
// reported as unresolved. A struct that is not a literal here (a parameter, a
// call result, a copy loaded through a pointer) is not looked into: it was
// built elsewhere, where its funcs are followed or reported.
//
// Only the join points are pruned: the module's registry.RunItems, whose
// call-graph edges join every caller to every callback its other callers pass.
// What a join point calls is reached through the callbacks named at each call.
//
// The output is JSON for the guard test in internal/plugins/maintenance, which
// compares it with a reviewed allowlist; an unresolved func argument fails the
// test until it is allowlisted with a reason.
//
// What this does not do, which is why it complements the tripwire tests and
// does not replace them:
//
//   - VTA is unsound for reflection: a call made through reflect, or a method
//     found by name, is not an edge.
//   - VTA is imprecise for function values stored in struct fields: a func
//     stored in a field and called later has every func of its type as a
//     possible callee or, for some shapes, none. It can both add callees that
//     never run (false positives, which the allowlist records with a reason)
//     and miss ones that do. A func read from a field and passed on is an
//     unresolved argument, not a followed one.
//   - Code outside the module (the standard library, third-party packages and
//     the mediacore module) is not walked: what it calls back is reached only
//     through the func and interface values handed to it at a call we scan.
//   - A receiver is judged by its static type. A store read through a variable
//     typed as a store-family interface is not a sink even if it was filled
//     from the plugin's deps; the conversion check above is what catches that
//     fill, and only where the conversion is a visible instruction.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

const (
	modulePath     = "github.com/falkcorp/audiobook-organizer"
	maintenancePkg = modulePath + "/internal/plugins/maintenance"
	repairsPkg     = modulePath + "/internal/repairs"
)

// rootMethods are the methods of a fixer the shell or the lane calls: the
// repairs.Fixer methods that can run code (Plan, Replan, Apply, Meta), the
// optional ApplyScoped (BeginApply), follow-up (AfterApply), PermissionScoped
// (RequiredPermissions) and PlanOnly hooks. A type that lacks a hook simply
// has no such root.
var rootMethods = []string{"Plan", "Replan", "Apply", "Meta", "BeginApply", "AfterApply", "RequiredPermissions", "PlanOnly"}

// joinPoints are functions of this module that run a callback they were given
// for many different callers. The call graph joins every caller to every
// callback (and every callback to every caller), so their edges are not
// followed; the callbacks named at each call are (see passedFuncs).
var joinPoints = map[string]bool{
	modulePath + "/internal/operations/registry.RunItems": true,
}

// storeFamily are the interface names whose methods are the shell's stores:
// calling an accessor on one of these is the correct way to read a store.
var storeFamily = []string{"FixerStores", "FixerBookStores", "FixerUserStores"}

type report struct {
	Fixers     []string    `json:"fixers"`
	Roots      int         `json:"roots"`
	Reachable  int         `json:"reachable"`
	Accessors  []string    `json:"accessors"`
	StoreSinks []sink      `json:"storeSinks"`
	Deps       []dep       `json:"deps"`
	Unresolved []unresolve `json:"unresolved"`
}

type sink struct {
	Func     string   `json:"func"`
	Accessor string   `json:"accessor"`
	Pos      string   `json:"pos"`
	Path     []string `json:"path"` // a call path from a root to Func
}

type dep struct {
	Method  string   `json:"method"`
	Callers []string `json:"callers"`
	Path    []string `json:"path"` // the shortest call path from a root to the first caller found
}

type unresolve struct {
	Caller string `json:"caller"`
	Callee string `json:"callee"`
	Pos    string `json:"pos"`
	Arg    string `json:"arg"` // what the func argument is, for the reviewer
}

func main() {
	repo := flag.String("repo", ".", "root of the audiobook-organizer module")
	flag.Parse()
	abs, err := filepath.Abs(*repo)
	if err != nil {
		fatal(err)
	}
	rep, err := analyze(abs)
	if err != nil {
		fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(rep); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fixerstores:", err)
	os.Exit(1)
}

func analyze(repo string) (*report, error) {
	cfg := &packages.Config{
		Mode: packages.LoadAllSyntax,
		Dir:  repo,
		// The pass reads source only. Without cgo no generated cgo files are
		// needed to type-check, which keeps the build cache untouched.
		Env: append(os.Environ(), "CGO_ENABLED=0"),
	}
	pkgs, err := packages.Load(cfg, maintenancePkg)
	if err != nil {
		return nil, err
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("loaded %d packages for %s, want 1", len(pkgs), maintenancePkg)
	}
	maint := pkgs[0]
	if len(maint.Errors) > 0 {
		return nil, fmt.Errorf("%s does not load: %v", maintenancePkg, maint.Errors[0])
	}

	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	a := &analysis{prog: prog, maint: maint.Types, repo: repo, depPath: map[string][]string{}}
	if err := a.types(maint); err != nil {
		return nil, err
	}
	roots := a.roots()

	// VTA over every function, seeded by CHA, as the x/tools docs recommend.
	// Synthetic nodes are kept: they are how a promoted method or a bound
	// method value reaches its target, and their own bodies are never searched
	// (see isSearched).
	graph := vta.CallGraph(ssautil.AllFunctions(prog), cha.CallGraph(prog))
	return a.walk(graph, roots), nil
}

type analysis struct {
	prog  *ssa.Program
	maint *types.Package
	repo  string

	depPath map[string][]string // method -> path to the first caller found

	fixerIface  *types.Interface
	depsType    types.Type
	storeIfaces []types.Type
	accessors   map[string]bool
	fixers      []types.Type // pointer types
}

func (a *analysis) types(maint *packages.Package) error {
	scope := maint.Types.Scope()
	lookup := func(name string) (types.Type, error) {
		o := scope.Lookup(name)
		if o == nil {
			return nil, fmt.Errorf("%s.%s not found", maintenancePkg, name)
		}
		return o.Type(), nil
	}
	var err error
	if a.depsType, err = lookup("ServerDeps"); err != nil {
		return err
	}
	for _, n := range storeFamily {
		t, err := lookup(n)
		if err != nil {
			return err
		}
		a.storeIfaces = append(a.storeIfaces, t)
	}
	stores := a.storeIfaces[0].Underlying().(*types.Interface)
	a.accessors = map[string]bool{}
	for i := 0; i < stores.NumMethods(); i++ {
		a.accessors[stores.Method(i).Name()] = true
	}
	rp, ok := maint.Imports[repairsPkg]
	if !ok {
		return fmt.Errorf("%s does not import %s", maintenancePkg, repairsPkg)
	}
	fo := rp.Types.Scope().Lookup("Fixer")
	if fo == nil {
		return fmt.Errorf("%s.Fixer not found", repairsPkg)
	}
	a.fixerIface = fo.Type().Underlying().(*types.Interface)
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || tn.IsAlias() || types.IsInterface(tn.Type()) {
			continue
		}
		if _, isTP := tn.Type().(*types.TypeParam); isTP {
			continue
		}
		pt := types.NewPointer(tn.Type())
		if types.Implements(pt, a.fixerIface) {
			a.fixers = append(a.fixers, pt)
		}
	}
	if len(a.fixers) == 0 {
		return fmt.Errorf("no type in %s implements repairs.Fixer", maintenancePkg)
	}
	return nil
}

// roots are the entry methods of every fixer type that exist on it.
func (a *analysis) roots() []*ssa.Function {
	var out []*ssa.Function
	for _, pt := range a.fixers {
		ms := a.prog.MethodSets.MethodSet(pt)
		for _, name := range rootMethods {
			sel := ms.Lookup(a.maint, name)
			if sel == nil {
				continue
			}
			if fn := a.prog.MethodValue(sel); fn != nil {
				out = append(out, fn)
			}
		}
	}
	return out
}

func (a *analysis) inMaint(fn *ssa.Function) bool {
	return fn != nil && fn.Pkg != nil && fn.Pkg.Pkg == a.maint
}

// inModule reports whether fn is a function of this module.
func (a *analysis) inModule(fn *ssa.Function) bool {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	path := fn.Pkg.Pkg.Path()
	return path == modulePath || strings.HasPrefix(path, modulePath+"/")
}

// isJoinPoint is a function of the joinPoints list.
func (a *analysis) isJoinPoint(fn *ssa.Function) bool {
	if fn == nil || !a.inModule(fn) {
		return false
	}
	o := fn
	if og := fn.Origin(); og != nil {
		o = og
	}
	return joinPoints[o.Pkg.Pkg.Path()+"."+o.Name()]
}

// traversable is a function the walk follows: one of this module (but not a
// join point), or a wrapper the compiler made (a promoted method, a bound
// method value), which has no package of its own and only forwards.
func (a *analysis) traversable(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	if a.inModule(fn) {
		return !a.isJoinPoint(fn)
	}
	return fn.Pkg == nil && fn.Synthetic != ""
}

// isSearched reports whether fn's own body is searched for sinks: a function of
// this module that the compiler did not synthesize. A wrapper for a promoted
// method (pluginFixerStores.OpsStore) calls the accessor on ServerDeps by
// construction; that is the fallback, which the conversion check (in
// fixerStores) is what reports.
func (a *analysis) isSearched(fn *ssa.Function) bool {
	return a.inModule(fn) && fn.Synthetic == ""
}

// isStoreFamily reports whether t is an interface through which a store is the
// shell's to give: FixerStores, either half of it, or an interface made only of
// FixerStores accessors (a narrow parameter type).
func (a *analysis) isStoreFamily(t types.Type) bool {
	for _, si := range a.storeIfaces {
		if types.Identical(t, si) {
			return true
		}
	}
	iface, ok := t.Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 {
		return false
	}
	for i := 0; i < iface.NumMethods(); i++ {
		if !a.accessors[iface.Method(i).Name()] {
			return false
		}
	}
	return true
}

func (a *analysis) walk(graph *callgraph.Graph, roots []*ssa.Function) *report {
	parent := map[*ssa.Function]*ssa.Function{}
	seen := map[*ssa.Function]bool{}
	var queue []*ssa.Function
	push := func(fn, from *ssa.Function) {
		if fn == nil || seen[fn] || !a.traversable(fn) {
			return
		}
		seen[fn] = true
		parent[fn] = from
		queue = append(queue, fn)
	}
	for _, r := range roots {
		push(r, nil)
	}

	rep := &report{Roots: len(roots)}
	depCallers := map[string]map[string]bool{}
	sinkSeen := map[string]bool{}
	unresolvedSeen := map[string]bool{}

	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]

		if node := graph.Nodes[fn]; node != nil {
			for _, e := range node.Out {
				push(e.Callee.Func, fn)
			}
		}
		if !a.isSearched(fn) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, in := range b.Instrs {
				a.scan(fn, in, push, rep, depCallers, sinkSeen, unresolvedSeen, parent)
			}
		}
	}

	rep.Reachable = len(seen)
	for _, pt := range a.fixers {
		rep.Fixers = append(rep.Fixers, types.TypeString(pt, func(p *types.Package) string { return p.Name() }))
	}
	sort.Strings(rep.Fixers)
	for n := range a.accessors {
		rep.Accessors = append(rep.Accessors, n)
	}
	sort.Strings(rep.Accessors)
	sort.Slice(rep.StoreSinks, func(i, j int) bool {
		if rep.StoreSinks[i].Func != rep.StoreSinks[j].Func {
			return rep.StoreSinks[i].Func < rep.StoreSinks[j].Func
		}
		return rep.StoreSinks[i].Accessor < rep.StoreSinks[j].Accessor
	})
	for m, cs := range depCallers {
		d := dep{Method: m, Path: a.depPath[m]}
		for c := range cs {
			d.Callers = append(d.Callers, c)
		}
		sort.Strings(d.Callers)
		rep.Deps = append(rep.Deps, d)
	}
	sort.Slice(rep.Deps, func(i, j int) bool { return rep.Deps[i].Method < rep.Deps[j].Method })
	sort.Slice(rep.Unresolved, func(i, j int) bool {
		if rep.Unresolved[i].Caller != rep.Unresolved[j].Caller {
			return rep.Unresolved[i].Caller < rep.Unresolved[j].Caller
		}
		return rep.Unresolved[i].Callee < rep.Unresolved[j].Callee
	})
	return rep
}

// scan searches one instruction of a searched function: accessor and dep
// calls, conversions into a store-family interface, and the func and interface
// values it passes out.
func (a *analysis) scan(fn *ssa.Function, in ssa.Instruction, push func(fn, from *ssa.Function),
	rep *report, depCallers map[string]map[string]bool, sinkSeen map[string]bool,
	unresolvedSeen map[string]bool, parent map[*ssa.Function]*ssa.Function) {

	pos := func() string { return a.pos(in.Pos()) }

	if call, ok := in.(ssa.CallInstruction); ok {
		cc := call.Common()
		if cc.IsInvoke() {
			a.method(fn, cc.Method, cc.Value.Type(), pos(), rep, depCallers, sinkSeen, parent)
		}
		a.passedValues(fn, call, push, rep, unresolvedSeen)
	}

	switch v := in.(type) {
	case *ssa.ChangeInterface:
		if a.isStoreFamily(v.Type()) && !a.isStoreFamily(v.X.Type()) {
			a.conversion(fn, "convert:"+typeName(v.X.Type(), a.maint)+"->"+typeName(v.Type(), a.maint), pos(), rep, sinkSeen, parent)
		}
	case *ssa.TypeAssert:
		// An assertion from an interface the plugin's deps satisfy. An assertion
		// from `any` is how the shell's stores come off a context.
		if x, ok := v.X.Type().Underlying().(*types.Interface); ok && x.NumMethods() > 0 &&
			a.isStoreFamily(v.AssertedType) && !a.isStoreFamily(v.X.Type()) && types.AssignableTo(a.depsType, v.X.Type()) {
			a.conversion(fn, "convert:"+typeName(v.X.Type(), a.maint)+"->"+typeName(v.AssertedType, a.maint), pos(), rep, sinkSeen, parent)
		}
	case *ssa.MakeInterface:
		if a.isStoreFamily(v.Type()) && a.isMaintType(v.X.Type()) {
			a.conversion(fn, "convert:"+typeName(v.X.Type(), a.maint)+"->"+typeName(v.Type(), a.maint), pos(), rep, sinkSeen, parent)
		}
	}

	// A method value or expression of an interface method (p.deps.OpsStore
	// used as a func value), which is a call waiting to happen.
	var ops [10]*ssa.Value
	for _, op := range in.Operands(ops[:0]) {
		f, ok := (*op).(*ssa.Function)
		if !ok || f.Synthetic == "" {
			continue
		}
		if !strings.HasPrefix(f.Synthetic, "bound method wrapper") && !strings.HasPrefix(f.Synthetic, "thunk") {
			continue
		}
		obj, ok := f.Object().(*types.Func)
		if !ok {
			continue
		}
		if recv := obj.Type().(*types.Signature).Recv(); recv != nil && types.IsInterface(recv.Type()) {
			a.method(fn, obj, recv.Type(), pos(), rep, depCallers, sinkSeen, parent)
		}
	}
}

func typeName(t types.Type, from *types.Package) string {
	return types.TypeString(t, func(p *types.Package) string {
		if p == from {
			return ""
		}
		return p.Name()
	})
}

// isMaintType reports whether t (or what it points to) is a named type of the
// maintenance package.
func (a *analysis) isMaintType(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() == a.maint
}

func (a *analysis) conversion(fn *ssa.Function, what, pos string, rep *report, sinkSeen map[string]bool, parent map[*ssa.Function]*ssa.Function) {
	key := a.name(fn) + "|" + what
	if sinkSeen[key] {
		return
	}
	sinkSeen[key] = true
	rep.StoreSinks = append(rep.StoreSinks, sink{Func: a.name(fn), Accessor: what, Pos: pos, Path: a.path(fn, parent)})
}

// method classifies a call of interface method m on a receiver of type recv.
func (a *analysis) method(fn *ssa.Function, m *types.Func, recv types.Type, pos string,
	rep *report, depCallers map[string]map[string]bool, sinkSeen map[string]bool, parent map[*ssa.Function]*ssa.Function) {

	name := m.Name()
	if a.accessors[name] {
		if a.isStoreFamily(recv) {
			return // the shell's own stores: the right way to read one
		}
		key := a.name(fn) + "|" + name
		if sinkSeen[key] {
			return
		}
		sinkSeen[key] = true
		rep.StoreSinks = append(rep.StoreSinks, sink{
			Func: a.name(fn), Accessor: name, Pos: pos, Path: a.path(fn, parent),
		})
		return
	}
	// Any other method of ServerDeps is a dep: a receiver is one when the
	// plugin's deps satisfy it.
	if !types.IsInterface(recv) || !types.AssignableTo(a.depsType, recv) {
		return
	}
	if depCallers[name] == nil {
		depCallers[name] = map[string]bool{}
		a.depPath[name] = a.path(fn, parent)
	}
	depCallers[name][a.name(fn)] = true
}

// onlyRead reports whether every use of the address v is a load or a further
// field address that is only loaded (or a capture by a closure that only reads).
func (a *analysis) onlyRead(v ssa.Value, depth int) bool {
	if depth > 4 {
		return false
	}
	refs := v.Referrers()
	if refs == nil {
		return true
	}
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.UnOp:
			if r.Op != token.MUL {
				return false
			}
		case *ssa.DebugRef:
		case *ssa.FieldAddr:
			if !a.onlyRead(r, depth+1) {
				return false
			}
		case *ssa.MakeClosure:
			clo := r.Fn.(*ssa.Function)
			for i, b := range r.Bindings {
				if b == v && (i >= len(clo.FreeVars) || !a.onlyRead(clo.FreeVars[i], depth+1)) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// fieldOfParamCopy reports whether addr is the address of a field (at any
// depth) of a local variable that holds nothing but a by-value parameter: the
// callee's copy of a struct its caller built.
func (a *analysis) fieldOfParamCopy(addr ssa.Value, depth int) bool {
	if depth > 4 {
		return false
	}
	fa, ok := addr.(*ssa.FieldAddr)
	if !ok {
		return false
	}
	switch x := fa.X.(type) {
	case *ssa.FieldAddr:
		return a.fieldOfParamCopy(x, depth+1)
	case *ssa.Alloc:
		stores := 0
		for _, r := range *x.Referrers() {
			switch r := r.(type) {
			case *ssa.Store:
				if r.Addr != ssa.Value(x) {
					return false
				}
				if _, isParam := r.Val.(*ssa.Parameter); !isParam {
					return false
				}
				stores++
			case *ssa.FieldAddr:
				// a field of the copy: only loads of it keep it a copy
				for _, rr := range *r.Referrers() {
					switch rr.(type) {
					case *ssa.UnOp, *ssa.DebugRef, *ssa.FieldAddr:
					default:
						return false
					}
				}
			case *ssa.UnOp, *ssa.DebugRef:
			case *ssa.MakeClosure:
				// captured by reference: fine while the closure only reads it
				clo := r.Fn.(*ssa.Function)
				for i, b := range r.Bindings {
					if b == ssa.Value(x) && (i >= len(clo.FreeVars) || !a.onlyRead(clo.FreeVars[i], 0)) {
						return false
					}
				}
			default:
				return false
			}
		}
		return stores == 1
	}
	return false
}

// allocOf finds the variable a pointer value addresses: the Alloc itself, or
// the one a closure captured by reference.
func (a *analysis) allocOf(x ssa.Value, depth int) *ssa.Alloc {
	if depth > 6 {
		return nil
	}
	switch x := x.(type) {
	case *ssa.Alloc:
		return x
	case *ssa.FreeVar:
		fn := x.Parent()
		idx := -1
		for i, fv := range fn.FreeVars {
			if fv == x {
				idx = i
			}
		}
		parent := fn.Parent()
		if idx < 0 || parent == nil {
			return nil
		}
		var out *ssa.Alloc
		for _, b := range parent.Blocks {
			for _, in := range b.Instrs {
				if mc, ok := in.(*ssa.MakeClosure); ok && mc.Fn == fn {
					al := a.allocOf(mc.Bindings[idx], depth+1)
					if al == nil || (out != nil && al != out) {
						return nil
					}
					out = al
				}
			}
		}
		return out
	}
	return nil
}

// addUnresolved records a func (or func-carrying aggregate) argument the pass
// could not follow, once per caller, callee and argument description.
func (a *analysis) addUnresolved(rep *report, seen map[string]bool, fn *ssa.Function, callName string, call ssa.CallInstruction, v ssa.Value) {
	u := unresolve{Caller: a.name(fn), Callee: callName, Pos: a.pos(call.Pos()), Arg: a.describe(v)}
	key := u.Caller + " -> " + u.Callee + " [" + u.Arg + "]"
	if seen[key] {
		return
	}
	seen[key] = true
	rep.Unresolved = append(rep.Unresolved, u)
}

// carriesFunc reports whether a value of type t, passed by value, can hold a
// func: a func-typed field of a struct (looked at to a small depth), or a slice
// of such structs (the slice a variadic call builds). Pointers are not
// followed: a pointer to a big struct is not a literal that holds callbacks.
func (a *analysis) carriesFunc(t types.Type, depth int) bool {
	if depth > 3 {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			ft := u.Field(i).Type()
			if _, ok := ft.Underlying().(*types.Signature); ok {
				return true
			}
			if a.carriesFunc(ft, depth+1) {
				return true
			}
		}
	case *types.Slice:
		_, isStruct := u.Elem().Underlying().(*types.Struct)
		return isStruct && a.carriesFunc(u.Elem(), depth+1)
	}
	return false
}

// resolveAggregate finds the function bodies stored into a struct value or a
// slice of struct values built in a literal: the values stored into the
// variable it was loaded from, field by field and element by element. ok is
// false only when a func stored in the literal is one resolveFunc cannot
// follow. An aggregate that is not a literal here (a parameter, a nil slice, a
// call result, a copy loaded through a pointer or field) resolves to nothing,
// resolved: it was built elsewhere.
func (a *analysis) resolveAggregate(v ssa.Value, depth int) (fns []*ssa.Function, ok bool) {
	if depth > 8 {
		return nil, false
	}
	if _, isFn := v.Type().Underlying().(*types.Signature); isFn {
		return a.resolveFunc(v, 0) // its own depth budget: the aggregate's was spent reaching it
	}
	if !a.carriesFunc(v.Type(), 0) {
		return nil, true
	}
	switch v := v.(type) {
	case *ssa.Const, *ssa.Parameter:
		return nil, true
	case *ssa.UnOp:
		if v.Op == token.MUL {
			if a.allocOf(v.X, 0) == nil {
				// A copy loaded through a pointer or a field: the value was
				// built where it was stored, not here, so there is no literal
				// to read. Not followed, and not reported (a stated limit).
				return nil, true
			}
			return a.storesInto(v.X, depth+1)
		}
	case *ssa.Slice:
		if a.allocOf(v.X, 0) == nil {
			return nil, true
		}
		return a.storesInto(v.X, depth+1)
	case *ssa.Phi:
		ok = true
		for _, e := range v.Edges {
			f, eok := a.resolveAggregate(e, depth+1)
			fns = append(fns, f...)
			ok = ok && eok
		}
		return fns, ok
	}
	// Any other source (a call result, a field, a tuple element): the aggregate
	// was built where it came from, not in a literal here. Not followed and not
	// reported; the funcs inside it are followed or reported where it was built.
	return nil, true
}

// storesInto finds what is stored into the variable at addr and, through
// FieldAddr and IndexAddr, into its fields and elements, resolving each stored
// value. An address that goes anywhere else (passed to a call, stored away)
// is not followed.
func (a *analysis) storesInto(addr ssa.Value, depth int) (fns []*ssa.Function, ok bool) {
	if depth > 10 {
		return nil, false
	}
	refs := addr.Referrers()
	if refs == nil {
		return nil, true
	}
	ok = true
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.Store:
			if r.Addr != addr {
				return nil, false // the address itself is stored somewhere
			}
			f, sok := a.resolveAggregate(r.Val, depth+1)
			fns = append(fns, f...)
			ok = ok && sok
		case *ssa.FieldAddr:
			f, sok := a.storesInto(r, depth+1)
			fns = append(fns, f...)
			ok = ok && sok
		case *ssa.IndexAddr:
			f, sok := a.storesInto(r, depth+1)
			fns = append(fns, f...)
			ok = ok && sok
		case *ssa.UnOp, *ssa.DebugRef, *ssa.Slice:
			// a load, debug info, or the slice built over the array
		case *ssa.MakeClosure:
			// Captured by reference: what the closure stores through its free
			// variable is stored into the variable too.
			clo := r.Fn.(*ssa.Function)
			for i, b := range r.Bindings {
				if b != addr || i >= len(clo.FreeVars) {
					continue
				}
				f, sok := a.storesInto(clo.FreeVars[i], depth+1)
				fns = append(fns, f...)
				ok = ok && sok
			}
		default:
			return nil, false
		}
	}
	return fns, ok
}

// describe says what a func value is, for the report: the field it was read
// from, the call that returned it, or the SSA form.
func (a *analysis) describe(v ssa.Value) string {
	switch v := v.(type) {
	case *ssa.UnOp:
		if fa, ok := v.X.(*ssa.FieldAddr); ok {
			if st, ok := fa.X.Type().Underlying().(*types.Pointer); ok {
				if s, ok := st.Elem().Underlying().(*types.Struct); ok {
					return "field " + typeName(st.Elem(), a.maint) + "." + s.Field(fa.Field).Name()
				}
			}
		}
		return "load of " + typeName(v.Type(), a.maint)
	case *ssa.Call:
		if c := v.Call.StaticCallee(); c != nil {
			return "result of " + c.RelString(a.maint)
		}
		return "result of a dynamic call"
	case *ssa.FreeVar:
		return "captured variable of type " + typeName(v.Type(), a.maint)
	case *ssa.Extract:
		return "extract from a call result"
	}
	return fmt.Sprintf("%T of type %s", v, typeName(v.Type(), a.maint))
}

// resolveFunc finds the function bodies a func-typed value can be: a function
// literal or named function, the branches of a phi, the operand of a func type
// conversion, and a captured variable's value at the closure's creation. A
// parameter resolves to nothing, resolved: it is followed where the caller's
// own caller passed it. ok is false when some source of the value is a field,
// a call result or anything else this does not look through.
func (a *analysis) resolveFunc(v ssa.Value, depth int) (fns []*ssa.Function, ok bool) {
	if depth > 6 {
		return nil, false
	}
	switch v := v.(type) {
	case *ssa.MakeClosure:
		return []*ssa.Function{v.Fn.(*ssa.Function)}, true
	case *ssa.Function:
		return []*ssa.Function{v}, true
	case *ssa.Const, *ssa.Parameter:
		return nil, true
	case *ssa.ChangeType:
		return a.resolveFunc(v.X, depth+1)
	case *ssa.Convert:
		return a.resolveFunc(v.X, depth+1)
	case *ssa.UnOp:
		// A load of a local variable that escaped to the heap (captured by a
		// closure, or address-taken): what it can hold is what is stored into
		// it. A load through a field or anything else is not looked through.
		if v.Op != token.MUL {
			return nil, false
		}
		if a.fieldOfParamCopy(v.X, 0) {
			// A func field of a struct that came in as a parameter (by value, so
			// the callee holds a copy): the callback was put there where the
			// struct was built, and is followed there.
			return nil, true
		}
		alloc := a.allocOf(v.X, 0)
		if alloc == nil {
			return nil, false
		}
		ok = true
		for _, r := range *alloc.Referrers() {
			switch r := r.(type) {
			case *ssa.Store:
				if r.Addr == ssa.Value(alloc) {
					f, sok := a.resolveFunc(r.Val, depth+1)
					fns = append(fns, f...)
					ok = ok && sok
				}
			case *ssa.UnOp, *ssa.DebugRef:
				// a load, or debug info
			case *ssa.MakeClosure:
				// Captured by reference: the closure's own stores through the
				// free variable are stores into the variable too.
				for i, b := range r.Bindings {
					if b != ssa.Value(alloc) {
						continue
					}
					clo := r.Fn.(*ssa.Function)
					if i >= len(clo.FreeVars) {
						return nil, false
					}
					for _, blk := range clo.Blocks {
						for _, in := range blk.Instrs {
							switch in := in.(type) {
							case *ssa.Store:
								if in.Addr == ssa.Value(clo.FreeVars[i]) {
									f, sok := a.resolveFunc(in.Val, depth+1)
									fns = append(fns, f...)
									ok = ok && sok
								}
							case *ssa.MakeClosure:
								for _, bb := range in.Bindings {
									if bb == ssa.Value(clo.FreeVars[i]) {
										return nil, false // captured again, deeper
									}
								}
							}
						}
					}
				}
			default:
				return nil, false // its address goes somewhere this does not follow
			}
		}
		return fns, ok
	case *ssa.Call:
		c := v.Call.StaticCallee()
		if c == nil || v.Call.IsInvoke() {
			return nil, false
		}
		if !a.inModule(c) {
			// A function outside the module can hand back only what it was
			// given or made itself: with no func or interface argument there
			// is none of ours in it.
			for _, arg := range v.Call.Args {
				u := arg.Type().Underlying()
				if _, isFn := u.(*types.Signature); isFn {
					return nil, false
				}
				if types.IsInterface(arg.Type()) {
					return nil, false
				}
			}
			return nil, true
		}
		if len(c.Blocks) == 0 {
			return nil, false
		}
		ok = true
		for _, b := range c.Blocks {
			if r, isRet := b.Instrs[len(b.Instrs)-1].(*ssa.Return); isRet {
				if len(r.Results) != 1 {
					return nil, false
				}
				f, rok := a.resolveFunc(r.Results[0], depth+1)
				fns = append(fns, f...)
				ok = ok && rok
			}
		}
		return fns, ok
	case *ssa.Phi:
		ok = true
		for _, e := range v.Edges {
			f, eok := a.resolveFunc(e, depth+1)
			fns = append(fns, f...)
			ok = ok && eok
		}
		return fns, ok
	case *ssa.FreeVar:
		fn := v.Parent()
		idx := -1
		for i, fv := range fn.FreeVars {
			if fv == v {
				idx = i
			}
		}
		parent := fn.Parent()
		if idx < 0 || parent == nil {
			return nil, false
		}
		found, ok := false, true
		for _, b := range parent.Blocks {
			for _, in := range b.Instrs {
				mc, isMC := in.(*ssa.MakeClosure)
				if !isMC || mc.Fn != fn {
					continue
				}
				found = true
				f, fok := a.resolveFunc(mc.Bindings[idx], depth+1)
				fns = append(fns, f...)
				ok = ok && fok
			}
		}
		return fns, found && ok
	}
	return nil, false
}

// passedValues connects a call to the func and interface values it passes out,
// for every kind of call. The call graph cannot say which callbacks reach which
// caller of a shared helper (registry.RunItems, errgroup.Go, sync.Once.Do,
// context.AfterFunc, or a store's Update(func(tx) ...)), so the values the call
// names are followed directly:
//
//   - a function literal (MakeClosure) or a named function: an edge to its body;
//   - a func that is a parameter of the caller: skipped, it is followed where
//     the caller's own caller passed it;
//   - a maintenance value made into an interface for a function outside the
//     module: an edge to every method of its type, since that function may call
//     any of them;
//   - any other func argument (read from a field, returned by a call, a captured
//     variable): reported as unresolved.
func (a *analysis) passedValues(fn *ssa.Function, call ssa.CallInstruction, push func(fn, from *ssa.Function),
	rep *report, unresolvedSeen map[string]bool) {

	cc := call.Common()
	callee := cc.StaticCallee()
	external := callee == nil || !a.inModule(callee)
	callName := "(dynamic)"
	switch {
	case callee != nil:
		callName = callee.String()
	case cc.IsInvoke():
		callName = "(" + typeName(cc.Value.Type(), a.maint) + ")." + cc.Method.Name()
	}
	for _, v := range cc.Args {
		if _, isFunc := v.Type().Underlying().(*types.Signature); isFunc {
			fns, ok := a.resolveFunc(v, 0)
			for _, f := range fns {
				push(f, fn)
			}
			// Only the maintenance package's own code is held to resolving
			// what it passes: it is where our callbacks are made. Code of the
			// module's other packages forwards what it was given, which was
			// followed at the call that gave it.
			if !ok && a.inMaint(fn) {
				a.addUnresolved(rep, unresolvedSeen, fn, callName, call, v)
			}
			continue
		}
		if a.carriesFunc(v.Type(), 0) {
			// A struct passed by value, or the slice built for variadic
			// arguments, that holds func fields (registry.RunItemsOptions: Label,
			// CheckpointFn, CheckpointStateFn): the funcs stored into it are
			// callbacks of ours as much as a bare func argument is.
			fns, ok := a.resolveAggregate(v, 0)
			for _, f := range fns {
				push(f, fn)
			}
			if !ok && a.inMaint(fn) {
				a.addUnresolved(rep, unresolvedSeen, fn, callName, call, v)
			}
			continue
		}
		if mi, ok := v.(*ssa.MakeInterface); ok && external && a.isMaintType(mi.X.Type()) {
			ms := a.prog.MethodSets.MethodSet(mi.X.Type())
			for i := 0; i < ms.Len(); i++ {
				push(a.prog.MethodValue(ms.At(i)), fn)
			}
		}
	}
}

func (a *analysis) name(fn *ssa.Function) string {
	return fn.RelString(a.maint)
}

func (a *analysis) pos(p token.Pos) string {
	if !p.IsValid() {
		return ""
	}
	ps := a.prog.Fset.Position(p)
	if rel, err := filepath.Rel(a.repo, ps.Filename); err == nil {
		ps.Filename = filepath.ToSlash(rel)
	}
	return fmt.Sprintf("%s:%d", ps.Filename, ps.Line)
}

// path is a call path from a root to fn, root first.
func (a *analysis) path(fn *ssa.Function, parent map[*ssa.Function]*ssa.Function) []string {
	var rev []string
	for f := fn; f != nil; f = parent[f] {
		rev = append(rev, a.name(f))
		if len(rev) > 64 {
			break
		}
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}
