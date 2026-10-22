// file: internal/database/pebble_store_list_mark_test.go
// version: 1.0.0
// guid: 2e7b9d41-6a85-4c30-b1f7-9d03e5a8c264
// last-edited: 2026-10-22

package database

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// The revert of a collection or playlist row writes the member list and stamps
// the survivor-side marker in ONE batch, so a death between the two can never
// leave the list written and the marker standing. These tests drive the real
// store: the stamp lands with the list, a refused write stamps nothing, and a
// list left unchanged is still stamped.

func seedMarkRows(t *testing.T, s *PebbleStore, op string, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-row-%d", op, i)
		require.NoError(t, s.CreateOperationChange(&OperationChange{
			ID: ids[i], OperationID: op, BookID: fmt.Sprintf("book-%d", i), ChangeType: "metadata_update",
			FieldName: "title", OldValue: "a", NewValue: "b",
		}))
	}
	return ids
}

func revertedStates(t *testing.T, s *PebbleStore, op string) map[string]bool {
	t.Helper()
	rows, err := s.GetOperationChanges(op)
	require.NoError(t, err)
	out := map[string]bool{}
	for _, r := range rows {
		out[r.ID] = r.RevertedAt != nil
	}
	return out
}

func TestUpdateCollectionBooksAndMark_ListAndStampLandTogether(t *testing.T) {
	s := newTestCollectionsStore(t)
	col, err := s.CreateCollection(&Collection{Name: "c", Type: CollectionTypeStatic, BookIDs: []string{"a", "b"}})
	require.NoError(t, err)
	ids := seedMarkRows(t, s, "op1", 3)

	got, err := s.UpdateCollectionBooksAndMark(col.ID, func(cur []string) ([]string, error) {
		return append(cur, "c"), nil
	}, OperationMark{OperationID: "op1", ChangeIDs: ids[:2]})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, got.BookIDs)

	stored, err := s.GetCollection(col.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, stored.BookIDs)
	require.Equal(t, map[string]bool{ids[0]: true, ids[1]: true, ids[2]: false}, revertedStates(t, s, "op1"))
}

// fn refusing the write (the book is already listed, the survivor was removed
// by hand) stamps nothing: the marker stands for the retry.
func TestUpdateCollectionBooksAndMark_RefusedWriteStampsNothing(t *testing.T) {
	s := newTestCollectionsStore(t)
	col, err := s.CreateCollection(&Collection{Name: "c", Type: CollectionTypeStatic, BookIDs: []string{"a"}})
	require.NoError(t, err)
	ids := seedMarkRows(t, s, "op1", 1)

	boom := errors.New("refused")
	_, err = s.UpdateCollectionBooksAndMark(col.ID, func([]string) ([]string, error) { return nil, boom }, OperationMark{OperationID: "op1", ChangeIDs: ids})
	require.ErrorIs(t, err, boom)
	stored, err := s.GetCollection(col.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, stored.BookIDs)
	require.Equal(t, map[string]bool{ids[0]: false}, revertedStates(t, s, "op1"))

	_, err = s.UpdateCollectionBooksAndMark("missing", func(cur []string) ([]string, error) { return cur, nil }, OperationMark{OperationID: "op1", ChangeIDs: ids})
	require.ErrorIs(t, err, ErrCollectionNotFound)
	require.Equal(t, map[string]bool{ids[0]: false}, revertedStates(t, s, "op1"))
}

// A list that fn leaves as it is still gets its stamp: the caller asked for the
// rows to be reverted with this write.
func TestUpdateCollectionBooksAndMark_UnchangedListIsStillStamped(t *testing.T) {
	s := newTestCollectionsStore(t)
	col, err := s.CreateCollection(&Collection{Name: "c", Type: CollectionTypeStatic, BookIDs: []string{"a"}})
	require.NoError(t, err)
	ids := seedMarkRows(t, s, "op1", 1)

	_, err = s.UpdateCollectionBooksAndMark(col.ID, func(cur []string) ([]string, error) { return cur, nil }, OperationMark{OperationID: "op1", ChangeIDs: ids})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{ids[0]: true}, revertedStates(t, s, "op1"))
}

// Rows already stamped must not stop the list from being written.
func TestUpdateCollectionBooksAndMark_AlreadyStampedRowsStillWriteTheList(t *testing.T) {
	s := newTestCollectionsStore(t)
	col, err := s.CreateCollection(&Collection{Name: "c", Type: CollectionTypeStatic, BookIDs: []string{"a"}})
	require.NoError(t, err)
	ids := seedMarkRows(t, s, "op1", 1)
	require.NoError(t, s.MarkOperationChangesReverted("op1", ids))

	_, err = s.UpdateCollectionBooksAndMark(col.ID, func(cur []string) ([]string, error) { return append(cur, "b"), nil }, OperationMark{OperationID: "op1", ChangeIDs: ids})
	require.NoError(t, err)
	stored, err := s.GetCollection(col.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, stored.BookIDs)
}

func TestUpdateUserPlaylistAndMark_ListAndStampLandTogether(t *testing.T) {
	s := newTestCollectionsStore(t)
	pl, err := s.CreateUserPlaylist(&UserPlaylist{Name: "p", Type: UserPlaylistTypeStatic, BookIDs: []string{"a"}})
	require.NoError(t, err)
	ids := seedMarkRows(t, s, "op1", 2)

	cur, err := s.GetUserPlaylist(pl.ID)
	require.NoError(t, err)
	cur.BookIDs = []string{"a", "b"}
	require.NoError(t, s.UpdateUserPlaylistAndMark(cur, OperationMark{OperationID: "op1", ChangeIDs: ids[:1]}))

	stored, err := s.GetUserPlaylist(pl.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, stored.BookIDs)
	require.Equal(t, map[string]bool{ids[0]: true, ids[1]: false}, revertedStates(t, s, "op1"))
}

// A stale playlist (another writer got in first) is refused by the version
// check and stamps nothing, so the caller re-reads and retries with the marker
// still standing.
func TestUpdateUserPlaylistAndMark_VersionConflictStampsNothing(t *testing.T) {
	s := newTestCollectionsStore(t)
	pl, err := s.CreateUserPlaylist(&UserPlaylist{Name: "p", Type: UserPlaylistTypeStatic, BookIDs: []string{"a"}})
	require.NoError(t, err)
	ids := seedMarkRows(t, s, "op1", 1)

	stale, err := s.GetUserPlaylist(pl.ID)
	require.NoError(t, err)
	winner, err := s.GetUserPlaylist(pl.ID)
	require.NoError(t, err)
	winner.BookIDs = []string{"a", "w"}
	require.NoError(t, s.UpdateUserPlaylist(winner))

	stale.BookIDs = []string{"a", "s"}
	err = s.UpdateUserPlaylistAndMark(stale, OperationMark{OperationID: "op1", ChangeIDs: ids})
	require.ErrorIs(t, err, ErrUserPlaylistVersionConflict)
	stored, err := s.GetUserPlaylist(pl.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "w"}, stored.BookIDs)
	require.Equal(t, map[string]bool{ids[0]: false}, revertedStates(t, s, "op1"))
}
