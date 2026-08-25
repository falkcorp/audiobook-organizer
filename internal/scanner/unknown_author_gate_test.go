// file: internal/scanner/unknown_author_gate_test.go
// version: 1.1.0
// guid: 3d9a5f71-2e84-4c06-b1f3-6a05e97c2db4
// last-edited: 2026-08-25

package scanner

import (
	"context"
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

func TestRowHasRealAuthor(t *testing.T) {
	id := func(i int) *int { return &i }
	placeholder := id(54846)

	for _, tc := range []struct {
		name        string
		authorID    *int
		placeholder *int
		want        bool
	}{
		{"nil author is not a real author", nil, placeholder, false},
		{"zero author is not a real author", id(0), placeholder, false},
		{"the placeholder is not a real author", id(54846), placeholder, false},
		{"a different author is real", id(38566), placeholder, true},
		// A database that has never filed an authorless book has no placeholder
		// row; every non-zero ID there is genuinely real.
		{"no placeholder row exists", id(38566), nil, true},
		{"nil author with no placeholder row", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, rowHasRealAuthor(tc.authorID, tc.placeholder))
		})
	}
}

// lookupPlaceholderAuthorID must not CREATE the row it looks for. Creating it
// would mint, on any database that had so far avoided it, the exact row the
// gate is trying to treat as absent.
func TestLookupPlaceholderAuthorIDDoesNotCreateTheRow(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	require.Nil(t, lookupPlaceholderAuthorID(), "a fresh store has no placeholder author")

	got, err := store.GetAuthorByName(authorname.Placeholder)
	require.NoError(t, err)
	require.Nil(t, got, "the lookup created the placeholder author row it was only meant to read")
}

// And once the row does exist it must be found, so the test above cannot pass
// by the lookup being broken outright.
func TestLookupPlaceholderAuthorIDFindsAnExistingRow(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	created, err := store.CreateAuthor(authorname.Placeholder)
	require.NoError(t, err)
	require.NotNil(t, created)

	got := lookupPlaceholderAuthorID()
	require.NotNil(t, got, "the placeholder author exists but the lookup did not find it")
	require.Equal(t, created.ID, *got)
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
