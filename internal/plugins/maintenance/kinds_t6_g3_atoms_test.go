// file: internal/plugins/maintenance/kinds_t6_g3_atoms_test.go
// version: 1.0.0
// guid: 7c3d9a18-5e2b-4f60-8b94-1a6e0d4f2c85
// last-edited: 2026-11-13

package maintenance_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestT6G3_StartRequirementMatchesAtomTable fails when the settings.manage
// scopes copied into the group's kinds drift from internal/books.AtomTable
// (internal/books imports this package, so the group cannot read the table).
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
	var got []string
	for _, p := range maintenance.T6G3StartRequirement().Permissions() {
		got = append(got, string(p))
	}
	assert.Equal(t, want, got)
}
