// file: internal/plugins/maintenance/kinds_t6_g4_atoms_test.go
// version: 1.0.3
// guid: 4b9e0d7a-52c3-4f18-8a6d-1e7c3b95a0f2
// last-edited: 2026-11-15

package maintenance_test

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/mediacore/rbac"
)

// TestT6G4StartRequirement_MatchesSettingsManageAtom: the scopes each of group 4's kinds
// requires to start are the settings.manage row of internal/books.AtomTable. The
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
	starts := maintenance.T6G4KindStarts(t)
	if len(starts) != 14 {
		t.Fatalf("group 4 has %d native kinds, want 14", len(starts))
	}
	// Every kind, not just the shared constructor's value: each one's declared
	// Start requirement must be the settings.manage atom.
	for kind, req := range starts {
		if req.Kind() != rbac.KindAllOf {
			t.Errorf("%s: start requirement kind = %v, want AllOf (HEAD's settings.manage needs every scope)", kind, req.Kind())
		}
		var got []string
		for _, s := range req.Permissions() {
			got = append(got, string(s))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: start scopes = %v, want the settings.manage atom's %v", kind, got, want)
		}
	}
	if req := maintenance.T6G4StartRequirement(); req.Kind() != rbac.KindAllOf {
		t.Errorf("shared start requirement kind = %v, want AllOf", req.Kind())
	}
}
