// file: internal/database/store_width_test.go
// version: 1.0.1
// guid: 7d1b6c2e-4a93-4f58-9e0d-3c8a52b7f1e4
// last-edited: 2026-10-10

package database

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// storeMethodBaseline is the flattened method count of the Store interface.
// This is a ratchet: it may only be lowered, by hand, in the PR that lowers
// the count. The test fails only on a rise. 07-S3 and 07-M14 are the PRs
// expected to lower the companion reference baseline in
// store_consumers_test.go; any PR that removes Store methods lowers this one.
//
// Why a test and not interfacebloat: that linter counts declared entries
// (Store declares 6 embedded interfaces), so it cannot see the 453 methods
// the embeds flatten to.
const storeMethodBaseline = 453

// flattenedMethodCount parses the non-test Go files in dir and returns the
// number of distinct methods of the interface named iface, following
// embedded interfaces declared in the same package. An embedded interface
// from another package is counted as one "<external:Name>" entry; callers
// that need a complete count must fail when any is present (see
// flattenedExternalEmbeds).
func flattenedMethodCount(t *testing.T, dir, iface string) int {
	t.Helper()
	return len(flattenedMethods(t, dir, iface))
}

func flattenedExternalEmbeds(methods map[string]bool) []string {
	var ext []string
	for k := range methods {
		if strings.HasPrefix(k, "<external:") {
			ext = append(ext, k)
		}
	}
	return ext
}

func flattenedMethods(t *testing.T, dir, iface string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	// go/build applies the default build constraints, so the count matches the
	// package the compiler builds (parser.ParseDir ignored build tags).
	bp, err := build.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("import %s: %v", dir, err)
	}
	var files []*ast.File
	for _, name := range bp.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	ifaces := map[string]*ast.InterfaceType{}
	{
		for _, f := range files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, s := range gd.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok {
						if it, ok := ts.Type.(*ast.InterfaceType); ok {
							ifaces[ts.Name.Name] = it
						}
					}
				}
			}
		}
	}
	var flatten func(name string, seen map[string]bool) map[string]bool
	flatten = func(name string, seen map[string]bool) map[string]bool {
		out := map[string]bool{}
		it, ok := ifaces[name]
		if !ok || seen[name] {
			return out
		}
		seen[name] = true
		for _, m := range it.Methods.List {
			if len(m.Names) > 0 {
				for _, n := range m.Names {
					out[n.Name] = true
				}
				continue
			}
			switch e := m.Type.(type) {
			case *ast.Ident:
				for k := range flatten(e.Name, seen) {
					out[k] = true
				}
			case *ast.SelectorExpr:
				out["<external:"+e.Sel.Name+">"] = true
			}
		}
		return out
	}
	return flatten(iface, map[string]bool{})
}

func TestStoreFlattenedWidth(t *testing.T) {
	methods := flattenedMethods(t, ".", "Store")
	if ext := flattenedExternalEmbeds(methods); len(ext) > 0 {
		t.Fatalf("Store embeds interfaces from other packages %v: the flattened count would be incomplete; extend flattenedMethods to resolve them", ext)
	}
	n := len(methods)
	if n <= 100 {
		t.Fatalf("Store flattened to only %d methods: the parse found (almost) nothing", n)
	}
	catalog := flattenedMethodCount(t, ".", "catalogStore")
	if catalog <= 0 || catalog >= n {
		t.Fatalf("positive control failed: catalogStore flattened to %d methods, want 0 < catalogStore < Store (%d)", catalog, n)
	}
	if n > storeMethodBaseline {
		t.Fatalf("database.Store flattens to %d methods, baseline is %d. Do not add methods to Store; "+
			"narrow the consumer to its own small interface instead. The baseline may only be lowered, never raised.",
			n, storeMethodBaseline)
	}
	if n < storeMethodBaseline {
		t.Logf("NOTE (not a failure): Store is now %d methods; lower storeMethodBaseline in this PR", n)
	}
}
