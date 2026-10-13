// file: internal/plugins/runitems_concurrency_lint_test.go
// version: 1.1.0
// guid: 6b0f7a3e-2c9d-4d58-8e41-a7c5d9f1b2e3
// last-edited: 2026-10-13

package plugins

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	// runItemsImportPath is the package that owns RunItems.
	runItemsImportPath = modulePath + "/internal/operations/registry"

	// sequentialMarker, on the RunItems call's line or the line above it,
	// records that an omitted Concurrency is deliberate. The reason after it
	// is required: the marker is a statement that someone decided, not a
	// switch that makes the lint quiet.
	sequentialMarker = "runitems:sequential"
)

// TestRunItemsCallsDeclareConcurrency fails when a RunItemsOptions literal
// has no Concurrency key, when registry.RunItems is called with no options at
// all, or when RunItems is used other than as a direct call, unless a
// `// runitems:sequential <reason>` comment covers it.
//
// WHY: RunItemsOptions.Concurrency defaults to zero and RunItems clamps
// anything below 1 up to 1, so a call that omits the field is an ordinary
// sequential loop wearing a worker-pool API. Five whole-library ops shipped
// that way (acoustid.lsh-backfill, acoustid.reset-all x2, deluge.centralize,
// deluge.path-update), and CLAUDE.md pointed at one of them as the exemplar of
// the parallel shape. An explicit value, even `Concurrency: 1`, is a choice a
// reviewer can see; an omission is indistinguishable from forgetting.
//
// What is judged: EVERY composite literal of type RunItemsOptions, wherever it
// is built (a call argument, an `opts :=` variable, a helper's return value,
// a slice element), so a helper like linkRunOptions(...) is checked where it
// constructs the value rather than skipped at the call. A call with no options
// argument is flagged at the call. A reference to RunItems that is not a call
// (a function value, which would hide the call from this lint) is flagged.
// What is NOT judged: options assembled by assigning fields one at a time to a
// zero value built elsewhere, which is why a bare `RunItemsOptions{}` is
// itself flagged: set the key in the literal.
//
// The marker covers a call on its own line, or on the line(s) of a contiguous
// comment block directly above it; for a literal it also covers the enclosing
// RunItems call. The reason after the marker is required.
func TestRunItemsCallsDeclareConcurrency(t *testing.T) {
	root := moduleRootDir(t)

	var offenders []string
	for _, top := range []string{"internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "mocks", "testdata", "node_modules":
					return filepath.SkipDir
				}
				// The package that defines RunItems and its own helpers.
				if filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(root, "internal", "operations", "registry")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, runItemsOffenders(t, path, filepath.ToSlash(rel))...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}

	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s\n\t  set RunItemsOptions.Concurrency (runtime.NumCPU() for CPU/DB-bound work, a small fixed number "+
			"for a rate-limited target), or add `// %s <reason>` on or above the call", o, sequentialMarker)
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

// runItemsOffenders returns one "path:line: reason" entry per violation in the
// file.
func runItemsOffenders(t *testing.T, path, rel string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}

	// Local names the registry package is imported under in this file, and
	// whether it is dot-imported (then RunItems and RunItemsOptions are bare).
	names := map[string]bool{}
	dot := false
	for _, imp := range f.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil || p != runItemsImportPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["registry"] = true
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	if len(names) == 0 && !dot {
		return nil
	}

	lines := strings.Split(string(src), "\n")
	ownLineComment := func(line int) bool {
		return line >= 1 && line <= len(lines) && strings.HasPrefix(strings.TrimSpace(lines[line-1]), "//")
	}
	// Lines carrying a valid marker: `//`, the marker, whitespace, a reason.
	marked := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			text, ok := strings.CutPrefix(c.Text, "//")
			if !ok {
				continue
			}
			rest, ok := strings.CutPrefix(strings.TrimLeft(text, " \t"), sequentialMarker)
			if ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') && strings.TrimSpace(rest) != "" {
				marked[fset.Position(c.Pos()).Line] = true
			}
		}
	}
	// covered: the marker is on this line, or in the contiguous block of
	// own-line comments directly above it.
	covered := func(line int) bool {
		if marked[line] {
			return true
		}
		for k := line - 1; ownLineComment(k); k-- {
			if marked[k] {
				return true
			}
		}
		return false
	}

	isRunItemsRef := func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			return ok && names[id.Name] && x.Sel.Name == "RunItems"
		case *ast.Ident:
			return dot && x.Name == "RunItems"
		}
		return false
	}
	isOptionsType := func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			return ok && names[id.Name] && x.Sel.Name == "RunItemsOptions"
		case *ast.Ident:
			return dot && x.Name == "RunItemsOptions"
		}
		return false
	}
	unwrapGeneric := func(e ast.Expr) ast.Expr {
		switch x := e.(type) {
		case *ast.IndexExpr:
			return x.X
		case *ast.IndexListExpr:
			return x.X
		}
		return e
	}

	var out []string
	flag := func(pos token.Pos, msg string) {
		out = append(out, rel+":"+strconv.Itoa(fset.Position(pos).Line)+": "+msg)
	}

	skipIdent := map[*ast.Ident]bool{}
	callFuns := map[ast.Expr]bool{}      // RunItems expressions that are a call's Fun
	owner := map[*ast.CompositeLit]int{} // options literal -> line of its RunItems call
	judgeLit := func(lit *ast.CompositeLit) {
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Concurrency" {
					return
				}
			}
		}
		line := fset.Position(lit.Pos()).Line
		if covered(line) {
			return
		}
		if cl, ok := owner[lit]; ok && covered(cl) {
			return
		}
		flag(lit.Pos(), "RunItemsOptions has no Concurrency key (clamps to one core)")
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			fun := unwrapGeneric(x.Fun)
			if !isRunItemsRef(fun) {
				return true
			}
			callFuns[fun] = true
			line := fset.Position(x.Pos()).Line
			if len(x.Args) < 5 {
				if !covered(line) {
					flag(x.Pos(), "RunItems called with no RunItemsOptions (runs on one core)")
				}
				return true
			}
			if lit, ok := x.Args[len(x.Args)-1].(*ast.CompositeLit); ok {
				owner[lit] = line
			}
		case *ast.CompositeLit:
			switch tp := x.Type.(type) {
			case *ast.ArrayType:
				// []RunItemsOptions{{...}}: the elements carry no type.
				if isOptionsType(tp.Elt) {
					for _, e := range x.Elts {
						if el, ok := e.(*ast.CompositeLit); ok && el.Type == nil {
							judgeLit(el)
						}
					}
				}
			case *ast.MapType:
				if isOptionsType(tp.Value) {
					for _, e := range x.Elts {
						if kv, ok := e.(*ast.KeyValueExpr); ok {
							if el, ok := kv.Value.(*ast.CompositeLit); ok && el.Type == nil {
								judgeLit(el)
							}
						}
					}
				}
			default:
				if x.Type != nil && isOptionsType(x.Type) {
					judgeLit(x)
				}
			}
		case *ast.SelectorExpr:
			skipIdent[x.Sel] = true // a selector's Sel is not a bare reference
			if isRunItemsRef(x) && !callFuns[x] && !covered(fset.Position(x.Pos()).Line) {
				flag(x.Pos(), "RunItems used other than as a direct call (hides the call from this lint)")
			}
		case *ast.Ident:
			if !skipIdent[x] && isRunItemsRef(x) && !callFuns[x] && !covered(fset.Position(x.Pos()).Line) {
				flag(x.Pos(), "RunItems used other than as a direct call (hides the call from this lint)")
			}
		}
		return true
	})
	return out
}

// TestRunItemsLintRecognisesOffenders pins the lint's own behaviour on
// synthetic source, so a refactor of the walk cannot quietly turn it into a
// check that never fires (or one that fires on correct code).
func TestRunItemsLintRecognisesOffenders(t *testing.T) {
	const header = `package x
import (
	"context"
	rg "` + runItemsImportPath + `"
)
var _ = context.Background
`
	const dotHeader = `package x
import . "` + runItemsImportPath + `"
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
		{"spread options", header, "func f() { rg.RunItems(ctx, r, items, fn, opts...) }", 0},

		// Every literal is judged where it is built.
		{"literal in a variable", header, "func f() { opts := rg.RunItemsOptions{ErrMode: 1}; _ = opts }", 1},
		{"literal in a variable with key", header, "func f() { opts := rg.RunItemsOptions{Concurrency: 2}; _ = opts }", 0},
		{"helper return literal", header, "func build() rg.RunItemsOptions { return rg.RunItemsOptions{Label: nil} }", 1},
		{"pointer literal", header, "func f() { o := &rg.RunItemsOptions{ErrMode: 1}; _ = o }", 1},
		{"slice element missing key", header, "var o = []rg.RunItemsOptions{{ErrMode: 1}}", 1},
		{"slice element with key", header, "var o = []rg.RunItemsOptions{{Concurrency: 4}}", 0},
		{"map value missing key", header, "var o = map[string]rg.RunItemsOptions{\"a\": {ErrMode: 1}}", 1},
		{"options built elsewhere, not a literal", header, "func f() { rg.RunItems(ctx, r, items, fn, build()) }", 0},

		// RunItems used other than as a call hides the call.
		{"function value", header, "var run = rg.RunItems[int]", 1},
		{"function value assigned", header, "func f() { run := rg.RunItems[string]; _ = run }", 1},

		// Dot imports resolve the bare names.
		{"dot import no options", dotHeader, "func f() { RunItems(ctx, r, items, fn) }", 1},
		{"dot import literal missing key", dotHeader, "func f() { RunItems(ctx, r, items, fn, RunItemsOptions{ErrMode: 1}) }", 1},
		{"dot import literal with key", dotHeader, "func f() { RunItems(ctx, r, items, fn, RunItemsOptions{Concurrency: 3}) }", 0},
		{"dot import function value", dotHeader, "var run = RunItems[int]", 1},
		{"dot import unrelated method", dotHeader, "func f() { s.RunItems() }", 0},

		// The marker.
		{"marker with reason above", header, "func f() {\n// runitems:sequential one book at a time\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 0},
		{"marker above a comment block", header, "func f() {\n// runitems:sequential one book at a time\n// and a second line of why\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 0},
		{"marker on the call line", header, "func f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) // runitems:sequential reason\n}", 0},
		{"marker covers a literal below the call", header, "func f() {\n// runitems:sequential reason\nrg.RunItems(ctx, r, items, func(context.Context, int) error {\nreturn nil\n}, rg.RunItemsOptions{\nErrMode: 1,\n}) }", 0},
		{"marker without reason", header, "func f() {\n// runitems:sequential\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
		{"marker run into a word", header, "func f() {\n// runitems:sequentially reason\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
		{"marker as trailing comment on the previous code line", header, "func f() {\n_ = 1 // runitems:sequential reason\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
		{"marker separated by code", header, "func f() {\n// runitems:sequential reason\n_ = 1\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "x.go")
			if err := os.WriteFile(p, []byte(tc.header+tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := runItemsOffenders(t, p, "x.go"); len(got) != tc.want {
				t.Errorf("offenders = %v, want %d", got, tc.want)
			}
		})
	}
}
