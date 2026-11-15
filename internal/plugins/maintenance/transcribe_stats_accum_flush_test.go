// file: internal/plugins/maintenance/transcribe_stats_accum_flush_test.go
// version: 1.0.0
// guid: 8e41c7d2-6b09-4a35-9c1f-d27a5e03b918
// last-edited: 2026-11-15

package maintenance

import (
	"sync"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// transcribeOrderSink records every snapshot PutTranscribeStats receives, in
// arrival order. The first Put is held until a second one arrives or holdFor
// elapses, which is what lets a test force the interleaving where a newer
// snapshot reaches the sink while an older one is still in flight. The hold is
// the only timed part: it can end early or late without failing a correct
// flush, and it only decides whether a broken one gets the chance to show.
type transcribeOrderSink struct {
	mu      sync.Mutex
	puts    []database.TranscribeStats
	calls   int
	holdFor time.Duration
	second  chan struct{}
	entered chan struct{}
}

func newTranscribeOrderSink(holdFor time.Duration) *transcribeOrderSink {
	return &transcribeOrderSink{holdFor: holdFor, second: make(chan struct{}), entered: make(chan struct{})}
}

func (s *transcribeOrderSink) PutTranscribeStats(st *database.TranscribeStats) error {
	s.mu.Lock()
	n := s.calls
	s.calls++
	if n == 0 {
		close(s.entered)
	}
	if n == 1 {
		close(s.second)
	}
	s.mu.Unlock()
	if n == 0 {
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

func (s *transcribeOrderSink) snapshot() []database.TranscribeStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.TranscribeStats(nil), s.puts...)
}

// Review finding C5 (stack): a flush that has copied its snapshot and is still
// writing must not be overtaken by a newer flush. The first Put is held until a
// second one shows up (or 100 ms), so without the write mutex the newer snapshot
// is recorded first and the older one last.
func TestTranscribeStatsAccumFlush_NewerSnapshotCannotOvertakeOlder(t *testing.T) {
	sink := newTranscribeOrderSink(100 * time.Millisecond)
	a := newTranscribeStatsAccum(sink, "run-batman", 10, time.Now())

	a.mu.Lock()
	a.stats.Attempted = 1
	a.mu.Unlock()
	var wg sync.WaitGroup
	wg.Go(func() { a.flush(false) })
	<-sink.entered // the first flush has copied Attempted=1 and is inside Put

	a.mu.Lock()
	a.stats.Attempted = 2
	a.mu.Unlock()
	wg.Go(func() { a.flush(true) })
	wg.Wait()

	puts := sink.snapshot()
	if len(puts) != 2 {
		t.Fatalf("expected 2 writes, got %d", len(puts))
	}
	if last := puts[len(puts)-1]; last.Attempted != 2 || !last.Done {
		t.Fatalf("last persisted snapshot = Attempted %d Done %v, want Attempted 2 Done true (the older snapshot overtook the newer)", last.Attempted, last.Done)
	}
}
