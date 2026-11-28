// file: internal/books/interim_atoms_test.go
// version: 0.1.0
// guid: 2d6f9b13-85ac-4e07-b3f2-7a1c40e9d652
// last-edited: 2026-11-28

package books

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/rbac"
)

// The export's table is the atom table, row for row: same atoms, same scopes in
// the same order, one row per atom.
func TestInterimAtoms_AreTheAtomTableRowForRow(t *testing.T) {
	got := interimAtoms()
	table := AtomTable()
	require.Len(t, got, len(table))
	for i, a := range table {
		assert.Equal(t, a.Name, string(got[i].Atom))
		var want []string
		for _, s := range a.Scopes {
			want = append(want, string(s))
		}
		assert.Equal(t, want, got[i].Scopes, a.Name)
	}
}

// An atom is interactive-only when any of its scopes is Sensitive in the
// catalog, and a scope the catalog does not declare counts as Sensitive: a row
// the catalog does not cover must fail closed.
func TestInterimAtomsFrom_ClassifiesBySensitiveAndFailsClosedOnUnknown(t *testing.T) {
	specs := []rbac.PermissionSpec{
		{Name: "core:users.view", Class: rbac.ClassView},
		{Name: "core:backup.restore", Sensitive: true},
		{Name: "books:view"},
	}
	table := []Atom{
		{Name: "plain", Scopes: []rbac.Permission{"core:users.view", "books:view"}},
		{Name: "sensitive", Scopes: []rbac.Permission{"books:view", "core:backup.restore"}},
		{Name: "unknown", Scopes: []rbac.Permission{"books:view", "core:undeclared"}},
	}
	got := interimAtomsFrom(table, specs)
	require.Len(t, got, 3)
	assert.False(t, got[0].InteractiveOnly, "plain")
	assert.True(t, got[1].InteractiveOnly, "a Sensitive scope makes the atom interactive-only")
	assert.True(t, got[2].InteractiveOnly, "an undeclared scope fails closed")
}

// Every scope of the real table is declared, so none of its rows is classed
// interactive-only by the fail-closed rule, and the flag matches the catalog
// asked independently.
func TestInterimAtoms_EveryScopeIsDeclaredAndTheFlagFollowsTheCatalog(t *testing.T) {
	cat, err := rbac.NewCatalog(slices.Concat(rbac.CoreScopes(), booksPermissions())...)
	require.NoError(t, err)
	for _, row := range interimAtoms() {
		want := false
		for _, sc := range row.Scopes {
			spec, ok := cat.Lookup(rbac.Permission(sc))
			require.True(t, ok, "%s: scope %s is not declared", row.Atom, sc)
			want = want || spec.Sensitive
		}
		assert.Equal(t, want, row.InteractiveOnly, string(row.Atom))
	}
}
