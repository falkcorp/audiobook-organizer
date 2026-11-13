// file: internal/plugins/maintenance/kinds_t6_g4_atoms_test.go
// version: 1.0.0
// guid: 4b9e0d7a-52c3-4f18-8a6d-1e7c3b95a0f2
// last-edited: 2026-11-13

package maintenance_test

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
)

// TestT6G4StartRequirement_MatchesSettingsManageAtom: the scopes group 4's kinds
// require to start are the settings.manage row of internal/books.AtomTable. The
// maintenance package cannot import books, so it carries a copy; this fails when
// the two drift.
func TestT6G4StartRequirement_MatchesSettingsManageAtom(t *testing.T) {
	var want []string
	for _, a := range books.AtomTable() {
		if a.Name == "settings.manage" {
			for _, s := range a.Scopes {
				want = append(want, string(s))
			}
		}
	}
	if len(want) == 0 {
		t.Fatal("AtomTable has no settings.manage row")
	}
	var got []string
	for _, s := range maintenance.T6G4StartRequirement().Permissions() {
		got = append(got, string(s))
	}
	if !slices.Equal(got, want) {
		t.Errorf("group 4 start scopes = %v, want the settings.manage atom's %v", got, want)
	}
}
