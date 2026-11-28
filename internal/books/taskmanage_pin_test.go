// file: internal/books/taskmanage_pin_test.go
// version: 0.1.0
// guid: 1b6e9d03-4a7c-4f58-9e21-d8c05a3f7b62
// last-edited: 2026-11-22

package books

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/taskmanage"
)

// TestAtomTable_SettingsManageIsTheTaskManageRequirement: the settings.manage
// row's scopes and route requirement are exactly what every books kind states
// as tasks.Permissions.Manage (owner ruling 2026-11-22), so HEAD's route gate
// and the kinds' control gate cannot drift apart.
func TestAtomTable_SettingsManageIsTheTaskManageRequirement(t *testing.T) {
	for _, a := range AtomTable() {
		if a.Name != "settings.manage" {
			continue
		}
		if !slices.Equal(a.Scopes, taskmanage.Scopes()) {
			t.Errorf("settings.manage scopes = %v, want taskmanage.Scopes() %v", a.Scopes, taskmanage.Scopes())
		}
		want := taskmanage.Requirement()
		if a.Route.Kind() != want.Kind() || !slices.Equal(a.Route.Permissions(), want.Permissions()) {
			t.Errorf("settings.manage route = %v, want taskmanage.Requirement() %v", a.Route, want)
		}
		return
	}
	t.Fatal("AtomTable has no settings.manage row")
}
