// file: internal/database/pebble_store_book_path_owner_test.go
// version: 1.0.0
// guid: 8f1c3e57-2a94-4d6b-b0e8-4c7a9d2f5e31
// last-edited: 2026-09-13

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func pathOwner(t *testing.T, s *PebbleStore, path string) string {
	t.Helper()
	b, err := s.GetBookByFilePath(path)
	require.NoError(t, err)
	if b == nil {
		return ""
	}
	return b.ID
}

// A second live book created on an occupied path does not take the first
// book's lookup key; both remain in the multi-valued index.
func TestCreateBook_DoesNotTakePathKeyFromLiveOwner(t *testing.T) {
	s := setupTestPebbleStore(t)
	a, err := s.CreateBook(&Book{Title: "A", FilePath: "/lib/Shared"})
	require.NoError(t, err)
	b, err := s.CreateBook(&Book{Title: "B", FilePath: "/lib/Shared"})
	require.NoError(t, err)

	require.Equal(t, a.ID, pathOwner(t, s, "/lib/Shared"))
	ids, err := s.LiveBookIDsAtPath("/lib/Shared")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{a.ID, b.ID}, ids)
}

// A key naming a trashed book is stale: a new book takes it.
func TestCreateBook_TakesPathKeyFromSoftDeletedOwner(t *testing.T) {
	s := setupTestPebbleStore(t)
	a, err := s.CreateBook(&Book{Title: "A", FilePath: "/lib/Shared"})
	require.NoError(t, err)
	yes := true
	a.MarkedForDeletion = &yes
	_, err = s.UpdateBook(a.ID, a)
	require.NoError(t, err)

	b, err := s.CreateBook(&Book{Title: "B", FilePath: "/lib/Shared"})
	require.NoError(t, err)
	require.Equal(t, b.ID, pathOwner(t, s, "/lib/Shared"))
}

// Moving a book off a path whose key another book owns leaves that key
// alone (compare-and-delete), and moving onto an owned path does not take it.
func TestUpdateBook_PathMoveComparesBeforeDeleting(t *testing.T) {
	s := setupTestPebbleStore(t)
	owner, err := s.CreateBook(&Book{Title: "Owner", FilePath: "/lib/New"})
	require.NoError(t, err)
	guest, err := s.CreateBook(&Book{Title: "Guest", FilePath: "/lib/New"})
	require.NoError(t, err)
	require.Equal(t, owner.ID, pathOwner(t, s, "/lib/New"))

	guest.FilePath = "/lib/Elsewhere"
	_, err = s.UpdateBook(guest.ID, guest)
	require.NoError(t, err)
	require.Equal(t, owner.ID, pathOwner(t, s, "/lib/New"), "guest leaving must not delete the owner's key")
	require.Equal(t, guest.ID, pathOwner(t, s, "/lib/Elsewhere"))

	guest.FilePath = "/lib/New"
	_, err = s.UpdateBook(guest.ID, guest)
	require.NoError(t, err)
	require.Equal(t, owner.ID, pathOwner(t, s, "/lib/New"), "guest arriving must not take the owner's key")
	require.Equal(t, "", pathOwner(t, s, "/lib/Elsewhere"), "guest's own old key is removed")

	// The owner moving away still deletes its own key.
	owner.FilePath = "/lib/Moved"
	_, err = s.UpdateBook(owner.ID, owner)
	require.NoError(t, err)
	require.Equal(t, owner.ID, pathOwner(t, s, "/lib/Moved"))
	require.Equal(t, "", pathOwner(t, s, "/lib/New"))
}

// Deleting a book whose path key another book owns leaves the key alone;
// deleting the owner removes it.
func TestDeleteBook_PathKeyCompareAndDelete(t *testing.T) {
	s := setupTestPebbleStore(t)
	owner, err := s.CreateBook(&Book{Title: "Owner", FilePath: "/lib/New"})
	require.NoError(t, err)
	guest, err := s.CreateBook(&Book{Title: "Guest", FilePath: "/lib/New"})
	require.NoError(t, err)

	require.NoError(t, s.DeleteBook(guest.ID))
	require.Equal(t, owner.ID, pathOwner(t, s, "/lib/New"))

	require.NoError(t, s.DeleteBook(owner.ID))
	require.Equal(t, "", pathOwner(t, s, "/lib/New"))
}
