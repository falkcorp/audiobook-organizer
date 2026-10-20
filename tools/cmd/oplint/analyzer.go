// file: tools/cmd/oplint/analyzer.go
// version: 1.0.1
// guid: 48295d11-35d4-4ecf-b2e5-cb82b9451d3d
// last-edited: 2026-10-19

package main

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

// writePrefixes are the method-name prefixes that make a call on a store
// declared in internal/database a write (rule writer-bypass). Tune here.
var writePrefixes = []string{
	"Create", "Update", "Delete", "Set", "Modify", "Upsert", "Insert", "Put",
	"Add", "Remove", "Save", "Mark", "Record", "Repoint", "Merge",
}

// writerFields are the ops.*Spec fields whose function runs with a Writer in
// scope; a store write inside one bypasses it (rule writer-bypass).
var writerFields = map[string]bool{"Run": true, "Item": true, "Apply": true, "AfterApply": true}

// specTypes are the pkg/ops composite literal types whose function fields
// rules writer-bypass and store-in-deps inspect.
var specTypes = map[string]bool{"TaskSpec": true, "BatchSpec": true, "FixerSpec": true, "PipelineSpec": true}

// constructors are the pkg/ops functions that build a Definition (rule
// unbundled-def).
var constructors = map[string]bool{"Task": true, "Batch": true, "Fixer": true, "Pipeline": true}

const (
	modulePath  = "github.com/falkcorp/audiobook-organizer"
	opsPath     = modulePath + "/pkg/ops"
	opstestPath = opsPath + "/opstest"
	dbPath      = modulePath + "/internal/database"
	catalogPath = modulePath + "/internal/opscatalog"
	v2Registry  = modulePath + "/internal/operations/registry"

	allowDirective = "//oplint:allow"
)

// Diagnostic categories, one per rule. The driver counts findings by
// category; only catMissingHelp is a warning, every other one fails.
const (
	catUnbundled    = "unbundled-def"
	catUnregistered = "unregistered-bundle"
	catWriterBypass = "writer-bypass"
	catStoreInDeps  = "store-in-deps"
	catSequential   = "sequential-no-reason"
	catMissingHelp  = "missing-help"
	catOwnerReview  = "owner-review"
)

// Analyzer is oplint's go/analysis analyzer.
var Analyzer = &analysis.Analyzer{
	Name: "oplint",
	Doc: `check ops v3 definitions (05-operations-v3 sdk-api.md §11)

Rules, by diagnostic category:
  unbundled-def        an ops.Task/Batch/Fixer/Pipeline call not reachable from the package's Ops(Deps) ops.Bundle
  unregistered-bundle  a package defining Ops(...) ops.Bundle that internal/opscatalog All does not reference,
                       or an All entry that is not a direct pkg.Ops reference
  writer-bypass        inside a Run/Item/Apply/AfterApply func of an ops.*Spec literal (and the same-package
                       functions it calls, one level deep), a write-named method call on a store declared in
                       internal/database; allowed only with "//oplint:allow <reason>" on the call line
  store-in-deps        a Deps struct (or a struct an Ops function takes) with a field that is or implements
                       database.Store, or a database.Store captured by a definition's closure (R21)
  sequential-no-reason ops.Sequential("") with an empty or blank reason
  missing-help         (warning) an ops.Common literal with no Help
  owner-review         any ops.Deletes or Deleter() use; never silently allowed, "//oplint:allow <reason>"
                       records the owner's sign-off

_test.go files, pkg/ops itself and pkg/ops/opstest are never reported.`,
	Run: runPass,
}

// probeV2 extends writer-bypass to the Run field of v2 registry.OperationDef
// literals. It is an informational probe of how much v2 code a port would
// have to move behind the Writer (05-PR7 reports the count); the gate never
// sets it.
var probeV2 bool

func init() {
	Analyzer.Flags.BoolVar(&probeV2, "probe-v2", false,
		"also apply writer-bypass to v2 registry.OperationDef Run funcs (informational)")
}

func runPass(pass *analysis.Pass) (any, error) {
	path := pass.Pkg.Path()
	if path == opsPath || path == opstestPath || strings.HasPrefix(path, opstestPath+"/") {
		return nil, nil
	}
	c := &lintState{pass: pass, decls: map[types.Object]ast.Node{}, allows: map[string]map[int]string{}}
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
	c.findOps()
	c.unbundledDefs()
	c.unregisteredBundle()
	if path == catalogPath {
		c.catalogEntries()
	}
	c.storeInDeps()
	c.walkCalls()
	c.specLiterals()
	return nil, nil
}

type lintState struct {
	pass  *analysis.Pass
	files []*ast.File
	// decls maps each package-level func, method and var object to its
	// declaration: the *ast.FuncDecl, the var's own initializer expression,
	// or the *ast.ValueSpec when one call initializes several vars.
	decls map[types.Object]ast.Node
	// allows maps file name -> line -> the text after //oplint:allow.
	allows map[string]map[int]string
	ops    *ast.FuncDecl // the package's Ops(...) ops.Bundle, or nil
	store  *types.Interface
	once   sync.Once
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
				continue // e.g. //oplint:allowed -- not the directive
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

// isOpsNamed reports whether t (or *t) is the pkg/ops named type name.
func isOpsNamed(t types.Type, names map[string]bool) (string, bool) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != opsPath {
		return "", false
	}
	return n.Obj().Name(), names[n.Obj().Name()]
}

// opsFunc returns the name of the pkg/ops package-level function call
// invokes, or "".
func (c *lintState) opsFunc(call *ast.CallExpr) string {
	fn, ok := typeutil.Callee(c.pass.TypesInfo, call).(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != opsPath {
		return ""
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return ""
	}
	return fn.Name()
}

// findOps sets c.ops to the package-level func Ops whose single result is
// ops.Bundle.
func (c *lintState) findOps() {
	for obj, d := range c.decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || obj.Name() != "Ops" {
			continue
		}
		sig := obj.Type().(*types.Signature)
		if sig.Results().Len() != 1 {
			continue
		}
		if _, ok := isOpsNamed(sig.Results().At(0).Type(), map[string]bool{"Bundle": true}); ok {
			c.ops = fd
		}
	}
}

// reachableFromOps returns every package-level object Ops refers to,
// transitively through the bodies and initializers of package-level funcs,
// methods and vars.
func (c *lintState) reachableFromOps() map[types.Object]bool {
	seen := map[types.Object]bool{}
	if c.ops == nil {
		return seen
	}
	var queue []ast.Node
	start := c.pass.TypesInfo.Defs[c.ops.Name]
	seen[start] = true
	queue = append(queue, c.ops)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		ast.Inspect(n, func(x ast.Node) bool {
			id, ok := x.(*ast.Ident)
			if !ok {
				return true
			}
			obj := c.pass.TypesInfo.Uses[id]
			if fn, ok := obj.(*types.Func); ok {
				obj = fn.Origin()
			}
			if obj == nil || seen[obj] {
				return true
			}
			if d, ok := c.decls[obj]; ok {
				seen[obj] = true
				queue = append(queue, d)
			}
			return true
		})
	}
	return seen
}

// unbundledDefs reports every Task/Batch/Fixer/Pipeline call in a
// package-level declaration Ops does not reach (rule 1).
func (c *lintState) unbundledDefs() {
	reach := c.reachableFromOps()
	reported := map[token.Pos]bool{}
	for obj, d := range c.decls {
		if reach[obj] {
			continue
		}
		ast.Inspect(d, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || reported[call.Pos()] {
				return true
			}
			name := c.opsFunc(call)
			if !constructors[name] {
				return true
			}
			reported[call.Pos()] = true
			why := "this package declares no Ops(Deps) ops.Bundle"
			if c.ops != nil {
				why = "Ops does not reach " + obj.Name()
			}
			c.pass.Report(analysis.Diagnostic{Pos: call.Pos(), Category: catUnbundled,
				Message: fmt.Sprintf("ops.%s definition is not in the package's bundle (%s); list it in Ops so the catalog registers it", name, why)})
			return true
		})
	}
	// A func with no object (go/types records none for some init and blank
	// funcs) is not in decls; check its body here.
	for _, f := range c.files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || c.pass.TypesInfo.Defs[fd.Name] != nil {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && constructors[c.opsFunc(call)] {
					c.pass.Report(analysis.Diagnostic{Pos: call.Pos(), Category: catUnbundled,
						Message: fmt.Sprintf("ops.%s definition built in %s is not in the package's bundle; build it from Ops", c.opsFunc(call), fd.Name.Name)})
				}
				return true
			})
		}
	}
}

// catalogCache holds, per catalog directory, the import paths its All
// references as pkg.Ops.
var catalogCache sync.Map // dir -> catalogInfo

type catalogInfo struct {
	refs map[string]bool
	err  error
}

// unregisteredBundle reports a package whose Ops is not referenced from
// opscatalog.All (rule 2). The catalog is read from source: its All entries
// are resolved through its imports, and the catalog package's own pass
// (catalogEntries) proves, with types, that every entry is a direct pkg.Ops
// reference, so the import-resolved list is the type-checked one.
func (c *lintState) unregisteredBundle() {
	if c.ops == nil || c.pass.Pkg.Path() == catalogPath {
		return
	}
	dir := c.locateCatalog()
	if dir == "" {
		c.pass.Report(analysis.Diagnostic{Pos: c.ops.Name.Pos(), Category: catUnregistered,
			Message: "cannot find " + catalogPath + " to check that this bundle is listed in All"})
		return
	}
	v, _ := catalogCache.LoadOrStore(dir, sync.OnceValue(func() catalogInfo { return readCatalog(dir) }))
	info := v.(func() catalogInfo)()
	if info.err != nil {
		c.pass.Report(analysis.Diagnostic{Pos: c.ops.Name.Pos(), Category: catUnregistered,
			Message: "cannot read " + catalogPath + ": " + info.err.Error()})
		return
	}
	if !info.refs[c.pass.Pkg.Path()] {
		c.pass.Report(analysis.Diagnostic{Pos: c.ops.Name.Pos(), Category: catUnregistered,
			Message: fmt.Sprintf("%s defines Ops(...) ops.Bundle but %s.All does not reference %s.Ops; add it, or its defs never register", c.pass.Pkg.Path(), "opscatalog", c.pass.Pkg.Name())})
	}
}

// locateCatalog finds the catalog package's directory by walking up from
// this package's directory: a go.mod whose module path prefixes catalogPath
// (module mode), or a directory holding catalogPath itself (a GOPATH src
// directory, as analysistest uses).
func (c *lintState) locateCatalog() string {
	dir := filepath.Dir(c.pass.Fset.File(c.files[0].Pos()).Name())
	for {
		if p := filepath.Join(dir, filepath.FromSlash(catalogPath)); isDir(p) {
			return p
		}
		if mod := modulePathOf(filepath.Join(dir, "go.mod")); mod != "" {
			if rest, ok := strings.CutPrefix(catalogPath, mod+"/"); ok {
				if p := filepath.Join(dir, filepath.FromSlash(rest)); isDir(p) {
					return p
				}
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// modulePathOf returns the module path a go.mod declares, or "".
func modulePathOf(gomod string) string {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// readCatalog parses the catalog directory's non-test files and returns the
// import paths of every pkg.Ops element of the All var.
func readCatalog(dir string) catalogInfo {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return catalogInfo{err: err}
	}
	refs := map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			return catalogInfo{err: err}
		}
		imports := map[string]string{}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			local := p[strings.LastIndex(p, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			imports[local] = p
		}
		for _, el := range allElements(f) {
			ast.Inspect(el, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Ops" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && imports[id.Name] != "" {
					refs[imports[id.Name]] = true
				}
				return true
			})
		}
	}
	return catalogInfo{refs: refs}
}

// allElements returns the elements of every composite literal assigned to a
// package-level var named All in f.
func allElements(f *ast.File) []ast.Expr {
	var out []ast.Expr
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name != "All" || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.CompositeLit); ok {
					out = append(out, lit.Elts...)
				}
			}
		}
	}
	return out
}

// catalogEntries runs on the catalog package itself: every element of All
// must name another package's Ops function as pkg.Ops, either as the entry
// itself or called inside the entry's func literal (which builds that
// package's own Deps from the catalog's). That makes the import-resolved
// reading in unregisteredBundle exact (rule 2).
func (c *lintState) catalogEntries() {
	for _, f := range c.files {
		for _, el := range allElements(f) {
			found := false
			ast.Inspect(el, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if _, ok := c.pass.TypesInfo.Uses[id].(*types.PkgName); !ok {
					return true
				}
				if fn, ok := c.pass.TypesInfo.Uses[sel.Sel].(*types.Func); ok && c.isBundleFunc(fn) {
					found = true
				}
				return true
			})
			if !found {
				c.pass.Report(analysis.Diagnostic{Pos: el.Pos(), Category: catUnregistered,
					Message: "every opscatalog.All entry must name a package's Ops function as pkg.Ops (directly, or called in the entry's func literal); oplint resolves bundles by it"})
			}
		}
	}
}

// isBundleFunc reports a package-level func named Ops, declared in another
// package, whose single result is ops.Bundle.
func (c *lintState) isBundleFunc(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || fn.Name() != "Ops" || sig.Recv() != nil || fn.Pkg() == nil || fn.Pkg() == c.pass.Pkg || sig.Results().Len() != 1 {
		return false
	}
	_, ok = isOpsNamed(sig.Results().At(0).Type(), map[string]bool{"Bundle": true})
	return ok
}

// storeIface returns database.Store's interface, if the package can see it.
func (c *lintState) storeIface() *types.Interface {
	c.once.Do(func() {
		seen := map[*types.Package]bool{}
		var visit func(p *types.Package) bool
		visit = func(p *types.Package) bool {
			if seen[p] {
				return false
			}
			seen[p] = true
			if p.Path() == dbPath {
				if tn, ok := p.Scope().Lookup("Store").(*types.TypeName); ok {
					c.store, _ = tn.Type().Underlying().(*types.Interface)
				}
				return true
			}
			for _, imp := range p.Imports() {
				if visit(imp) {
					return true
				}
			}
			return false
		}
		visit(c.pass.Pkg)
	})
	return c.store
}

// isStore reports whether t is, embeds or implements database.Store.
func (c *lintState) isStore(t types.Type) bool {
	st := c.storeIface()
	if st == nil || t == nil {
		return false
	}
	if types.Implements(t, st) {
		return true
	}
	if _, isPtr := t.(*types.Pointer); !isPtr {
		if _, isIface := t.Underlying().(*types.Interface); !isIface {
			return types.Implements(types.NewPointer(t), st)
		}
	}
	return false
}

// storeInDeps reports a Deps struct field, or a field of a struct an Ops
// function takes, that is database.Store or implements it (rule 4, R21).
func (c *lintState) storeInDeps() {
	if c.storeIface() == nil {
		return
	}
	checked := map[*types.Struct]bool{}
	check := func(st *types.Struct, at token.Pos, what string) {
		if checked[st] {
			return
		}
		checked[st] = true
		for fld := range st.Fields() {
			if !c.isStore(fld.Type()) {
				continue
			}
			pos := fld.Pos()
			if fld.Pkg() != c.pass.Pkg {
				pos = at
			}
			c.pass.Report(analysis.Diagnostic{Pos: pos, Category: catStoreInDeps,
				Message: fmt.Sprintf("%s field %s is database.Store or implements it; declare the narrow interface the op calls (sdk-api.md §11.1, R21)", what, fld.Name())})
		}
	}
	for _, f := range c.files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				ts := s.(*ast.TypeSpec)
				if ts.Name.Name != "Deps" {
					continue
				}
				if st, ok := c.pass.TypesInfo.Defs[ts.Name].Type().Underlying().(*types.Struct); ok {
					check(st, ts.Pos(), "Deps")
				}
			}
		}
	}
	if c.ops != nil {
		sig := c.pass.TypesInfo.Defs[c.ops.Name].Type().(*types.Signature)
		for v := range sig.Params().Variables() {
			t := v.Type()
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			if st, ok := t.Underlying().(*types.Struct); ok {
				check(st, c.ops.Name.Pos(), "Ops parameter")
			}
		}
	}
}

// walkCalls applies the call-shaped rules (5 and 7) to every non-test file.
func (c *lintState) walkCalls() {
	for _, f := range c.files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch c.opsFunc(call) {
			case "Sequential":
				if len(call.Args) == 1 {
					if tv, ok := c.pass.TypesInfo.Types[call.Args[0]]; ok && tv.Value != nil && tv.Value.Kind() == constant.String &&
						strings.TrimSpace(constant.StringVal(tv.Value)) == "" {
						c.pass.Report(analysis.Diagnostic{Pos: call.Pos(), Category: catSequential,
							Message: `ops.Sequential needs a reason: say why this op cannot use a worker pool`})
					}
				}
			case "Deletes":
				c.ownerReview(call, "ops.Deletes")
			}
			if fn, ok := typeutil.Callee(c.pass.TypesInfo, call).(*types.Func); ok && fn.Name() == "Deleter" &&
				fn.Pkg() != nil && fn.Pkg().Path() == opsPath {
				c.ownerReview(call, "Deleter()")
			}
			return true
		})
	}
}

// ownerReview reports a delete-capable call unless an //oplint:allow with a
// reason (the owner's sign-off) is on its line (rule 7).
func (c *lintState) ownerReview(call *ast.CallExpr, what string) {
	if c.allowed(call, catOwnerReview) {
		return
	}
	c.pass.Report(analysis.Diagnostic{Pos: call.Pos(), Category: catOwnerReview,
		Message: what + " needs owner review (standing rule: never delete book_file rows); record the sign-off with //oplint:allow <reason>"})
}

// allowed reports whether call's line carries //oplint:allow with a reason.
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
			Message: "//oplint:allow needs a reason after it"})
		return true
	}
	return true
}

// specLiterals applies rules 3 (writer bypass), 4 (captured store) and 6
// (missing Help) to the pkg/ops composite literals.
func (c *lintState) specLiterals() {
	reported := map[token.Pos]bool{}
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
			if _, ok := isOpsNamed(tv.Type, map[string]bool{"Common": true}); ok {
				c.missingHelp(lit, tv.Type)
				return true
			}
			if _, ok := isOpsNamed(tv.Type, specTypes); !ok {
				if probeV2 && isV2Def(tv.Type) {
					c.v2Probe(lit, reported)
				}
				return true
			}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, _ := kv.Key.(*ast.Ident)
				if key == nil {
					continue
				}
				if fl, ok := kv.Value.(*ast.FuncLit); ok {
					c.capturedStores(fl)
				}
				if !writerFields[key.Name] {
					continue
				}
				switch v := kv.Value.(type) {
				case *ast.FuncLit:
					c.writerBypass(v.Body, key.Name, 0, map[ast.Node]bool{}, reported)
				default:
					if fd := c.samePackageDecl(v); fd != nil && fd.Body != nil {
						c.writerBypass(fd.Body, key.Name, 0, map[ast.Node]bool{fd: true}, reported)
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
// one level deep (depth 0 is the spec's own function).
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
			if c.allowed(call, catWriterBypass) {
				return true
			}
			via := ""
			if depth > 0 {
				via = " (via a function it calls)"
			}
			c.pass.Report(analysis.Diagnostic{Pos: call.Pos(), Category: catWriterBypass,
				Message: fmt.Sprintf("%s.%s writes the store directly inside %s%s; write through rc.Writer() (ops.Writer) so the write is fenced, previewed and recorded, or add //oplint:allow <reason>", recvName(fn), fn.Name(), field, via)})
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

// isStoreWrite reports a method declared in internal/database (or below it)
// whose name starts with a write prefix.
func (c *lintState) isStoreWrite(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil || fn.Pkg() == nil {
		return false
	}
	p := fn.Pkg().Path()
	if p != dbPath && !strings.HasPrefix(p, dbPath+"/") {
		return false
	}
	for _, pre := range writePrefixes {
		if strings.HasPrefix(fn.Name(), pre) {
			return true
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

// capturedStores reports a variable declared outside fl, used inside it,
// whose type is database.Store or implements it (rule 4, R21).
func (c *lintState) capturedStores(fl *ast.FuncLit) {
	seen := map[types.Object]bool{}
	ast.Inspect(fl.Body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		v, ok := c.pass.TypesInfo.Uses[id].(*types.Var)
		if !ok || v.IsField() || seen[v] || (v.Pos() >= fl.Pos() && v.Pos() < fl.End()) {
			return true
		}
		seen[v] = true
		if c.isStore(v.Type()) {
			c.pass.Report(analysis.Diagnostic{Pos: id.Pos(), Category: catStoreInDeps,
				Message: fmt.Sprintf("definition captures %s, which is database.Store or implements it; take the narrow interface the op calls from Deps (R21)", v.Name())})
		}
		return true
	})
}

// missingHelp warns on an ops.Common literal with no Help, or a constant
// blank one (rule 6).
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
	c.pass.Report(analysis.Diagnostic{Pos: lit.Pos(), Category: catMissingHelp,
		Message: "warning: ops.Common has no Help; give the def list and the generated form a 1-3 sentence description"})
}

// isV2Def reports registry.OperationDef.
func isV2Def(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == v2Registry && n.Obj().Name() == "OperationDef"
}

// v2Probe applies writer-bypass to a v2 OperationDef literal's Run field
// (the -probe-v2 flag).
func (c *lintState) v2Probe(lit *ast.CompositeLit, reported map[token.Pos]bool) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, _ := kv.Key.(*ast.Ident); key == nil || key.Name != "Run" {
			continue
		}
		switch v := kv.Value.(type) {
		case *ast.FuncLit:
			c.writerBypass(v.Body, "v2 Run", 0, map[ast.Node]bool{}, reported)
		default:
			if fd := c.samePackageDecl(v); fd != nil && fd.Body != nil {
				c.writerBypass(fd.Body, "v2 Run", 0, map[ast.Node]bool{fd: true}, reported)
			}
		}
	}
}
