// file: internal/scanner/ai_parse_async_test.go
// version: 2.0.0
// guid: d0871769-241c-463d-91ac-02ba0dac2f94
// last-edited: 2026-08-24

package scanner

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/require"
)

// withEnqueueHook swaps the package-level hook for the duration of a test and
// restores it afterwards. The hook is global state shared with the live scan
// path, so a test that leaked a stub into it would silently redirect every
// subsequent test's AI phase.
func withEnqueueHook(t *testing.T, fn func(context.Context, []AIParseCandidate) error) {
	t.Helper()
	prev := SetEnqueueAIParse(fn)
	t.Cleanup(func() { SetEnqueueAIParse(prev) })
}

// seedBooks creates a row per path so enqueueAIParse can resolve an ID, and
// returns the scan-side Book values that name them.
func seedBooks(t *testing.T, store database.Store, paths ...string) []Book {
	t.Helper()
	books := make([]Book, len(paths))
	for i, p := range paths {
		_, err := store.CreateBook(&database.Book{FilePath: p, Title: "seed"})
		require.NoError(t, err)
		books[i] = Book{FilePath: p}
	}
	return books
}

func TestEnqueueAIParseReportsUnavailableWhenNoQueueIsWired(t *testing.T) {
	withEnqueueHook(t, nil)

	_, err := enqueueAIParse(context.Background(), []Book{{FilePath: "/a.m4b"}}, []int{0}, logger.New("test"))

	if !errors.Is(err, ErrAIParseEnqueueUnavailable) {
		t.Fatalf("want ErrAIParseEnqueueUnavailable so the caller parses inline, got %v", err)
	}
}

func TestEnqueueAIParseChunksCandidatesAndDeliversEveryBook(t *testing.T) {
	store := aiSaveStore(t)

	// One more than two full chunks, so the trailing partial flush is exercised
	// rather than landing exactly on a chunk boundary.
	total := aiParseEnqueueChunk*2 + 1
	paths := make([]string, total)
	for i := range paths {
		paths[i] = fmt.Sprintf("/lib/book-%d.m4b", i)
	}
	books := seedBooks(t, store, paths...)
	candidates := make([]int, total)
	for i := range candidates {
		candidates[i] = i
	}

	var batchSizes []int
	var delivered []string
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		batchSizes = append(batchSizes, len(batch))
		for _, b := range batch {
			delivered = append(delivered, b.FilePath)
		}
		return nil
	})

	queued, err := enqueueAIParse(context.Background(), books, candidates, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, total, queued)

	wantSizes := []int{aiParseEnqueueChunk, aiParseEnqueueChunk, 1}
	require.Equal(t, wantSizes, batchSizes)

	// Every candidate must reach the queue exactly once. A chunking bug that
	// drops the tail leaves those books permanently unparsed, and nothing
	// downstream would report it.
	require.Len(t, delivered, total)
	for i, path := range delivered {
		require.Equalf(t, fmt.Sprintf("/lib/book-%d.m4b", i), path, "delivered[%d] out of order or corrupted", i)
	}
}

// TestEnqueueAIParseCarriesTheRowIDNotJustThePath pins the fix for the defect
// that made the whole feature silently lossy: OrganizeOneBook sends every book
// under RootDir through ReOrganizeInPlace, which is a true safeRename. A batch
// that carried only a path found nothing when it ran, and discarded the parse
// with no error and no log line.
func TestEnqueueAIParseCarriesTheRowIDNotJustThePath(t *testing.T) {
	store := aiSaveStore(t)
	books := seedBooks(t, store, "/lib/a.m4b")

	var got []AIParseCandidate
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		got = append(got, batch...)
		return nil
	})
	_, err := enqueueAIParse(context.Background(), books, []int{0}, logger.New("test"))
	require.NoError(t, err)

	require.Len(t, got, 1)
	require.NotEmpty(t, got[0].ID, "the candidate must carry its row ID; a path does not survive organize")

	row, err := store.GetBookByFilePath("/lib/a.m4b")
	require.NoError(t, err)
	require.Equal(t, row.ID, got[0].ID)
}

// TestEnqueueAIParseDropsCandidatesWithNoRow: saveBookToDatabase returns early
// without creating a row for a file that duplicates an already version-linked
// book. Queuing those with an empty ID would send the batch looking for a row
// that does not exist.
func TestEnqueueAIParseDropsCandidatesWithNoRow(t *testing.T) {
	store := aiSaveStore(t)
	books := seedBooks(t, store, "/lib/real.m4b")
	books = append(books, Book{FilePath: "/lib/no-row.m4b"})

	var got []AIParseCandidate
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		got = append(got, batch...)
		return nil
	})
	queued, err := enqueueAIParse(context.Background(), books, []int{0, 1}, logger.New("test"))
	require.NoError(t, err)

	require.Len(t, got, 1, "the row-less candidate must be dropped, not queued with an empty ID")
	require.Equal(t, "/lib/real.m4b", got[0].FilePath)
	// Both candidate positions are still consumed: `queued` is an index into
	// the candidate list, not a count of books.
	require.Equal(t, 2, queued)
}

func TestEnqueueAIParseCopiesBooksRatherThanAliasingTheScanSlice(t *testing.T) {
	store := aiSaveStore(t)
	// The scan's slice is reused and mutated after the AI phase returns. If the
	// enqueued batch aliased it, the operation would serialize whatever the scan
	// wrote later, not the candidate it nominated.
	books := seedBooks(t, store, "/a.m4b")
	books[0].Title = "as nominated"

	var captured []AIParseCandidate
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		captured = batch
		return nil
	})

	_, err := enqueueAIParse(context.Background(), books, []int{0}, logger.New("test"))
	require.NoError(t, err)
	books[0].Title = "mutated after enqueue"

	require.Len(t, captured, 1)
	require.Equal(t, "as nominated", captured[0].Title, "captured batch aliases the scan slice")
}

func TestEnqueueAIParseSkipsOutOfRangeCandidates(t *testing.T) {
	store := aiSaveStore(t)
	books := seedBooks(t, store, "/a.m4b")

	var delivered []string
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		for _, b := range batch {
			delivered = append(delivered, b.FilePath)
		}
		return nil
	})

	_, err := enqueueAIParse(context.Background(), books, []int{-1, 0, 7}, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, []string{"/a.m4b"}, delivered)
}

func TestEnqueueAIParsePropagatesQueueErrors(t *testing.T) {
	store := aiSaveStore(t)
	books := seedBooks(t, store, "/a.m4b")

	// The caller distinguishes this from ErrAIParseEnqueueUnavailable to decide
	// whether to log a warning before falling back to inline parsing.
	boom := errors.New("registry unavailable")
	withEnqueueHook(t, func(context.Context, []AIParseCandidate) error { return boom })

	_, err := enqueueAIParse(context.Background(), books, []int{0}, logger.New("test"))

	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrAIParseEnqueueUnavailable,
		"a queue failure must not masquerade as an unwired queue")
}

// TestEnqueueAIParseReportsTheChunkBoundaryItReached is what stops the caller
// double-billing the LLM. Chunks already accepted cannot be recalled, so an
// enqueue failure part-way through must leave the caller parsing only the
// REMAINDER inline -- not the whole candidate list.
func TestEnqueueAIParseReportsTheChunkBoundaryItReached(t *testing.T) {
	store := aiSaveStore(t)

	total := aiParseEnqueueChunk*2 + 5
	paths := make([]string, total)
	for i := range paths {
		paths[i] = fmt.Sprintf("/lib/b-%d.m4b", i)
	}
	books := seedBooks(t, store, paths...)
	candidates := make([]int, total)
	for i := range candidates {
		candidates[i] = i
	}

	calls := 0
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		calls++
		if calls == 2 {
			return errors.New("registry went away")
		}
		return nil
	})

	queued, err := enqueueAIParse(context.Background(), books, candidates, logger.New("test"))
	require.Error(t, err)
	require.Equal(t, aiParseEnqueueChunk, queued,
		"the first chunk was accepted and must not be re-parsed inline")
}

func TestRunAIParseForBooksIsANoopWhenAIParsingIsDisabled(t *testing.T) {
	prev := config.AppConfig.EnableAIParsing
	config.AppConfig.EnableAIParsing = false
	t.Cleanup(func() { config.AppConfig.EnableAIParsing = prev })

	// A batch that arrives after the operator turned AI off is not an error.
	// Returning one would mark the operation failed and, under a retrying
	// policy, retry it forever against a backend that is switched off.
	summary, err := RunAIParseForBooks(context.Background(), []AIParseCandidate{{FilePath: "/a.m4b"}}, logger.New("test"))
	require.NoError(t, err)
	require.True(t, summary.Disabled, "a disabled backend must be distinguishable from a run that found nothing")
	require.False(t, summary.Aborted(), "disabled is not a failure")
}

func TestRunAIParseForBooksIsANoopOnAnEmptyBatch(t *testing.T) {
	_, err := RunAIParseForBooks(context.Background(), nil, logger.New("test"))
	require.NoError(t, err)
}

// TestEnqueueAIParseStripsFieldsTheAIPhaseNeverReads bounds the operation's
// params row. A multi-file book carries every segment path and every segment
// hash; a batch of 200 of them would serialize megabytes into a single op row,
// none of which anything on this path reads. It would also make the params blob
// a second, stale copy of data the scan already persisted.
func TestEnqueueAIParseStripsFieldsTheAIPhaseNeverReads(t *testing.T) {
	store := aiSaveStore(t)
	_, err := store.CreateBook(&database.Book{FilePath: "/lib/book.m4b", Title: "seed"})
	require.NoError(t, err)

	books := []Book{{
		FilePath:      "/lib/book.m4b",
		Title:         "Kept",
		Author:        "Kept",
		Series:        "Kept",
		Position:      3,
		Narrator:      "Kept",
		Publisher:     "Kept",
		SegmentFiles:  []string{"/lib/p1.mp3", "/lib/p2.mp3"},
		SegmentHashes: map[string]string{"/lib/p1.mp3": "deadbeef"},
		FileHash:      "cafebabe",
		Format:        ".m4b",
		Duration:      12345,
	}}

	var got []AIParseCandidate
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		got = append(got, batch...)
		return nil
	})
	_, err = enqueueAIParse(context.Background(), books, []int{0}, logger.New("test"))
	require.NoError(t, err)

	require.Len(t, got, 1)
	b := got[0]

	// The seven fields the AI phase and its saver actually touch, plus the ID.
	require.Equal(t, "/lib/book.m4b", b.FilePath)
	require.Equal(t, "Kept", b.Title)
	require.Equal(t, "Kept", b.Author)
	require.Equal(t, "Kept", b.Series)
	require.Equal(t, 3, b.Position)
	require.Equal(t, "Kept", b.Narrator)
	require.Equal(t, "Kept", b.Publisher)
	require.NotEmpty(t, b.ID)

	// AIParseCandidate is a closed struct, so the segment fields cannot ride
	// along by construction -- which is the point. Round-tripping the value
	// back into a Book proves the AI phase still sees nothing it does not need.
	back := b.book()
	require.Nil(t, back.SegmentFiles)
	require.Nil(t, back.SegmentHashes)
	require.Empty(t, back.FileHash)
}
