// file: internal/scanner/unknown_author_gate_test.go
// version: 2.1.0
// guid: 3d9a5f71-2e84-4c06-b1f3-6a05e97c2db4
// last-edited: 2026-08-25

package scanner

import (
	"context"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TestExtractInfoFromPathDoesNotLaunderThePlaceholder uses the exact shape of a
// real production path, because the loop only closes on organized files: the
// organizer writes ".../Unknown Author/<title>/<title> - Unknown Author.mp3",
// and the next scan parses the author back out of that filename.
//
// Measured against production on 2026-08-25, before the fix, this returned
// Author="Unknown Author" for the path below.
func TestExtractInfoFromPathDoesNotLaunderThePlaceholder(t *testing.T) {
	b := &Book{FilePath: "/mnt/bigdata/books/audiobook-organizer/Unknown Author/Pratchett 036/Pratchett 036 - Unknown Author.mp3"}
	extractInfoFromPath(b)

	require.Empty(t, b.Author,
		"the scan read the organizer's own placeholder back out of the filename and kept it "+
			"as an author; that non-nil AuthorID then closes the AI nomination gate forever")

	// The converse, so the fix cannot pass by simply breaking extraction: the
	// title half of the same parse is still wanted.
	require.Equal(t, "Pratchett 036", b.Title,
		"clearing the placeholder author must not discard the title parsed from the same filename")
}

// A real author in the same filename position must survive untouched -- without
// this, clearing the author unconditionally would also pass the test above.
func TestExtractInfoFromPathKeepsARealAuthor(t *testing.T) {
	b := &Book{FilePath: "/mnt/bigdata/books/audiobook-organizer/Terry Pratchett/Mort/Mort - Terry Pratchett.mp3"}
	extractInfoFromPath(b)

	require.Equal(t, "Terry Pratchett", b.Author,
		"a real author was dropped: the placeholder guard is matching too broadly")
}

// TestExtractInfoFromPathDoesNotSubstituteTheTitleForTheAuthor pins a regression
// this fix introduced and then backed out.
//
// Clearing the placeholder BEFORE the directory fallback (rather than on the
// defer, after it) opens that fallback for organized books. The organizer's
// layout is <root>/<author>/<title>/<file>, so the immediate parent is the
// TITLE, and the book ends up attributed to itself: Author = "Pratchett 036".
//
// That is worse than the bug being fixed. The placeholder at least announces
// itself -- authorname.IsPlaceholder can find it, and this whole change is built
// on being able to. A title masquerading as an author closes the same gate and
// is indistinguishable from a real one.
func TestExtractInfoFromPathDoesNotSubstituteTheTitleForTheAuthor(t *testing.T) {
	b := &Book{FilePath: "/mnt/bigdata/books/audiobook-organizer/Unknown Author/Pratchett 036/Pratchett 036 - Unknown Author.mp3"}
	extractInfoFromPath(b)

	require.NotEqual(t, "Pratchett 036", b.Author,
		"the book's own title was recorded as its author -- the directory fallback fired on an "+
			"organized path, where the immediate parent is the title, not the author")
	require.Empty(t, b.Author, "expected no author at all for a book whose author is genuinely unknown")
}

func TestRowHasRealAuthor(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	ph, err := store.CreateAuthor(authorname.Placeholder)
	require.NoError(t, err)
	real, err := store.CreateAuthor("Terry Pratchett")
	require.NoError(t, err)
	require.NotEqual(t, ph.ID, real.ID)

	id := func(i int) *int { return &i }
	oracle := newPlaceholderAuthors()

	require.False(t, rowHasRealAuthor(nil, oracle), "nil author is not a real author")
	require.False(t, rowHasRealAuthor(id(0), oracle), "zero author is not a real author")
	require.False(t, rowHasRealAuthor(id(ph.ID), oracle), "the placeholder is not a real author")
	require.True(t, rowHasRealAuthor(id(real.ID), oracle), "a real author was rejected")
	// An id no row answers for must not be read as the placeholder: "we could
	// not tell" is not grounds for skipping a book.
	require.True(t, rowHasRealAuthor(id(999999), oracle), "an unresolvable author id was treated as the placeholder")
}

// TestPlaceholderAuthorsResolvesByNameNotByTheNameIndex is the regression test
// for the defect this oracle exists to avoid.
//
// Production carries TWO author rows both named "Unknown Author" (54845 with no
// books, 54846 with 2,128). The author:name: index maps one normalized name to
// exactly ONE id, so GetAuthorByName can only ever return one of them. An
// implementation that compared against that single id would guard one row and
// leave every book under the other one gated -- and if the index happened to
// name the empty row, the entire fix would be inert in production while every
// other test in this file still passed.
//
// The duplicate USED to be built by racing CreateAuthor -- it was check-then-create
// with no atomicity, so 8 concurrent callers with the same name each minted a row.
// That defect is now fixed (CreateAuthor serializes and re-checks under the lock),
// so the race no longer produces a second row and this test's own precondition
// guard correctly reported that it could no longer observe the defect.
//
// The fixture is now built deterministically, via UpdateAuthorName, which
// re-points author:name: at the renamed row. That reproduces the production shape
// exactly -- two rows sharing one name, with the index naming only one of them --
// and it is strictly better than the race in one respect: it lets the test CHOOSE
// which row the index names, so it can pin the worst case named above, where the
// index points at the row that is NOT the one the books hang off.
//
// The duplicates remain real in production regardless of the CreateAuthor fix:
// preventing new ones does not repair the ones already there.
func TestPlaceholderAuthorsResolvesByNameNotByTheNameIndex(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	// Row 1: the placeholder, created normally. The name index points here.
	first, err := store.CreateAuthor(authorname.Placeholder)
	require.NoError(t, err)
	require.NotNil(t, first)

	// Row 2: created under a different name, then RENAMED to the placeholder.
	// UpdateAuthorName re-points author:name: at this row, so row 1 becomes the
	// SHADOW -- a real placeholder row that no name lookup can ever reach. This is
	// the production shape (ids 54845 and 54846) built deterministically.
	second, err := store.CreateAuthor("Temporarily Not The Placeholder")
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotEqual(t, first.ID, second.ID, "the two fixture rows collapsed into one")
	require.NoError(t, store.UpdateAuthorName(second.ID, authorname.Placeholder))

	viaIndex, err := store.GetAuthorByName(authorname.Placeholder)
	require.NoError(t, err)
	require.NotNil(t, viaIndex)

	// The precondition that gives this test its teeth: a placeholder row the name
	// index does NOT point at. Assert the fixture actually achieved it rather than
	// assuming the rename won -- if UpdateAuthorName ever stopped re-pointing the
	// index, this test would silently go back to examining a single row.
	require.Equalf(t, second.ID, viaIndex.ID,
		"the name index still points at row %d, so the rename did not take and there is no shadow row",
		viaIndex.ID)
	shadow := first.ID
	require.NotEqual(t, shadow, viaIndex.ID, "shadow and indexed row are the same row")

	oracle := newPlaceholderAuthors()
	require.True(t, oracle.is(viaIndex.ID), "the indexed placeholder row was not recognised")
	require.Truef(t, oracle.is(shadow),
		"placeholder row %d -- which the name index does NOT point at -- was treated as a real "+
			"author; every book under it stays permanently gated", shadow)
}

// The cache must not turn a real author into a placeholder or vice versa, and it
// is read concurrently by the scan's worker pool.
func TestPlaceholderAuthorsCachesAndIsConcurrencySafe(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	ph, err := store.CreateAuthor(authorname.Placeholder)
	require.NoError(t, err)
	real, err := store.CreateAuthor("Terry Pratchett")
	require.NoError(t, err)

	oracle := newPlaceholderAuthors()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				require.True(t, oracle.is(ph.ID))
			} else {
				require.False(t, oracle.is(real.ID))
			}
		}(i)
	}
	wg.Wait()
}

// TestScanNominatesABookWhoseOnlyAuthorIsThePlaceholder covers the GATE CALL
// SITE, which the unit tests above cannot reach.
//
// rowHasRealAuthor passing its own table proves nothing about whether
// ProcessBooksParallel actually consults it: a mutant restoring the old inline
// `dbExisting.AuthorID != nil && *dbExisting.AuthorID != 0` check leaves every
// unit test in this file green. That is the same failure this package already
// shipped once, on the AI-enqueue test, and it is why this test exists.
func TestScanNominatesABookWhoseOnlyAuthorIsThePlaceholder(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })

	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })

	oldExts := config.AppConfig.SupportedExtensions
	oldAI := config.AppConfig.EnableAIParsing
	oldBackend := config.AppConfig.AIBackend
	t.Cleanup(func() {
		config.AppConfig.SupportedExtensions = oldExts
		config.AppConfig.EnableAIParsing = oldAI
		config.AppConfig.AIBackend = oldBackend
	})
	config.AppConfig.SupportedExtensions = []string{".mp3"}
	config.AppConfig.EnableAIParsing = true
	config.AppConfig.AIBackend.LLMMode = config.AIBackendModeLocal
	config.AppConfig.AIBackend.LocalBaseURL = "http://127.0.0.1:1"
	config.AppConfig.AIBackend.LocalLLMModel = "test-model"

	dir := t.TempDir()
	segs := writeSegments(t, dir, "placeholder-authored.mp3", "really-authored.mp3")

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(_ context.Context, book *Book) error {
		if existing, err := store.GetBookByFilePath(book.FilePath); err == nil && existing != nil {
			return nil
		}
		_, err := store.CreateBook(&database.Book{FilePath: book.FilePath, Title: book.Title})
		return err
	}

	// The book under test: a title, and an author that is ONLY the placeholder.
	placeholderID, err := resolveAuthorID(authorname.Placeholder)
	require.NoError(t, err)
	require.NotNil(t, placeholderID)
	_, err = store.CreateBook(&database.Book{
		FilePath: segs[0],
		Title:    "A Book Whose Author Is Unknown",
		AuthorID: placeholderID,
	})
	require.NoError(t, err)

	// The KNOWN-GOOD TWIN: a real author, which must still close the gate. Without
	// it, a mutant that nominates every book would pass the assertion below.
	realID, err := resolveAuthorID("Terry Pratchett")
	require.NoError(t, err)
	require.NotNil(t, realID)
	require.NotEqual(t, *placeholderID, *realID, "the fixture cannot distinguish the two authors")
	_, err = store.CreateBook(&database.Book{
		FilePath: segs[1],
		Title:    "A Book With A Real Author",
		AuthorID: realID,
	})
	require.NoError(t, err)

	var queued []AIParseCandidate
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		queued = append(queued, batch...)
		return nil
	})

	books := []Book{
		{FilePath: segs[0], Format: ".mp3", Title: "A Book Whose Author Is Unknown"},
		{FilePath: segs[1], Format: ".mp3", Title: "A Book With A Real Author", Author: "Terry Pratchett"},
	}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, nil))

	paths := make(map[string]bool, len(queued))
	for _, c := range queued {
		paths[c.FilePath] = true
	}

	require.Truef(t, paths[segs[0]],
		"the book whose only author is the placeholder was NOT nominated for AI parsing; "+
			"it is permanently unhealable. queued=%v", paths)
	require.Falsef(t, paths[segs[1]],
		"the book with a real author WAS nominated, so the assertion above proves nothing -- "+
			"this scan nominates everything. queued=%v", paths)
}
