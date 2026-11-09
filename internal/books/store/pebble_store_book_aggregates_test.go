// file: internal/books/store/pebble_store_book_aggregates_test.go
// version: 1.0.2
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-11-09

package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTestBook is a helper that creates a book with optional Duration/FileSize
// snapshot values (simulating an import-time snapshot).
func makeTestBook(t *testing.T, store Store, title string, snapshotDuration *int, snapshotFileSize *int64) string {
	t.Helper()
	book := &Book{
		Title:    title,
		FilePath: "/tmp/" + title + ".m4b",
		Duration: snapshotDuration,
		FileSize: snapshotFileSize,
	}
	created, err := store.CreateBook(book)
	require.NoError(t, err)
	return created.ID
}

// addFile is a helper that creates a BookFile with given duration/size and
// returns the file ID.
func addFile(t *testing.T, store Store, bookID, path string, duration int, size int64) string {
	t.Helper()
	f := &BookFile{
		BookID:   bookID,
		FilePath: path,
		Duration: Secs(duration),
		FileSize: size,
		Format:   "m4b",
	}
	err := store.CreateBookFile(f)
	require.NoError(t, err)
	return f.ID
}

// TestRecomputeBookAggregates_ThreeFiles verifies that after adding three files
// and then updating one, the book-level Duration and FileSize equal the sums.
func TestRecomputeBookAggregates_ThreeFiles(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Create book with stale snapshot values (simulating import-time data).
	dur := 100
	sz := int64(1000)
	bookID := makeTestBook(t, store, "three-file-book", &dur, &sz)

	// Add 3 files: the hook in CreateBookFile triggers recomputation after each.
	addFile(t, store, bookID, "/tmp/file1.m4b", 3600, 50_000_000)
	addFile(t, store, bookID, "/tmp/file2.m4b", 3600, 60_000_000)
	fid3 := addFile(t, store, bookID, "/tmp/file3.m4b", 3000, 40_000_000)

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book)
	// After 3 creates, aggregate should be sum of all 3 files.
	assert.Equal(t, 3600+3600+3000, *book.Duration, "duration should be sum of 3 files")
	assert.Equal(t, int64(50_000_000+60_000_000+40_000_000), *book.FileSize, "file size should be sum of 3 files")

	// Now update file3 duration to 1800 and verify aggregate updates.
	f3, err := store.GetBookFileByID(bookID, fid3)
	require.NoError(t, err)
	require.NotNil(t, f3)
	f3.Duration = Secs(1800)
	f3.FileSize = 20_000_000
	err = store.UpdateBookFile(fid3, f3)
	require.NoError(t, err)

	book, err = store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book)
	assert.Equal(t, 3600+3600+1800, *book.Duration, "duration should reflect updated file3")
	assert.Equal(t, int64(50_000_000+60_000_000+20_000_000), *book.FileSize, "file size should reflect updated file3")
}

// TestRecomputeBookAggregates_SingleFile verifies that a single-file book
// gets the correct aggregate from one BookFile.
func TestRecomputeBookAggregates_SingleFile(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	bookID := makeTestBook(t, store, "single-file-book", nil, nil)
	addFile(t, store, bookID, "/tmp/single.m4b", 7200, 100_000_000)

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book)
	assert.Equal(t, 7200, *book.Duration)
	assert.Equal(t, int64(100_000_000), *book.FileSize)
}

// TestRecomputeBookAggregates_PartialData_NoClobber verifies the partial-data
// rule: when ALL files have zero duration (e.g., a scanner run didn't populate
// Duration yet), the old non-zero snapshot must be preserved with a WARN.
func TestRecomputeBookAggregates_PartialData_NoClobber(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// Simulate a book with a good existing aggregate (e.g., from a previous
	// full scan) — Duration=10800 from 3 files.
	dur := 10800
	sz := int64(300_000_000)
	bookID := makeTestBook(t, store, "partial-data-book", &dur, &sz)

	// Now CreateBookFile with Duration=0 (scanner didn't fill it in yet).
	// The hook fires, but since filesWithDuration==0 and book.Duration is
	// already non-zero, the partial-data rule must preserve the old value.
	addFile(t, store, bookID, "/tmp/nodur.m4b", 0 /*duration*/, 0 /*size*/)

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book)

	// Old snapshot must be preserved — not clobbered by the zero sum.
	require.NotNil(t, book.Duration)
	assert.Equal(t, 10800, *book.Duration, "partial-data rule: must keep old duration")
	require.NotNil(t, book.FileSize)
	assert.Equal(t, int64(300_000_000), *book.FileSize, "partial-data rule: must keep old file size")
}

// TestRecomputeBookAggregates_PartialData_DoUpdate verifies that when the new
// sum has at least one file-with-duration where the old snapshot had zero, the
// update is written (we always write when the existing is nil or zero).
func TestRecomputeBookAggregates_PartialData_DoUpdate(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// No existing aggregate (nil = no snapshot set).
	bookID := makeTestBook(t, store, "zero-to-nonzero", nil, nil)

	// Add a file with real duration — recompute must write the new sum.
	addFile(t, store, bookID, "/tmp/real.m4b", 5400, 80_000_000)

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, book)
	require.NotNil(t, book.Duration)
	assert.Equal(t, 5400, *book.Duration, "new non-zero aggregate should be written when old is nil")
	assert.Equal(t, int64(80_000_000), *book.FileSize)
}

// TestRecomputeBookAggregates_HookOnUpdate verifies that updating a BookFile's
// duration field triggers the hook and updates Book.Duration.
func TestRecomputeBookAggregates_HookOnUpdate(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	bookID := makeTestBook(t, store, "hook-on-update", nil, nil)
	fid := addFile(t, store, bookID, "/tmp/hooktest.m4b", 3600, 50_000_000)

	// Confirm initial state.
	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.Equal(t, 3600, *book.Duration)

	// Update the file's duration via UpdateBookFile.
	bf, err := store.GetBookFileByID(bookID, fid)
	require.NoError(t, err)
	bf.Duration = Secs(7200)
	require.NoError(t, store.UpdateBookFile(fid, bf))

	// Book should reflect the new duration.
	book, err = store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.Equal(t, 7200, *book.Duration, "hook must update Book.Duration on UpdateBookFile")
}

// TestRecomputeBookAggregates_Idempotent verifies that calling
// RecomputeBookAggregates twice gives the same result.
func TestRecomputeBookAggregates_Idempotent(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	bookID := makeTestBook(t, store, "idempotent-book", nil, nil)
	addFile(t, store, bookID, "/tmp/idem1.m4b", 3600, 50_000_000)
	addFile(t, store, bookID, "/tmp/idem2.m4b", 1800, 25_000_000)

	// Call explicit recompute twice.
	ps := store.(*PebbleStore)
	require.NoError(t, ps.RecomputeBookAggregates(bookID))
	require.NoError(t, ps.RecomputeBookAggregates(bookID))

	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.Equal(t, 3600+1800, *book.Duration, "idempotent: same result on second call")
	assert.Equal(t, int64(50_000_000+25_000_000), *book.FileSize)
}

// TestRecomputeBookAggregates_DeleteFile verifies that deleting a BookFile
// triggers the hook and reduces the aggregate.
func TestRecomputeBookAggregates_DeleteFile(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	bookID := makeTestBook(t, store, "delete-file-book", nil, nil)
	fid1 := addFile(t, store, bookID, "/tmp/del1.m4b", 3600, 50_000_000)
	addFile(t, store, bookID, "/tmp/del2.m4b", 1800, 25_000_000)

	// Confirm both files contribute.
	book, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.Equal(t, 5400, *book.Duration)

	// Delete one file — aggregate must shrink.
	require.NoError(t, store.DeleteBookFile(fid1))

	book, err = store.GetBookByID(bookID)
	require.NoError(t, err)
	assert.Equal(t, 1800, *book.Duration, "duration must shrink after DeleteBookFile")
	assert.Equal(t, int64(25_000_000), *book.FileSize)
}

// TestRecomputeBookAggregates_BackfillSentinel verifies that after a direct
// call to MarkBookAggregatesBackfillDone, IsBookAggregatesBackfillDone
// returns true and a second check does not re-run.
func TestRecomputeBookAggregates_BackfillSentinel(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	ps := store.(*PebbleStore)

	assert.False(t, ps.IsBookAggregatesBackfillDone(), "sentinel must be absent before marking done")
	require.NoError(t, ps.MarkBookAggregatesBackfillDone())
	assert.True(t, ps.IsBookAggregatesBackfillDone(), "sentinel must be present after marking done")
}
