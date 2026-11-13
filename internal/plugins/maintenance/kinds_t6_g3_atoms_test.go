// file: internal/plugins/maintenance/kinds_t6_g3_atoms_test.go
// version: 1.1.0
// guid: 7c3d9a18-5e2b-4f60-8b94-1a6e0d4f2c85
// last-edited: 2026-11-13

package maintenance_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestT6G3_StartRequirementMatchesAtomTable fails when the Perms.Start of any
// kind of the group drifts from internal/books.AtomTable's settings.manage row
// (internal/books imports this package, so the group cannot read the table).
// It reads the Perms.Start each kind is built with, and every one of the 14
// must be an all-of requirement of exactly the table's scopes, in its order.
func TestT6G3_StartRequirementMatchesAtomTable(t *testing.T) {
	var want []string
	for _, a := range books.AtomTable() {
		if a.Name == "settings.manage" {
			for _, p := range a.Route.Permissions() {
				want = append(want, string(p))
			}
		}
	}
	require.NotEmpty(t, want, "the atom table has a settings.manage row with scopes")
	starts := maintenance.T6G3KindStarts()
	require.Len(t, starts, 14, "every kind of the group is read")
	for kind, req := range starts {
		assert.Equal(t, rbac.KindAllOf, req.Kind(), "%s: HEAD's settings.manage needs every scope", kind)
		var got []string
		for _, p := range req.Permissions() {
			got = append(got, string(p))
		}
		assert.Equal(t, want, got, "%s", kind)
	}
}
