// file: internal/books/cutover/m3/closure_test.go
// version: 1.0.0
// guid: 37dd9a27-9fcd-4dbc-9a6c-f1dbc4958d38
// last-edited: 2026-11-12

package m3

import (
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// impliesMigration is an M3 over a catalog in which books:manage implies
// books:edit and books:edit implies books:view: an owner who holds only the
// implying scope holds every scope it implies, transitively.
func impliesMigration(t testing.TB) *Migration {
	t.Helper()
	specs := rbac.CoreScopes()
	specs = append(specs,
		rbac.PermissionSpec{Name: "books:view", Title: "view", Description: "books view"},
		rbac.PermissionSpec{Name: "books:edit", Title: "edit", Description: "books edit", Implies: []rbac.Permission{"books:view"}},
		rbac.PermissionSpec{Name: "books:manage", Title: "manage", Description: "books manage", Implies: []rbac.Permission{"books:edit"}},
		rbac.PermissionSpec{Name: "books:reading", Title: "reading", Description: "books reading"},
	)
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{
		StepID: "books/identity-cutover",
		Atoms: []AtomRow{
			{Name: "library.view", Scopes: []rbac.Permission{"books:view"}},
			{Name: "library.edit", Scopes: []rbac.Permission{"books:edit"}},
			{Name: "library.manage", Scopes: []rbac.Permission{"books:manage"}},
		},
		SeedRoles: []SeedRole{
			{ID: "admin", Permissions: []rbac.Permission{rbac.CoreAdmin}},
			{ID: "viewer", Permissions: []rbac.Permission{"books:view"}},
		},
		Catalog: cat,
		Ambient: []rbac.Permission{"core:self", "core:metrics.read", "books:reading"},
		Auth:    noAuth{}, Roles: noRoles{}, Native: noNative{},
		Legacy: func() storage.Reader { return nil }, Now: func() time.Time { return testNow }, KeyTTL: 90 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestClosure: a scope's closure holds what it implies, transitively, once
// each, and nothing that merely implies it.
func TestClosure(t *testing.T) {
	m := impliesMigration(t)
	got := m.closure([]rbac.Permission{"books:manage"})
	for _, want := range []rbac.Permission{"books:manage", "books:edit", "books:view"} {
		if !slices.Contains(got, want) {
			t.Errorf("closure(books:manage) = %v, missing %s", got, want)
		}
	}
	if len(got) != 3 {
		t.Errorf("closure(books:manage) = %v, want exactly the three scopes once each", got)
	}
	if got := m.closure([]rbac.Permission{"books:view"}); !slices.Equal(got, []rbac.Permission{"books:view"}) {
		t.Errorf("closure(books:view) = %v: a scope does not hold what implies it", got)
	}
	if got := m.closure([]rbac.Permission{"books:manage", "books:edit"}); len(got) != 3 {
		t.Errorf("closure(manage, edit) = %v, want the shared scopes once", got)
	}
}

// TestKeyScopes_ImpliedScopesCountAsHeld: a key is intersected with its owner's
// scopes, and an owner who holds books:manage holds books:edit and books:view
// through it. The key keeps what the owner holds by implication and still
// loses what the owner does not hold at all.
func TestKeyScopes_ImpliedScopesCountAsHeld(t *testing.T) {
	m := impliesMigration(t)
	ambient := []string{"books:reading", "core:metrics.read", "core:self"}
	asStrings := func(ps []rbac.Permission) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = string(p)
		}
		slices.Sort(out)
		return out
	}
	for _, c := range []struct {
		name  string
		atoms []string
		owner []rbac.Permission
		want  []string
	}{
		{"manage implies edit and view, two links deep", []string{"library.view", "library.edit"}, []rbac.Permission{"books:manage"},
			append([]string{"books:edit", "books:view"}, ambient...)},
		{"edit implies view", []string{"library.view"}, []rbac.Permission{"books:edit"},
			append([]string{"books:view"}, ambient...)},
		{"edit does not imply manage: the key loses it", []string{"library.view", "library.manage"}, []rbac.Permission{"books:edit"},
			append([]string{"books:view"}, ambient...)},
		{"view implies nothing above it", []string{"library.edit"}, []rbac.Permission{"books:view"}, ambient},
	} {
		got, _, err := m.keyScopes(c.atoms, c.owner)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		slices.Sort(c.want)
		if g := asStrings(got); !slices.Equal(g, c.want) {
			t.Errorf("%s: scopes %v, want %v", c.name, g, c.want)
		}
	}
}
