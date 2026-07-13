// file: internal/merge/combine_test.go
// version: 1.1.0
// guid: 2c8e4d1a-9f3b-4e6a-8b7c-1d2e3f4a5b6c
// last-edited: 2026-07-13
// last-edited: 2026-06-21

package merge

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestService_CombineBooks combines three single-file books into ONE multi-file
// book on a chosen survivor. It covers both shapes a "single-file book" can take:
//   - books with a real BookFile row (book1, book3)
//   - a virtual book with only a FilePath and no BookFile row (book2, the survivor)
//
// Asserts: the survivor owns all three files, the two absorbed books are deleted,
// external-id mappings are reassigned to the survivor, and the survivor's own
// virtual file is materialized as a BookFile.
func TestService_CombineBooks(t *testing.T) {
	store := setupTestStore(t)

	// Survivor: virtual single-file book (FilePath only, no BookFile row).
	survivor := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Chapter 02",
		Format:   "mp3",
		FilePath: "/tmp/book/ch02.mp3",
	}
	// book1: real BookFile row.
	book1 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Chapter 01",
		Format:   "mp3",
		FilePath: "/tmp/book/ch01.mp3",
	}
	// book3: real BookFile row.
	book3 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Chapter 03",
		Format:   "mp3",
		FilePath: "/tmp/book/ch03.mp3",
	}

	for _, b := range []*database.Book{survivor, book1, book3} {
		_, err := store.CreateBook(b)
		require.NoError(t, err)
	}

	// Give book1 and book3 real BookFile rows.
	file1 := &database.BookFile{ID: ulid.Make().String(), BookID: book1.ID, FilePath: book1.FilePath, Format: "mp3"}
	require.NoError(t, store.CreateBookFile(file1))
	file3 := &database.BookFile{ID: ulid.Make().String(), BookID: book3.ID, FilePath: book3.FilePath, Format: "mp3"}
	require.NoError(t, store.CreateBookFile(file3))

	// Map an external (iTunes) PID onto each absorbed book to verify reassignment.
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: "PID-1", BookID: book1.ID,
	}))
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: "PID-3", BookID: book3.ID,
	}))

	ms := NewService(store)
	res, err := ms.CombineBooks([]string{book1.ID, book3.ID, survivor.ID}, survivor.ID, nil)
	require.NoError(t, err)

	assert.Equal(t, survivor.ID, res.PrimaryID)
	// 3 files moved: survivor's materialized virtual file + book1's + book3's.
	assert.Equal(t, 3, res.FilesMoved)
	assert.Equal(t, 2, res.BooksDeleted)

	// Survivor owns ALL three files now.
	files, err := store.GetBookFiles(survivor.ID)
	require.NoError(t, err)
	assert.Len(t, files, 3, "survivor should own all 3 files")
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.FilePath] = true
	}
	assert.True(t, paths["/tmp/book/ch01.mp3"], "ch01 present")
	assert.True(t, paths["/tmp/book/ch02.mp3"], "survivor own file materialized")
	assert.True(t, paths["/tmp/book/ch03.mp3"], "ch03 present")

	// The two absorbed books are gone.
	b1, _ := store.GetBookByID(book1.ID)
	assert.Nil(t, b1, "book1 should be hard-deleted")
	b3, _ := store.GetBookByID(book3.ID)
	assert.Nil(t, b3, "book3 should be hard-deleted")

	// External-id mappings reassigned to the survivor.
	if eid := AsExternalIDReassigner(store); eid != nil {
		mappings, err := store.GetExternalIDsForBook(survivor.ID)
		require.NoError(t, err)
		got := map[string]bool{}
		for _, m := range mappings {
			got[m.ExternalID] = true
		}
		assert.True(t, got["PID-1"], "PID-1 reassigned to survivor")
		assert.True(t, got["PID-3"], "PID-3 reassigned to survivor")
	}

	// Data-loss invariant: after a combine (which deletes the absorbed books and
	// materializes files on the survivor) the store must have no dangling index
	// rows or contradictory book states.
	dbtest.AssertStoreInvariants(t, store)
}

func TestService_CombineBooks_TooFew(t *testing.T) {
	ms := NewService(nil)
	_, err := ms.CombineBooks([]string{"one"}, "one", nil)
	require.Error(t, err)
}

func TestService_CombineBooks_RequiresPrimary(t *testing.T) {
	ms := NewService(nil)
	_, err := ms.CombineBooks([]string{"a", "b"}, "", nil)
	require.Error(t, err)
}

func TestService_CombineBooks_PrimaryNotInSet(t *testing.T) {
	store := setupTestStore(t)
	a := &database.Book{ID: ulid.Make().String(), Title: "A", Format: "mp3", FilePath: "/tmp/a.mp3"}
	b := &database.Book{ID: ulid.Make().String(), Title: "B", Format: "mp3", FilePath: "/tmp/b.mp3"}
	_, err := store.CreateBook(a)
	require.NoError(t, err)
	_, err = store.CreateBook(b)
	require.NoError(t, err)

	ms := NewService(store)
	// primaryID resolves to a real book but is not part of bookIDs.
	c := &database.Book{ID: ulid.Make().String(), Title: "C", Format: "mp3", FilePath: "/tmp/c.mp3"}
	_, err = store.CreateBook(c)
	require.NoError(t, err)
	_, err = ms.CombineBooks([]string{a.ID, b.ID}, c.ID, nil)
	require.Error(t, err)
}
