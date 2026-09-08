// file: internal/plugins/acoustid/backfill_concurrency_test.go
// version: 1.0.0
// guid: 4b1c7e92-6d05-4a38-9f71-2c8ab6d34e50
// last-edited: 2026-09-07

package acoustid

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// captureReporter is the minimum registry.Reporter (== sdk.Reporter) needed to
// drive RunItems, recording every checkpoint so the watermark can be inspected.
type captureReporter struct {
	mu          sync.Mutex
	checkpoints []BackfillParams
}

func (r *captureReporter) UpdateProgress(int, int, string) error      { return nil }
func (r *captureReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *captureReporter) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
func (r *captureReporter) IsCanceled() bool                           { return false }
func (r *captureReporter) Trigger(context.Context, string, any) error { return nil }
func (r *captureReporter) SetCurrentItem(string)                      {}

func (r *captureReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}

func (r *captureReporter) Checkpoint(state any) error {
	cp, ok := state.(BackfillParams)
	if !ok {
		return fmt.Errorf("checkpoint state is %T, want BackfillParams", state)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoints = append(r.checkpoints, cp)
	return nil
}

func (r *captureReporter) saved() []BackfillParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]BackfillParams(nil), r.checkpoints...)
}

// backfillFixture builds nBooks books of filesPerBook files each.
//
// Every file carries AcoustIDSeg0, so fingerprintEligibility stops at "already
// fingerprinted" and returns skipped WITHOUT touching the filesystem or invoking
// fpcalc — which is what lets this run in CI at all. BookSigV1 is non-nil so the
// signature-synthesis branch stays out of the way; it is exercised elsewhere.
func backfillFixture(nBooks, filesPerBook int) ([]database.Book, *database.MockStore) {
	books := make([]database.Book, nBooks)
	files := make(map[string][]database.BookFile, nBooks)
	for i := range books {
		id := fmt.Sprintf("book-%04d", i)
		sig := "signature-present"
		books[i] = database.Book{ID: id, BookSigV1: &sig}
		bf := make([]database.BookFile, filesPerBook)
		for j := range bf {
			bf[j] = database.BookFile{
				ID:           fmt.Sprintf("%s-file-%d", id, j),
				BookID:       id,
				FilePath:     fmt.Sprintf("/does/not/exist/%s-%d.m4b", id, j),
				AcoustIDSeg0: "AQADtAcSRY",
			}
		}
		files[id] = bf
	}
	store := &database.MockStore{
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return files[bookID], nil
		},
	}
	return books, store
}

// TestBackfillTally_IsExactUnderConcurrency is the measurement that earns the
// worker pool, in the same shape as the API-key lost-update fix: run the real
// work concurrently and check that no outcome went missing.
//
// The counters are written by every worker AND read by the Label closure, which
// run_items.go invokes inside each worker too. As plain ints that is a lost
// update — two workers read the same value, both add one, and one file's outcome
// vanishes from the total. Under -race it is also a reported data race.
//
// It runs the options built by backfillRunOptions rather than a copy of them, so
// dropping Concurrency in production makes this test sequential and its guard
// below fails rather than the test quietly continuing to pass.
func TestBackfillTally_IsExactUnderConcurrency(t *testing.T) {
	const (
		nBooks       = 240
		filesPerBook = 3
	)
	books, store := backfillFixture(nBooks, filesPerBook)
	p := New(nil, store, nil)

	var tally backfillTally
	rep := &captureReporter{}
	opts := backfillRunOptions(books, 0, &tally, rep)

	// Without this the whole test degrades to a sequential loop and proves
	// nothing about concurrency — the exact failure mode being guarded against.
	if opts.Concurrency < 2 {
		t.Fatalf("backfillRunOptions produced Concurrency=%d; the op is sequential and this test is vacuous", opts.Concurrency)
	}

	err := registry.RunItems(context.Background(), rep, books, func(_ context.Context, b database.Book) error {
		return p.backfillBook(b, &tally, rep.Logger())
	}, opts)
	if err != nil {
		t.Fatalf("RunItems: %v", err)
	}

	if got, want := tally.skipped.Load(), int64(nBooks*filesPerBook); got != want {
		t.Errorf("skipped tally = %d, want %d — %d outcomes were lost to concurrent increments", got, want, want-got)
	}
	if got := tally.fingerprinted.Load(); got != 0 {
		t.Errorf("fingerprinted = %d, want 0: the fixture is pre-fingerprinted, so nothing should have run fpcalc", got)
	}
	if got := tally.failed.Load(); got != 0 {
		t.Errorf("failed = %d, want 0", got)
	}
}

// TestBackfillCheckpoint_IsValidatableOnResume closes the loop between the two
// halves of the resume story: whatever the run writes must be something
// resolveResumePoint will actually accept. A watermark stored without its book
// ID, or against the wrong index, would be silently rejected on the next run and
// the op would restart from zero forever while appearing to checkpoint fine.
func TestBackfillCheckpoint_IsValidatableOnResume(t *testing.T) {
	books, store := backfillFixture(200, 1)
	p := New(nil, store, nil)

	var tally backfillTally
	rep := &captureReporter{}
	err := registry.RunItems(context.Background(), rep, books, func(_ context.Context, b database.Book) error {
		return p.backfillBook(b, &tally, rep.Logger())
	}, backfillRunOptions(books, 0, &tally, rep))
	if err != nil {
		t.Fatalf("RunItems: %v", err)
	}

	saved := rep.saved()
	if len(saved) == 0 {
		t.Fatal("no checkpoint was written for a 200-book run")
	}

	for _, cp := range saved {
		if cp.WatermarkBookID == "" {
			t.Fatalf("checkpoint at watermark %d carries no book ID; resolveResumePoint will reject it", cp.Watermark)
		}
		if cp.LastProcessedBookID != "" {
			t.Errorf("checkpoint still writes the legacy LastProcessedBookID (%q); it is read-only now", cp.LastProcessedBookID)
		}
		idx, reason := resolveResumePoint(books, cp)
		if reason != "" {
			t.Errorf("checkpoint at watermark %d was rejected on resume: %s", cp.Watermark, reason)
		}
		if idx != cp.Watermark {
			t.Errorf("resume index = %d, want the stored watermark %d", idx, cp.Watermark)
		}
	}
}

func TestResolveResumePoint_AcceptsAMatchingWatermark(t *testing.T) {
	books, _ := backfillFixture(10, 1)
	state := BackfillParams{Watermark: 4, WatermarkBookID: books[3].ID}

	idx, reason := resolveResumePoint(books, state)
	if reason != "" {
		t.Fatalf("unexpected rejection: %s", reason)
	}
	if idx != 4 {
		t.Errorf("resume index = %d, want 4", idx)
	}
}

// TestResolveResumePoint_RestartsWhenTheCollectionShifted is the reason the book
// ID is stored beside the index at all.
//
// GetAllBooksFullFrom is ID-ordered, so importing a book that sorts earlier
// shifts every later position down by one. A bare index resume would then skip
// exactly one book permanently — it would never be fingerprinted and nothing
// would ever revisit it, because the watermark had already moved past it.
func TestResolveResumePoint_RestartsWhenTheCollectionShifted(t *testing.T) {
	books, _ := backfillFixture(10, 1)
	state := BackfillParams{Watermark: 4, WatermarkBookID: books[3].ID}

	// A new book sorting before every existing one, as an import would produce.
	shifted := append([]database.Book{{ID: "book-0000-new"}}, books...)

	idx, reason := resolveResumePoint(shifted, state)
	if idx != 0 {
		t.Errorf("resume index = %d, want 0: the stored index now points at a different book", idx)
	}
	if reason == "" {
		t.Error("a discarded checkpoint must report why; silence here looks identical to a clean resume")
	}
}

func TestResolveResumePoint_RejectsAWatermarkWithNoBookID(t *testing.T) {
	books, _ := backfillFixture(10, 1)

	idx, reason := resolveResumePoint(books, BackfillParams{Watermark: 4})
	if idx != 0 {
		t.Errorf("resume index = %d, want 0: an unvalidatable index must not be trusted", idx)
	}
	if reason == "" {
		t.Error("expected a reason for discarding the checkpoint")
	}
}

func TestResolveResumePoint_RejectsAWatermarkPastTheEnd(t *testing.T) {
	books, _ := backfillFixture(10, 1)
	state := BackfillParams{Watermark: 99, WatermarkBookID: "book-0098"}

	idx, reason := resolveResumePoint(books, state)
	if idx != 0 {
		t.Errorf("resume index = %d, want 0", idx)
	}
	if reason == "" {
		t.Error("expected a reason for discarding the checkpoint")
	}
}

// TestResolveResumePoint_MigratesALegacyCheckpoint covers the upgrade: a
// checkpoint written by the pre-worker-pool binary carries only the last book ID
// and no index. Rejecting it would silently restart a nightly job that was most
// of the way through the library.
func TestResolveResumePoint_MigratesALegacyCheckpoint(t *testing.T) {
	books, _ := backfillFixture(10, 1)
	state := BackfillParams{LastProcessedBookID: books[6].ID}

	idx, reason := resolveResumePoint(books, state)
	if reason != "" {
		t.Fatalf("a legacy checkpoint must be honoured, not discarded: %s", reason)
	}
	if idx != 7 {
		t.Errorf("resume index = %d, want 7 (the book after the last completed one)", idx)
	}
}

func TestResolveResumePoint_RestartsWhenTheLegacyBookIsGone(t *testing.T) {
	books, _ := backfillFixture(10, 1)
	state := BackfillParams{LastProcessedBookID: "book-deleted"}

	idx, reason := resolveResumePoint(books, state)
	if idx != 0 {
		t.Errorf("resume index = %d, want 0", idx)
	}
	if reason == "" {
		t.Error("expected a reason for discarding the checkpoint")
	}
}

func TestResolveResumePoint_StartsAtZeroWithNoCheckpoint(t *testing.T) {
	books, _ := backfillFixture(10, 1)

	idx, reason := resolveResumePoint(books, BackfillParams{})
	if idx != 0 || reason != "" {
		t.Errorf("got (%d, %q), want (0, \"\") — a first run is not a discarded checkpoint", idx, reason)
	}
}

// TestBackfillWorkers_ReadsTheSharedKnob pins the pool size to the same
// FP_PARALLEL_WORKERS setting the rescan op uses, so an operator turning fpcalc
// pressure down does not have to discover a second dial.
func TestBackfillWorkers_ReadsTheSharedKnob(t *testing.T) {
	orig := config.AppConfig.FPParallelWorkers
	t.Cleanup(func() { config.AppConfig.FPParallelWorkers = orig })

	config.AppConfig.FPParallelWorkers = 12
	if got := backfillWorkers(); got != 12 {
		t.Errorf("backfillWorkers() = %d, want 12", got)
	}

	// Out-of-range values fall back rather than being clamped to something the
	// operator did not ask for.
	for _, bad := range []int{0, -1, 33} {
		config.AppConfig.FPParallelWorkers = bad
		if got := backfillWorkers(); got != 4 {
			t.Errorf("backfillWorkers() with FPParallelWorkers=%d = %d, want the default 4", bad, got)
		}
	}

	// The default must still be a pool. A default of 1 would make every
	// deployment that never sets the knob sequential — the original bug.
	config.AppConfig.FPParallelWorkers = 0
	if got := backfillWorkers(); got < 2 {
		t.Errorf("default worker count = %d; the op would run sequentially out of the box", got)
	}
}
