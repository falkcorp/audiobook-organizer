// file: internal/undo/author_delete_preflight_test.go
// version: 1.0.0
// guid: 0b7c2e94-5d31-4f8a-9e26-c3a1d8f47b05
// last-edited: 2026-10-14

package undo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// legacyNameStore is a ConflictChecker for one author delete row. Its name index
// holds a leftover entry naming a merged-away id: GetAuthorByName follows that
// id's tombstone to a live survivor, while the restore's own read (the raw entry,
// counted only for a live row of that id) finds no holder.
type legacyNameStore struct {
	rows      []*database.OperationChange
	survivor  *database.Author
	rawHolder int
}

func (s legacyNameStore) GetOperationChanges(string) ([]*database.OperationChange, error) {
	return s.rows, nil
}
func (legacyNameStore) GetBookByID(string) (*database.Book, error)  { return nil, nil }
func (legacyNameStore) GetSeriesByID(int) (*database.Series, error) { return nil, nil }
func (legacyNameStore) GetSeriesByName(string, *int) (*database.Series, error) {
	return nil, nil
}
func (legacyNameStore) GetAuthorByID(int) (*database.Author, error) { return nil, nil }
func (s legacyNameStore) GetAuthorByName(string) (*database.Author, error) {
	return s.survivor, nil
}
func (s legacyNameStore) AuthorNameHolder(string) (int, error) { return s.rawHolder, nil }

// The preflight predicts the restore: it reads the name holder the way
// RestoreAuthor does. Reading it through GetAuthorByName refused a row the
// restore would bring back, because a leftover entry naming a merged-away id
// resolved to that id's survivor.
func TestPreflightAuthorDelete_ReadsTheNameHolderAsTheRestoreDoes(t *testing.T) {
	row, err := NewAuthorDeleteRow(ChangeTypeAuthorMergeDelete,
		AuthorSnapshot{ID: 41, Name: "Synthetic Restored"},
		AuthorMergeDelete{IntoAuthorID: 42, IntoName: "Synthetic Keep"})
	require.NoError(t, err)
	row.ID, row.OperationID = "row-1", "op-legacy-name"

	store := legacyNameStore{
		rows:     []*database.OperationChange{row},
		survivor: &database.Author{ID: 77, Name: "Synthetic Survivor"},
	}
	rep, err := PreflightUndoConflicts(store, "op-legacy-name")
	require.NoError(t, err)
	require.Equal(t, 1, rep.Safe, "no live author holds the entry, so the restore writes it: safe")

	store.rawHolder = 77
	rep, err = PreflightUndoConflicts(store, "op-legacy-name")
	require.NoError(t, err)
	require.Zero(t, rep.Safe, "a live third author holding the entry is still refused")
}
