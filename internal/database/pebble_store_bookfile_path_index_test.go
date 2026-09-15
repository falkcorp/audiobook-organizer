// file: internal/database/pebble_store_bookfile_path_index_test.go
// version: 1.0.0
// guid: 77b0ad96-dd9c-4c25-80da-e5dda6de5d36
// last-edited: 2026-09-14

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// book_file_path is a single-owner index: two rows at one path share one
// entry, naming whichever wrote last. An update of the OTHER row must not
// delete that entry. Before the owner check, moving B off P deleted the entry
// that named A, and GetBookFileByPath(P) returned nothing while A still sat
// at P.
func TestPathIndex_UpdateOfNonOwnerKeepsOwnersEntry(t *testing.T) {
	s := setupTestPebbleStore(t)
	b, err := s.CreateBook(&Book{Title: "T", FilePath: "/lib/T"})
	require.NoError(t, err)

	const p = "/lib/T/01.mp3"
	rowB := &BookFile{BookID: b.ID, FilePath: p, Format: "mp3"}
	require.NoError(t, s.CreateBookFile(rowB))
	rowA := &BookFile{BookID: b.ID, FilePath: p, Format: "mp3"}
	require.NoError(t, s.CreateBookFile(rowA))

	got, err := s.GetBookFileByPath(p)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, rowA.ID, got.ID, "index names the last writer, A")

	// Move B off P. The entry for P names A, so it must survive.
	movedB := *rowB
	movedB.FilePath = "/lib/T/02.mp3"
	require.NoError(t, s.UpdateBookFile(rowB.ID, &movedB))

	got, err = s.GetBookFileByPath(p)
	require.NoError(t, err)
	require.NotNil(t, got, "updating B dropped A's path index entry")
	require.Equal(t, rowA.ID, got.ID)

	got, err = s.GetBookFileByPath("/lib/T/02.mp3")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, rowB.ID, got.ID)

	// Now move the owner, A. Its own entry at P is removed (no row is at P any
	// more) and its new path resolves to it; B's entry is untouched.
	movedA := *rowA
	movedA.FilePath = "/lib/T/03.mp3"
	require.NoError(t, s.UpdateBookFile(rowA.ID, &movedA))

	got, err = s.GetBookFileByPath(p)
	require.NoError(t, err)
	require.Nil(t, got, "no row holds P")

	got, err = s.GetBookFileByPath("/lib/T/03.mp3")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, rowA.ID, got.ID)

	got, err = s.GetBookFileByPath("/lib/T/02.mp3")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, rowB.ID, got.ID)
}

// An in-place update of the owner (path unchanged) keeps the entry.
func TestPathIndex_InPlaceUpdateOfOwnerKeepsEntry(t *testing.T) {
	s := setupTestPebbleStore(t)
	f := seedPatchFile(t, s)

	upd := *f
	upd.Duration = 999
	require.NoError(t, s.UpdateBookFile(f.ID, &upd))

	got, err := s.GetBookFileByPath(f.FilePath)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, f.ID, got.ID)
}
