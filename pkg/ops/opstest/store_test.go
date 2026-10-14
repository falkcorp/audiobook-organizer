// file: pkg/ops/opstest/store_test.go
// version: 1.0.2
// guid: be635650-8d61-463e-b257-fce48c067cd3
// last-edited: 2026-10-14

package opstest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// absentOnPurpose are the capabilities the registry looks up that the
// World's store leaves out, each with why the harness does not need it.
var absentOnPurpose = map[string]string{
	"WarmupStatuser":      "no warm-up gate: the World's store is ready at once, so ops dispatch without one",
	"standDownPersister":  "register.go asserts it on the service registry's store, never on the harness's",
	"legacyOpStore":       "v1 operation rows: the harness enqueues no legacy op",
	"legacyBackfillStore": "v1 operation rows: the harness enqueues no legacy op",
}

// capabilityPackages maps a package qualifier used in a capability's type
// argument to its directory under the repo root.
var capabilityPackages = map[string]string{
	"":          "internal/operations/registry",
	"registry":  "internal/operations/registry",
	"opswriter": "internal/operations/opswriter",
	"database":  "internal/database",
}

// TestWorldStoreServesEveryCapability: the World's store has no Unwrap, so a
// capability it does not implement resolves to nothing in production code
// (database.AsCapability), not to the raw Pebble store underneath; a writer
// or runner store found missing is silently left nil. This reads every
// capability the registry resolves from its store (AsCapability type
// arguments and type assertions on a store) and requires the World's store
// to have every method of each, or absentOnPurpose to say why not. A
// capability the registry gains later fails here by name.
func TestWorldStoreServesEveryCapability(t *testing.T) {
	root := repoRoot(t)
	r := newIfaceResolver(t, root)
	have := map[string]bool{}
	ws := reflect.TypeOf((*worldStore)(nil))
	for i := range ws.NumMethod() {
		have[ws.Method(i).Name] = true
	}
	caps := registryCapabilities(t, filepath.Join(root, capabilityPackages["registry"]))
	if len(caps) == 0 {
		t.Fatal("found no capability lookup in the registry: the scan is broken")
	}
	for _, c := range caps {
		name := c[strings.LastIndex(c, ".")+1:]
		if why, ok := absentOnPurpose[name]; ok {
			t.Logf("%s: not served (%s)", c, why)
			continue
		}
		var missing []string
		for _, m := range r.methods(c) {
			if !have[m] {
				missing = append(missing, m)
			}
		}
		if len(missing) > 0 {
			t.Errorf("the registry resolves capability %s from its store, and the World's store lacks %v: add "+
				"them to store.go (recorded and gated if they write), or say in absentOnPurpose why the "+
				"harness needs none", c, missing)
		}
	}
	for name := range absentOnPurpose {
		found := false
		for _, c := range caps {
			found = found || strings.HasSuffix(c, name)
		}
		if !found {
			t.Errorf("absentOnPurpose lists %s, which the registry no longer resolves: drop it", name)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// go test runs a package's tests in its source directory, so the root is
	// the nearest parent holding go.mod. runtime.Caller is not usable here:
	// CI builds with -trimpath, which turns the file name into an import path
	// that does not exist on disk. (Same walk as internal/arch's repoRoot.)
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found in any parent directory")
		}
		dir = parent
	}
}

// registryCapabilities lists, sorted and unique, the type argument of every
// database.AsCapability call and the type of every type assertion on an
// expression named *store / *Store in the registry's non-test sources.
func registryCapabilities(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, f := range parseDir(t, dir) {
		ast.Inspect(f, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.IndexExpr:
				if sel, ok := e.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "AsCapability" {
					out = append(out, exprName(e.Index))
				}
			case *ast.TypeAssertExpr:
				if e.Type != nil && strings.HasSuffix(strings.ToLower(exprName(e.X)), "store") {
					out = append(out, exprName(e.Type))
				}
			}
			return true
		})
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func exprName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprName(x.X) + "." + x.Sel.Name
	}
	return ""
}

func parseDir(t *testing.T, dir string) []*ast.File {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var out []*ast.File
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		out = append(out, f)
	}
	return out
}

// ifaceResolver finds an interface's method names by source, following
// embedded interfaces across the packages capabilityPackages names.
type ifaceResolver struct {
	t     *testing.T
	root  string
	decls map[string]map[string]*ast.InterfaceType // package qualifier -> type name -> declaration
}

func newIfaceResolver(t *testing.T, root string) *ifaceResolver {
	return &ifaceResolver{t: t, root: root, decls: map[string]map[string]*ast.InterfaceType{}}
}

func (r *ifaceResolver) pkg(q string) map[string]*ast.InterfaceType {
	if q == "registry" {
		q = ""
	}
	if d, ok := r.decls[q]; ok {
		return d
	}
	dir, ok := capabilityPackages[q]
	if !ok {
		r.t.Fatalf("capability package %q is not in capabilityPackages", q)
	}
	d := map[string]*ast.InterfaceType{}
	for _, f := range parseDir(r.t, filepath.Join(r.root, dir)) {
		for _, decl := range f.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.TYPE {
				continue
			}
			for _, s := range g.Specs {
				ts := s.(*ast.TypeSpec)
				if it, ok := ts.Type.(*ast.InterfaceType); ok {
					d[ts.Name.Name] = it
				}
			}
		}
	}
	r.decls[q] = d
	return d
}

// methods resolves name ("batchStore", "opswriter.BookStore") in the context
// of package q of the name's own qualifier.
func (r *ifaceResolver) methods(name string) []string {
	q, n := "", name
	if i := strings.LastIndex(name, "."); i >= 0 {
		q, n = name[:i], name[i+1:]
	}
	it, ok := r.pkg(q)[n]
	if !ok {
		r.t.Fatalf("capability %s is not an interface declared in %s", name, capabilityPackages[q])
	}
	var out []string
	for _, f := range it.Methods.List {
		if len(f.Names) > 0 {
			for _, id := range f.Names {
				out = append(out, id.Name)
			}
			continue
		}
		emb := exprName(f.Type) // an embedded interface, in q unless qualified
		if !strings.Contains(emb, ".") && q != "" {
			emb = q + "." + emb
		}
		out = append(out, r.methods(emb)...)
	}
	return out
}
