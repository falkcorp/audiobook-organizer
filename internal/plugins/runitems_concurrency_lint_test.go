// file: internal/plugins/runitems_concurrency_lint_test.go
// version: 2.0.1
// guid: 6b0f7a3e-2c9d-4d58-8e41-a7c5d9f1b2e3
// last-edited: 2026-10-16

package plugins

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/go/packages"
)

// modulePath is this module's import path. It was declared in
// plugins_wiring_test.go until 05-PR7 replaced that test with
// internal/opscatalog TestCatalogLinkedIntoBinary.
const modulePath = "github.com/falkcorp/audiobook-organizer"

const (
	// runItemsImportPath is the package that owns RunItems.
	runItemsImportPath = modulePath + "/internal/operations/registry"

	// sequentialMarker, on a RunItems call's line or in the comment block
	// directly above it, records that the call is deliberately not a worker
	// pool (or that its options are deliberately not provable here). The
	// reason after it is required: the marker is a statement that someone
	// decided, not a switch that makes the lint quiet.
	sequentialMarker = "runitems:sequential"
)

// TestRunItemsCallsDeclareConcurrency fails when a registry.RunItems call's
// options are not PROVABLY a RunItemsOptions with a non-zero Concurrency,
// unless a `// runitems:sequential <reason>` comment covers the call.
//
// WHY: RunItemsOptions.Concurrency defaults to zero and RunItems clamps
// anything below 1 up to 1, so a call that omits the field is an ordinary
// sequential loop wearing a worker-pool API. Five whole-library ops shipped
// that way (acoustid.lsh-backfill, acoustid.reset-all x2, deluge.centralize,
// deluge.path-update), and CLAUDE.md pointed at one of them as the exemplar of
// the parallel shape. An explicit value, even `Concurrency: 1`, is a choice a
// reviewer can see; an omission is indistinguishable from forgetting.
//
// The check is type-based (go/types via go/packages), not syntactic, so an
// alias, a dot import, a renamed import and an elided composite literal are
// all resolved. It flags:
//   - any composite literal whose TYPE is RunItemsOptions (including elided
//     elements of []RunItemsOptions and []*RunItemsOptions) that has no
//     Concurrency key, or whose Concurrency is the constant 0;
//   - a RunItems call with no options argument;
//   - a RunItems call whose options argument cannot be traced to such a
//     literal: a spread (`opts...`), a function parameter, a variable that is
//     assigned more than once, or a call to something that is not a function
//     with source in this tree. Tracing follows a local variable's single
//     assignment and a function's return statements (a helper such as
//     linkRunOptions), and rejects a `v.Concurrency = 0` assignment;
//   - any use of RunItems that is not a direct call (a function value would
//     hide the call from this lint).
//
// A call that is correct but not traceable (options built in a way the walk
// cannot follow) says so with the marker and a reason, or builds its options
// through a helper that returns a literal.
func TestRunItemsCallsDeclareConcurrency(t *testing.T) {
	root := moduleRootDir(t)
	start := time.Now()

	pkgs := loadRunItemsPackages(t, root)
	offenders := lintRunItems(t, pkgs, root, os.ReadFile)

	t.Logf("type-checked %d packages that import the registry in %s", len(pkgs), time.Since(start).Round(time.Millisecond))
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s\n\t  set RunItemsOptions.Concurrency to a non-zero value in a literal the lint can trace "+
			"(runtime.NumCPU() for CPU/DB-bound work, a small fixed number for a rate-limited target), "+
			"or add `// %s <reason>` on or above the call", o, sequentialMarker)
	}
}

// moduleRootDir returns the repository root: this test lives in internal/plugins.
func moduleRootDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected go.mod at %s: %v", root, err)
	}
	return root
}

// loadRunItemsPackages type-checks the non-test packages under internal/ and
// pkg/ that import the registry (the only ones that can call RunItems). Two
// loads: a cheap import-graph listing to find them, then a typed load of just
// those, with their dependencies read from export data rather than re-checked.
func loadRunItemsPackages(t *testing.T, root string) []*packages.Package {
	t.Helper()
	list, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedImports,
		Dir:  root,
	}, "./internal/...", "./pkg/...")
	if err != nil {
		t.Fatalf("list packages: %v", err)
	}
	var paths []string
	for _, p := range list {
		if p.PkgPath == runItemsImportPath || strings.Contains(p.PkgPath, "/mocks") {
			continue // the package that defines RunItems, and generated mocks
		}
		if _, ok := p.Imports[runItemsImportPath]; ok {
			paths = append(paths, p.PkgPath)
		}
	}
	if len(paths) == 0 {
		t.Fatal("found no package that imports the registry: the package listing is broken")
	}
	return loadTyped(t, root, nil, paths)
}

func loadTyped(t *testing.T, root string, overlay map[string][]byte, patterns []string) []*packages.Package {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:     root,
		Overlay: overlay,
	}, patterns...)
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Errorf("package %s does not type-check: %v", p.PkgPath, e)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	return pkgs
}

// runItemsFuncDecl is a function with source in the loaded set, for tracing
// the options a helper returns.
type runItemsFuncDecl struct {
	decl *ast.FuncDecl
	info *types.Info
}

// lintRunItems returns one "path:line: reason" entry per violation.
func lintRunItems(t *testing.T, pkgs []*packages.Package, root string, read func(string) ([]byte, error)) []string {
	t.Helper()

	// ---- indexes over the whole loaded set --------------------------------
	funcs := map[string]runItemsFuncDecl{} // types.Func.FullName() -> source
	assigns := map[types.Object][]ast.Expr{}
	fieldAssigns := map[types.Object][]ast.Expr{} // v.Concurrency = e
	infoOf := map[*ast.File]*types.Info{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Syntax {
			infoOf[f] = pkg.TypesInfo
			indexFile(f, pkg.TypesInfo, funcs, assigns, fieldAssigns)
		}
	}

	// ---- per-file marker tables ------------------------------------------
	type fileLines struct {
		lines  []string
		marked map[int]bool
	}
	fileTables := map[string]*fileLines{}
	tableFor := func(fset *token.FileSet, f *ast.File) *fileLines {
		name := fset.Position(f.Pos()).Filename
		if ft, ok := fileTables[name]; ok {
			return ft
		}
		ft := &fileLines{marked: map[int]bool{}}
		if src, err := read(name); err == nil {
			ft.lines = strings.Split(string(src), "\n")
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if validMarker(c.Text) {
					ft.marked[fset.Position(c.Pos()).Line] = true
				}
			}
		}
		fileTables[name] = ft
		return ft
	}

	var out []string
	for _, pkg := range pkgs {
		info, fset := pkg.TypesInfo, pkg.Fset
		for _, f := range pkg.Syntax {
			ft := tableFor(fset, f)
			ownLineComment := func(line int) bool {
				if line < 1 || line > len(ft.lines) {
					return false
				}
				s := strings.TrimSpace(ft.lines[line-1])
				return strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/*")
			}
			// covered: the marker is on this line, or in the contiguous block
			// of own-line comments directly above it.
			covered := func(line int) bool {
				if ft.marked[line] {
					return true
				}
				for k := line - 1; ownLineComment(k); k-- {
					if ft.marked[k] {
						return true
					}
				}
				return false
			}
			flag := func(pos token.Pos, msg string) {
				p := fset.Position(pos)
				rel, err := filepath.Rel(root, p.Filename)
				if err != nil {
					rel = p.Filename
				}
				out = append(out, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), p.Line, msg))
			}

			callIdents := map[*ast.Ident]bool{}
			ownerLine := map[*ast.CompositeLit]int{}

			// Pass 1: calls, so pass 2 can tell a call from any other use.
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id := runItemsCallee(info, call.Fun)
				if id == nil {
					return true
				}
				callIdents[id] = true
				line := fset.Position(call.Pos()).Line
				if len(call.Args) < 5 {
					if !covered(line) {
						flag(call.Pos(), "RunItems called with no RunItemsOptions (runs on one core)")
					}
					return true
				}
				if call.Ellipsis.IsValid() {
					if !covered(line) {
						flag(call.Pos(), "RunItems options passed as a spread; cannot be proven to set Concurrency")
					}
					return true
				}
				for _, arg := range call.Args[4:] {
					if lit, ok := ast.Unparen(arg).(*ast.CompositeLit); ok {
						ownerLine[lit] = line
					}
					if why := untraceable(arg, info, funcs, assigns, fieldAssigns, 0); why != "" && !covered(line) {
						flag(arg.Pos(), "RunItems options cannot be proven to set Concurrency: "+why)
					}
				}
				return true
			})

			// Pass 2: every composite literal of the options type.
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isOptionsType(info.TypeOf(lit)) {
					return true
				}
				if setsConcurrency(lit, info) {
					return true
				}
				line := fset.Position(lit.Pos()).Line
				if covered(line) {
					return true
				}
				if cl, ok := ownerLine[lit]; ok && covered(cl) {
					return true
				}
				flag(lit.Pos(), "RunItemsOptions has no non-zero Concurrency (clamps to one core)")
				return true
			})

			// Pass 3: any other use of RunItems hides the call from this lint.
			ast.Inspect(f, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || callIdents[id] || !isRunItemsFunc(info.Uses[id]) {
					return true
				}
				if !covered(fset.Position(id.Pos()).Line) {
					flag(id.Pos(), "RunItems used other than as a direct call (hides the call from this lint)")
				}
				return true
			})
		}

	}
	return out
}

// validMarker reports whether a comment (// or /* */) carries the marker
// followed by whitespace and a non-empty reason.
func validMarker(comment string) bool {
	text := comment
	if rest, ok := strings.CutPrefix(text, "//"); ok {
		text = rest
	} else if rest, ok := strings.CutPrefix(text, "/*"); ok {
		text = strings.TrimSuffix(rest, "*/")
	} else {
		return false
	}
	rest, ok := strings.CutPrefix(strings.TrimLeft(text, " \t"), sequentialMarker)
	return ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') && strings.TrimSpace(rest) != ""
}

func isRunItemsFunc(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == runItemsImportPath && fn.Name() == "RunItems"
}

// runItemsCallee returns the identifier naming registry.RunItems when fun is a
// (possibly instantiated) reference to it, whatever the import alias.
func runItemsCallee(info *types.Info, fun ast.Expr) *ast.Ident {
	switch x := ast.Unparen(fun).(type) {
	case *ast.IndexExpr:
		return runItemsCallee(info, x.X)
	case *ast.IndexListExpr:
		return runItemsCallee(info, x.X)
	case *ast.SelectorExpr:
		if isRunItemsFunc(info.Uses[x.Sel]) {
			return x.Sel
		}
	case *ast.Ident:
		if isRunItemsFunc(info.Uses[x]) {
			return x
		}
	}
	return nil
}

// isOptionsType reports whether t is registry.RunItemsOptions or a pointer to
// it, seeing through aliases.
func isOptionsType(t types.Type) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	named, ok := t.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == runItemsImportPath &&
		named.Obj().Name() == "RunItemsOptions"
}

// constZero reports whether e is a constant expression equal to zero.
func constZero(info *types.Info, e ast.Expr) bool {
	tv, ok := info.Types[e]
	if !ok || tv.Value == nil {
		return false
	}
	switch tv.Value.Kind() {
	case constant.Int, constant.Float:
		return constant.Sign(tv.Value) == 0
	}
	return false
}

// setsConcurrency reports whether the options literal has a Concurrency key
// whose value is not the constant 0.
func setsConcurrency(lit *ast.CompositeLit, info *types.Info) bool {
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Concurrency" {
			return !constZero(info, kv.Value)
		}
	}
	return false
}

// indexFile records, for tracing: function declarations by full name, every
// whole-variable assignment, and every `v.Concurrency = e` assignment. A nil
// expression marks an assignment the trace cannot follow (a zero-value `var`,
// a multi-value right-hand side).
func indexFile(f *ast.File, info *types.Info, funcs map[string]runItemsFuncDecl, assigns, fieldAssigns map[types.Object][]ast.Expr) {
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if fn, ok := info.Defs[x.Name].(*types.Func); ok && x.Body != nil {
				funcs[fn.FullName()] = runItemsFuncDecl{decl: x, info: info}
			}
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				var rhs ast.Expr
				if len(x.Lhs) == len(x.Rhs) {
					rhs = x.Rhs[i]
				}
				switch l := ast.Unparen(lhs).(type) {
				case *ast.Ident:
					if obj := info.ObjectOf(l); obj != nil {
						assigns[obj] = append(assigns[obj], rhs)
					}
				case *ast.SelectorExpr:
					if base, ok := ast.Unparen(l.X).(*ast.Ident); ok && l.Sel.Name == "Concurrency" {
						if obj := info.ObjectOf(base); obj != nil {
							fieldAssigns[obj] = append(fieldAssigns[obj], rhs)
						}
					}
				}
			}
		case *ast.ValueSpec:
			for i, name := range x.Names {
				var rhs ast.Expr
				if len(x.Values) == len(x.Names) {
					rhs = x.Values[i]
				}
				if obj := info.ObjectOf(name); obj != nil {
					assigns[obj] = append(assigns[obj], rhs)
				}
			}
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if id, ok := e.(*ast.Ident); ok {
					if obj := info.ObjectOf(id); obj != nil {
						assigns[obj] = append(assigns[obj], nil)
					}
				}
			}
		}
		return true
	})
}

// untraceable returns "" when expr is provably a RunItemsOptions literal (the
// literal itself is judged by the literal pass) and otherwise says why not.
func untraceable(expr ast.Expr, info *types.Info, funcs map[string]runItemsFuncDecl,
	assigns, fieldAssigns map[types.Object][]ast.Expr, depth int) string {
	if depth > 4 {
		return "the trace is too deep"
	}
	switch x := ast.Unparen(expr).(type) {
	case *ast.CompositeLit:
		if isOptionsType(info.TypeOf(x)) {
			return ""
		}
		return "not a RunItemsOptions literal"
	case *ast.CallExpr:
		var callee *types.Func
		switch fun := ast.Unparen(x.Fun).(type) {
		case *ast.Ident:
			callee, _ = info.Uses[fun].(*types.Func)
		case *ast.SelectorExpr:
			if sel := info.Selections[fun]; sel != nil {
				callee, _ = sel.Obj().(*types.Func)
			} else {
				callee, _ = info.Uses[fun.Sel].(*types.Func)
			}
		}
		if callee == nil {
			return "a call whose function is not statically known"
		}
		fd, ok := funcs[callee.FullName()]
		if !ok {
			return "a call to " + callee.Name() + ", which has no source in the checked tree"
		}
		returns := 0
		why := ""
		ast.Inspect(fd.decl.Body, func(n ast.Node) bool {
			switch r := n.(type) {
			case *ast.FuncLit:
				return false // returns in a nested closure are not this function's
			case *ast.ReturnStmt:
				returns++
				if len(r.Results) == 0 {
					why = callee.Name() + " has a bare return"
				} else if w := untraceable(r.Results[0], fd.info, funcs, assigns, fieldAssigns, depth+1); w != "" && why == "" {
					why = callee.Name() + ": " + w
				}
			}
			return true
		})
		if returns == 0 {
			return callee.Name() + " has no return"
		}
		return why
	case *ast.Ident:
		obj := info.ObjectOf(x)
		if _, ok := obj.(*types.Var); !ok {
			return "not a variable"
		}
		list := assigns[obj]
		if len(list) != 1 || list[0] == nil {
			return fmt.Sprintf("variable %s is not assigned exactly once from a traceable expression", x.Name)
		}
		for _, fe := range fieldAssigns[obj] {
			if fe == nil || constZero(info, fe) {
				return fmt.Sprintf("variable %s has Concurrency assigned to zero or an untraceable value", x.Name)
			}
		}
		return untraceable(list[0], info, funcs, assigns, fieldAssigns, depth+1)
	}
	return "an expression the lint cannot trace"
}

// TestRunItemsLintRecognisesOffenders pins the lint's own behaviour on
// synthetic, type-correct source loaded through the same go/packages path as
// the real tree, so a refactor of the walk cannot quietly turn it into a
// check that never fires (or one that fires on correct code).
func TestRunItemsLintRecognisesOffenders(t *testing.T) {
	const header = `package x

import (
	"context"

	rg "` + runItemsImportPath + `"
)

var (
	ctx   context.Context
	r     rg.Reporter
	items []int
)

func fn(context.Context, int) error { return nil }

func okOpts() rg.RunItemsOptions { return rg.RunItemsOptions{Concurrency: 4} }

`
	const dotHeader = `package x

import (
	"context"

	. "` + runItemsImportPath + `"
)

var (
	ctx   context.Context
	r     Reporter
	items []int
)

func fn(context.Context, int) error { return nil }

type S struct{}

func (S) RunItems() {}

var _ = RunItemsOptions{Concurrency: 1}

`
	cases := []struct {
		name   string
		header string
		body   string
		want   int
	}{
		{"omitted key", header, "func f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{ErrMode: 1}) }", 1},
		{"no options at all", header, "func f() { rg.RunItems(ctx, r, items, fn) }", 1},
		{"explicit key", header, "func f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{Concurrency: 4}) }", 0},
		{"explicit one", header, "func f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{Concurrency: 1}) }", 0},
		{"explicit type argument", header, "func f() { rg.RunItems[int](ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},

		// Probe cases: the ways the syntactic lint could be dodged.
		{"zero-value var passed", header, "func f() { var o rg.RunItemsOptions; o.ErrMode = 1; rg.RunItems(ctx, r, items, fn, o) }", 1},
		{"empty spread slice", header, "func f() { rg.RunItems(ctx, r, items, fn, []rg.RunItemsOptions{}...) }", 1},
		{"spread of a variable", header, "func f(o ...rg.RunItemsOptions) { rg.RunItems(ctx, r, items, fn, o...) }", 1},
		{"pointer slice elided element", header, "var o = []*rg.RunItemsOptions{{ErrMode: 1}}", 1},
		{"local alias type", header, "type opts = rg.RunItemsOptions\nfunc f() { rg.RunItems(ctx, r, items, fn, opts{ErrMode: 1}) }", 1},
		{"explicit zero", header, "func f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{Concurrency: 0}) }", 1},
		{"zero through a constant", header, "const none = 0\nfunc f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{Concurrency: none}) }", 1},

		// Tracing: a variable, a helper, and the ways it can fail.
		{"variable from a literal with key", header, "func f() { o := rg.RunItemsOptions{Concurrency: 2}; rg.RunItems(ctx, r, items, fn, o) }", 0},
		{"variable from a helper with key", header, "func f() { o := okOpts(); o.ErrMode = 1; rg.RunItems(ctx, r, items, fn, o) }", 0},
		{"helper call with key", header, "func f() { rg.RunItems(ctx, r, items, fn, okOpts()) }", 0},
		{"helper whose literal lacks the key", header, "func badOpts() rg.RunItemsOptions { return rg.RunItemsOptions{Label: nil} }\nfunc f() { rg.RunItems(ctx, r, items, fn, badOpts()) }", 1},
		{"variable reassigned", header, "func f() { o := okOpts(); o = rg.RunItemsOptions{Concurrency: 2}; rg.RunItems(ctx, r, items, fn, o) }", 1},
		{"variable Concurrency set to zero", header, "func f() { o := okOpts(); o.Concurrency = 0; rg.RunItems(ctx, r, items, fn, o) }", 1},
		{"function parameter", header, "func f(o rg.RunItemsOptions) { rg.RunItems(ctx, r, items, fn, o) }", 1},
		{"literal with key, standalone", header, "func f() { o := rg.RunItemsOptions{Concurrency: 2}; _ = o }", 0},
		{"literal without key, standalone", header, "func f() { o := rg.RunItemsOptions{ErrMode: 1}; _ = o }", 1},
		{"pointer literal", header, "func f() { o := &rg.RunItemsOptions{ErrMode: 1}; _ = o }", 1},
		{"slice element missing key", header, "var o = []rg.RunItemsOptions{{ErrMode: 1}}", 1},
		{"slice element with key", header, "var o = []rg.RunItemsOptions{{Concurrency: 4}}", 0},
		{"map value missing key", header, "var o = map[string]rg.RunItemsOptions{\"a\": {ErrMode: 1}}", 1},

		// RunItems used other than as a call hides the call.
		{"function value", header, "var run = rg.RunItems[int]", 1},
		{"function value assigned", header, "func f() { run := rg.RunItems[string]; _ = run }", 1},

		// Dot imports resolve the bare names; an unrelated method is not RunItems.
		{"dot import no options", dotHeader, "func f() { RunItems(ctx, r, items, fn) }", 1},
		{"dot import literal missing key", dotHeader, "func f() { RunItems(ctx, r, items, fn, RunItemsOptions{ErrMode: 1}) }", 1},
		{"dot import literal with key", dotHeader, "func f() { RunItems(ctx, r, items, fn, RunItemsOptions{Concurrency: 3}) }", 0},
		{"dot import function value", dotHeader, "var run = RunItems[int]", 1},
		{"dot import unrelated method", dotHeader, "func f() { S{}.RunItems() }", 0},
		{"field named RunItems", header, "type T struct{ RunItems int }\nvar _ = T{RunItems: 1}", 0},

		// The marker.
		{"marker with reason above", header, "func f() {\n// runitems:sequential one book at a time\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 0},
		{"marker above a comment block", header, "func f() {\n// runitems:sequential one book at a time\n// and a second line of why\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 0},
		{"marker on the call line", header, "func f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) // runitems:sequential reason\n}", 0},
		{"block comment marker", header, "func f() {\n/* runitems:sequential reason */\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 0},
		{"marker covers an untraceable call", header, "func f(o rg.RunItemsOptions) {\n// runitems:sequential reason\nrg.RunItems(ctx, r, items, fn, o) }", 0},
		{"marker covers a literal below the call", header, "func f() {\n// runitems:sequential reason\nrg.RunItems(ctx, r, items, func(context.Context, int) error {\nreturn nil\n}, rg.RunItemsOptions{\nErrMode: 1,\n}) }", 0},
		{"marker without reason", header, "func f() {\n// runitems:sequential\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
		{"block marker without reason", header, "func f() {\n/* runitems:sequential */\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
		{"marker run into a word", header, "func f() {\n// runitems:sequentially reason\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
		{"marker as trailing comment on the previous code line", header, "func f() {\n_ = 1 // runitems:sequential reason\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
		{"marker separated by code", header, "func f() {\n// runitems:sequential reason\n_ = 1\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
	}

	root := moduleRootDir(t)
	overlay := map[string][]byte{}
	var patterns []string
	dirOf := func(i int) string { return fmt.Sprintf("zzlintsynthetic/c%02d", i) }
	for i, tc := range cases {
		dir := filepath.Join(root, "internal", "plugins", dirOf(i))
		overlay[filepath.Join(dir, "x.go")] = []byte(tc.header + tc.body)
		patterns = append(patterns, modulePath+"/internal/plugins/"+filepath.ToSlash(dirOf(i)))
	}
	start := time.Now()
	pkgs := loadTyped(t, root, overlay, patterns)
	t.Logf("loaded %d synthetic packages in %s", len(pkgs), time.Since(start).Round(time.Millisecond))

	byDir := map[string]*packages.Package{}
	for _, p := range pkgs {
		byDir[strings.TrimPrefix(p.PkgPath, modulePath+"/internal/plugins/")] = p
	}
	read := func(name string) ([]byte, error) {
		if src, ok := overlay[name]; ok {
			return src, nil
		}
		return os.ReadFile(name)
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg := byDir[dirOf(i)]
			if pkg == nil {
				t.Fatalf("synthetic package %s was not loaded", dirOf(i))
			}
			if got := lintRunItems(t, []*packages.Package{pkg}, root, read); len(got) != tc.want {
				t.Errorf("offenders = %v, want %d", got, tc.want)
			}
		})
	}
}
