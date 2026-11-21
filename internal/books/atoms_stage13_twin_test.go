// file: internal/books/atoms_stage13_twin_test.go
// version: 0.1.0
// guid: 4d0f7c3a-6a1e-4b52-9c88-3e2b5f10a7d4
// last-edited: 2026-11-21

package books

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// stage13TwinFile is the core test file that holds the discharge of
// F4-07-RANDOM-ROLES and the atom table it runs stage 13 over. core is its own
// module and cannot import this package, so that table is a copy: this reads
// it with go/ast and holds it to AtomTable, the same direction the cutover
// fixture's golden is held (atoms_cutover_fixture_test.go).
const stage13TwinFile = "../../core/httpapi/legacy_stage13_test.go"

// stage13TwinTable reads s13Atoms' map literal: atom name to scope names, in
// the order written.
func stage13TwinTable(t *testing.T) map[string][]rbac.Permission {
	t.Helper()
	src, err := os.ReadFile(stage13TwinFile)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), stage13TwinFile, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	unquote := func(e ast.Expr) string {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Fatalf("%s: want a string literal, got %T", stage13TwinFile, e)
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "s13Atoms" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatalf("%s has no func s13Atoms", stage13TwinFile)
	}
	out := map[string][]rbac.Permission{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if _, isMap := lit.Type.(*ast.MapType); !isMap {
			return true
		}
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				t.Fatalf("%s: s13Atoms entry is not key: value", stage13TwinFile)
			}
			scopes, ok := kv.Value.(*ast.CompositeLit)
			if !ok {
				t.Fatalf("%s: s13Atoms value is not a literal list", stage13TwinFile)
			}
			name := unquote(kv.Key)
			if _, dup := out[name]; dup {
				t.Errorf("%s: atom %s is listed twice", stage13TwinFile, name)
			}
			var perms []rbac.Permission
			for _, s := range scopes.Elts {
				perms = append(perms, rbac.Permission(unquote(s)))
			}
			out[name] = perms
		}
		return false
	})
	return out
}

// TestAtomTable_MatchesStage13RandomRolesTable: the table the random-roles
// test runs stage 13 over (core/httpapi s13Atoms) is AtomTable's route rows,
// name for name and scope for scope. A row books adds, drops or changes fails
// here until the core table follows, so the test's "the successor admits it" is
// about the table books ships. The one atom missing from it, playlists.create,
// is the row no route enforces (a zero route requirement), and it must stay
// absent.
func TestAtomTable_MatchesStage13RandomRolesTable(t *testing.T) {
	twin := stage13TwinTable(t)
	routed := 0
	for _, a := range AtomTable() {
		if a.Route.IsZero() {
			if _, in := twin[a.Name]; in {
				t.Errorf("%s gates no route (zero route requirement) and must not be in the stage 13 table", a.Name)
			}
			continue
		}
		routed++
		got, in := twin[a.Name]
		switch {
		case !in:
			t.Errorf("AtomTable's %s is not in the stage 13 random-roles table (core/httpapi s13Atoms)", a.Name)
		case !slices.Equal(got, a.Scopes):
			t.Errorf("%s: AtomTable scopes %v, stage 13 table %v", a.Name, a.Scopes, got)
		}
		if !reflect.DeepEqual(a.Route, rbac.AllOf(a.Scopes...)) {
			t.Errorf("%s: the route requirement is not AllOf its scopes, which the stage 13 test assumes", a.Name)
		}
	}
	if routed == 0 || len(twin) != routed {
		t.Errorf("the stage 13 table has %d atoms, AtomTable %d routed rows", len(twin), routed)
	}
}
