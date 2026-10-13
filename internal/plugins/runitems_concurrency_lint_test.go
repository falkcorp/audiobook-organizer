// file: internal/plugins/runitems_concurrency_lint_test.go
// version: 1.0.0
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

// TestRunItemsCallsDeclareConcurrency fails when a registry.RunItems call
// passes a RunItemsOptions literal with no Concurrency key (or passes no
// options at all) and does not carry a `// runitems:sequential <reason>`
// comment.
//
// WHY: RunItemsOptions.Concurrency defaults to zero and RunItems clamps
// anything below 1 up to 1, so a call that omits the field is an ordinary
// sequential loop wearing a worker-pool API. Five whole-library ops shipped
// that way (acoustid.lsh-backfill, acoustid.reset-all x2, deluge.centralize,
// deluge.path-update), and CLAUDE.md pointed at one of them as the exemplar of
// the parallel shape. An explicit value, even `Concurrency: 1`, is a choice a
// reviewer can see; an omission is indistinguishable from forgetting.
//
// Scope: the options argument must be a composite literal to be judged. A call
// that passes a variable or a helper's result (backfillRunOptions(...),
// linkRunOptions(...)) is not inspected, because the field is set (or not)
// where that value is built, which is a different expression this test cannot
// follow without type information. Those builders are the reviewable place to
// set Concurrency.
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

// runItemsOffenders returns one "path:line: reason" entry per RunItems call in
// the file that omits Concurrency without a sequential marker.
func runItemsOffenders(t *testing.T, path, rel string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}

	// Local names the registry package is imported under in this file.
	names := map[string]bool{}
	for _, imp := range f.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil || p != runItemsImportPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["registry"] = true
		case imp.Name.Name != "_" && imp.Name.Name != ".":
			names[imp.Name.Name] = true
		}
	}
	if len(names) == 0 {
		return nil
	}

	// Lines carrying the marker with a non-empty reason.
	marked := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), "/*"))
			reason, ok := strings.CutPrefix(text, sequentialMarker)
			if ok && strings.TrimSpace(strings.TrimSuffix(reason, "*/")) != "" {
				marked[fset.Position(c.Pos()).Line] = true
			}
		}
	}

	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isRegistryRunItems(call.Fun, names) {
			return true
		}
		line := fset.Position(call.Pos()).Line
		if marked[line] || marked[line-1] {
			return true
		}
		// RunItems(ctx, reporter, items, fn, opts...): options are arg 5.
		if len(call.Args) < 5 {
			out = append(out, rel+":"+strconv.Itoa(line)+": RunItems called with no RunItemsOptions (runs on one core)")
			return true
		}
		lit, ok := call.Args[len(call.Args)-1].(*ast.CompositeLit)
		if !ok {
			return true // built elsewhere; see the test's doc comment
		}
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Concurrency" {
					return true
				}
			}
		}
		out = append(out, rel+":"+strconv.Itoa(line)+": RunItemsOptions has no Concurrency key (clamps to one core)")
		return true
	})
	return out
}

// isRegistryRunItems reports whether fun is <registry alias>.RunItems, with or
// without explicit type arguments.
func isRegistryRunItems(fun ast.Expr, names map[string]bool) bool {
	switch e := fun.(type) {
	case *ast.IndexExpr:
		fun = e.X
	case *ast.IndexListExpr:
		fun = e.X
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "RunItems" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && names[id.Name]
}

// TestRunItemsLintRecognisesOffenders pins the lint's own behaviour on
// synthetic source, so a refactor of the walk cannot quietly turn it into a
// check that never fires.
func TestRunItemsLintRecognisesOffenders(t *testing.T) {
	const header = `package x
import (
	"context"
	rg "` + runItemsImportPath + `"
)
var _ = context.Background
`
	cases := []struct {
		name string
		body string
		want int
	}{
		{"omitted key", "func f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{ErrMode: 1}) }", 1},
		{"no options at all", "func f() { rg.RunItems(ctx, r, items, fn) }", 1},
		{"explicit key", "func f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{Concurrency: 4}) }", 0},
		{"explicit one", "func f() { rg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{Concurrency: 1}) }", 0},
		{"marker with reason above", "func f() {\n// runitems:sequential one book at a time\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 0},
		{"marker without reason", "func f() {\n// runitems:sequential\nrg.RunItems(ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
		{"options built elsewhere", "func f() { rg.RunItems(ctx, r, items, fn, build()) }", 0},
		{"explicit type argument", "func f() { rg.RunItems[int](ctx, r, items, fn, rg.RunItemsOptions{}) }", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "x.go")
			if err := os.WriteFile(p, []byte(header+tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := runItemsOffenders(t, p, "x.go"); len(got) != tc.want {
				t.Errorf("offenders = %v, want %d", got, tc.want)
			}
		})
	}
}
