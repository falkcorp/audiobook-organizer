// file: internal/scanner/ai_parse_enqueue_scan_test.go
// version: 1.0.0
// guid: 003dfb63-62eb-4147-8fbe-d3580d984034
// last-edited: 2026-08-24

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
	segs := writeSegments(t, dir, "part01.mp3")

	oldSaver := saveBook
	t.Cleanup(func() { saveBook = oldSaver })
	saveBook = func(_ context.Context, book *Book) error {
		if existing, err := store.GetBookByFilePath(book.FilePath); err == nil && existing != nil {
			return nil
		}
		_, err := store.CreateBook(&database.Book{FilePath: book.FilePath, Title: book.Title})
		return err
	}

	var queued []Book
	withEnqueueHook(t, func(_ context.Context, batch []Book) error {
		queued = append(queued, batch...)
		return nil
	})

	// Series is empty and the store has no prior row carrying title+author, so
	// this book is nominated as an AI candidate.
	books := []Book{{
		FilePath: segs[0],
		Format:   ".mp3",
		Title:    "A Book Without A Series",
	}}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, nil))

	require.Lenf(t, queued, 1,
		"the scan did not hand its AI candidate to the queue; it parsed inline and blocked on the LLM")
	require.Equal(t, segs[0], queued[0].FilePath)
}
