// file: internal/plugins/maintenance/duration_reextract_perbook_test.go
// version: 1.1.0
// guid: 4d727c43-4cac-44cd-a1d2-3e4c07c99da4
// last-edited: 2026-09-20

package maintenance

// Regression tests for the 2026-09-19 duration-reextract incident: one
// 1,494-file book took ~25 minutes because every per-segment UpdateBookFile
// recomputed the whole book's aggregates (O(n^2)), the op reported no progress
// meanwhile, and the collector ignored the watchdog's cancel.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

func applyReextractParams(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(durationReextractParams{DryRun: boolPtr(false), Workers: 4, SkipAgeDays: 0})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return b
}

// reextractSegs builds n fingerprinted segments whose stored Duration (50s)
// has drifted from the fingerprint duration (100s), so every one is rewritten.
func reextractSegs(bookID string, n int) []database.BookFile {
	segs := make([]database.BookFile, n)
	for i := range segs {
		segs[i] = database.BookFile{
			ID:                             fmt.Sprintf("%s-f%03d", bookID, i),
			BookID:                         bookID,
			FilePath:                       fmt.Sprintf("/lib/%s/part%03d.mp3", bookID, i),
			TrackNumber:                    i + 1,
			FileSize:                       2_000_000,
			Duration:                       50,
			AcoustIDFingerprintDurationSec: 100,
		}
	}
	return segs
}

// (a) A many-segment book must trigger exactly ONE aggregate recompute. Runs
// against a real PebbleStore: the per-row recompute lives inside
// PebbleStore.UpdateBookFile, so a MockStore (which has no recompute) would
// count one either way and could never go red.
func TestDurationReextract_ManySegmentBook_RecomputesAggregatesOnce(t *testing.T) {
	s := newRepairPebble(t)
	book, err := s.CreateBook(&database.Book{Title: "Big Book", FilePath: "/lib/big"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	const n = 30
	for _, f := range reextractSegs(book.ID, n) {
		f.ID = ""
		if err := s.CreateBookFile(&f); err != nil {
			t.Fatalf("CreateBookFile: %v", err)
		}
	}

	logs := aggtest.Capture(t) // after setup: only the op's recomputes count
	p := New(fakeDeps{store: s})
	if err := p.runDurationReextract(context.Background(), applyReextractParams(t), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := aggtest.CountInvocations(logs(), book.ID); got != 1 {
		t.Errorf("RecomputeBookAggregates ran %d times for a %d-segment book, want exactly 1", got, n)
	}
	files, _ := s.GetBookFiles(book.ID)
	for _, f := range files {
		if f.Duration != 100 {
			t.Fatalf("segment %s Duration=%d, want 100", f.ID, f.Duration)
		}
	}
	got, _ := s.GetBookByID(book.ID)
	if got == nil || got.Duration == nil || *got.Duration != 100*n {
		t.Fatalf("Book.Duration=%v, want %d", got.Duration, 100*n)
	}
}

// singleBookMock serves one book with the given segments from a MockStore.
func singleBookMock(book database.Book, segs []database.BookFile) *database.MockStore {
	books := []database.Book{book}
	return &database.MockStore{
		CountAllBooksFunc:       func() (int, error) { return 1, nil },
		GetAllBooksFullFromFunc: pageBooksFullFrom(books),
		GetBookFilesFunc: func(id string) ([]database.BookFile, error) {
			if id != book.ID {
				return nil, nil
			}
			return append([]database.BookFile(nil), segs...), nil
		},
		GetBookByIDFunc: func(string) (*database.Book, error) { cp := book; return &cp, nil },
	}
}

// (b) A cancel during one book's segment writes must stop the writes between
// rows and make the op return ctx's error promptly.
func TestDurationReextract_CancelStopsSegmentWritesMidBook(t *testing.T) {
	const n, cancelAfter = 200, 3
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := singleBookMock(database.Book{ID: "big", Title: "Big", FilePath: "/lib/big"}, reextractSegs("big", n))
	var writes atomic.Int64
	store.UpdateBookFileFunc = func(string, *database.BookFile) error {
		if writes.Add(1) == cancelAfter {
			cancel() // the watchdog cancels while the book is mid-write
		}
		return nil
	}
	var recomputes atomic.Int64
	store.RecomputeBookAggregatesFunc = func(string) error { recomputes.Add(1); return nil }

	p := New(fakeDeps{store: store})
	done := make(chan error, 1)
	go func() { done <- p.runDurationReextract(ctx, applyReextractParams(t), &fakeReporter{}) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run returned %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("op did not return after cancel")
	}
	if got := writes.Load(); got != cancelAfter {
		t.Errorf("wrote %d segments after a cancel at %d, want the writes to stop at %d", got, cancelAfter, cancelAfter)
	}
}

// progressCaptureReporter records progress messages and liveness touches.
type progressCaptureReporter struct {
	fakeReporter
	mu      sync.Mutex
	msgs    []string
	touches atomic.Int64
}

func (r *progressCaptureReporter) UpdateProgress(_, _ int, msg string) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	return nil
}
func (r *progressCaptureReporter) TouchLiveness() { r.touches.Add(1) }

var _ registry.LivenessToucher = (*progressCaptureReporter)(nil)

// (c) Liveness and progress must be reported DURING one long book, not only
// between books: the watchdog saw nothing for 25 minutes on 2026-09-19.
func TestDurationReextract_ReportsProgressDuringLongBook(t *testing.T) {
	prev := reextractSegmentProgressInterval
	reextractSegmentProgressInterval = 0
	t.Cleanup(func() { reextractSegmentProgressInterval = prev })

	const n = 50
	store := singleBookMock(database.Book{ID: "big", Title: "Big", FilePath: "/lib/big"}, reextractSegs("big", n))
	rep := &progressCaptureReporter{}
	var touchesAtWrite []int64
	store.UpdateBookFileFunc = func(string, *database.BookFile) error {
		touchesAtWrite = append(touchesAtWrite, rep.touches.Load())
		return nil
	}

	p := New(fakeDeps{store: store})
	if err := p.runDurationReextract(context.Background(), applyReextractParams(t), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(touchesAtWrite) != n {
		t.Fatalf("wrote %d segments, want %d", len(touchesAtWrite), n)
	}
	// Liveness must advance between segment writes of the same book.
	if last := touchesAtWrite[n-1]; last < n-1 {
		t.Errorf("liveness touched %d times before the last of %d segment writes, want >= %d", last, n, n-1)
	}
	segLines := 0
	rep.mu.Lock()
	for _, m := range rep.msgs {
		if strings.Contains(m, "wrote segment") {
			segLines++
		}
	}
	rep.mu.Unlock()
	if segLines < n {
		t.Errorf("got %d mid-book progress lines for %d segments, want one per segment at interval 0", segLines, n)
	}
}
