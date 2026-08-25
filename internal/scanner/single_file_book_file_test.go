// file: internal/scanner/single_file_book_file_test.go
// version: 1.4.0
// guid: 4d81b0f2-6ce7-4a39-91b5-2f0a7c63d5e8
// last-edited: 2026-08-25

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// The scan never called createBookFilesForBook for a genuinely single-file book
// -- the call site was guarded on len(SegmentFiles) > 1 -- so those books got no
// book_file row from the scan at all. The only rows they ever acquired came from
// ensureSingleFileBookFile, a backfill inside the auto-organize hook.
//
// That was survivable until the scan cache moved onto book_file rows:
// GetScanCacheMap iterates book_file:* and UpdateBookFileScanCache resolves the
// row BY PATH, so a book with no file row has no entry to read and no row to
// stamp. Every single-file book would be re-read and re-hashed on every scan
// forever -- the same defect the per-file cache was built to remove, arriving
// through the other door.
//
// A real Pebble store rather than dbmocks, for the reason the sibling suite
// gives: these assertions are about which path the row can be FOUND under, and a
// mock that answers for any path cannot express that.

// TestSingleFileBookGetsItsBookFileRow is the fix itself: the row must exist.
func TestSingleFileBookGetsItsBookFileRow(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	audio := filepath.Join(dir, "The Whole Book.m4b")
	require.NoError(t, os.WriteFile(audio, []byte("audio-bytes"), 0o644))

	book, err := store.CreateBook(&database.Book{FilePath: audio, Title: "The Whole Book"})
	require.NoError(t, err)

	// Exactly the shape the scan produces for a single-file book: no segments.
	createSingleFileBookFile(&Book{FilePath: audio}, logger.New("test"))

	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, 1,
		"a single-file book must get exactly one book_file row from the scan; "+
			"without it the per-file scan cache has nothing to key on and the "+
			"book is re-read on every scan forever")
	require.Equal(t, audio, files[0].FilePath)
}

// TestSingleFileBookKeepsItsFilePath is the trap.
//
// createBookFilesForBook normalizes Book.FilePath to the containing directory
// whenever the path it is handed is a file. That is right for a multi-file book
// (whose FilePath is segs[0], a stand-in for the folder) and wrong for a
// single-file book, whose row genuinely belongs at its file.
//
// The naive version of this fix -- relaxing the len(SegmentFiles) > 1 guard --
// creates the row AND moves it, which is why the guard is a separate argument
// rather than something inferred inside the function. This test fails against
// that version while TestSingleFileBookGetsItsBookFileRow still passes, so it is
// the one that has to exist.
func TestSingleFileBookKeepsItsFilePath(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	audio := filepath.Join(dir, "Solo.m4b")
	require.NoError(t, os.WriteFile(audio, []byte("audio-bytes"), 0o644))

	book, err := store.CreateBook(&database.Book{FilePath: audio, Title: "Solo"})
	require.NoError(t, err)

	createSingleFileBookFile(&Book{FilePath: audio}, logger.New("test"))

	fresh, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	require.Equal(t, audio, fresh.FilePath,
		"a single-file book's row must stay at its FILE; normalizing it to %q "+
			"misfiles it for every path-based lookup and is what the "+
			"keepFilePath argument exists to prevent", dir)

	// And the row must still be findable by the path the scan will use next time.
	byPath, err := store.GetBookByFilePath(audio)
	require.NoError(t, err)
	require.NotNil(t, byPath, "the row must still resolve by its file path")
	require.Equal(t, book.ID, byPath.ID)
}

// TestSingleFileBooksSharingADirectoryDoNotStealEachOthersFiles pins the reason
// the file is passed EXPLICITLY instead of letting createBookFilesForBook scan
// the folder.
//
// With a nil segment list that function reads the whole containing directory and
// makes every audio file in it a row for the ONE book it was called about. In an
// unorganized library two unrelated single-file books sharing a folder is the
// ordinary case, not an edge case, and the failure is silent: each book claims
// the other's audio.
func TestSingleFileBooksSharingADirectoryDoNotStealEachOthersFiles(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	first := filepath.Join(dir, "Book One.m4b")
	second := filepath.Join(dir, "Book Two.m4b")
	require.NoError(t, os.WriteFile(first, []byte("one"), 0o644))
	require.NoError(t, os.WriteFile(second, []byte("two"), 0o644))

	b1, err := store.CreateBook(&database.Book{FilePath: first, Title: "Book One"})
	require.NoError(t, err)
	b2, err := store.CreateBook(&database.Book{FilePath: second, Title: "Book Two"})
	require.NoError(t, err)

	createSingleFileBookFile(&Book{FilePath: first}, logger.New("test"))
	createSingleFileBookFile(&Book{FilePath: second}, logger.New("test"))

	f1, err := store.GetBookFiles(b1.ID)
	require.NoError(t, err)
	require.Len(t, f1, 1, "Book One must claim exactly its own file, not the folder")
	require.Equal(t, first, f1[0].FilePath)

	f2, err := store.GetBookFiles(b2.ID)
	require.NoError(t, err)
	require.Len(t, f2, 1, "Book Two must claim exactly its own file, not the folder")
	require.Equal(t, second, f2[0].FilePath)
}

// TestSingleFileHelperIgnoresADirectoryBook pins the stat guard.
//
// A book whose FilePath is a directory but which has no book_file rows is a
// different defect. Papering over it with one row pointing at the folder would
// make the scan cache stamp the directory inode's mtime and size -- 128 bytes in
// the production measurement -- which is exactly the VALUE grain mismatch the
// per-file cache exists to remove. Doing nothing is the correct answer.
func TestSingleFileHelperIgnoresADirectoryBook(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	book, err := store.CreateBook(&database.Book{FilePath: dir, Title: "Directory Book"})
	require.NoError(t, err)

	createSingleFileBookFile(&Book{FilePath: dir}, logger.New("test"))

	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Empty(t, files,
		"a directory book must not be given a book_file row pointing at the folder")
}

// TestSingleFileBookFileIsVisibleToTheScanCache closes the loop on WHY this
// matters, rather than asserting only that a row exists.
//
// UpdateBookFileScanCache resolves the row by path and reports false when there
// is none. Before this fix that was the outcome for every single-file book, and
// the book then had no entry in GetScanCacheMap either -- so it could never be
// skipped. This asserts both halves against the real store.
func TestSingleFileBookFileIsVisibleToTheScanCache(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	audio := filepath.Join(dir, "Cached.m4b")
	require.NoError(t, os.WriteFile(audio, []byte("audio-bytes"), 0o644))
	_, err := store.CreateBook(&database.Book{FilePath: audio, Title: "Cached"})
	require.NoError(t, err)

	info, err := os.Stat(audio)
	require.NoError(t, err)

	// Known-good twin for the assertion below: with no book_file row the stamp
	// must report "no row at this path". If this ever returns true the later
	// assertion proves nothing, because it would pass without the fix.
	stampedBefore, err := store.UpdateBookFileScanCache(audio, info.ModTime().Unix(), info.Size())
	require.NoError(t, err)
	require.False(t, stampedBefore,
		"precondition: with no book_file row there must be nothing to stamp")

	createSingleFileBookFile(&Book{FilePath: audio}, logger.New("test"))

	stampedAfter, err := store.UpdateBookFileScanCache(audio, info.ModTime().Unix(), info.Size())
	require.NoError(t, err)
	require.True(t, stampedAfter,
		"after the fix the scan must be able to stamp the file's own row")

	cache, err := store.GetScanCacheMap()
	require.NoError(t, err)
	entry, ok := cache[audio]
	require.True(t, ok,
		"the file must appear in the scan cache keyed by its OWN path; the walk "+
			"looks up the file path, so an entry under anything else is a miss")
	require.Equal(t, info.Size(), entry.Size,
		"the cached size must be the FILE's, not a directory inode's")
}

// TestProcessBooksParallelGivesASingleFileBookItsFileRow covers the CALL SITE,
// which the five tests above do not.
//
// They all invoke createSingleFileBookFile directly, so deleting the else branch
// in ProcessBooksParallel leaves every one of them green while the defect is
// fully restored in production. That gap is the entire reason this test exists:
// the bug was never in the helper, it was in the helper never being reached.
//
// The multi-file book alongside it is a known-good twin. Without it a harness
// that silently created no book_file rows for ANY book -- a broken store, a
// misconfigured extension list -- would fail this test in a way that reads as
// "the single-file fix regressed", pointing at the wrong thing. The twin must
// pass for the single-file assertion to mean anything.
func TestProcessBooksParallelGivesASingleFileBookItsFileRow(t *testing.T) {
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
	config.AppConfig.SupportedExtensions = []string{".mp3", ".m4b"}

	soloDir := t.TempDir()
	solo := filepath.Join(soloDir, "Solo Book.m4b")
	require.NoError(t, os.WriteFile(solo, []byte("solo-audio"), 0o644))

	multiDir := t.TempDir()
	segs := writeSegments(t, multiDir, "part01.mp3", "part02.mp3")

	// Keyed by FILE PATH, not Title. ProcessBooksParallel re-derives the title
	// from the filename before saving, so the "Multi Book" set below arrives here
	// as "part01" -- a lookup by title silently returns "", GetBookFiles answers
	// zero rows, and the failure reads exactly like the fix having regressed.
	// The path is the one identifier the pipeline does not rewrite.
	//
	// The mutex is not decorative: these run on separate workers.
	var mu sync.Mutex
	ids := map[string]string{}
	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(_ context.Context, book *Book) error {
		created, err := store.CreateBook(&database.Book{FilePath: book.FilePath, Title: book.Title})
		if err != nil {
			return err
		}
		mu.Lock()
		ids[book.FilePath] = created.ID
		mu.Unlock()
		return nil
	}

	books := []Book{
		{FilePath: solo, Format: ".m4b", Title: "Solo Book"},
		{FilePath: segs[0], Format: ".mp3", Title: "Multi Book", SegmentFiles: segs},
	}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 2, nil, nil))

	// Known-good twin first: if this fails the harness is broken, not the fix.
	multiFiles, err := store.GetBookFiles(ids[segs[0]])
	require.NoError(t, err)
	require.Len(t, multiFiles, 2,
		"twin check: the multi-file book must still get its rows; if it does not, "+
			"this harness cannot observe anything about the single-file case")

	soloFiles, err := store.GetBookFiles(ids[solo])
	require.NoError(t, err)
	require.Len(t, soloFiles, 1,
		"ProcessBooksParallel must give a genuinely single-file book its one "+
			"book_file row; without the call site the helper is never reached and "+
			"the book has nothing for the per-file scan cache to key on")
	require.Equal(t, solo, soloFiles[0].FilePath)

	// And the single-file book's row must NOT have been moved to its directory.
	soloRow, err := store.GetBookByID(ids[solo])
	require.NoError(t, err)
	require.Equal(t, solo, soloRow.FilePath,
		"the single-file book's row moved to %q; the call site must pass "+
			"keepFilePath, not normalizeToDirectory", soloDir)
}

// TestSingleFileBookReachesTheScanCacheMap is the end-to-end assertion, and the
// only one here that actually proves the claim in the changelog.
//
// Every other test in this file stops one inference short: they prove a row is
// created, and at which path. That the row then makes the book SKIPPABLE runs
// through a condition in another package that none of them touch --
// bookStampDescribesExactlyOneFile, which mirrors a book-grain stamp onto the
// file row only when `len(files) == 1 && files[0].FilePath == book.FilePath`.
// GetScanCacheMap in turn skips any row whose LastScanMtime is nil.
//
// That chain is why `keepFilePath` is load-bearing rather than tidy. Normalising
// a single-file book's row to its parent directory satisfies "a row exists" and
// still fails `files[0].FilePath == book.FilePath`, so the mirror refuses, the
// row is never stamped, and the fix is INERT while looking correct. The second
// half of this test asserts exactly that, so the trap cannot be reintroduced by
// someone "simplifying" the two call sites back into one.
func TestSingleFileBookReachesTheScanCacheMap(t *testing.T) {
	// stampAndLookup creates a single-file book, gives it a file row under the
	// supplied normalisation, mirrors a stamp, and reports whether the scan
	// cache can see the file.
	stampAndLookup := func(t *testing.T, normalize bool) (bool, string) {
		t.Helper()
		store, cleanup := setupPebbleStore(t)
		defer cleanup()
		prev := database.GetGlobalStore()
		database.SetGlobalStore(store)
		SetStore(store)
		t.Cleanup(func() { database.SetGlobalStore(prev); SetStore(nil) })

		dir := t.TempDir()
		p := filepath.Join(dir, "solo.m4b")
		require.NoError(t, os.WriteFile(p, []byte("audio bytes"), 0o644))

		created, err := store.CreateBook(&database.Book{Title: "Solo", FilePath: p})
		require.NoError(t, err)

		createBookFilesForBook(p, []string{p}, logger.New("test"), normalize)

		fi, err := os.Stat(p)
		require.NoError(t, err)
		// The book-grain writer the scanner still uses. Its mirror is the thing
		// under test.
		require.NoError(t, store.UpdateScanCache(created.ID, fi.ModTime().Unix(), fi.Size()))

		cache, err := store.GetScanCacheMap()
		require.NoError(t, err)
		_, found := cache[p]
		return found, p
	}

	t.Run("kept at its file path, the cache can see it", func(t *testing.T) {
		found, p := stampAndLookup(t, keepFilePath)
		require.True(t, found,
			"the scan cache has no entry for %s, so the next scan re-reads and re-hashes it; "+
				"creating the book_file row is only half the fix -- it has to be at the book's own path "+
				"or bookStampDescribesExactlyOneFile refuses to mirror the stamp onto it", p)
	})

	t.Run("normalized to its directory, the fix is inert", func(t *testing.T) {
		found, p := stampAndLookup(t, normalizeToDirectory)
		require.False(t, found,
			"expected normalising a single-file book's row to its parent directory to DEFEAT the "+
				"scan cache for %s (that is why the single-file call site passes keepFilePath). "+
				"If this now passes, the mirror condition in bookStampDescribesExactlyOneFile changed "+
				"and the comment at the call site is stale -- verify it rather than deleting this test", p)
	})
}
