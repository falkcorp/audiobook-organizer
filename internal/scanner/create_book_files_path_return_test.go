// file: internal/scanner/create_book_files_path_return_test.go
// version: 1.0.0
// guid: 7f2b41c8-93ad-4e05-b6d1-8c0e5a72f394
// last-edited: 2026-08-24

package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// These tests cover the FilePath desynchronization described in the doc comment
// on createBookFilesForBook (#134).
//
// WHY A REAL PEBBLE STORE AND NOT dbmocks: the bug is that after normalization
// the row can only be found under its NEW path. A mock configured with
// GetBookByFilePath(anything) -> book cannot express that -- it answers for the
// dead path too, so the desync is invisible and the test passes against the bug.
// A real store desynchronizes for real. The one test below that does use a mock
// uses it for the opposite reason: to force an UpdateBook FAILURE, which a real
// store will not do on demand.

// TestCreateBookFilesForBookReturnsThePathTheRowMovedTo pins the contract that
// the returned path is the one a caller can actually find the row under, and
// -- just as important -- that the path the caller passed IN no longer works.
//
// The second assertion is the whole bug. Without it this test would still pass
// if normalization silently stopped happening.
func TestCreateBookFilesForBookReturnsThePathTheRowMovedTo(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	seg1 := filepath.Join(dir, "part01.mp3")
	seg2 := filepath.Join(dir, "part02.mp3")
	require.NoError(t, os.WriteFile(seg1, []byte("aaaa"), 0o644))
	require.NoError(t, os.WriteFile(seg2, []byte("bbbb"), 0o644))

	// FilePath points at the FIRST SEGMENT, not the directory. That is the shape
	// the multi-file grouping path produces (Book{FilePath: segs[0]}), and it is
	// what makes the normalization branch run.
	book, err := store.CreateBook(&database.Book{FilePath: seg1, Title: "Multi Part Book"})
	require.NoError(t, err)

	moved := createBookFilesForBook(seg1, []string{seg1, seg2}, logger.New("test"))

	if moved == "" {
		t.Fatal("createBookFilesForBook normalized the row but reported no move; " +
			"the caller will keep using a path that no longer resolves (#134)")
	}
	if moved != dir {
		t.Errorf("reported move to %q, want the containing directory %q", moved, dir)
	}

	// The returned path must actually resolve. This is the half the caller relies on.
	atNew, err := store.GetBookByFilePath(moved)
	require.NoError(t, err)
	if atNew == nil {
		t.Fatalf("no book row at the reported path %q -- the return value is unusable", moved)
	}
	if atNew.ID != book.ID {
		t.Errorf("path %q resolved to book %q, want %q", moved, atNew.ID, book.ID)
	}

	// And the path the caller passed in must NOT resolve any more. This is the
	// desync itself: it is why a caller that ignores the return value silently
	// loses both the scan-cache write-back and chapter persistence.
	atOld, err := store.GetBookByFilePath(seg1)
	require.NoError(t, err)
	if atOld != nil {
		t.Fatalf("precondition failed: the pre-normalization path %q still resolves, "+
			"so this test cannot observe the desync it exists to catch", seg1)
	}
}

// TestCreateBookFilesForBookReportsNoMoveWhenAlreadyNormalized guards the other
// direction: "" must mean "the row is still where you left it". A caller that
// overwrote its in-memory path with an unconditional directory would corrupt
// genuinely directory-rooted books.
func TestCreateBookFilesForBookReportsNoMoveWhenAlreadyNormalized(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	seg1 := filepath.Join(dir, "part01.mp3")
	seg2 := filepath.Join(dir, "part02.mp3")
	require.NoError(t, os.WriteFile(seg1, []byte("aaaa"), 0o644))
	require.NoError(t, os.WriteFile(seg2, []byte("bbbb"), 0o644))

	// FilePath is ALREADY the directory -- normalization must not fire.
	_, err := store.CreateBook(&database.Book{FilePath: dir, Title: "Already Normalized"})
	require.NoError(t, err)

	moved := createBookFilesForBook(dir, []string{seg1, seg2}, logger.New("test"))

	if moved != "" {
		t.Errorf("reported a move to %q for a row that did not move; "+
			"\"\" must mean \"still where you left it\"", moved)
	}
}

// TestCreateBookFilesForBookReportsNoMoveWhenUpdateBookFails covers the error
// path. If the normalizing write fails the row is STILL at the caller's original
// path, so claiming a move would send the caller's scan-cache write-back and
// chapter persistence to a path with no row -- reintroducing #134 inside the
// error handler, where it would be even harder to spot.
//
// A mock is used here specifically because a real store will not fail on demand.
func TestCreateBookFilesForBookReportsNoMoveWhenUpdateBookFails(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	dir := t.TempDir()
	seg1 := filepath.Join(dir, "part01.mp3")
	seg2 := filepath.Join(dir, "part02.mp3")
	require.NoError(t, os.WriteFile(seg1, []byte("aaaa"), 0o644))
	require.NoError(t, os.WriteFile(seg2, []byte("bbbb"), 0o644))

	store.EXPECT().GetBookByFilePath(seg1).Return(&database.Book{
		ID: "book-1", Title: "Multi Part Book", FilePath: seg1,
	}, nil)
	store.EXPECT().GetBookFiles("book-1").Return(nil, nil)
	store.EXPECT().BatchUpsertBookFiles(mock.Anything).Return(nil)
	store.EXPECT().GetBookByID("book-1").Return(&database.Book{
		ID: "book-1", Title: "Multi Part Book", FilePath: seg1,
	}, nil)
	store.EXPECT().UpdateBook("book-1", mock.Anything).
		Return(nil, fmt.Errorf("pebble: write failed"))

	moved := createBookFilesForBook(seg1, []string{seg1, seg2}, logger.New("test"))

	if moved != "" {
		t.Errorf("reported a move to %q after UpdateBook FAILED; the row is still at %q",
			moved, seg1)
	}
}

// TestProcessBooksParallelWritesScanCacheForNormalizedMultiFileBook is the
// end-to-end test, and the one that pins the production symptom rather than the
// mechanism.
//
// The three tests above all still pass if the CALL SITE drops the returned path
// on the floor -- which is precisely the bug that shipped. This one does not:
// it asserts what was actually measured on prod on 2026-08-24, that a
// normalized multi-file book ends a scan with LastScanMtime still nil and so is
// re-read and re-hashed on every scan for the life of the library.
func TestProcessBooksParallelWritesScanCacheForNormalizedMultiFileBook(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	dir := t.TempDir()
	seg1 := filepath.Join(dir, "part01.mp3")
	seg2 := filepath.Join(dir, "part02.mp3")
	require.NoError(t, os.WriteFile(seg1, []byte("aaaa"), 0o644))
	require.NoError(t, os.WriteFile(seg2, []byte("bbbb"), 0o644))

	// Stand in for the real saver: create the row at the path the grouping path
	// produces (the first segment), which is what makes normalization fire.
	var createdID string
	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error {
		created, err := store.CreateBook(&database.Book{
			FilePath: book.FilePath,
			Title:    book.Title,
		})
		if err != nil {
			return err
		}
		createdID = created.ID
		return nil
	}

	books := []Book{{
		FilePath:     seg1,
		Format:       ".mp3",
		Title:        "Multi Part Book",
		SegmentFiles: []string{seg1, seg2},
	}}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, nil))
	require.NotEmpty(t, createdID, "the stub saver never ran")

	got, err := store.GetBookByID(createdID)
	require.NoError(t, err)
	require.NotNil(t, got, "book disappeared")

	// Confirm the scan really did normalize -- otherwise the assertion below
	// would be trivially satisfied by a scan that never moved the row at all.
	if got.FilePath != dir {
		t.Fatalf("precondition failed: FilePath = %q, want the normalized directory %q; "+
			"this test cannot observe #134 unless normalization actually happened", got.FilePath, dir)
	}

	if got.LastScanMtime == nil {
		t.Fatal("LastScanMtime is nil after a full scan: the scan-cache write-back looked " +
			"the book up under its pre-normalization path, found nothing, and counted it as " +
			"scanCacheNoRowCount. Every future scan re-reads and re-hashes every segment " +
			"of this book. This is the prod symptom measured 2026-08-24 (#134).")
	}
}
