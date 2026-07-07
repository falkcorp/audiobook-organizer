// file: internal/server/embedding_backfill_test.go
// version: 1.3.0
// guid: 4f81c2ae-6b39-47d5-9ae1-3c5d8b12f7a4
// last-edited: 2026-07-07

package server

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestDedupScanProgressLogger_BucketCrossings verifies that a callback driven
// at FullScan's actual step size (done = i+1 with i%10 == 0) emits a log line
// approximately every `interval` books — the scenario the original
// `done%interval == 0` check silently broke.
func TestDedupScanProgressLogger_BucketCrossings(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	progress := newDedupScanProgressLogger(1000, logf)

	// Simulate FullScan calling progress("scan", i+1, total) on every i where
	// i%10 == 0.
	const total = 2500
	for i := 0; i < total; i++ {
		if i%10 == 0 || i == total-1 {
			progress("scan", i+1, total)
		}
	}

	// Expected log lines: one at the first crossing of 1000, one at 2000, and
	// one at total completion (2500). None of those satisfy the buggy
	// `done%1000 == 0` check since done values are always of the form 10k+1.
	if got, want := len(lines), 3; got != want {
		t.Fatalf("expected %d log lines, got %d: %v", want, got, lines)
	}
	// First two should be at bucket crossings near 1000 and 2000.
	if lines[0] != "[INFO] Dedup scan progress (scan): 1001/2500" {
		t.Errorf("first log line = %q", lines[0])
	}
	if lines[1] != "[INFO] Dedup scan progress (scan): 2001/2500" {
		t.Errorf("second log line = %q", lines[1])
	}
	// Last one is the completion line.
	if lines[2] != "[INFO] Dedup scan progress (scan): 2500/2500" {
		t.Errorf("final log line = %q", lines[2])
	}
}

// TestDedupScanProgressLogger_EveryItem exercises the closure against a caller
// that invokes progress for every single item, not just on a 10-step. The
// logger should still only fire once per bucket.
func TestDedupScanProgressLogger_EveryItem(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	progress := newDedupScanProgressLogger(100, logf)

	const total = 350
	for i := 0; i < total; i++ {
		progress("scan", i+1, total)
	}

	// Expected: one log line at each of done=100, 200, 300, and the completion
	// line at done=350 — four lines total.
	if got, want := len(lines), 4; got != want {
		t.Fatalf("expected %d log lines, got %d: %v", want, got, lines)
	}
}

// TestDedupScanProgressLogger_SmallTotal verifies that a scan smaller than the
// interval still emits the final completion line.
func TestDedupScanProgressLogger_SmallTotal(t *testing.T) {
	var lines []string
	progress := newDedupScanProgressLogger(1000, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	const total = 42
	for i := 0; i < total; i++ {
		if i%10 == 0 || i == total-1 {
			progress("scan", i+1, total)
		}
	}

	if len(lines) != 1 {
		t.Fatalf("expected 1 log line (completion only), got %d: %v", len(lines), lines)
	}
	if lines[0] != "[INFO] Dedup scan progress (scan): 42/42" {
		t.Errorf("completion line = %q", lines[0])
	}
}

// TestDedupScanProgressLogger_NonPositiveInterval defends against a caller
// passing 0 or a negative interval — the logger should degrade gracefully by
// treating the interval as 1 (log every call) rather than div-by-zero or loop
// forever.
func TestDedupScanProgressLogger_NonPositiveInterval(t *testing.T) {
	count := 0
	progress := newDedupScanProgressLogger(0, func(format string, args ...any) {
		count++
	})
	for i := 0; i < 5; i++ {
		progress("scan", i+1, 5)
	}
	if count != 5 {
		t.Errorf("interval=0 should log every call; got %d calls", count)
	}
}

// fakeEmbedBookStatus deterministically maps a book ID to an EmbedStatus (or
// an error), cycling through every bucket the real EmbedBook can return plus
// an error case, so the aggregate stats exercise every branch of
// embedBooksConcurrent's switch.
func fakeEmbedBookStatus(id string) (dedup.EmbedStatus, error) {
	n, _ := strconv.Atoi(id)
	switch n % 5 {
	case 0:
		return dedup.EmbedStatusEmbedded, nil
	case 1:
		return dedup.EmbedStatusCached, nil
	case 2:
		return dedup.EmbedStatusSkippedNonPrimary, nil
	case 3:
		return dedup.EmbedStatusSkippedEmptyTitle, nil
	default: // n % 5 == 4
		return 0, fmt.Errorf("fake embed error for book %s", id)
	}
}

// TestEmbedBooksConcurrent_ParallelMatchesSerial is the CONC-6 parallel==serial
// regression test: run registry.RunItems-backed embedBooksConcurrent once
// sequentially (Concurrency=1) and once with the production concurrency
// (embeddingBackfillConcurrency), and assert every bucket count is identical.
// The two runs use fresh mutable counters and a fake embedFn — no shared
// state crosses runs — so any mismatch means the parallel path is dropping or
// double-counting items. Run with -race (see Makefile gate) to confirm the
// counters mutex actually prevents a data race under concurrency>1.
func TestEmbedBooksConcurrent_ParallelMatchesSerial(t *testing.T) {
	const n = 500
	books := make([]database.BookCore, n)
	for i := 0; i < n; i++ {
		books[i] = database.BookCore{ID: strconv.Itoa(i)}
	}

	serial, err := embedBooksConcurrent(context.Background(), books, 1, func(_ context.Context, id string) (dedup.EmbedStatus, error) {
		return fakeEmbedBookStatus(id)
	})
	if err != nil {
		t.Fatalf("serial run: unexpected error: %v", err)
	}

	parallel, err := embedBooksConcurrent(context.Background(), books, embeddingBackfillConcurrency, func(_ context.Context, id string) (dedup.EmbedStatus, error) {
		return fakeEmbedBookStatus(id)
	})
	if err != nil {
		t.Fatalf("parallel run: unexpected error: %v", err)
	}

	if serial != parallel {
		t.Fatalf("parallel stats diverged from serial: serial=%+v parallel=%+v", serial, parallel)
	}
	if parallel.Visited != n {
		t.Fatalf("expected Visited=%d, got %d", n, parallel.Visited)
	}
	// n=500, n%5 buckets: 100 embedded, 100 cached, 100 skipped_non_primary,
	// 100 skipped_empty_title, 100 errors.
	want := embeddingBackfillStats{Embedded: 100, Cached: 100, SkippedNonPrimary: 100, SkippedEmptyTitle: 100, Errors: 100, Visited: 500}
	if parallel != want {
		t.Fatalf("unexpected stats: got %+v, want %+v", parallel, want)
	}
}

// TestEmbedBooksConcurrent_ConcurrentCallsAreSerialized exercises the
// counters mutex directly under high concurrency with a large item count and
// an embedFn with no artificial delay, so the race detector has many
// interleavings to catch if the mutex were ever removed.
func TestEmbedBooksConcurrent_ConcurrentCallsAreSerialized(t *testing.T) {
	const n = 2000
	books := make([]database.BookCore, n)
	for i := 0; i < n; i++ {
		books[i] = database.BookCore{ID: strconv.Itoa(i)}
	}

	var calls sync.Map // set of visited IDs, to also confirm no double-processing
	stats, err := embedBooksConcurrent(context.Background(), books, 16, func(_ context.Context, id string) (dedup.EmbedStatus, error) {
		if _, dup := calls.LoadOrStore(id, true); dup {
			t.Errorf("book %s processed more than once", id)
		}
		return dedup.EmbedStatusEmbedded, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Visited != n || stats.Embedded != n {
		t.Fatalf("expected Visited=Embedded=%d, got %+v", n, stats)
	}
}

// TestEmbedAuthorsConcurrent_ParallelMatchesSerial mirrors the book test for
// the author loop: EmbedAuthor's outcome is boolean (success/error), so the
// only shared state is the completed count.
func TestEmbedAuthorsConcurrent_ParallelMatchesSerial(t *testing.T) {
	const n = 400
	authors := make([]database.Author, n)
	for i := 0; i < n; i++ {
		authors[i] = database.Author{ID: i}
	}
	fakeEmbed := func(_ context.Context, id int) error {
		if id%4 == 3 {
			return fmt.Errorf("fake embed author error for %d", id)
		}
		return nil
	}

	serialCount, err := embedAuthorsConcurrent(context.Background(), authors, 1, fakeEmbed)
	if err != nil {
		t.Fatalf("serial run: unexpected error: %v", err)
	}
	parallelCount, err := embedAuthorsConcurrent(context.Background(), authors, embeddingBackfillConcurrency, fakeEmbed)
	if err != nil {
		t.Fatalf("parallel run: unexpected error: %v", err)
	}

	if serialCount != parallelCount {
		t.Fatalf("parallel author count diverged from serial: serial=%d parallel=%d", serialCount, parallelCount)
	}
	// n=400, id%4==3 fails a quarter of the time: 300 successes.
	if want := 300; parallelCount != want {
		t.Fatalf("unexpected author count: got %d, want %d", parallelCount, want)
	}
}

// TestEmbeddingBackfillReporter_CancelAndProgress exercises the Reporter
// adapter's cancellation and interval-logging behavior directly, since
// runEmbeddingBackfill itself requires a live Server/dedup.Engine/store to
// exercise end-to-end.
func TestEmbeddingBackfillReporter_CancelAndProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &embeddingBackfillReporter{ctx: ctx, logEvery: 10}

	if r.IsCanceled() {
		t.Fatal("expected IsCanceled=false before cancel")
	}
	cancel()
	if !r.IsCanceled() {
		t.Fatal("expected IsCanceled=true after cancel")
	}

	// UpdateProgress/Log/Checkpoint/RunPhase/Trigger/SetCurrentItem must
	// never error or panic even with the inert no-op paths.
	if err := r.UpdateProgress(10, 100, "progress"); err != nil {
		t.Errorf("UpdateProgress: %v", err)
	}
	if err := r.Checkpoint(struct{}{}); err != nil {
		t.Errorf("Checkpoint: %v", err)
	}
	if err := r.Trigger(ctx, "event", nil); err != nil {
		t.Errorf("Trigger: %v", err)
	}
	r.SetCurrentItem("label") // must not panic

	ran := false
	if err := r.RunPhase(ctx, "phase", func(_ context.Context, rep registry.Reporter) error {
		ran = true
		if rep != r {
			t.Error("RunPhase should pass the same reporter through")
		}
		return nil
	}); err != nil {
		t.Errorf("RunPhase: %v", err)
	}
	if !ran {
		t.Error("RunPhase did not invoke fn")
	}

	// nil-ctx reporter must not panic.
	nilCtxReporter := &embeddingBackfillReporter{}
	if nilCtxReporter.IsCanceled() {
		t.Error("nil-ctx reporter should report not canceled")
	}
}
