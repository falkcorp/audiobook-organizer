// file: internal/scanner/ai_parse_enqueue_scan_test.go
// version: 1.4.0
// guid: 003dfb63-62eb-4147-8fbe-d3580d984034
// last-edited: 2026-08-25

package scanner

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TestProcessBooksParallelQueuesAICandidatesInsteadOfParsingInline is the test
// that covers the actual behaviour change, and it exists because its absence was
// measured rather than guessed.
//
// The unit tests around enqueueAIParse and library.ai-parse all pass against a
// ProcessBooksParallel that ignores the queue entirely: a mutant replacing the
// call site's enqueue with a hard-coded ErrAIParseEnqueueUnavailable -- i.e. the
// whole feature reverted to inline parsing -- survived the FULL scanner suite on
// 2026-08-24. Every one of those tests calls enqueueAIParse directly. Nothing
// asserted the scan calls it at all.
//
// The base URL points at a closed port on purpose. If the scan regresses to
// inline parsing the batch phase reaches for the LLM and gets nothing, which is
// the production symptom; the assertion below is what turns that into a failure
// rather than a slow no-op.
func TestProcessBooksParallelQueuesAICandidatesInsteadOfParsingInline(t *testing.T) {
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
	segs := writeSegments(t, dir, "part01.mp3", "already-known.mp3")

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	// The fake saver gives every book its one book_file row, because the scan
	// cache is keyed on book_file rows rather than book rows: a book with no file
	// row has no cache entry at all, so BOTH assertions below would pass
	// vacuously -- the candidate would read as "not stamped" simply because
	// nothing could ever stamp it. Both books must be STAMPABLE for the
	// withheld-stamp assertion to mean anything.
	//
	// This is also the production shape. A genuinely single-file book gets its row
	// from ensureSingleFileBookFile (or, for rows predating that, the scan-cache
	// backfill); createBookFilesForBook is never called for one.
	saveBook = func(_ context.Context, book *Book) error {
		if existing, err := store.GetBookByFilePath(book.FilePath); err == nil && existing != nil {
			return nil
		}
		created, err := store.CreateBook(&database.Book{FilePath: book.FilePath, Title: book.Title})
		if err != nil {
			return err
		}
		return store.CreateBookFile(&database.BookFile{
			BookID: created.ID, FilePath: created.FilePath, TrackNumber: 1,
		})
	}

	var queued []AIParseCandidate
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		queued = append(queued, batch...)
		return nil
	})

	// The KNOWN-GOOD TWIN. This row already carries a title and an author, which
	// closes the AI nomination gate, so this book is NOT a candidate and the scan
	// must stamp it normally. Without it the cache assertion below cannot tell
	// "the stamp was correctly withheld" from "the query never finds anything" --
	// which is exactly how the first version of this test passed against a mutant
	// that stamped every book.
	authorID, err := resolveAuthorID("A Known Author")
	require.NoError(t, err)
	require.NotNil(t, authorID)
	twin, err := store.CreateBook(&database.Book{
		FilePath: segs[1],
		Title:    "Already Known",
		AuthorID: authorID,
	})
	require.NoError(t, err)
	// The twin needs its file row for the same reason as above: it is the control
	// that proves the cache query can see a stamp at all.
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		BookID: twin.ID, FilePath: segs[1], TrackNumber: 1,
	}))

	// Series is empty and the store has no prior row carrying title+author, so
	// the FIRST book is nominated as an AI candidate.
	books := []Book{
		{FilePath: segs[0], Format: ".mp3", Title: "A Book Without A Series"},
		{FilePath: segs[1], Format: ".mp3", Title: "Already Known", Author: "A Known Author"},
	}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, nil))

	require.Lenf(t, queued, 1,
		"the scan did not hand its AI candidate to the queue; it parsed inline and blocked on the LLM")
	require.Equal(t, segs[0], queued[0].FilePath)
	require.NotEmpty(t, queued[0].ID,
		"the candidate must carry its row ID; the path does not survive organize's in-place rename")

	// And the scan must NOT have stamped the scan cache for it.
	//
	// This is the mechanism the op's ResumeDrop policy rests on. The stamp means
	// "fully processed, skip next time", and classifySkipFile returns BEFORE the
	// AI nomination check -- so a book stamped here is never re-nominated no
	// matter how empty its fields are. Stamping a book whose parse has only been
	// PROMISED turns every dropped, aborted or cancelled batch into permanent
	// silent loss. The AI phase writes this stamp instead, once a parse has
	// actually been attempted.
	// GetScanCacheMap is keyed by FILE PATH, not by row ID -- see
	// database/pebble_store_scancache.go. Keying this lookup on the ID is how the
	// first version of this assertion passed against the mutant it was written to
	// kill: the lookup found nothing whatever the code did.
	cache, err := store.GetScanCacheMap()
	require.NoError(t, err)

	_, twinStamped := cache[segs[1]]
	require.Truef(t, twinStamped,
		"the known-good twin was not stamped either, so the assertion below proves "+
			"nothing about the candidate -- this query cannot see a stamp at all. cache=%v", cache)

	_, stamped := cache[segs[0]]
	require.False(t, stamped,
		"the scan stamped the scan cache for a book whose AI parse has only been queued; "+
			"a dropped batch would now be unrecoverable and the next scan would skip the file")
}
