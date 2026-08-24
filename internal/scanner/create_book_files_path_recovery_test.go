// file: internal/scanner/create_book_files_path_recovery_test.go
// version: 1.0.0
// guid: 4d81f0a6-2c57-4b93-9e6a-71cf05d8b2ae
// last-edited: 2026-08-24

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Tests for the SECOND half of #134.
//
// The merged fix closed the path that CREATES the FilePath desync, and every
// test for it starts from an empty store -- so none of them could observe that
// a book normalized by an EARLIER scan has no route back. This file covers the
// rescan, which is where that population lives.

// seedNormalizedBook builds the state a scan leaves behind after it has already
// normalized a multi-file book: the row lives at the DIRECTORY, and its
// BookFiles carry the individual segment paths.
func seedNormalizedBook(t *testing.T, store *database.PebbleStore, dir string, segs []string, title string) *database.Book {
	t.Helper()
	book, err := store.CreateBook(&database.Book{FilePath: dir, Title: title})
	require.NoError(t, err)
	for i, seg := range segs {
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			BookID:      book.ID,
			FilePath:    seg,
			TrackNumber: i + 1,
		}))
	}
	return book
}

func writeSegments(t *testing.T, dir string, names ...string) []string {
	t.Helper()
	var out []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		require.NoError(t, os.WriteFile(p, []byte("audio-"+n), 0o644))
		out = append(out, p)
	}
	return out
}

// TestCreateBookFilesForBookRecoversAnAlreadyNormalizedRow is the core of the
// follow-up. Scan 2 asks about the SEGMENT path; the row is at the directory.
// Before the recovery lookup this returned "" and the caller kept a dead path,
// so chapters and the scan-cache entry were skipped on every scan forever.
func TestCreateBookFilesForBookRecoversAnAlreadyNormalizedRow(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	segs := writeSegments(t, dir, "part01.mp3", "part02.mp3")
	book := seedNormalizedBook(t, store, dir, segs, "Already Normalized Book")

	// Precondition: the segment path genuinely has no row. Without this the
	// test could pass without the recovery ever being needed.
	orphan, err := store.GetBookByFilePath(segs[0])
	require.NoError(t, err)
	require.Nil(t, orphan, "precondition: the segment path must NOT resolve")

	got := createBookFilesForBook(segs[0], segs, logger.New("test"))

	if got != dir {
		t.Fatalf("createBookFilesForBook(%q) = %q, want the directory %q -- "+
			"an already-normalized book has no route back and stays broken on every scan (#134)",
			segs[0], got, dir)
	}

	// The recovered path must resolve to the SAME book, not merely to something.
	at, err := store.GetBookByFilePath(got)
	require.NoError(t, err)
	require.NotNil(t, at)
	require.Equal(t, book.ID, at.ID, "recovered path resolved to a different book")
}

// TestCreateBookFilesForBookDoesNotRecoverAForeignBook pins the ownership check.
// A directory can hold a book that does NOT own this segment; claiming it would
// send chapters and the scan-cache entry to the wrong book, which is worse than
// sending them nowhere.
func TestCreateBookFilesForBookDoesNotRecoverAForeignBook(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	segs := writeSegments(t, dir, "part01.mp3", "part02.mp3")
	stranger := writeSegments(t, dir, "unrelated.mp3")

	// A book lives at the directory, but it owns only `unrelated.mp3`.
	seedNormalizedBook(t, store, dir, stranger, "Some Other Book")

	got := createBookFilesForBook(segs[0], segs, logger.New("test"))

	if got != "" {
		t.Fatalf("createBookFilesForBook(%q) = %q, but the book at that directory does "+
			"NOT own %q -- recovering it would write this book's chapters and scan-cache "+
			"entry onto an unrelated book", segs[0], got, segs[0])
	}
}

// TestProcessBooksParallelDirectoryBookKeepsItsPath pins the invariant that the
// directory-book call site's `_ =` discard relies on. That call site justifies
// the discard in a comment; this makes the claim executable, so a later change
// that routes a FILE path through it fails here instead of silently.
func TestProcessBooksParallelDirectoryBookKeepsItsPath(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	segs := writeSegments(t, dir, "part01.mp3", "part02.mp3")

	_, err := store.CreateBook(&database.Book{FilePath: dir, Title: "Directory Book"})
	require.NoError(t, err)

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	if got := createBookFilesForBook(dir, segs, logger.New("test")); got != "" {
		t.Fatalf("a directory-rooted book reported a move to %q; the normalization guard "+
			"is !info.IsDir(), so a directory must never move", got)
	}
}

// TestProcessBooksParallelSecondScanKeepsTheBookServiceable is the rescan test
// the suite did not have. Every prior ProcessBooksParallel test is a first
// import, which is why a mutant that drops the `moved != ""` guard survived:
// on a rescan createBookFilesForBook returns "" (BookFiles already exist), so
// an unguarded assignment blanks FilePath to "" and destroys the write-back.
func TestProcessBooksParallelSecondScanKeepsTheBookServiceable(t *testing.T) {
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
	segs := writeSegments(t, dir, "part01.mp3", "part02.mp3")

	var createdID string
	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error {
		// Mimic the real saver's re-link. Looking the path up EXACTLY is not
		// enough: after scan 1 the row lives at the parent directory, so an
		// exact-path saver creates a second row for the same book on every
		// rescan. Production reaches the same row via segment-hash dedup; this
		// stub reaches it via the directory, which is equivalent for the
		// FilePath behaviour under test and keeps the test focused on that.
		if existing, err := store.GetBookByFilePath(book.FilePath); err == nil && existing != nil {
			createdID = existing.ID
			return nil
		}
		if existing, err := store.GetBookByFilePath(filepath.Dir(book.FilePath)); err == nil && existing != nil {
			createdID = existing.ID
			return nil
		}
		created, err := store.CreateBook(&database.Book{FilePath: book.FilePath, Title: book.Title})
		if err != nil {
			return err
		}
		createdID = created.ID
		return nil
	}

	mkBooks := func() []Book {
		return []Book{{
			FilePath:     segs[0],
			Format:       ".mp3",
			Title:        "Multi Part Book",
			SegmentFiles: segs,
		}}
	}

	// Scan 1 -- imports and normalizes.
	require.NoError(t, ProcessBooksParallel(t.Context(), mkBooks(), 1, nil, nil))
	require.NotEmpty(t, createdID)
	firstID := createdID

	afterFirst, err := store.GetBookByID(firstID)
	require.NoError(t, err)
	require.Equal(t, dir, afterFirst.FilePath, "precondition: scan 1 must have normalized the row")

	// Scan 2 -- the rescan. The walk re-emits the SEGMENT path, exactly as
	// production does, because grouping makes no store calls and cannot know
	// the row moved.
	books := mkBooks()
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, nil))

	// The in-memory book must still name a real, resolvable path. An unguarded
	// assignment of the return value blanks this to "".
	if books[0].FilePath == "" {
		t.Fatal("rescan blanked Book.FilePath to \"\": the return value was assigned " +
			"unconditionally, so PersistChaptersForBook(\"\") and writeBackScanCache(\"\") " +
			"both fail and the whole second-scan write-back is destroyed")
	}
	resolved, err := store.GetBookByFilePath(books[0].FilePath)
	require.NoError(t, err)
	if resolved == nil {
		t.Fatalf("after the rescan Book.FilePath = %q, which resolves to no row -- "+
			"chapters and the scan-cache write-back are both silently skipped", books[0].FilePath)
	}
	require.Equal(t, firstID, resolved.ID, "the rescan pointed the book at a different row")

	// And the scan-cache entry must be present after the rescan, not just the
	// first import.
	final, err := store.GetBookByID(firstID)
	require.NoError(t, err)
	require.NotNil(t, final.LastScanMtime,
		"LastScanMtime is nil after a RESCAN: the already-normalized row was never recovered")
}

// TestProcessBooksParallelPersistsChaptersForNormalizedMultiFileBook closes the
// OTHER half of #134 -- the half no test could observe.
//
// The scan-cache test uses 4-byte fake mp3s, so ffprobe fails, no chapters are
// ever synthesized, and PersistChaptersForBook returns at its len(chapters)==0
// guard no matter WHICH path it was handed. That let a mutant which moves the
// `books[idx].FilePath = moved` assignment to AFTER the chapter call pass the
// entire package -- while reintroducing exactly the failure the fix documents
// ("chapters were never persisted at all").
//
// Real fixtures with real durations are what make the path matter.
func TestProcessBooksParallelPersistsChaptersForNormalizedMultiFileBook(t *testing.T) {
	requireChapterTestFFprobe(t)
	var segs []string
	for n := 1; n <= 6; n++ {
		p := chapterTestOdysseyMP3Track(n)
		requireChapterTestFixture(t, p)
		segs = append(segs, p)
	}

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

	var createdID string
	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, book *Book) error {
		created, err := store.CreateBook(&database.Book{FilePath: book.FilePath, Title: book.Title})
		if err != nil {
			return err
		}
		createdID = created.ID
		return nil
	}

	books := []Book{{
		FilePath:     segs[0],
		Format:       ".mp3",
		Title:        "The Odyssey",
		SegmentFiles: segs,
	}}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, nil))
	require.NotEmpty(t, createdID, "the stub saver never ran")

	got, err := store.GetBookByID(createdID)
	require.NoError(t, err)
	require.NotNil(t, got)

	// Precondition: normalization must actually have happened, or the chapter
	// assertion below proves nothing about the path.
	dir := filepath.Dir(segs[0])
	require.Equal(t, dir, got.FilePath,
		"precondition: the row must have been normalized to its directory")

	chs, err := store.GetChaptersForBook(createdID)
	require.NoError(t, err)
	if len(chs) == 0 {
		t.Fatal("no chapters persisted after the scan: PersistChaptersForBook was handed " +
			"the pre-normalization segment path, found no row there, and returned quietly. " +
			"This is the half of #134 the doc comment calls \"chapters were never persisted " +
			"at all\".")
	}
	require.Len(t, chs, 6, "the 6-track fixture should synthesize one chapter per track")
}

// TestProcessBooksParallelKeepsPathWhenNothingMoved kills the mutant that drops
// the `moved != ""` guard at the call site.
//
// It needs the one state where createBookFilesForBook legitimately returns ""
// for a path that DOES have a row: the row sitting at the segment path with its
// BookFiles already created (the rescan early-return). Reached in production
// when an earlier normalization write failed, or when the row was pointed back
// at the segment path. An unguarded `books[idx].FilePath = createBookFiles...()`
// then blanks FilePath to "", and both consumers below it collapse -- chapters
// look up "" and writeBackScanCache fails its os.Stat.
func TestProcessBooksParallelKeepsPathWhenNothingMoved(t *testing.T) {
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
	segs := writeSegments(t, dir, "part01.mp3", "part02.mp3")

	// The row stays at the SEGMENT path and already has its BookFiles, so
	// createBookFilesForBook takes the "BookFiles already created" early return
	// and correctly reports no move.
	book, err := store.CreateBook(&database.Book{FilePath: segs[0], Title: "Not Normalized"})
	require.NoError(t, err)
	for i, seg := range segs {
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			BookID: book.ID, FilePath: seg, TrackNumber: i + 1,
		}))
	}

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(ctx context.Context, b *Book) error { return nil }

	books := []Book{{
		FilePath:     segs[0],
		Format:       ".mp3",
		Title:        "Not Normalized",
		SegmentFiles: segs,
	}}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, nil))

	if books[0].FilePath != segs[0] {
		t.Fatalf("Book.FilePath = %q after a scan that moved nothing, want %q -- "+
			"the \"\" return was assigned unconditionally, so every consumer below "+
			"the call site is handed a path that resolves to no row", books[0].FilePath, segs[0])
	}

	final, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, final.LastScanMtime,
		"LastScanMtime is nil: writeBackScanCache was handed a blanked path and its os.Stat failed")
}
