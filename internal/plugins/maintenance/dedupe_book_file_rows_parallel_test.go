// file: internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go
// version: 1.0.0
// guid: 7f21c6ad-95be-4c30-8d02-5b3a1e6f4c99
// last-edited: 2026-08-04

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// concurrentReporter is deliberately NOT the package's shared fakeReporter: that
// one appends to a plain slice, which would itself race once the op runs books in
// parallel — and a racing test harness reports a race that is not in the code
// under test. This one is mutex-guarded so `-race` failures mean something.
type concurrentReporter struct {
	mu       sync.Mutex
	progress int
}

func (r *concurrentReporter) UpdateProgress(cur, _ int, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur > r.progress {
		r.progress = cur
	}
	return nil
}
func (r *concurrentReporter) Log(_ slog.Level, _ string, _ ...slog.Attr) error { return nil }
func (r *concurrentReporter) Logger() *slog.Logger                            { return slog.Default() }
func (r *concurrentReporter) Checkpoint(_ any) error                          { return nil }
func (r *concurrentReporter) IsCanceled() bool                                { return false }
func (r *concurrentReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}
func (r *concurrentReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }
func (r *concurrentReporter) SetCurrentItem(_ string)                          {}

// seedDupBooks creates `books` books, each holding `copies` rows that all point at
// the SAME file path — the exact shape the op exists to collapse.
func seedDupBooks(t *testing.T, s *database.PebbleStore, books, copies int) []string {
	t.Helper()
	ids := make([]string, 0, books)
	for b := 0; b < books; b++ {
		bk, err := s.CreateBook(&database.Book{Title: fmt.Sprintf("Dup Book %02d", b)})
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		path := fmt.Sprintf("/lib/dup-%02d/track.m4b", b)
		for c := 0; c < copies; c++ {
			f := &database.BookFile{
				BookID:   bk.ID,
				FilePath: path,
				Duration: 3600,
				FileSize: 58000000, // ~129 kbps at 3600s — a plausible real bitrate
			}
			if err := s.CreateBookFile(f); err != nil {
				t.Fatalf("CreateBookFile: %v", err)
			}
		}
		ids = append(ids, bk.ID)
	}
	return ids
}

// 🔴 THE CONCURRENCY REGRESSION. The book loop was sequential, which CLAUDE.md's
// concurrency rule forbids for a whole-library loop doing per-item DB work — and
// the first full production run proved the cost: ~1.7 min/book meant 176 books
// could not finish inside the op's own 2-hour timeout.
//
// Parallelising is only safe because the partition is disjoint: a book_file row
// belongs to exactly one book, so two workers can never touch the same row or the
// same RecomputeBookAggregates target. This exercises that for real — many books,
// each with many duplicate rows — so `go test -race` has an actual parallel path
// to inspect. The package's other tests are pure functions and prove nothing here.
func TestDedupeBookFileRows_ParallelApplyCollapsesEveryBook(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 24, 6

	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	// Required: writes made before warmup publishes are dropped from memdb, and
	// PASS 1 of this op reads GetAllBookFilesCore (memdb). Without this the op
	// would see an empty library and "correctly" do nothing.
	s.WaitForWarmup()

	bookIDs := seedDupBooks(t, s, books, copies)

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})
	rep := &concurrentReporter{}

	if err := p.runDedupeBookFileRows(context.Background(), raw, rep); err != nil {
		t.Fatalf("runDedupeBookFileRows: %v", err)
	}

	// Every book must be collapsed to exactly one row, and it must be the same
	// path — nothing invented, nothing cross-contaminated between workers.
	for i, id := range bookIDs {
		files, ferr := s.GetBookFiles(id)
		if ferr != nil {
			t.Fatalf("GetBookFiles(%s): %v", id, ferr)
		}
		if len(files) != 1 {
			t.Fatalf("book %d (%s): %d rows survived, want exactly 1", i, id, len(files))
		}
		wantPath := fmt.Sprintf("/lib/dup-%02d/track.m4b", i)
		if files[0].FilePath != wantPath {
			t.Fatalf("book %d: surviving row path = %q, want %q", i, files[0].FilePath, wantPath)
		}
		// The survivor must still carry its data — a parallel worker must not have
		// blanked it while collapsing a neighbouring book.
		if files[0].Duration != 3600 {
			t.Fatalf("book %d: surviving duration = %d, want 3600", i, files[0].Duration)
		}
	}

	if rep.progress != books {
		t.Fatalf("progress reported %d/%d books; RunItems must count every completion", rep.progress, books)
	}
}

// A dry run must mutate nothing, and that has to hold under concurrency too —
// a missing Apply check inside a worker would delete rows the operator never
// approved, which is the one failure this op must never have.
func TestDedupeBookFileRows_ParallelDryRunDeletesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 12, 4

	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	bookIDs := seedDupBooks(t, s, books, copies)

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: false})
	if err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{}); err != nil {
		t.Fatalf("runDedupeBookFileRows (dry run): %v", err)
	}

	for i, id := range bookIDs {
		files, ferr := s.GetBookFiles(id)
		if ferr != nil {
			t.Fatalf("GetBookFiles: %v", ferr)
		}
		if len(files) != copies {
			t.Fatalf("book %d: dry run changed the row count to %d, want %d untouched",
				i, len(files), copies)
		}
	}
}
