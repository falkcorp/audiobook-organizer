// file: internal/scanner/file_ownership_review2_test.go
// version: 1.0.0
// guid: 4d8b2f6a-1c3e-4a97-b5d0-7e2c9f1a3b68
// last-edited: 2026-09-28

package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/require"
)

// TestProcessBooksParallel_SkippedNominationIsWithdrawn is S-7(b) of the second
// 2026-09-28 review. The owner here is a sub-grouped book whose path IS the
// scanned group's first file, so a nomination that survives the skip resolves
// (enqueueAIParse looks the path up) to the OWNER and queues an AI parse for
// it. The earlier B1 test's owner sat at its folder, where a surviving
// nomination resolved to no row and was dropped, so removing the withdrawal
// passed it.
func TestProcessBooksParallel_SkippedNominationIsWithdrawn(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })
	f := newOwnershipFixture(t)
	origGlobal := database.GetGlobalStore()
	database.SetGlobalStore(f.store)
	t.Cleanup(func() { database.SetGlobalStore(origGlobal) })
	useScannerStore(t, f.store)

	config.AppConfig.EnableAIParsing = true
	config.AppConfig.AIBackend.LLMMode = config.AIBackendModeLocal
	config.AppConfig.AIBackend.LocalBaseURL = "http://192.0.2.1:1"
	config.AppConfig.AIBackend.LocalLLMModel = "test-model"
	config.AppConfig.MinBookSizeBytes = 0

	owner, err := f.store.GetBookByID(f.parent.ID)
	require.NoError(t, err)
	owner.FilePath = f.chapters[0] // the sub-group shape: path on the first file
	_, err = f.store.UpdateBook(owner.ID, owner)
	require.NoError(t, err)

	var mu sync.Mutex
	var queued []AIParseCandidate
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		mu.Lock()
		defer mu.Unlock()
		queued = append(queued, batch...)
		return nil
	})

	// Two of the owner's three files: a piece of a larger book, skipped. No
	// series, so the worker nominates it for AI before the save.
	books := []Book{{FilePath: f.chapters[0], SegmentFiles: f.chapters[:2], Format: ".mp3"}}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, logger.New("test")))

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, queued, "the skipped book's AI nomination was not withdrawn; it queued a parse for the owner %s", f.parent.ID)
}

// TestProcessBooksParallel_DirectoryBookGainsNewFiles is S-5: a directory book
// already at its path gets rows for audio files that arrived in its folder
// after import. createBookFilesForBook returns as soon as a book has rows, so
// before this they were never imported.
func TestProcessBooksParallel_DirectoryBookGainsNewFiles(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })
	f := newOwnershipFixture(t)
	useScannerStore(t, f.store)
	config.AppConfig.EnableAIParsing = false
	config.AppConfig.MinBookSizeBytes = 0

	books := []Book{{FilePath: f.dir, Format: ".mp3", Title: "Eldest", Author: "Christopher Paolini"}}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, logger.New("test")))

	rows, err := f.store.GetBookFiles(f.parent.ID)
	require.NoError(t, err)
	require.Len(t, rows, len(f.chapters)+len(f.loose), "the folder's new files were not appended to its book")
	all, err := f.store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	require.Len(t, all, 1)
	owner, err := f.store.GetBookByID(f.parent.ID)
	require.NoError(t, err)
	require.NotNil(t, owner.NeedsRescan)
	require.True(t, *owner.NeedsRescan)

	v, err := checkFileOwnership(&Book{FilePath: f.dir})
	require.NoError(t, err)
	require.False(t, v.skip, "after the append the folder is the book again: %s", v.reason)
}

// TestHandleOwnershipSkip_ITunesOwnerIsNotGrown is S-8: a staged arrival in the
// hands-off iTunes tree is recorded as a skip and appends nothing.
func TestHandleOwnershipSkip_ITunesOwnerIsNotGrown(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)
	useScannerStore(t, store)
	dir := filepath.Join(t.TempDir(), "books", "itunes", "Author", "Book")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	old, arrived := filepath.Join(dir, "01.mp3"), filepath.Join(dir, "02.mp3")
	for _, p := range []string{old, arrived} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}
	b, err := store.CreateBook(&database.Book{Title: "Book", FilePath: dir})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: old, TrackNumber: 1}))

	skips := &OwnershipSkips{}
	failures := &FileFailures{}
	se := &ownershipSkipError{Path: dir, Owners: []string{b.ID}, AppendTo: b.ID, Unowned: []string{arrived}}
	require.True(t, handleOwnershipSkip(context.Background(), se, skips, failures, nil, logger.New("test")))

	rows, err := store.GetBookFiles(b.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "a file was appended to a book in the iTunes tree")
	require.Equal(t, 1, skips.Total())
	require.Zero(t, failures.Total())
}

// upsertFailStore fails every BatchUpsertScannedBookFiles.
type upsertFailStore struct{ *database.PebbleStore }

func (upsertFailStore) BatchUpsertScannedBookFiles([]database.ScannedBookFile) error {
	return errors.New("injected upsert failure")
}

// TestHandleOwnershipSkip_FailedAppendIsCountedAndListed: a failed append is a
// file failure of the run (counted for the summary, listed with the failures),
// not a Warn line nobody aggregates.
func TestHandleOwnershipSkip_FailedAppendIsCountedAndListed(t *testing.T) {
	f := newOwnershipFixture(t)
	useScannerStore(t, upsertFailStore{f.store})
	ctx, rc := withScanRunCounters(context.Background())
	failures := &FileFailures{}
	se := &ownershipSkipError{Path: f.chapters[0], Owners: []string{f.parent.ID}, AppendTo: f.parent.ID, Unowned: f.loose}

	require.True(t, handleOwnershipSkip(ctx, se, &OwnershipSkips{}, failures, nil, logger.New("test")))
	require.Equal(t, int64(1), rc.appendFailed.Load())
	require.Equal(t, 1, failures.Total())
}

// TestProcessBooksParallel_StagedArrivalKeepsProviderChapters: the append
// rebuilds only a chapter list synthesized from the files. A provider's or
// user's list is kept.
func TestProcessBooksParallel_StagedArrivalKeepsProviderChapters(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })
	f := newOwnershipFixture(t)
	useScannerStore(t, f.store)
	config.AppConfig.EnableAIParsing = false
	config.AppConfig.MinBookSizeBytes = 0

	provider := []database.Chapter{
		{ID: 1, StartSec: 0, EndSec: 900, Title: "Prologue"},
		{ID: 2, StartSec: 900, EndSec: 2000, Title: "The Twins"},
		{ID: 3, StartSec: 2000, EndSec: 3100, Title: "The Hunt"},
	}
	require.NoError(t, f.store.SaveChaptersForBook(f.parent.ID, provider))

	all := append(append([]string{}, f.chapters...), f.loose...)
	books := []Book{{FilePath: f.chapters[0], SegmentFiles: all, Format: ".mp3",
		Title: "Eldest", Author: "Christopher Paolini", Series: "Inheritance"}}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, logger.New("test")))

	rows, err := f.store.GetBookFiles(f.parent.ID)
	require.NoError(t, err)
	require.Len(t, rows, len(all), "the new files were not appended")
	got, err := f.store.GetChaptersForBook(f.parent.ID)
	require.NoError(t, err)
	require.Equal(t, provider, got, "a provider chapter list was overwritten by a synthesized one")
}

// rawRowsStore serves one raw row at one path, bypassing the Pebble read path
// (which corrects legacy millisecond durations itself), so the scanner's own
// guard is what is tested.
type rawRowsStore struct {
	*database.PebbleStore
	path string
	row  database.BookFile
}

func (s rawRowsStore) BookFilesAtPath(path string) ([]database.BookFile, error) {
	if path == s.path {
		return []database.BookFile{s.row}, nil
	}
	return nil, nil
}

// TestStoredBookFileDurationSec_IgnoresLegacyMillis: a legacy row whose
// seconds field holds milliseconds is not trusted; the file is probed instead.
func TestStoredBookFileDurationSec_IgnoresLegacyMillis(t *testing.T) {
	f := newOwnershipFixture(t)
	const size = 60_000_000
	require.NoError(t, os.Truncate(f.chapters[0], size))
	require.True(t, database.DurationLooksLikeMillis(size, 3_600_000), "fixture must look like milliseconds")
	useScannerStore(t, rawRowsStore{PebbleStore: f.store, path: f.chapters[0],
		row: database.BookFile{ID: "raw", BookID: f.parent.ID, FilePath: f.chapters[0], FileSize: size, Duration: 3_600_000}})
	require.Zero(t, storedBookFileDurationSec(f.chapters[0]), "a millisecond duration was trusted as seconds")

	useScannerStore(t, rawRowsStore{PebbleStore: f.store, path: f.chapters[0],
		row: database.BookFile{ID: "raw", BookID: f.parent.ID, FilePath: f.chapters[0], FileSize: size, Duration: 3600}})
	require.Equal(t, 3600, storedBookFileDurationSec(f.chapters[0]), "a plausible seconds value was not used")
}
