// file: tools/fixerstores/main.go
// version: 1.0.0
// guid: 4e7c1a93-d2b8-4f60-8a15-6c3b9e0d7f24
// last-edited: 2026-11-12

// Command fixerstores answers one question about the repairs fixers of
// internal/plugins/maintenance: which of the plugin's store accessors can a
// fixer's Plan, Replan, Apply, BeginApply or AfterApply reach other than
// through the shell's FixerStores?
//
// A fixer that runs under the native shell must read and write only the stores
// the shell bound to its context (FixerStores). The plugin's own deps
// (ServerDeps) hand out the same accessors, and a fixer that calls one of them
// reads HEAD's store instead of the shell's. The tripwire tests in
// stores_fixer_test.go catch that for the paths they execute; this pass is the
// static complement that covers the paths a test does not reach.
//
// How: it loads the maintenance package and everything it imports, builds SSA
// and a call graph with variable type analysis (VTA, seeded by CHA), and walks
// from the roots (the five methods above, of every concrete type that
// implements repairs.Fixer). Every reachable function of the maintenance
// package is searched for calls (or method values) of
//
//   - one of the FixerStores accessors on anything that is not FixerStores: a
//     "store sink", reported with the call path that reaches it, and
//   - any other ServerDeps method: a "dep", which is not a store but is still
//     something a fixer reaches through the plugin's captured deps and not
//     through the shell.
//
// The output is JSON for the guard test in internal/plugins/maintenance, which
// compares it with a reviewed allowlist.
//
// Limits, which is why this complements the tripwire tests and does not
// replace them:
//
//   - VTA is unsound for reflection: a call made through reflect, or a method
//     found by name, is not an edge.
//   - VTA is imprecise for function values stored in struct fields: a func
//     stored in a field and called later has every func of its type as a
//     possible callee or, for some shapes, none. It can both add callees that
//     never run (false positives, which the allowlist records with a reason)
//     and miss ones that do.
//   - Traversal stops at the first function outside the maintenance package.
//     What such a function calls back is found only where a func value reaches
//     it as an argument and that argument is statically a function literal or
//     a named function (see passedFuncs); an argument that is a parameter or a
//     field is reported as unresolved, not followed.
//   - The generic and synchronisation helpers (sync.Once.Do, errgroup,
//     registry.RunItems, context.AfterFunc) are exactly such functions. The
//     call graph's own edges through them join every caller to every
//     callback, so they are not followed at all: the argument rule connects
//     the caller to the callbacks it names, and nothing else.
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

// rootMethods are the fixer entry points: the repairs.Fixer methods and the
// optional ApplyScoped / follow-up hooks the shell calls.
var rootMethods = []string{"Plan", "Replan", "Apply", "BeginApply", "AfterApply"}

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

// traversable is a function the walk follows: one of the maintenance package,
// or a wrapper the compiler made (a promoted method, a bound method value),
// which has no package of its own and only forwards.
func (a *analysis) traversable(fn *ssa.Function) bool {
	return a.inMaint(fn) || (fn != nil && fn.Pkg == nil && fn.Synthetic != "")
}

// isSearched reports whether fn's own body is searched for sinks: a function
// of the maintenance package that the compiler did not synthesize. A wrapper
// for a promoted method (pluginFixerStores.OpsStore) calls the accessor on
// ServerDeps by construction; that is the fallback the shell replaces, not a
// call a fixer wrote.
func (a *analysis) isSearched(fn *ssa.Function) bool {
	return a.inMaint(fn) && fn.Synthetic == ""
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

	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]

		if node := graph.Nodes[fn]; node != nil {
			for _, e := range node.Out {
				// Only functions of the maintenance package are followed (see
				// traversable): the join points outside it connect every
				// caller to every callback, which passedFuncs does precisely.
				push(e.Callee.Func, fn)
			}
		}
		if !a.isSearched(fn) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, in := range b.Instrs {
				a.scan(fn, in, push, rep, depCallers, sinkSeen, parent)
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
		if rep.Unresolved[i].Pos != rep.Unresolved[j].Pos {
			return rep.Unresolved[i].Pos < rep.Unresolved[j].Pos
		}
		return rep.Unresolved[i].Callee < rep.Unresolved[j].Callee
	})
	return rep
}

// scan searches one instruction of a searched function: accessor and dep
// calls, and the func values it passes out to functions of other packages.
func (a *analysis) scan(fn *ssa.Function, in ssa.Instruction, push func(fn, from *ssa.Function),
	rep *report, depCallers map[string]map[string]bool, sinkSeen map[string]bool, parent map[*ssa.Function]*ssa.Function) {

	pos := func() string { return a.pos(in.Pos()) }

	// A call (or go / defer) through an interface method.
	if call, ok := in.(ssa.CallInstruction); ok {
		cc := call.Common()
		if cc.IsInvoke() {
			a.method(fn, cc.Method, cc.Value.Type(), pos(), rep, depCallers, sinkSeen, parent)
		} else {
			a.passedFuncs(fn, call, push, rep)
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

// method classifies a call of interface method m on a receiver of type recv.
func (a *analysis) method(fn *ssa.Function, m *types.Func, recv types.Type, pos string,
	rep *report, depCallers map[string]map[string]bool, sinkSeen map[string]bool, parent map[*ssa.Function]*ssa.Function) {

	for _, si := range a.storeIfaces {
		if types.Identical(recv, si) {
			return // the shell's own stores: the right way to read one
		}
	}
	name := m.Name()
	if a.accessors[name] {
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

// passedFuncs connects a call to the func values it passes. A call of a
// function outside the maintenance package (registry.RunItems, errgroup.Go,
// sync.Once.Do, context.AfterFunc) runs the callback its caller wrote, but the
// call graph cannot say which callbacks reach which caller of a shared generic
// or synchronising helper, so the callbacks the call names are followed
// directly: a function literal (MakeClosure) or a named function. An argument
// that is neither is reported.
func (a *analysis) passedFuncs(fn *ssa.Function, call ssa.CallInstruction, push func(fn, from *ssa.Function), rep *report) {
	cc := call.Common()
	callee := cc.StaticCallee()
	if callee != nil && a.inMaint(callee) {
		return // followed by the call graph
	}
	callName := "(dynamic)"
	if callee != nil {
		callName = callee.String()
	}
	var args []ssa.Value
	args = append(args, cc.Args...)
	for _, v := range args {
		if _, isFunc := v.Type().Underlying().(*types.Signature); !isFunc {
			continue
		}
		switch v := v.(type) {
		case *ssa.MakeClosure:
			push(v.Fn.(*ssa.Function), fn)
		case *ssa.Function:
			push(v, fn)
		case *ssa.Const:
			// nil
		default:
			rep.Unresolved = append(rep.Unresolved, unresolve{
				Caller: a.name(fn), Callee: callName, Pos: a.pos(call.Pos()),
			})
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
