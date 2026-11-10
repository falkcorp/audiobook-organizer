// file: internal/books/store/pebble_book_file_errors_test.go
// version: 1.0.1
// guid: d4e5f6a7-b8c9-0123-defa-123456789012
// last-edited: 2026-11-09

package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordFileError_CreateNew(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	err := ps.RecordFileError("/audio/book.m4b", "book-1", "ffmpeg", "decode error")
	require.NoError(t, err)

	ids, err := ps.ListBooksWithFileErrors()
	require.NoError(t, err)
	assert.Equal(t, []string{"book-1"}, ids)
}

func TestRecordFileError_Idempotent_IncrementsOccurrences(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	require.NoError(t, ps.RecordFileError("/audio/book.m4b", "book-2", "ffmpeg", "first error"))
	require.NoError(t, ps.RecordFileError("/audio/book.m4b", "book-2", "ffmpeg", "second error"))

	// GetBrokenFileCount should count book-2 exactly once.
	count, err := ps.GetBrokenFileCount()
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestClearFileError_RemovesRecord(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	require.NoError(t, ps.RecordFileError("/audio/book.m4b", "book-3", "tag", "bad tag"))
	require.NoError(t, ps.ClearFileError("/audio/book.m4b"))

	count, err := ps.GetBrokenFileCount()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestClearFileError_NonExistent_NoError(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	err := ps.ClearFileError("/nonexistent/path.m4b")
	require.NoError(t, err)
}

func TestListBooksWithFileErrors_MultipleBooks(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	require.NoError(t, ps.RecordFileError("/audio/a.m4b", "book-a", "ffmpeg", "err"))
	require.NoError(t, ps.RecordFileError("/audio/b.m4b", "book-b", "ffmpeg", "err"))
	require.NoError(t, ps.RecordFileError("/audio/a2.m4b", "book-a", "ffmpeg", "err")) // second file, same book

	ids, err := ps.ListBooksWithFileErrors()
	require.NoError(t, err)
	assert.Len(t, ids, 2, "expected 2 distinct books")
}

func TestGetBrokenFileCount_Empty(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	count, err := ps.GetBrokenFileCount()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestGetBookMetadataHashStats_Empty(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	stats, err := ps.GetBookMetadataHashStats()
	require.NoError(t, err)
	require.NotNil(t, stats)
	assert.Equal(t, 0, stats.TotalBooks)
	assert.Equal(t, 0, stats.WithMetadataHash)
	assert.Equal(t, 0, stats.MissingMetadataHash)
}

func TestGetBookMetadataHashStats_Mixed(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	src := "audible"
	asin1, asin2 := "B001", "B002"
	hash := "abc123"
	importPath := "/lib/audiobooks"

	books := []Book{
		{Title: "Hashed Book", MetadataSourceHash: &hash, MetadataSource: &src, ASIN: &asin1, SourceImportPath: &importPath},
		{Title: "Unhashed Book", MetadataSource: &src, ASIN: &asin2, SourceImportPath: &importPath},
		{Title: "No ID Book"},
	}
	for i := range books {
		created, err := store.CreateBook(&books[i])
		require.NoError(t, err)
		books[i].ID = created.ID
	}

	ps := store.(*PebbleStore)
	stats, err := ps.GetBookMetadataHashStats()
	require.NoError(t, err)
	assert.Equal(t, 3, stats.TotalBooks)
	assert.Equal(t, 1, stats.WithMetadataHash, "one book has a hash")
	assert.Equal(t, 2, stats.MissingMetadataHash, "two books are missing hashes")
	assert.Equal(t, 2, stats.WithASINOrISBN, "two books have ASIN")
	assert.Equal(t, 1, stats.MissingHashHasID, "one book is missing hash but has ASIN")
	assert.Len(t, stats.ByLibrary, 1, "one library path")
	assert.Equal(t, "/lib/audiobooks", stats.ByLibrary[0].Path)
	assert.Equal(t, 2, stats.ByLibrary[0].TotalBooks, "two books in that library")
}
