// file: internal/scanner/ai_parse_async_test.go
// version: 1.1.0
// guid: d0871769-241c-463d-91ac-02ba0dac2f94
// last-edited: 2026-08-24

package scanner

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// withEnqueueHook swaps the package-level hook for the duration of a test and
// restores it afterwards. The hook is global state shared with the live scan
// path, so a test that leaked a stub into it would silently redirect every
// subsequent test's AI phase.
func withEnqueueHook(t *testing.T, fn func(context.Context, []Book) error) {
	t.Helper()
	prev := EnqueueAIParseFn
	EnqueueAIParseFn = fn
	t.Cleanup(func() { EnqueueAIParseFn = prev })
}

func TestEnqueueAIParseReportsUnavailableWhenNoQueueIsWired(t *testing.T) {
	withEnqueueHook(t, nil)

	err := enqueueAIParse(context.Background(), []Book{{FilePath: "/a.m4b"}}, []int{0}, logger.New("test"))

	if !errors.Is(err, ErrAIParseEnqueueUnavailable) {
		t.Fatalf("want ErrAIParseEnqueueUnavailable so the caller parses inline, got %v", err)
	}
}

func TestEnqueueAIParseChunksCandidatesAndDeliversEveryBook(t *testing.T) {
	// One more than two full chunks, so the trailing partial flush is exercised
	// rather than landing exactly on a chunk boundary.
	total := aiParseEnqueueChunk*2 + 1
	books := make([]Book, total)
	for i := range books {
		books[i] = Book{FilePath: fmt.Sprintf("/lib/book-%d.m4b", i)}
	}
	candidates := make([]int, total)
	for i := range candidates {
		candidates[i] = i
	}

	var batchSizes []int
	var delivered []string
	withEnqueueHook(t, func(_ context.Context, batch []Book) error {
		batchSizes = append(batchSizes, len(batch))
		for _, b := range batch {
			delivered = append(delivered, b.FilePath)
		}
		return nil
	})

	if err := enqueueAIParse(context.Background(), books, candidates, logger.New("test")); err != nil {
		t.Fatalf("enqueueAIParse: %v", err)
	}

	wantSizes := []int{aiParseEnqueueChunk, aiParseEnqueueChunk, 1}
	if len(batchSizes) != len(wantSizes) {
		t.Fatalf("want %d batches %v, got %d batches %v", len(wantSizes), wantSizes, len(batchSizes), batchSizes)
	}
	for i, want := range wantSizes {
		if batchSizes[i] != want {
			t.Errorf("batch %d: want %d books, got %d", i, want, batchSizes[i])
		}
	}

	// Every candidate must reach the queue exactly once. A chunking bug that
	// drops the tail leaves those books permanently unparsed, and nothing
	// downstream would report it.
	if len(delivered) != total {
		t.Fatalf("want %d books delivered, got %d", total, len(delivered))
	}
	for i, path := range delivered {
		if want := fmt.Sprintf("/lib/book-%d.m4b", i); path != want {
			t.Fatalf("delivered[%d] = %q, want %q (order or contents corrupted)", i, path, want)
		}
	}
}

func TestEnqueueAIParseCopiesBooksRatherThanAliasingTheScanSlice(t *testing.T) {
	// The scan's slice is reused and mutated after the AI phase returns. If the
	// enqueued batch aliased it, the operation would serialize whatever the scan
	// wrote later, not the candidate it nominated.
	books := []Book{{FilePath: "/a.m4b", Title: "as nominated"}}

	var captured []Book
	withEnqueueHook(t, func(_ context.Context, batch []Book) error {
		captured = batch
		return nil
	})

	if err := enqueueAIParse(context.Background(), books, []int{0}, logger.New("test")); err != nil {
		t.Fatalf("enqueueAIParse: %v", err)
	}
	books[0].Title = "mutated after enqueue"

	if len(captured) != 1 {
		t.Fatalf("want 1 captured book, got %d", len(captured))
	}
	if captured[0].Title != "as nominated" {
		t.Errorf("captured batch aliases the scan slice: title is %q, want %q", captured[0].Title, "as nominated")
	}
}

func TestEnqueueAIParseSkipsOutOfRangeCandidates(t *testing.T) {
	books := []Book{{FilePath: "/a.m4b"}}

	var delivered []string
	withEnqueueHook(t, func(_ context.Context, batch []Book) error {
		for _, b := range batch {
			delivered = append(delivered, b.FilePath)
		}
		return nil
	})

	if err := enqueueAIParse(context.Background(), books, []int{-1, 0, 7}, logger.New("test")); err != nil {
		t.Fatalf("enqueueAIParse: %v", err)
	}

	if len(delivered) != 1 || delivered[0] != "/a.m4b" {
		t.Fatalf("want only the in-range book delivered, got %v", delivered)
	}
}

func TestEnqueueAIParsePropagatesQueueErrors(t *testing.T) {
	// The caller distinguishes this from ErrAIParseEnqueueUnavailable to decide
	// whether to log a warning before falling back to inline parsing.
	boom := errors.New("registry unavailable")
	withEnqueueHook(t, func(context.Context, []Book) error { return boom })

	err := enqueueAIParse(context.Background(), []Book{{FilePath: "/a.m4b"}}, []int{0}, logger.New("test"))

	if !errors.Is(err, boom) {
		t.Fatalf("want the queue's error propagated, got %v", err)
	}
	if errors.Is(err, ErrAIParseEnqueueUnavailable) {
		t.Fatal("a queue failure must not masquerade as an unwired queue")
	}
}

func TestRunAIParseForBooksIsANoopWhenAIParsingIsDisabled(t *testing.T) {
	prev := config.AppConfig.EnableAIParsing
	config.AppConfig.EnableAIParsing = false
	t.Cleanup(func() { config.AppConfig.EnableAIParsing = prev })

	// A batch that arrives after the operator turned AI off is not an error.
	// Returning one would mark the operation failed and, under a retrying
	// policy, retry it forever against a backend that is switched off.
	if err := RunAIParseForBooks(context.Background(), []Book{{FilePath: "/a.m4b"}}, logger.New("test")); err != nil {
		t.Fatalf("want nil for a disabled backend, got %v", err)
	}
}

func TestRunAIParseForBooksIsANoopOnAnEmptyBatch(t *testing.T) {
	if err := RunAIParseForBooks(context.Background(), nil, logger.New("test")); err != nil {
		t.Fatalf("want nil for an empty batch, got %v", err)
	}
}

// TestEnqueueAIParseStripsFieldsTheAIPhaseNeverReads bounds the operation's
// params row. A multi-file book carries every segment path and every segment
// hash; a batch of 200 of them would serialize megabytes into a single op row,
// none of which anything on this path reads. It would also make the params blob
// a second, stale copy of data the scan already persisted.
func TestEnqueueAIParseStripsFieldsTheAIPhaseNeverReads(t *testing.T) {
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

	var got []Book
	withEnqueueHook(t, func(_ context.Context, batch []Book) error {
		got = append(got, batch...)
		return nil
	})
	if err := enqueueAIParse(context.Background(), books, []int{0}, logger.New("test")); err != nil {
		t.Fatalf("enqueueAIParse: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("want 1 book, got %d", len(got))
	}
	b := got[0]

	// The seven fields the AI phase and its saver actually touch.
	if b.FilePath != "/lib/book.m4b" || b.Title != "Kept" || b.Author != "Kept" ||
		b.Series != "Kept" || b.Position != 3 || b.Narrator != "Kept" || b.Publisher != "Kept" {
		t.Fatalf("a field the AI phase reads was dropped: %+v", b)
	}

	// Everything else must be gone.
	if b.SegmentFiles != nil {
		t.Errorf("SegmentFiles rode along into the op params: %v", b.SegmentFiles)
	}
	if b.SegmentHashes != nil {
		t.Errorf("SegmentHashes rode along into the op params: %v", b.SegmentHashes)
	}
	if b.FileHash != "" || b.Format != "" || b.Duration != 0 {
		t.Errorf("unread fields rode along: hash=%q format=%q duration=%d", b.FileHash, b.Format, b.Duration)
	}
}
