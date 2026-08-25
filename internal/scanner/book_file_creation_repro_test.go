// file: internal/scanner/book_file_creation_repro_test.go
// version: 1.0.0
// guid: c5bbcdd4-c7d5-4eea-b8d5-c111e1836398
// last-edited: 2026-08-25

package scanner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Production measurement 2026-08-25: book_file row creation regressed between
// 2026-08-11 (0.0% of books with no rows, over a 16,091-row pool) and
// 2026-08-14 (93.8%), and every day since runs 90-100%. A book row with no
// book_file rows has no route to any audio. See
// docs/audits/2026-08-25-book-file-creation-regression.md.
//
// The suite had no test that ran a DIRECTORY book through the whole of
// ProcessBooksParallel and then asserted the rows exist. Every existing test
// either calls createBookFilesForBook directly (so it cannot observe the
// pipeline failing to call it) or asserts on the returned path rather than on
// the rows. That gap is why an 11-day, 90-100% production failure was invisible
// to a green suite.
//
// These tests DISCRIMINATE between the two mechanisms, because both end in zero
// rows and only the cause differs:
//
//	no call was made          -> the pipeline never reached createBookFilesForBook
//	the call was made, no-op  -> it was reached and returned early
//
// A test asserting only "zero rows" would pass against a fix for the wrong one.

// TestProcessBooksParallelDirectoryBookAcquiresBookFileRows is the outcome test:
// a directory of audio goes in, and the book must come out PLAYABLE -- meaning
// its row has book_file rows pointing at the individual files.
func TestProcessBooksParallelDirectoryBookAcquiresBookFileRows(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	dir := t.TempDir()
	segs := writeSegments(t, dir, "part01.mp3", "part02.mp3", "part03.mp3")

	books := []Book{{FilePath: dir, Title: "Directory Book"}}
	require.NoError(t, ProcessBooksParallel(context.Background(), books, 1, nil, logger.New("test")))

	// Step 1: did the book row get saved at all? If not, the failure is in
	// saveBook and everything below is downstream noise.
	row, err := store.GetBookByFilePath(dir)
	require.NoError(t, err)
	require.NotNil(t, row,
		"no book row at %s after ProcessBooksParallel -- the failure is in saveBook, "+
			"not in book_file creation", dir)

	// Step 2: the actual outcome. This is the assertion the suite never had.
	files, err := store.GetBookFiles(row.ID)
	require.NoError(t, err)
	require.Len(t, files, len(segs),
		"book %s has %d book_file rows, want %d -- a book row with no book_file rows "+
			"has no route to any audio, which is the production regression measured "+
			"2026-08-25 (90-100%% of books created since 2026-08-14)", row.ID, len(files), len(segs))
}

// TestDirectoryBookRowIsReachableByItsOwnPath isolates the lookup that
// createBookFilesForBook performs first. If this passes while the test above
// fails, the lookup is fine and the fault is downstream of it -- which rules out
// the "book:path: index is broken" explanation directly rather than by inference.
func TestDirectoryBookRowIsReachableByItsOwnPath(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	created, err := store.CreateBook(&database.Book{FilePath: dir, Title: "Directory Book"})
	require.NoError(t, err)

	got, err := store.GetBookByFilePath(dir)
	require.NoError(t, err)
	require.NotNil(t, got,
		"a book saved at a DIRECTORY path is not reachable by that same path; "+
			"createBookFilesForBook looks the book up this way before every other "+
			"branch, so this returning nil sends it into the silent dbBook == nil return")
	require.Equal(t, created.ID, got.ID)
}

// TestCreateBookFilesForBookEnumeratesADirectory pins the enumeration itself,
// with segmentFiles nil -- the exact shape the directory call site uses
// (scanner.go site 1285 passes nil deliberately to trigger os.ReadDir).
//
// This is the discriminator. If ProcessBooksParallel leaves zero rows but this
// creates them, then the pipeline is not reaching the call, and a fix aimed at
// the lookup or the enumeration would be aimed at the wrong thing.
func TestCreateBookFilesForBookEnumeratesADirectory(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	dir := t.TempDir()
	segs := writeSegments(t, dir, "part01.mp3", "part02.mp3", "part03.mp3")

	row, err := store.CreateBook(&database.Book{FilePath: dir, Title: "Directory Book"})
	require.NoError(t, err)

	createBookFilesForBook(dir, nil, logger.New("test"), normalizeToDirectory)

	files, err := store.GetBookFiles(row.ID)
	require.NoError(t, err)
	require.Len(t, files, len(segs),
		"createBookFilesForBook(dir, nil, normalizeToDirectory) created %d rows, want %d -- with a nil file "+
			"list it must enumerate the directory via os.ReadDir filtered by "+
			"SupportedExtensions", len(files), len(segs))
}
