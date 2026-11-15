// file: internal/plugins/maintenance/duration_backfill_pool_test.go
// version: 1.0.1
// guid: 6df2f8ff-a5db-4063-902a-56a68a37c969
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The book grain of maintenance.duration-backfill runs its ffprobe workers as a
// seam (review MINOR 12, N3): HEAD's shell keeps its goroutines, the native
// shell runs the books as the items of a loop declared Fixed(workers). These
// tests pin what the native side must do that the goroutines never had to: run
// exactly the declared workers, honour the operator's cap, honour an operator
// pause between books, bound each book, count books in its progress, and not
// hang when a book's worker dies.

// gatedDurStore answers a book per id and holds every GetBookFiles (the first
// call a worker makes on a book) at a one-shot barrier of want concurrent
// callers, recording the most that were ever inside at once.
type gatedDurStore struct {
	zeroT6G4Store
	books    int
	want     int32
	patience time.Duration
	panicOn  string // GetBookFiles of this book id panics once ("" = never)

	in, maxIn atomic.Int32
	released  atomic.Bool
	panicked  atomic.Bool
}

func (s *gatedDurStore) CountAllBooks() (int, error) { return s.books, nil }

func (s *gatedDurStore) GetBookByID(id string) (*database.Book, error) {
	return &database.Book{ID: id, Title: "Batman " + id}, nil
}

func (s *gatedDurStore) GetBookFiles(id string) ([]database.BookFile, error) {
	if id == s.panicOn && s.panicked.CompareAndSwap(false, true) {
		panic("the Joker got into the worker")
	}
	cur := s.in.Add(1)
	defer s.in.Add(-1)
	for {
		m := s.maxIn.Load()
		if cur <= m || s.maxIn.CompareAndSwap(m, cur) {
			break
		}
	}
	if !s.released.Load() {
		deadline := time.Now().Add(s.patience)
		for s.in.Load() < s.want && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		s.released.Store(true)
	}
	return nil, nil
}

func durationBookIDs(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("bruce-%02d", i+1)
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

// runDurationNative runs the native kind in preview over n books by id.
func runDurationNative(t *testing.T, store *gatedDurStore, workers, books int, capWorkers int) (*t6g4Host, durationBackfillResult, error) {
	t.Helper()
	deps := t6g4Deps{
		Store:     store,
		NewWriter: func(tasks.Writer) t6g4Writer { return recT6G4Writer{rec: &t6g4Calls{}} },
	}
	def := t6g4KindByName(t, t6g4Kinds(deps, nil), durationBackfillKind)
	host, rc := newT6G4RC(durationBackfillKind, tasks.ModePreview, &t6g4RunWriter{})
	if capWorkers > 0 {
		host.SetCap(capWorkers)
	}
	raw, err := runT6G4Kind(t, def, rc, fmt.Sprintf(`{"workers":%d,"bookIds":%s}`, workers, durationBookIDs(books)))
	var out durationBackfillResult
	if err == nil {
		if uerr := json.Unmarshal(raw, &out); uerr != nil {
			t.Fatal(uerr)
		}
	}
	return host, out, err
}

func TestDurationBackfillNative_BookPoolRunsTheDeclaredWorkers(t *testing.T) {
	cases := []struct {
		name            string
		workers, capN   int
		want            int32
		patience        time.Duration
		wantMaxInFlight int32
	}{
		{"fixed(4)", 4, 0, 4, 5 * time.Second, 4},
		// The operator's parallelism cap lowers the declared 4 to 2. The drain
		// loops then run two at a time, and the queue is still drained.
		{"cap 2 under workers 4", 4, 2, 4, 300 * time.Millisecond, 2},
		{"sequential at workers 1", 1, 0, 2, 200 * time.Millisecond, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &gatedDurStore{books: 8, want: c.want, patience: c.patience}
			_, out, err := runDurationNative(t, store, c.workers, 8, c.capN)
			if err != nil {
				t.Fatal(err)
			}
			if got := store.maxIn.Load(); got != c.wantMaxInFlight {
				t.Fatalf("most workers inside a book at once = %d, want %d", got, c.wantMaxInFlight)
			}
			if out.Examined != 8 {
				t.Fatalf("examined %d of 8 books: the queue was not drained", out.Examined)
			}
		})
	}
}

func TestDurationBackfillNative_BookPoolCountsBooksInItsProgress(t *testing.T) {
	store := &gatedDurStore{books: 8, want: 4, patience: 5 * time.Second}
	host, _, err := runDurationNative(t, store, 4, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	// One item per book: the loop reports k of 8 books, ending at 8/8, on the
	// same denominator as the collector's heartbeat.
	ev := host.Progress()
	if len(ev) != 8 {
		t.Fatalf("the loop delivered %d progress events, want one per book (8): %+v", len(ev), ev)
	}
	top := 0
	for _, e := range ev {
		if e.Total != 8 || e.Done < 1 || e.Done > 8 {
			t.Fatalf("loop progress %d/%d is not books of 8: %+v", e.Done, e.Total, ev)
		}
		top = max(top, int(e.Done))
	}
	if top != 8 {
		t.Fatalf("the bar peaked at %d of 8 books", top)
	}
}

// pacedDurStore counts the books a worker has started and answers each at once.
type pacedDurStore struct {
	zeroT6G4Store
	books   int
	started atomic.Int32
}

func (s *pacedDurStore) CountAllBooks() (int, error) { return s.books, nil }
func (s *pacedDurStore) GetBookByID(id string) (*database.Book, error) {
	return &database.Book{ID: id, Title: "Batman " + id}, nil
}
func (s *pacedDurStore) GetBookFiles(string) ([]database.BookFile, error) {
	s.started.Add(1)
	time.Sleep(5 * time.Millisecond)
	return nil, nil
}

// An operator pause takes effect between books, mid-run: the pause gate runs
// before every item, and the books are the items.
func TestDurationBackfillNative_AnOperatorPauseHoldsTheNextBookMidRun(t *testing.T) {
	store := &pacedDurStore{books: 40}
	deps := t6g4Deps{Store: store, NewWriter: func(tasks.Writer) t6g4Writer { return recT6G4Writer{rec: &t6g4Calls{}} }}
	def := t6g4KindByName(t, t6g4Kinds(deps, nil), durationBackfillKind)
	host, rc := newT6G4RC(durationBackfillKind, tasks.ModePreview, &t6g4RunWriter{})
	done := make(chan error, 1)
	go func() {
		_, err := runT6G4Kind(t, def, rc, fmt.Sprintf(`{"workers":1,"bookIds":%s}`, durationBookIDs(40)))
		done <- err
	}()
	for store.started.Load() < 3 {
		time.Sleep(time.Millisecond)
	}
	host.Pause()
	time.Sleep(100 * time.Millisecond) // let the book in flight finish
	held := store.started.Load()
	time.Sleep(150 * time.Millisecond)
	if now := store.started.Load(); now != held {
		t.Fatalf("books kept starting while paused: %d then %d of 40", held, now)
	}
	if held >= 40 {
		t.Fatalf("the run finished before the pause took effect (%d books)", held)
	}
	host.Unpause()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not resume after the pause")
	}
	if got := store.started.Load(); got != 40 {
		t.Fatalf("started %d of 40 books", got)
	}
}

// The pool hands its loop the declared workers, a per-book timeout, the books'
// own denominator and a label that names the book.
func TestDurationPoolOn_DeclaresWorkersTimeoutDenominatorAndBookLabels(t *testing.T) {
	var got t6g4EachOpts
	var n int
	each := func(ctx context.Context, items int, o t6g4EachOpts, fn func(ctx context.Context, i int) error) error {
		got, n = o, items
		for i := 0; i < items; i++ {
			if err := fn(ctx, i); err != nil {
				return err
			}
		}
		return nil
	}
	jobs := make(chan database.Book, 3)
	for _, id := range []string{"gotham-1", "gotham-2", "gotham-3"} {
		jobs <- database.Book{ID: id}
	}
	close(jobs)
	var seen []string
	pool := durationPoolOn(each, func(id string) (*database.Book, error) {
		if id == "gotham-2" {
			return nil, nil // deleted since the producer saw it
		}
		return &database.Book{ID: id}, nil
	})
	if err := pool(context.Background(), 5, 99, jobs, func(_ context.Context, b database.Book) bool { seen = append(seen, b.ID); return true }); err != nil {
		t.Fatal(err)
	}
	if n != 3 || got.Concurrency != 5 || got.ProgressTotal != 99 || got.ItemTimeout != durationBookTimeout || got.Reason != durationPoolReason {
		t.Fatalf("items=%d opts=%+v", n, got)
	}
	if got.ItemTimeout <= 0 {
		t.Fatal("a book must be bounded")
	}
	if l := got.Label(2, 3); l != "book gotham-3" {
		t.Fatalf("label = %q", l)
	}
	if len(seen) != 2 || seen[0] != "gotham-1" || seen[1] != "gotham-3" {
		t.Fatalf("processed %v, want the books still there, in order", seen)
	}
}

func TestDurationBackfillNative_AWorkerThatPanicsEndsTheRunInsteadOfHangingIt(t *testing.T) {
	store := &gatedDurStore{books: 40, want: 1, patience: time.Millisecond, panicOn: "bruce-01"}
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		_, _, err := runDurationNative(t, store, 2, 40, 0)
		done <- result{err}
	}()
	select {
	case r := <-done:
		if r.err == nil || !strings.Contains(r.err.Error(), "bruce-01") || !strings.Contains(r.err.Error(), "panic") {
			t.Fatalf("want a panic error naming the book, got %v", r.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run hung after a worker panicked: the producer is blocked on a pool that is gone")
	}
}

func TestDurationPoolHEAD_RunsExactlyTheWorkersAndWaitsForThem(t *testing.T) {
	var started, running, handled atomic.Int32
	jobs := make(chan database.Book, 6)
	for i := 0; i < 6; i++ {
		jobs <- database.Book{ID: fmt.Sprintf("robin-%d", i)}
	}
	close(jobs)
	// Every worker holds its first book until all three are inside, so three
	// goroutines (no more, no fewer) must be draining jobs.
	var inside atomic.Int32
	err := durationPoolHEAD(context.Background(), 3, 99, jobs, func(context.Context, database.Book) bool {
		if inside.Add(1) <= 3 {
			started.Add(1)
			for inside.Load() < 3 {
				time.Sleep(time.Millisecond)
			}
		}
		running.Add(1)
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		handled.Add(1)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.Load() != 3 {
		t.Fatalf("started %d workers, want 3", started.Load())
	}
	if handled.Load() != 6 {
		t.Fatalf("handled %d of 6 books", handled.Load())
	}
	if running.Load() != 0 {
		t.Fatal("the pool returned while a worker was still running")
	}
}
