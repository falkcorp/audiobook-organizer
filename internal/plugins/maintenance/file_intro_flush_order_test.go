// file: internal/plugins/maintenance/file_intro_flush_order_test.go
// version: 1.0.0
// guid: c2ad847a-b6b5-4d01-9dd0-37434f3087e5
// last-edited: 2026-10-30

package maintenance

import (
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// orderSink records every snapshot PutFileIntroStats receives, in arrival
// order. With holdFirst set, the first Put waits until a second Put arrives or
// holdFor elapses, which is what lets a test force the interleaving where a
// newer snapshot reaches the sink while an older one is still in flight.
type orderSink struct {
	mu        sync.Mutex
	puts      []database.FileIntroStats
	calls     int // Puts entered, counted on entry (puts only grows on exit)
	holdFirst bool
	holdFor   time.Duration
	second    chan struct{}
	entered   chan struct{} // closed when the first Put has been entered
}

func newOrderSink(holdFirst bool, holdFor time.Duration) *orderSink {
	return &orderSink{
		holdFirst: holdFirst, holdFor: holdFor,
		second: make(chan struct{}), entered: make(chan struct{}),
	}
}

func (s *orderSink) PutFileIntroStats(st *database.FileIntroStats) error {
	s.mu.Lock()
	n := s.calls
	s.calls++
	hold := s.holdFirst && n == 0
	if n == 0 {
		close(s.entered)
	}
	if n == 1 {
		close(s.second)
	}
	s.mu.Unlock()
	if hold {
		select {
		case <-s.second:
		case <-time.After(s.holdFor):
		}
	}
	s.mu.Lock()
	s.puts = append(s.puts, *st)
	s.mu.Unlock()
	return nil
}

func (s *orderSink) GetFileIntroStats() (*database.FileIntroStats, error) { return nil, nil }

func (s *orderSink) snapshot() []database.FileIntroStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.FileIntroStats(nil), s.puts...)
}

// requireMonotonic fails when a later persisted snapshot is older (by
// UpdatedAt) or smaller (by OK) than an earlier one.
func requireMonotonic(t *testing.T, puts []database.FileIntroStats) {
	t.Helper()
	for i := 1; i < len(puts); i++ {
		prev, cur := puts[i-1], puts[i]
		if cur.UpdatedAt.Before(prev.UpdatedAt) || cur.OK < prev.OK {
			t.Fatalf("persisted stats went backwards at write %d: OK %d -> %d, UpdatedAt %v -> %v",
				i, prev.OK, cur.OK, prev.UpdatedAt, cur.UpdatedAt)
		}
	}
}

// A flush that has copied its snapshot and is still writing must not be
// overtaken by a newer flush: the first Put is held until a second one shows up
// (or 100 ms), so without the write mutex the newer snapshot is recorded first
// and the older one last.
func TestFileIntroAccumFlush_NewerSnapshotCannotOvertakeOlder(t *testing.T) {
	sink := newOrderSink(true, 100*time.Millisecond)
	a := &fileIntroAccum{sink: sink}

	a.update(func(s *database.FileIntroStats) { s.OK = 1 })
	var wg sync.WaitGroup
	wg.Go(func() { a.flush(false) })
	<-sink.entered // the first flush has copied OK=1 and is inside Put

	a.update(func(s *database.FileIntroStats) { s.OK = 2 })
	wg.Go(func() { a.flush(true) })
	wg.Wait()

	puts := sink.snapshot()
	if len(puts) != 2 {
		t.Fatalf("expected 2 writes, got %d", len(puts))
	}
	requireMonotonic(t, puts)
	if last := puts[len(puts)-1]; last.OK != 2 || !last.Done {
		t.Fatalf("last persisted snapshot = OK %d Done %v, want OK 2 Done true", last.OK, last.Done)
	}
}

// Many workers bumping the counters and flushing at once, as RunItems' workers
// do: whatever the interleaving, the sink's last write is the final counters.
func TestFileIntroAccumFlush_ConcurrentWorkersPersistMonotonicStats(t *testing.T) {
	const workers, perWorker = 16, 25
	sink := newOrderSink(false, 0)
	a := &fileIntroAccum{sink: sink}

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				a.update(func(s *database.FileIntroStats) { s.OK++ })
				a.flush(false)
			}
		})
	}
	wg.Wait()
	a.flush(true)

	puts := sink.snapshot()
	if len(puts) != workers*perWorker+1 {
		t.Fatalf("expected %d writes, got %d", workers*perWorker+1, len(puts))
	}
	requireMonotonic(t, puts)
	final := a.snapshot()
	last := puts[len(puts)-1]
	if last.OK != workers*perWorker || last.OK != final.OK || !last.Done {
		t.Fatalf("last persisted OK=%d Done=%v, final counters OK=%d, want %d and Done",
			last.OK, last.Done, final.OK, workers*perWorker)
	}
}
