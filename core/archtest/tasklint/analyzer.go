// file: core/archtest/tasklint/analyzer.go
// version: 2.3.0
// guid: 48295d11-35d4-4ecf-b2e5-cb82b9451d3d
// last-edited: 2026-11-10

// Package tasklint is the type-aware go/analysis check of core/tasks
// definitions (05-tasks §5T.8.4), moved from ABO's tools/cmd/oplint (04 §8.2).
// It is test-support (03 §3.3 archtest row, R4): apps run it from their own
// tests through Run, and no non-test package imports it.
//
// Its six rules, by diagnostic category:
//
//	unregistered-kind       a tasks.Task, tasks.Batch or tasks.Fixer call, or a
//	                        call of a helper returning tasks.Definition that
//	                        builds one, that no tasks.Definition argument of a
//	                        Registrar.Tasks or Registrar.DerivedIndex call reaches,
//	                        following what values are built from (a function
//	                        stands for its returns, so a kind built and
//	                        discarded is reported)
//	                        (nor, with -list-funcs, a called package-level func
//	                        returning []tasks.Definition: 05-tasks §5T.8.2 rule
//	                        4's books list functions, until the F5 flip). Run
//	                        checks it over every loaded package at once
//	                        (registration.go); this analyzer alone sees one
//	                        package, so it also flags a kind another package
//	                        registers
//	writer-bypass           a write-named method call on a type declared in a
//	                        store package (-stores) inside a definition body or a
//	                        same-package function it calls (one level deep);
//	                        "//tasklint:allow <reason>" on the call line records
//	                        a reviewed exception
//	store-capture           a definition body (or a same-package function it
//	                        calls, one level deep) reaching a value that is or
//	                        implements a store package's Store interface through
//	                        a variable it captures: a closure variable, a package
//	                        variable or the receiver of a method value
//	missing-help            (warning) a tasks.Common literal with no Help, or a
//	                        spec literal setting Common's fields as promoted
//	                        fields without one
//	sequential-empty-reason tasks.Sequential with an empty or blank reason
//	deletes-owner-review    every Effects.AlsoDeletes call; never silently
//	                        allowed, "//tasklint:allow <reason>" records the
//	                        owner's sign-off
//
// A definition body is the Run, Source, Item, Finish, Candidates, Load,
// Evaluate, Apply or AfterApply field of a tasks.TaskSpec, BatchSpec or
// FixerSpec literal, or of a literal of a type -legacy-defs names. Packages
// outside -module, the tasks package, its test kit and _test.go files are never
// reported.
package tasklint

import (
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

// Diagnostic categories, one per rule (05-tasks §5T.8.4). Only CatMissingHelp
// is a warning; Run fails on every other one.
const (
	CatUnregisteredKind   = "unregistered-kind"
	CatWriterBypass       = "writer-bypass"
	CatStoreCapture       = "store-capture"
	CatMissingHelp        = "missing-help"
	CatSequentialReason   = "sequential-empty-reason"
	CatDeletesOwnerReview = "deletes-owner-review"
)

// allowDirective is HEAD's //oplint:allow, renamed with the analyzer.
const allowDirective = "//tasklint:allow"

// Config holds every path the analyzer would otherwise hard-code. Each field
// is also an Analyzer flag (06 §6.6, §6.11.4 row H).
type Config struct {
	// ModulePath is the module whose packages are reported (-module).
	ModulePath string
	// TasksPath is core's tasks package (-tasks).
	TasksPath string
	// TasksTestPath is its test kit, never reported (-taskstest).
	TasksTestPath string
	// RegistrarPath is the package declaring Registrar, whose Tasks method
	// registers kinds (-registrar).
	RegistrarPath string
	// StorePackages are the module's store packages: a write-named method of
	// a type declared in one (or below it) is a module write, and a value
	// implementing one's Store interface is a store (-stores, comma separated).
	StorePackages []string
	// LegacyDefinitions are "importpath.TypeName" types whose literals also
	// count as definitions for writer-bypass and store-capture (-legacy-defs,
	// comma separated): the not-yet-converted forms of an app's engine. Only
	// books sets it, until the F5 flip deletes HEAD's engine.
	LegacyDefinitions []string
	// ListFuncs makes every package-level func whose one result is
	// []tasks.Definition a registration root when something calls it
	// (-list-funcs): books' list functions, which ABO passes to no
	// Registrar.Tasks call before the F5 flip (05-tasks §5T.8.2 rule 4,
	// §5T.8.4).
	ListFuncs bool
	// Constructors lists the functions ("importpath.Func" or
	// "importpath.Type.Method") allowed to carry ConstructorDirective
	// (-constructors, comma separated): a reviewed allowlist, so a new
	// constructor is a visible change to the caller's test. Run also reports
	// an entry that matches no directive in the loaded packages.
	Constructors []string

	// wholeProgram is set by Analyze, which checks unregistered-kind over all
	// loaded packages at once; the per-package passes then skip it.
	wholeProgram bool
}

// DefaultConfig is core's own configuration: module and tasks paths of
// github.com/falkcorp/mediacore, no store packages (a core kind writes core
// state through its owner's API, never a module store), no legacy forms and
// no list functions.
func DefaultConfig() Config {
	const core = "github.com/falkcorp/mediacore"
	return Config{
		ModulePath:    core,
		TasksPath:     core + "/tasks",
		TasksTestPath: core + "/tasks/taskstest",
		RegistrarPath: core + "/module",
	}
}

// listFlag is a comma-separated string list flag.
type listFlag struct{ p *[]string }

func (l listFlag) String() string {
	if l.p == nil {
		return ""
	}
	return strings.Join(*l.p, ",")
}

func (l listFlag) Set(s string) error {
	*l.p = nil
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*l.p = append(*l.p, part)
		}
	}
	return nil
}

// New returns an analyzer over its own copy of cfg; its Flags set that copy,
// so two analyzers never share state.
func New(cfg Config) *analysis.Analyzer {
	c := cfg
	c.StorePackages = slices.Clone(cfg.StorePackages)
	c.LegacyDefinitions = slices.Clone(cfg.LegacyDefinitions)
	c.Constructors = slices.Clone(cfg.Constructors)
	a := &analysis.Analyzer{
		Name: "tasklint",
		Doc:  "check core/tasks definitions (05-tasks §5T.8.4); rules in the package documentation",
		Run:  func(pass *analysis.Pass) (any, error) { return runPass(pass, &c) },
	}
	fs := &a.Flags
	fs.StringVar(&c.ModulePath, "module", c.ModulePath, "the module whose packages are reported")
	fs.StringVar(&c.TasksPath, "tasks", c.TasksPath, "import path of core's tasks package")
	fs.StringVar(&c.TasksTestPath, "taskstest", c.TasksTestPath, "import path of the tasks test kit (never reported)")
	fs.StringVar(&c.RegistrarPath, "registrar", c.RegistrarPath, "import path of the package declaring Registrar")
	fs.Var(listFlag{&c.StorePackages}, "stores", "comma-separated store packages (writer-bypass, store-capture)")
	fs.Var(listFlag{&c.LegacyDefinitions}, "legacy-defs", `comma-separated "importpath.Type" legacy definition types`)
	fs.Var(listFlag{&c.Constructors}, "constructors", `comma-separated functions allowed to carry //tasklint:constructor ("importpath.Func")`)
	fs.BoolVar(&c.ListFuncs, "list-funcs", c.ListFuncs, "treat package-level funcs returning []tasks.Definition as registration roots")
	return a
}

// Analyzer returns a fresh tasklint analyzer over DefaultConfig, the
// spec-named entry point (05-tasks §5T.8.4); set its Flags for another module,
// or call New. It is a function, not HEAD's package variable, because 03 R5
// (archtest.NoInit) forbids a package-level pointer in core.
func Analyzer() *analysis.Analyzer { return New(DefaultConfig()) }

// Ensure listFlag is a flag.Value.
var _ flag.Value = listFlag{}

// bodyFields are the definition fields whose function is a definition body
// (05-tasks §5T.8.4 store-capture's list, plus Fixer's AfterApply).
func bodyField(name string) bool {
	switch name {
	case "Run", "Source", "Item", "Finish", "Candidates", "Load", "Evaluate", "Apply", "AfterApply":
		return true
	}
	return false
}

// writeName reports a method name that makes a store call a write.
func writeName(name string) bool {
	for _, pre := range []string{
		"Create", "Update", "Delete", "Set", "Modify", "Upsert", "Insert", "Put",
		"Add", "Remove", "Save", "Mark", "Record", "Repoint", "Merge",
	} {
		if strings.HasPrefix(name, pre) {
			return true
		}
	}
	return false
}

func under(path, root string) bool {
	return root != "" && (path == root || strings.HasPrefix(path, root+"/"))
}

func runPass(pass *analysis.Pass, cfg *Config) (any, error) {
	path := pass.Pkg.Path()
	if !under(path, cfg.ModulePath) || under(path, cfg.TasksPath) || under(path, cfg.TasksTestPath) {
		return nil, nil
	}
	c := &lintState{pass: pass, cfg: cfg, decls: map[types.Object]ast.Node{},
		allows: map[string]map[int]string{}, stores: map[string]*types.Interface{}}
	for _, f := range pass.Files {
		name := pass.Fset.File(f.Pos()).Name()
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		c.files = append(c.files, f)
		c.indexFile(f, name)
	}
	if len(c.files) == 0 {
		return nil, nil
	}
	if !cfg.wholeProgram {
		for _, f := range unregisteredKinds(cfg, []*unit{{path: path, files: c.files, info: pass.TypesInfo}}, false) {
			pass.Report(analysis.Diagnostic{Pos: f.pos, Category: CatUnregisteredKind, Message: f.msg})
		}
	}
	c.walkCalls()
	c.specLiterals()
	return nil, nil
}

type lintState struct {
	pass  *analysis.Pass
	cfg   *Config
	files []*ast.File
	// decls maps each package-level func, method and var object to its
	// declaration: the *ast.FuncDecl, the var's own initializer expression,
	// or the *ast.ValueSpec when one call initializes several vars.
	decls map[types.Object]ast.Node
	// allows maps file name -> line -> the text after //tasklint:allow.
	allows map[string]map[int]string
	// stores caches each store package's Store interface (nil when absent).
	stores map[string]*types.Interface
}

func (c *lintState) indexFile(f *ast.File, name string) {
	info := c.pass.TypesInfo
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if obj := info.Defs[d.Name]; obj != nil {
				c.decls[obj] = d
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					obj := info.Defs[n]
					switch {
					case obj == nil:
					case len(vs.Values) == len(vs.Names):
						c.decls[obj] = vs.Values[i] // var a, b = x, y: each its own
					default:
						c.decls[obj] = vs
					}
				}
			}
		}
	}
	for _, cg := range f.Comments {
		for _, cm := range cg.List {
			if !strings.HasPrefix(cm.Text, allowDirective) {
				continue
			}
			rest := strings.TrimPrefix(cm.Text, allowDirective)
			if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
				continue // e.g. //tasklint:allowed -- not the directive
			}
			// A second "//" starts another remark, not the reason.
			if i := strings.Index(rest, "//"); i >= 0 {
				rest = rest[:i]
			}
			if c.allows[name] == nil {
				c.allows[name] = map[int]string{}
			}
			c.allows[name][c.pass.Fset.Position(cm.Pos()).Line] = strings.TrimSpace(rest)
		}
	}
}

// namedIn returns the origin named type of t (or *t) when it is declared in
// package path.
func namedIn(t types.Type, path string) (*types.Named, bool) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return nil, false
	}
	n = n.Origin()
	if n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != path {
		return nil, false
	}
	return n, true
}

// tasksFunc returns the name of the tasks package-level function call
// invokes, or "".
func (c *lintState) tasksFunc(call *ast.CallExpr) string {
	fn, _ := typeutil.Callee(c.pass.TypesInfo, call).(*types.Func)
	return tasksFuncName(fn, c.cfg.TasksPath)
}

// tasksFuncName returns fn's name when it is a package-level function of
// package tasksPath, or "".
func tasksFuncName(fn *types.Func, tasksPath string) string {
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != tasksPath {
		return ""
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return ""
	}
	return fn.Name()
}

// method returns the receiver's named type and the method a call invokes,
// when it is a method of a type declared in package path.
func (c *lintState) method(call *ast.CallExpr, path string) (recv string, name string) {
	return methodIn(c.pass.TypesInfo, call, path)
}

func methodIn(info *types.Info, call *ast.CallExpr, path string) (recv string, name string) {
	fn, ok := typeutil.Callee(info, call).(*types.Func)
	if !ok {
		return "", ""
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return "", ""
	}
	n, ok := namedIn(sig.Recv().Type(), path)
	if !ok {
		return "", ""
	}
	return n.Obj().Name(), fn.Name()
}

func isConstructor(name string) bool { return name == "Task" || name == "Batch" || name == "Fixer" }

// isListFunc reports a package-level func whose single result is
// []tasks.Definition.
func isListFunc(info *types.Info, tasksPath string, fd *ast.FuncDecl) bool {
	if fd.Recv != nil {
		return false
	}
	obj, ok := info.Defs[fd.Name].(*types.Func)
	if !ok {
		return false
	}
	res := obj.Type().(*types.Signature).Results()
	if res.Len() != 1 {
		return false
	}
	sl, ok := res.At(0).Type().(*types.Slice)
	if !ok {
		return false
	}
	n, ok := namedIn(sl.Elem(), tasksPath)
	return ok && n.Obj().Name() == "Definition"
}

func declName(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, s := range d.Specs {
			if vs, ok := s.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
				return vs.Names[0].Name
			}
		}
	}
	return "a declaration"
}

// walkCalls applies the call-shaped rules sequential-empty-reason and
// deletes-owner-review to every non-test file.
func (c *lintState) walkCalls() {
	for _, f := range c.files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if c.tasksFunc(call) == "Sequential" && len(call.Args) == 1 {
				if tv, ok := c.pass.TypesInfo.Types[call.Args[0]]; ok && tv.Value != nil && tv.Value.Kind() == constant.String &&
					strings.TrimSpace(constant.StringVal(tv.Value)) == "" {
					c.pass.Report(analysis.Diagnostic{Pos: call.Pos(), Category: CatSequentialReason,
						Message: `tasks.Sequential needs a reason: say why this work cannot use a worker pool`})
				}
			}
			if recv, name := c.method(call, c.cfg.TasksPath); recv == "Effects" && name == "AlsoDeletes" && !c.allowed(call, CatDeletesOwnerReview) {
				c.pass.Report(analysis.Diagnostic{Pos: call.Pos(), Category: CatDeletesOwnerReview,
					Message: "Effects.AlsoDeletes needs owner review (standing rule: never delete book_file rows); record the sign-off with //tasklint:allow <reason>"})
			}
			return true
		})
	}
}

// allowed reports whether call's line carries //tasklint:allow with a reason.
// A directive with no reason is itself reported, under category.
func (c *lintState) allowed(call *ast.CallExpr, category string) bool {
	pos := c.pass.Fset.Position(call.Pos())
	lines := c.allows[pos.Filename]
	if lines == nil {
		return false
	}
	reason, ok := lines[pos.Line]
	if !ok {
		reason, ok = lines[c.pass.Fset.Position(call.Lparen).Line]
	}
	if !ok {
		return false
	}
	if reason == "" {
		c.pass.Report(analysis.Diagnostic{Pos: call.Pos(), Category: category,
			Message: "//tasklint:allow needs a reason after it"})
	}
	return true
}

// isDefinitionType reports a tasks spec type or a configured legacy type.
func (c *lintState) isDefinitionType(t types.Type) bool {
	if n, ok := namedIn(t, c.cfg.TasksPath); ok {
		switch n.Obj().Name() {
		case "TaskSpec", "BatchSpec", "FixerSpec":
			return true
		}
		return false
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return false
	}
	return slices.Contains(c.cfg.LegacyDefinitions, n.Origin().Obj().Pkg().Path()+"."+n.Obj().Name())
}

// specLiterals applies writer-bypass and store-capture to every definition
// literal's bodies and missing-help to every tasks.Common literal.
func (c *lintState) specLiterals() {
	// One reported set per rule: a store write through a captured store is
	// both findings, at the same position.
	bypassed, captures := map[token.Pos]bool{}, map[token.Pos]bool{}
	for _, f := range c.files {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			tv, ok := c.pass.TypesInfo.Types[lit]
			if !ok {
				return true
			}
			if n, ok := namedIn(tv.Type, c.cfg.TasksPath); ok && n.Obj().Name() == "Common" {
				c.missingHelp(lit, tv.Type)
				return true
			}
			if !c.isDefinitionType(tv.Type) {
				return true
			}
			c.promotedHelp(lit, tv.Type)
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, _ := kv.Key.(*ast.Ident)
				if key == nil || !bodyField(key.Name) {
					continue
				}
				switch v := kv.Value.(type) {
				case *ast.FuncLit:
					c.writerBypass(v.Body, key.Name, 0, map[ast.Node]bool{}, bypassed)
					c.storeCapture(v.Body, v, nil, key.Name, 0, map[ast.Node]bool{}, captures)
				default:
					if fd := c.samePackageDecl(v); fd != nil && fd.Body != nil {
						c.writerBypass(fd.Body, key.Name, 0, map[ast.Node]bool{fd: true}, bypassed)
						c.storeCapture(fd.Body, fd, receiverOf(c.pass.TypesInfo, fd), key.Name, 0, map[ast.Node]bool{fd: true}, captures)
					}
				}
			}
			return true
		})
	}
}

// samePackageDecl returns the declaration of the same-package function or
// method e names (an identifier, a selector, or a call's callee), or nil.
func (c *lintState) samePackageDecl(e ast.Expr) *ast.FuncDecl {
	var obj types.Object
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		obj = c.pass.TypesInfo.Uses[e]
	case *ast.SelectorExpr:
		obj = c.pass.TypesInfo.Uses[e.Sel]
	case *ast.CallExpr:
		obj = typeutil.Callee(c.pass.TypesInfo, e)
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() != c.pass.Pkg {
		return nil
	}
	fd, _ := c.decls[fn.Origin()].(*ast.FuncDecl)
	return fd
}

// writerBypass reports store writes in body, following same-package calls
// one level deep (depth 0 is the definition's own body).
func (c *lintState) writerBypass(body ast.Node, field string, depth int, visited map[ast.Node]bool, reported map[token.Pos]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := typeutil.Callee(c.pass.TypesInfo, call).(*types.Func); ok && c.isStoreWrite(fn) {
			if reported[call.Pos()] {
				return true
			}
			reported[call.Pos()] = true
			if c.allowed(call, CatWriterBypass) {
				return true
			}
			via := ""
			if depth > 0 {
				via = " (via a function it calls)"
			}
			c.pass.Report(analysis.Diagnostic{Pos: call.Pos(), Category: CatWriterBypass,
				Message: fmt.Sprintf("%s.%s writes the store directly inside %s%s; write through rc.Writer() (tasks.Writer) so the write is fenced, previewed and recorded, or add //tasklint:allow <reason>", recvName(fn), fn.Name(), field, via)})
			return true
		}
		if depth == 0 {
			if fd := c.samePackageDecl(call); fd != nil && fd.Body != nil && !visited[fd] {
				visited[fd] = true
				c.writerBypass(fd.Body, field, depth+1, visited, reported)
			}
		}
		return true
	})
}

// isStoreWrite reports a method declared in a store package (or below one)
// whose name is write-named.
func (c *lintState) isStoreWrite(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil || fn.Pkg() == nil {
		return false
	}
	p := fn.Pkg().Path()
	for _, sp := range c.cfg.StorePackages {
		if under(p, sp) {
			return writeName(fn.Name())
		}
	}
	return false
}

func recvName(fn *types.Func) string {
	t := fn.Type().(*types.Signature).Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := types.Unalias(t).(*types.Named); ok {
		return n.Obj().Pkg().Name() + "." + n.Obj().Name()
	}
	return "store"
}

// storeIfaces returns the Store interface of every store package the package
// can see (directly or transitively imported).
func (c *lintState) storeIfaces() []*types.Interface {
	var out []*types.Interface
	for _, sp := range c.cfg.StorePackages {
		st, done := c.stores[sp]
		if !done {
			st = lookupStore(c.pass.Pkg, sp)
			c.stores[sp] = st
		}
		if st != nil {
			out = append(out, st)
		}
	}
	return out
}

func lookupStore(root *types.Package, path string) *types.Interface {
	seen := map[*types.Package]bool{}
	var visit func(p *types.Package) *types.Interface
	visit = func(p *types.Package) *types.Interface {
		if seen[p] {
			return nil
		}
		seen[p] = true
		if p.Path() == path {
			if tn, ok := p.Scope().Lookup("Store").(*types.TypeName); ok {
				st, _ := tn.Type().Underlying().(*types.Interface)
				return st
			}
			return nil
		}
		for _, imp := range p.Imports() {
			if st := visit(imp); st != nil {
				return st
			}
		}
		return nil
	}
	return visit(root)
}

// isStore reports whether t is, embeds or implements a store package's Store.
func (c *lintState) isStore(t types.Type) bool {
	if t == nil {
		return false
	}
	for _, st := range c.storeIfaces() {
		if types.Implements(t, st) {
			return true
		}
		if _, isPtr := t.(*types.Pointer); !isPtr {
			if _, isIface := t.Underlying().(*types.Interface); !isIface && types.Implements(types.NewPointer(t), st) {
				return true
			}
		}
	}
	return false
}

// receiverOf returns the receiver variable of a method declaration, or nil.
func receiverOf(info *types.Info, fd *ast.FuncDecl) *types.Var {
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return nil
	}
	v, _ := info.Defs[fd.Recv.List[0].Names[0]].(*types.Var)
	return v
}

// rootVar returns the variable an expression's selector chain starts from
// (d in d.Books.Store), or nil.
func (c *lintState) rootVar(e ast.Expr) *types.Var {
	for {
		switch x := ast.Unparen(e).(type) {
		case *ast.SelectorExpr:
			if _, isPkg := c.pass.TypesInfo.Uses[identOf(x.X)].(*types.PkgName); isPkg {
				v, _ := c.pass.TypesInfo.Uses[x.Sel].(*types.Var)
				return v // a qualified package variable
			}
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.Ident:
			v, _ := c.pass.TypesInfo.Uses[x].(*types.Var)
			return v
		default:
			return nil
		}
	}
}

func identOf(e ast.Expr) *ast.Ident {
	id, _ := ast.Unparen(e).(*ast.Ident)
	return id
}

// storeCapture reports every expression in body whose type is a store and
// whose selector chain starts from a variable captured by scope: declared
// outside it (a closure variable or a package variable) or scope's receiver.
// It follows same-package calls one level deep (rule store-capture).
func (c *lintState) storeCapture(body ast.Node, scope ast.Node, recv *types.Var, field string, depth int, visited map[ast.Node]bool, reported map[token.Pos]bool) {
	if len(c.storeIfaces()) == 0 {
		return
	}
	captured := func(v *types.Var) bool {
		if v == nil || v.IsField() {
			return false
		}
		if v == recv {
			return true
		}
		return v.Pos() < scope.Pos() || v.Pos() >= scope.End()
	}
	report := func(e ast.Expr, v *types.Var) {
		if reported[e.Pos()] {
			return
		}
		reported[e.Pos()] = true
		via := ""
		if depth > 0 {
			via = " (via a function it calls)"
		}
		c.pass.Report(analysis.Diagnostic{Pos: e.Pos(), Category: CatStoreCapture,
			Message: fmt.Sprintf("%s captures %s, a store, through %s%s; take the narrow interface the body calls from the dependencies its owner declares, and write through rc.Writer()", field, exprString(e), v.Name(), via)})
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr, *ast.Ident:
			e := x.(ast.Expr)
			tv, ok := c.pass.TypesInfo.Types[e]
			if !ok || !tv.IsValue() {
				return true
			}
			if v := c.rootVar(e); captured(v) && c.isStore(tv.Type) {
				report(e, v)
				return false
			}
		case *ast.CallExpr:
			if depth == 0 {
				if fd := c.samePackageDecl(x); fd != nil && fd.Body != nil && !visited[fd] {
					visited[fd] = true
					c.storeCapture(fd.Body, fd, receiverOf(c.pass.TypesInfo, fd), field, depth+1, visited, reported)
				}
			}
		}
		return true
	})
}

func exprString(e ast.Expr) string {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	}
	return "an expression"
}

// missingHelp warns on a tasks.Common literal with no Help, or a constant
// blank one (rule missing-help).
// promotedHelp applies missing-help to a core spec literal that sets Common's
// fields as promoted fields (TaskSpec{Title: ..., Help: ...}) instead of
// through a Common key; with a Common key, the Common literal is checked.
func (c *lintState) promotedHelp(lit *ast.CompositeLit, t types.Type) {
	n, ok := namedIn(t, c.cfg.TasksPath)
	if !ok || len(lit.Elts) == 0 {
		return
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return // positional: Common is the first element, a literal or not
		}
		if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Common" {
			return
		}
	}
	c.missingHelp(lit, n)
}

func (c *lintState) missingHelp(lit *ast.CompositeLit, t types.Type) {
	var help ast.Expr
	found := false
	for i, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Help" {
				help, found = kv.Value, true
			}
			continue
		}
		// Positional literal: Help is the struct's field of that name.
		if st, ok := t.Underlying().(*types.Struct); ok && i < st.NumFields() && st.Field(i).Name() == "Help" {
			help, found = el, true
		}
	}
	if found {
		tv, ok := c.pass.TypesInfo.Types[help]
		if !ok || tv.Value == nil || tv.Value.Kind() != constant.String || strings.TrimSpace(constant.StringVal(tv.Value)) != "" {
			return
		}
	}
	name := "tasks.Common"
	if n, ok := namedIn(t, c.cfg.TasksPath); ok {
		name = "tasks." + n.Obj().Name()
	}
	c.pass.Report(analysis.Diagnostic{Pos: lit.Pos(), Category: CatMissingHelp,
		Message: "warning: " + name + " has no Help; give the kind list and the generated form a 1-3 sentence description"})
}
