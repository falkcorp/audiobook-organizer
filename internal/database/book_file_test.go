// file: internal/database/book_file_test.go
// version: 1.1.0
// guid: b7c8d9e0-f1a2-3b4c-5d6e-7f8a9b0c1d2e
// last-edited: 2026-07-01

package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestStoreWithBook creates a temp SQLiteStore, runs all migrations (so that
// book_files and other migration-gated tables exist), creates a single Book for
// FK purposes, and returns the store, the book ID, and a cleanup function.
func newTestStoreWithBook(t *testing.T) (Store, string, func()) {
	t.Helper()
	store, cleanup := setupTestDB(t)

	if err := RunMigrations(store); err != nil {
		cleanup()
		t.Fatalf("RunMigrations failed: %v", err)
	}

	book := &Book{
		Title:    "Test Book",
		FilePath: "/tmp/test_book.m4b",
	}
	created, err := store.CreateBook(book)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)

	return store, created.ID, cleanup
}

// TestCreateBookFile creates a file with all fields populated and verifies they
// round-trip through the database unchanged.
func TestCreateBookFile(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	f := &BookFile{
		BookID:             bookID,
		FilePath:           "/mnt/books/narnia/disc1_track01.m4b",
		OriginalFilename:   "disc1_track01.m4b",
		ITunesPath:         "/iTunes/Narnia/disc1_track01.m4b",
		ITunesPersistentID: "ABCDEF1234567890",
		TrackNumber:        1,
		TrackCount:         8,
		DiscNumber:         1,
		DiscCount:          2,
		Title:              "The Lion, the Witch and the Wardrobe",
		Format:             "m4b",
		Codec:              "aac",
		Duration:           3600,
		FileSize:           104857600,
		BitrateKbps:        64,
		SampleRateHz:       44100,
		Channels:           2,
		BitDepth:           16,
		FileHash:           "sha256:abc123",
		OriginalFileHash:   "sha256:original456",
		Missing:            false,
	}

	err := store.CreateBookFile(f)
	require.NoError(t, err)
	assert.NotEmpty(t, f.ID, "ID should be populated after CreateBookFile")
	assert.False(t, f.CreatedAt.IsZero(), "CreatedAt should be set")
	assert.False(t, f.UpdatedAt.IsZero(), "UpdatedAt should be set")

	// Retrieve and verify all fields.
	got, err := store.GetBookFileByPath(f.FilePath)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, f.ID, got.ID)
	assert.Equal(t, bookID, got.BookID)
	assert.Equal(t, "/mnt/books/narnia/disc1_track01.m4b", got.FilePath)
	assert.Equal(t, "disc1_track01.m4b", got.OriginalFilename)
	assert.Equal(t, "/iTunes/Narnia/disc1_track01.m4b", got.ITunesPath)
	assert.Equal(t, "ABCDEF1234567890", got.ITunesPersistentID)
	assert.Equal(t, 1, got.TrackNumber)
	assert.Equal(t, 8, got.TrackCount)
	assert.Equal(t, 1, got.DiscNumber)
	assert.Equal(t, 2, got.DiscCount)
	assert.Equal(t, "The Lion, the Witch and the Wardrobe", got.Title)
	assert.Equal(t, "m4b", got.Format)
	assert.Equal(t, "aac", got.Codec)
	assert.Equal(t, 3600, got.Duration)
	assert.Equal(t, int64(104857600), got.FileSize)
	assert.Equal(t, 64, got.BitrateKbps)
	assert.Equal(t, 44100, got.SampleRateHz)
	assert.Equal(t, 2, got.Channels)
	assert.Equal(t, 16, got.BitDepth)
	assert.Equal(t, "sha256:abc123", got.FileHash)
	assert.Equal(t, "sha256:original456", got.OriginalFileHash)
	assert.False(t, got.Missing)
}

// TestCreateBookFile_DownloadHashRoundTrips verifies the DownloadHash
// provenance field persists through CreateBookFile/GetBookFileByID unchanged.
func TestCreateBookFile_DownloadHashRoundTrips(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	f := &BookFile{
		BookID:       bookID,
		FilePath:     "/mnt/books/narnia/download_hash_test.m4b",
		DelugeHash:   "torrent-info-hash",
		DownloadHash: "download-hash-value",
	}
	err := store.CreateBookFile(f)
	require.NoError(t, err)
	require.NotEmpty(t, f.ID)

	got, err := store.GetBookFileByID(bookID, f.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "torrent-info-hash", got.DelugeHash)
	assert.Equal(t, "download-hash-value", got.DownloadHash)
}

// TestGetBookFiles creates 3 files for one book with different disc/track
// numbers and verifies the returned slice is sorted disc ASC, track ASC,
// file_path ASC.
func TestGetBookFiles(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	files := []*BookFile{
		{BookID: bookID, FilePath: "/books/a/disc2_track01.m4b", DiscNumber: 2, TrackNumber: 1},
		{BookID: bookID, FilePath: "/books/a/disc1_track02.m4b", DiscNumber: 1, TrackNumber: 2},
		{BookID: bookID, FilePath: "/books/a/disc1_track01.m4b", DiscNumber: 1, TrackNumber: 1},
	}
	for _, f := range files {
		require.NoError(t, store.CreateBookFile(f))
	}

	got, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, got, 3)

	// Expected order: disc1/track1, disc1/track2, disc2/track1
	assert.Equal(t, "/books/a/disc1_track01.m4b", got[0].FilePath)
	assert.Equal(t, "/books/a/disc1_track02.m4b", got[1].FilePath)
	assert.Equal(t, "/books/a/disc2_track01.m4b", got[2].FilePath)
}

// TestGetBookFileByPID creates a file with an iTunes PID and looks it up by PID.
func TestGetBookFileByPID(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	pid := "FEED0BEEFCAFE0001"
	f := &BookFile{
		BookID:             bookID,
		FilePath:           "/books/pid_test.m4b",
		ITunesPersistentID: pid,
	}
	require.NoError(t, store.CreateBookFile(f))

	got, err := store.GetBookFileByPID(pid)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, f.ID, got.ID)
	assert.Equal(t, pid, got.ITunesPersistentID)
	assert.Equal(t, bookID, got.BookID)
}

// TestGetBookFileByPID_NotFound verifies nil is returned for an unknown PID.
func TestGetBookFileByPID_NotFound(t *testing.T) {
	store, _, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	got, err := store.GetBookFileByPID("DOESNOTEXIST")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestClearITunesPID inserts a file tagged with an iTunes PID, clears it,
// and verifies the PID + path are wiped and the row is no longer findable
// by PID. Covers the cleanup-orphans convergence path: after the ITL
// remove succeeds, the DB row must be scrubbed so cleanup-orphans stops
// re-enqueuing the same PID.
func TestClearITunesPID(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	pid := "DEADBEEFCAFE1234"
	f := &BookFile{
		BookID:             bookID,
		FilePath:           "/books/clear_test.m4b",
		ITunesPersistentID: pid,
		ITunesPath:         "C:\\iTunes\\clear_test.m4b",
	}
	require.NoError(t, store.CreateBookFile(f))

	cleared, err := store.ClearITunesPID(pid)
	require.NoError(t, err)
	assert.True(t, cleared)

	// PID lookup should now miss.
	got, err := store.GetBookFileByPID(pid)
	require.NoError(t, err)
	assert.Nil(t, got)

	// Row still present, just scrubbed.
	byID, err := store.GetBookFileByID(bookID, f.ID)
	require.NoError(t, err)
	require.NotNil(t, byID)
	assert.Empty(t, byID.ITunesPersistentID)
	assert.Empty(t, byID.ITunesPath)

	// Idempotent: clearing an already-cleared PID returns cleared=false.
	cleared2, err := store.ClearITunesPID(pid)
	require.NoError(t, err)
	assert.False(t, cleared2)
}

// TestGetBookFileByPath creates a file and retrieves it by its path.
func TestGetBookFileByPath(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	path := "/books/path_test.m4b"
	f := &BookFile{BookID: bookID, FilePath: path}
	require.NoError(t, store.CreateBookFile(f))

	got, err := store.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, f.ID, got.ID)
	assert.Equal(t, path, got.FilePath)
}

// TestGetBookFileByPath_NotFound verifies nil is returned for an unknown path.
func TestGetBookFileByPath_NotFound(t *testing.T) {
	store, _, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	got, err := store.GetBookFileByPath("/no/such/file.m4b")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestUpdateBookFile creates a file, updates several fields, then reads back
// and verifies the changes were persisted.
func TestUpdateBookFile(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	f := &BookFile{
		BookID:      bookID,
		FilePath:    "/books/update_test.m4b",
		TrackNumber: 1,
		Duration:    1000,
		Missing:     false,
	}
	require.NoError(t, store.CreateBookFile(f))
	originalCreatedAt := f.CreatedAt

	// Modify fields.
	f.TrackNumber = 5
	f.Duration = 9999
	f.Title = "Updated Title"
	f.Format = "mp3"
	f.Missing = true
	f.FilePath = "/books/update_test_renamed.m4b"

	err := store.UpdateBookFile(f.ID, f)
	require.NoError(t, err)

	got, err := store.GetBookFileByPath("/books/update_test_renamed.m4b")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, 5, got.TrackNumber)
	assert.Equal(t, 9999, got.Duration)
	assert.Equal(t, "Updated Title", got.Title)
	assert.Equal(t, "mp3", got.Format)
	assert.True(t, got.Missing)
	// CreatedAt should not change on update.
	assert.Equal(t, originalCreatedAt.Unix(), got.CreatedAt.Unix())
	// UpdatedAt should advance.
	assert.True(t, got.UpdatedAt.Equal(originalCreatedAt) || got.UpdatedAt.After(originalCreatedAt))
}

// TestDeleteBookFile creates a file, deletes it by ID, and verifies it is gone.
func TestDeleteBookFile(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	f := &BookFile{BookID: bookID, FilePath: "/books/delete_me.m4b"}
	require.NoError(t, store.CreateBookFile(f))
	require.NotEmpty(t, f.ID)

	err := store.DeleteBookFile(f.ID)
	require.NoError(t, err)

	got, err := store.GetBookFileByPath("/books/delete_me.m4b")
	require.NoError(t, err)
	assert.Nil(t, got, "file should be gone after DeleteBookFile")
}

// TestDeleteBookFilesForBook creates 3 files for one book, deletes all of them
// at once, and verifies none remain.
func TestDeleteBookFilesForBook(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		f := &BookFile{
			BookID:      bookID,
			FilePath:    "/books/bulk_delete_" + string(rune('a'+i)) + ".m4b",
			TrackNumber: i + 1,
		}
		require.NoError(t, store.CreateBookFile(f))
	}

	// Sanity check: 3 files exist.
	files, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, files, 3)

	err = store.DeleteBookFilesForBook(bookID)
	require.NoError(t, err)

	files, err = store.GetBookFiles(bookID)
	require.NoError(t, err)
	assert.Empty(t, files, "all files should be deleted")
}

// TestUpsertBookFile_MatchByPID creates a file with a PID, then upserts with
// the same book_id+PID but a different path. Expects an update (no duplicate).
func TestUpsertBookFile_MatchByPID(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	pid := "PID_UPSERT_TEST_01"
	f := &BookFile{
		BookID:             bookID,
		FilePath:           "/books/original_path.m4b",
		ITunesPersistentID: pid,
		TrackNumber:        1,
	}
	require.NoError(t, store.CreateBookFile(f))
	originalID := f.ID

	// Upsert: same PID, new path.
	updated := &BookFile{
		BookID:             bookID,
		FilePath:           "/books/new_path.m4b",
		ITunesPersistentID: pid,
		TrackNumber:        2,
	}
	err := store.UpsertBookFile(updated)
	require.NoError(t, err)

	// Should reuse the same row ID.
	assert.Equal(t, originalID, updated.ID, "Upsert by PID should update, not insert a new row")

	// Old path gone, new path visible.
	oldEntry, err := store.GetBookFileByPath("/books/original_path.m4b")
	require.NoError(t, err)
	assert.Nil(t, oldEntry, "old path should be gone after update")

	newEntry, err := store.GetBookFileByPath("/books/new_path.m4b")
	require.NoError(t, err)
	require.NotNil(t, newEntry)
	assert.Equal(t, originalID, newEntry.ID)
	assert.Equal(t, 2, newEntry.TrackNumber)

	// Only one file should exist for this book.
	all, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

// TestUpsertBookFile_MatchByPath creates a file without a PID, then upserts
// with same book_id+path. Expects an update, not a duplicate row.
func TestUpsertBookFile_MatchByPath(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	path := "/books/path_upsert.m4b"
	f := &BookFile{
		BookID:      bookID,
		FilePath:    path,
		TrackNumber: 1,
		Duration:    500,
	}
	require.NoError(t, store.CreateBookFile(f))
	originalID := f.ID

	// Upsert with same path, no PID, updated fields.
	updated := &BookFile{
		BookID:      bookID,
		FilePath:    path,
		TrackNumber: 3,
		Duration:    600,
	}
	err := store.UpsertBookFile(updated)
	require.NoError(t, err)
	assert.Equal(t, originalID, updated.ID, "Upsert by path should update, not insert a new row")

	got, err := store.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, originalID, got.ID)
	assert.Equal(t, 3, got.TrackNumber)
	assert.Equal(t, 600, got.Duration)

	all, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

// TestUpsertBookFile_NoMatch upserts a file with a new book_id+path combination.
// Expects a new row to be created.
func TestUpsertBookFile_NoMatch(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	f := &BookFile{
		BookID:      bookID,
		FilePath:    "/books/brand_new.m4b",
		TrackNumber: 1,
	}
	err := store.UpsertBookFile(f)
	require.NoError(t, err)
	assert.NotEmpty(t, f.ID, "Upsert with no match should create a new row with an ID")

	got, err := store.GetBookFileByPath("/books/brand_new.m4b")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, f.ID, got.ID)
}

// TestUpsertBookFile_PIDTakesPriority creates a file with a PID+path, then
// upserts using the same PID but a completely different path. The match should
// happen on PID (not path), and the path should be updated.
func TestUpsertBookFile_PIDTakesPriority(t *testing.T) {
	store, bookID, cleanup := newTestStoreWithBook(t)
	defer cleanup()

	pid := "PID_PRIORITY_TEST"
	f := &BookFile{
		BookID:             bookID,
		FilePath:           "/books/pid_priority_original.m4b",
		ITunesPersistentID: pid,
	}
	require.NoError(t, store.CreateBookFile(f))
	originalID := f.ID

	// Upsert: same PID, completely different path (not the original).
	upserted := &BookFile{
		BookID:             bookID,
		FilePath:           "/books/pid_priority_moved.m4b",
		ITunesPersistentID: pid,
		Title:              "Moved File",
	}
	err := store.UpsertBookFile(upserted)
	require.NoError(t, err)

	// Must match on PID — same row, not a new one.
	assert.Equal(t, originalID, upserted.ID, "PID lookup should take priority over path lookup")

	// The path should now reflect the new path.
	movedEntry, err := store.GetBookFileByPath("/books/pid_priority_moved.m4b")
	require.NoError(t, err)
	require.NotNil(t, movedEntry)
	assert.Equal(t, originalID, movedEntry.ID)
	assert.Equal(t, "Moved File", movedEntry.Title)

	// Original path should no longer exist.
	origEntry, err := store.GetBookFileByPath("/books/pid_priority_original.m4b")
	require.NoError(t, err)
	assert.Nil(t, origEntry, "original path should be gone after PID-matched upsert")

	// Still only one file for this book.
	all, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}
