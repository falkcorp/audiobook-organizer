// file: internal/plugins/maintenance/kinds_t6_g1_drift_test.go
// version: 1.0.1
// guid: 6e2f9b84-0d17-4a53-b8c6-1f5a3d90e7b2
// last-edited: 2026-11-13

package maintenance_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// TestT6G1StartDrift_MatchesSettingsManageAtom keeps t6g1SettingsManageScopes (a
// copy, because internal/books imports package maintenance) equal to the
// settings.manage atom every group 1 definition is gated on at HEAD.
func TestT6G1StartDrift_MatchesSettingsManageAtom(t *testing.T) {
	var want []string
	var wantKind rbac.RequirementKind
	for _, a := range books.AtomTable() {
		if a.Name == "settings.manage" {
			wantKind = a.Route.Kind()
			for _, p := range a.Route.Permissions() {
				want = append(want, string(p))
			}
		}
	}
	require.NotEmpty(t, want, "settings.manage atom not found")
	slices.Sort(want)

	defs := maintenance.T6G1KindsForTest()
	require.Len(t, defs, 14)
	for _, d := range defs {
		start := tasks.CommonOf(d).Perms.Start
		require.Equal(t, rbac.KindAllOf, start.Kind(), "%s start must need every scope (AllOf)", d.Kind())
		require.Equal(t, wantKind, start.Kind(), "%s start kind drifted from the settings.manage atom", d.Kind())
		var got []string
		for _, p := range start.Permissions() {
			got = append(got, string(p))
		}
		slices.Sort(got)
		require.Equal(t, want, got, "%s start requirement drifted from the settings.manage atom", d.Kind())
	}
}
