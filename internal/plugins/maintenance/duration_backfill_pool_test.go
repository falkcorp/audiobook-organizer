// file: internal/plugins/maintenance/duration_backfill_pool_test.go
// version: 1.0.0
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
// seam (review MINOR 12): HEAD's shell keeps its goroutines, the native shell
// runs the same drain loop as the items of a loop declared Fixed(workers). These
// tests pin what the native side must do that the goroutines never had to: run
// exactly the declared workers, honour the operator's cap, keep its own loop
// accounting out of the progress bar, and not hang when a worker dies.

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

func TestDurationBackfillNative_BookPoolParksItsLoopProgressAtTheEndOfTheBar(t *testing.T) {
	store := &gatedDurStore{books: 8, want: 4, patience: 5 * time.Second}
	host, _, err := runDurationNative(t, store, 4, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The loop's items are drain loops: one per worker, so its own accounting is
	// "k of workers". Delivered as that it would drag the bar to 4/4 at the end of
	// a run; it is parked at 8+k of 8+4, the end of the books' bar.
	ev := host.Progress()
	if len(ev) != 4 {
		t.Fatalf("the loop delivered %d progress events, want one per worker (4): %+v", len(ev), ev)
	}
	for _, e := range ev {
		if e.Done <= 8 || e.Total != 12 {
			t.Fatalf("loop progress %d/%d is not parked past the 8-book bar: %+v", e.Done, e.Total, ev)
		}
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
		if r.err == nil || !strings.Contains(r.err.Error(), "book workers") || !strings.Contains(r.err.Error(), "panic") {
			t.Fatalf("want a book workers panic error, got %v", r.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run hung after a worker panicked: the producer is blocked on a pool that is gone")
	}
}

func TestDurationPoolHEAD_RunsExactlyTheWorkersAndWaitsForThem(t *testing.T) {
	var started, running atomic.Int32
	err := durationPoolHEAD(context.Background(), 3, 99, func(context.Context) {
		started.Add(1)
		running.Add(1)
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.Load() != 3 {
		t.Fatalf("started %d workers, want 3", started.Load())
	}
	if running.Load() != 0 {
		t.Fatal("the pool returned while a worker was still running")
	}
}
