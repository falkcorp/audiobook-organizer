// file: internal/database/bookfiles_for_ids_fallback_test.go
// version: 1.2.2
// guid: 8c2f6d14-7a39-4e05-b1d8-3e9a5c07f2b6
// last-edited: 2026-10-05

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Pebble fallback of GetBookFilesForIDsCore (memdb not published) reads a
// book_file:<id>: range per book for up to perBookRangeMaxIDs ids, and scans
// every row above that. Both must give the same answer: rows grouped by book,
// a book with no rows absent, an unknown or deleted book absent.
func TestGetBookFilesForIDsCore_PebbleFallbackPerBookMatchesFullScan(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	// The memdb warmup is asynchronous, so asserting that it has NOT published
	// yet races (it failed on the CI race runner 2026-10-03). Force the Pebble
	// path instead: that is what this test is about.
	store.UseMemDB = false

	withFiles, err := store.CreateBook(&Book{Title: "Two Files", FilePath: "/lib/a", Format: "mp3"})
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, store.CreateBookFile(&BookFile{
			ID: fmt.Sprintf("fa-%d", i), BookID: withFiles.ID, FilePath: fmt.Sprintf("/lib/a/%d.mp3", i), TrackNumber: 2 - i,
		}))
	}
	noFiles, err := store.CreateBook(&Book{Title: "No Files", FilePath: "/lib/b", Format: "mp3"})
	require.NoError(t, err)
	gone, err := store.CreateBook(&Book{Title: "Deleted", FilePath: "/lib/c", Format: "mp3"})
	require.NoError(t, err)
	require.NoError(t, store.DeleteBook(gone.ID))

	ids := []string{withFiles.ID, noFiles.ID, gone.ID, "never-existed", withFiles.ID, ""}
	got, err := store.getBookFilesForIDsPebbleScan(ids)
	require.NoError(t, err)
	require.Len(t, got, 1, "only the book with rows appears")
	require.Len(t, got[withFiles.ID], 2)
	for _, f := range got[withFiles.ID] {
		require.Equal(t, withFiles.ID, f.BookID)
	}

	// Past the per-book threshold the full scan runs; same answer.
	many := append([]string{}, ids...)
	for i := len(many); i <= perBookRangeMaxIDs; i++ {
		many = append(many, fmt.Sprintf("pad-%d", i))
	}
	require.Greater(t, len(many), perBookRangeMaxIDs)
	scanned, err := store.getBookFilesForIDsPebbleScan(many)
	require.NoError(t, err)
	require.Len(t, scanned, 1)
	require.ElementsMatch(t, got[withFiles.ID], scanned[withFiles.ID])
}

// MetadataCacheGeneration moves on every cache write, Put and Delete alike,
// and on nothing else: the review snapshot rebuilds exactly when it moves.
func TestMetadataCacheGeneration_CountsCacheWrites(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	g0 := store.MetadataCacheGeneration()
	_, err = store.GetMetadataCache("b1")
	require.NoError(t, err)
	_, err = store.ListMetadataCacheKeys()
	require.NoError(t, err)
	require.Equal(t, g0, store.MetadataCacheGeneration(), "reads do not move it")
	require.NoError(t, store.PutMetadataCache(&MetadataCandidateCache{BookID: "b1"}))
	require.Equal(t, g0+1, store.MetadataCacheGeneration())
	require.NoError(t, store.DeleteMetadataCache("b1"))
	require.Equal(t, g0+2, store.MetadataCacheGeneration())
	require.Error(t, store.PutMetadataCache(&MetadataCandidateCache{}))
	require.Equal(t, g0+2, store.MetadataCacheGeneration(), "a refused write does not move it")
}

// UpdateBook's identity-change delete and DeleteBook's sidecar delete remove
// a book's cache row inside the book's own batch; both must move the
// generation (after the commit), or the review snapshot keeps serving a
// candidate that no longer exists. A write that deletes no row must not.
func TestMetadataCacheGeneration_MovesOnBookWritesThatDeleteTheRow(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&Book{Title: "T", FilePath: "/lib/t", Format: "mp3"})
	require.NoError(t, err)
	require.NoError(t, store.PutMetadataCache(&MetadataCandidateCache{BookID: book.ID}))

	g := store.MetadataCacheGeneration()
	book.Title = "T Retitled"
	_, err = store.UpdateBook(book.ID, book)
	require.NoError(t, err)
	require.Equal(t, g+1, store.MetadataCacheGeneration(), "the title change deleted the cache row")
	entry, err := store.GetMetadataCache(book.ID)
	require.NoError(t, err)
	require.Nil(t, entry)

	// No row left: another identity change deletes nothing and moves nothing.
	book.Title = "T Retitled Again"
	_, err = store.UpdateBook(book.ID, book)
	require.NoError(t, err)
	require.Equal(t, g+1, store.MetadataCacheGeneration())

	require.NoError(t, store.PutMetadataCache(&MetadataCandidateCache{BookID: book.ID}))
	g = store.MetadataCacheGeneration()
	require.NoError(t, store.DeleteBook(book.ID))
	require.Equal(t, g+1, store.MetadataCacheGeneration(), "DeleteBook deleted the cache row")

	require.NoError(t, store.SetRaw("metadata_cache:raw", []byte("{}")))
	require.NoError(t, store.DeleteRaw("metadata_cache:raw"))
	require.NoError(t, store.SetRaw("other:raw", []byte("{}")))
	require.Equal(t, g+3, store.MetadataCacheGeneration(), "raw writes count only under the prefix")
}
