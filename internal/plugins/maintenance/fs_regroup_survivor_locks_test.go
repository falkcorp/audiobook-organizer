// file: internal/plugins/maintenance/fs_regroup_survivor_locks_test.go
// version: 1.0.0
// guid: 6b1d9e47-2c85-4f30-a7d1-0e93b5c8f264
// last-edited: 2026-11-18

package maintenance

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// updateSurvivor writes a title that is a lockable column and a folder path that
// is not. These tests pin that the title honours the user's lock (and fails
// closed on an unreadable lock set) while the path, which no lock covers, still
// moves: a hold that stopped both would look the same as a guard that works.

type survivorLockErrStore struct{ *database.PebbleStore }

func (survivorLockErrStore) GetMetadataFieldStates(string) ([]database.MetadataFieldState, error) {
	return nil, fmt.Errorf("field states: store closed")
}

func survivorFixture(t *testing.T) (*database.PebbleStore, string, fsRepairGroup) {
	t.Helper()
	st := newRelinkTrashedStore(t)
	b, err := st.CreateBook(&database.Book{
		Title:            "Gotham Fragment 1",
		FilePath:         "/relink-trashed/survivor/old",
		IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)
	return st, b.ID, fsRepairGroup{
		Category:   "fragments",
		BookFolder: "/relink-trashed/survivor/new",
		Title:      "The Long Halloween",
		SurvivorID: b.ID,
	}
}

func journalFields(t *testing.T, st *database.PebbleStore, opID string) map[string]int {
	t.Helper()
	rows, err := st.GetOperationChanges(opID)
	require.NoError(t, err)
	out := map[string]int{}
	for _, r := range rows {
		out[r.FieldName]++
	}
	return out
}

func TestUpdateSurvivor_UnlockedTitleAndPathBothMove(t *testing.T) {
	st, id, g := survivorFixture(t)
	a := &fsApplier{store: st, opID: "op-survivor-ctl", reporter: &fakeReporter{}}

	a.updateSurvivor(g)

	got, err := st.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, "The Long Halloween", got.Title)
	require.Equal(t, "/relink-trashed/survivor/new", got.FilePath)
	require.Zero(t, a.titleKept.Load())
	require.Equal(t, map[string]int{"title": 1, "file_path": 1}, journalFields(t, st, "op-survivor-ctl"))
}

func TestUpdateSurvivor_UserLockedTitleIsKeptAndThePathStillMoves(t *testing.T) {
	st, id, g := survivorFixture(t)
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: id, Field: database.FieldKeyTitle, OverrideLocked: true,
	}))
	a := &fsApplier{store: st, opID: "op-survivor-lock", reporter: &fakeReporter{}}

	a.updateSurvivor(g)

	got, err := st.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, "Gotham Fragment 1", got.Title, "the locked title was rewritten")
	require.Equal(t, "/relink-trashed/survivor/new", got.FilePath, "the path is not lockable and must still move")
	require.EqualValues(t, 1, a.titleKept.Load())
	require.Equal(t, map[string]int{"file_path": 1}, journalFields(t, st, "op-survivor-lock"), "a title row was journaled for a title that did not change")
}

func TestUpdateSurvivor_UnreadableLocksKeepTheTitle(t *testing.T) {
	st, id, g := survivorFixture(t)
	a := &fsApplier{store: survivorLockErrStore{st}, opID: "op-survivor-err", reporter: &fakeReporter{}}

	a.updateSurvivor(g)

	got, err := st.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, "Gotham Fragment 1", got.Title, "an unreadable lock set was treated as not locked")
	require.Equal(t, "/relink-trashed/survivor/new", got.FilePath)
	require.EqualValues(t, 1, a.titleKept.Load())
}
