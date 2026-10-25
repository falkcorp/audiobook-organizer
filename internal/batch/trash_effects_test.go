// file: internal/batch/trash_effects_test.go
// version: 1.0.0
// guid: f183a58d-743d-4d9b-86cd-7f26999abaab
// last-edited: 2026-10-25

package batch

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestTrashEffects_Operations(t *testing.T) {
	cases := []struct {
		name string
		ops  []BatchOperationItem
		want TrashEffects
	}{
		{"metadata update only", []BatchOperationItem{{ID: "a", Action: "update", Updates: map[string]any{"title": "t"}}}, TrashEffects{}},
		{"update with other library_state", []BatchOperationItem{{ID: "a", Action: "update", Updates: map[string]any{"library_state": "organized"}}}, TrashEffects{}},
		{"soft delete", []BatchOperationItem{{ID: "a", Action: "delete"}}, TrashEffects{Trash: true}},
		{"hard_delete false is a soft delete", []BatchOperationItem{{ID: "a", Action: "delete", HardDelete: false}}, TrashEffects{Trash: true}},
		{"hard delete", []BatchOperationItem{{ID: "a", Action: "delete", HardDelete: true}}, TrashEffects{HardDelete: true}},
		{"restore action", []BatchOperationItem{{ID: "a", Action: "restore"}}, TrashEffects{Restore: true}},
		{"update marked_for_deletion true", []BatchOperationItem{{ID: "a", Action: "update", Updates: map[string]any{"marked_for_deletion": true}}}, TrashEffects{Trash: true}},
		{"update marked_for_deletion false", []BatchOperationItem{{ID: "a", Action: "update", Updates: map[string]any{"marked_for_deletion": false}}}, TrashEffects{Restore: true}},
		{"update library_state deleted, any spelling", []BatchOperationItem{{ID: "a", Action: "update", Updates: map[string]any{"library_state": " Deleted "}}}, TrashEffects{Trash: true}},
		{"non-bool marked_for_deletion is ignored by applyUpdates", []BatchOperationItem{{ID: "a", Action: "update", Updates: map[string]any{"marked_for_deletion": "true"}}}, TrashEffects{}},
		{"unknown action does nothing", []BatchOperationItem{{ID: "a", Action: "purge"}}, TrashEffects{}},
		{"mixed list unions the effects", []BatchOperationItem{
			{ID: "a", Action: "update", Updates: map[string]any{"title": "t"}},
			{ID: "b", Action: "restore"},
			{ID: "c", Action: "delete", HardDelete: true},
			{ID: "d", Action: "delete"},
		}, TrashEffects{HardDelete: true, Trash: true, Restore: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := (&BatchOperationsRequest{Operations: tc.ops}).TrashEffects()
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if got.Any() != (tc.want != TrashEffects{}) {
				t.Fatalf("Any() = %v for %+v", got.Any(), got)
			}
		})
	}
}

// The effects are read with the type assertions applyUpdates acts on: for every
// payload the classifier calls trash or restore, applyUpdates really does
// change the deletion columns, and for one it calls neither it does not.
func TestTrashEffects_AgreesWithApplyUpdates(t *testing.T) {
	for _, updates := range []map[string]any{
		{"marked_for_deletion": true},
		{"marked_for_deletion": false},
		{"library_state": "deleted"},
		{"library_state": "DELETED"},
		{"library_state": "organized"},
		{"marked_for_deletion": "true"},
		{"marked_for_deletion": nil},
		{"title": "x"},
	} {
		b := &database.Book{ID: "x", Title: "t"}
		applyUpdates(b, updates)
		touched := b.MarkedForDeletion != nil || (b.LibraryState != nil && strings.EqualFold(*b.LibraryState, "deleted"))
		e := updateEffects(updates)
		if (e.Trash || e.Restore) != touched {
			t.Fatalf("updates %v: classifier %+v but applyUpdates touched deletion state = %v", updates, e, touched)
		}
	}
}
