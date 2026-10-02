// file: internal/reconcile/soft_delete_records_state_test.go
// version: 1.0.0
// guid: c484bb18-2691-4395-ac04-9a531e0d13c4
// last-edited: 2026-10-01

package reconcile

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// lockFreeStore answers the field-lock read the metadata merge makes before
// the soft delete: no locks.
type lockFreeStore struct{ *fakeReconcileStore }

func (lockFreeStore) GetMetadataFieldStates(string) ([]database.MetadataFieldState, error) {
	return nil, nil
}

func (lockFreeStore) GetUserPreference(string) (*database.UserPreference, error) {
	return nil, nil
}

// MergeNoVGDuplicates' soft delete relabels the duplicate "deleted"; it must
// remember the state it overwrote so a restore can put it back
// (database.RestoreLibraryStateFromTrash).
func TestMergeNoVGDuplicates_SoftDeleteRecordsThePriorState(t *testing.T) {
	s := newFakeStore()
	vg := "vg-1"
	primary := true
	addCore(s, database.BookCore{ID: "p", Title: "Same", FilePath: "/lib/p/p.m4b", VersionGroupID: &vg, IsPrimaryVersion: &primary})
	s.byID["p"].IsPrimaryVersion = &primary
	addCore(s, database.BookCore{ID: "dupe", Title: "Same", FilePath: "/lib/d/d.m4b"})
	organized := "organized"
	s.byID["dupe"].LibraryState = &organized

	if _, err := MergeNoVGDuplicates(lockFreeStore{s}, "/lib", false); err != nil {
		t.Fatal(err)
	}
	got := s.byID["dupe"]
	if !got.IsSoftDeleted() || got.LibraryState == nil || *got.LibraryState != "deleted" {
		t.Fatalf("dupe not soft-deleted as expected: %+v", got)
	}
	if got.PreTrashLibraryState == nil || *got.PreTrashLibraryState != "organized" {
		t.Errorf("PreTrashLibraryState = %v, want organized", got.PreTrashLibraryState)
	}
}
