// file: internal/plugins/maintenance/whisper_stuck.go
// version: 1.0.0
// guid: c4e81a07-6d3f-4b52-a9e8-2f7d05b3c61a
// last-edited: 2026-10-25

package maintenance

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Shared handling for the two transcription ops when the Whisper worker's
// decoder cannot finish a clip. Both ops talk to the same workers, so both use
// the same limits and the same wording.

// transcribeTimeoutStrikeLimit is how many transcribe_timeout results one file
// (at one FileHash) may produce before it becomes a permanent per-file result
// that BOTH transcribe ops skip. One strike is retried: a decoder stall can be
// transient (a loaded host, a swapped-out model). A second on the same bytes
// means the audio itself sends the decoder into a loop, and retrying it every
// run costs a worker its decoder each time (the worker restarts it, but the
// clip's whole time budget is spent).
const transcribeTimeoutStrikeLimit = 2

// whisperStuckMaxBatches is how many batches in a row may come back "worker
// stuck" before a run stops. Every one of them was deferred with nothing
// written; without a stop a pool that stays wedged would defer the whole
// library one batch at a time and report a clean run.
const whisperStuckMaxBatches = 3

// errWhisperStuck marks a run stopped because the pool kept answering "worker
// stuck": the workers are reachable but wedged, which is for an operator to
// clear (restart them), not for the run to wait out.
var errWhisperStuck = errors.New("whisper workers stuck")

// stuckStreak counts consecutive stuck batches across a run's concurrent
// workers. Any batch that gets an answer resets it.
type stuckStreak struct{ n atomic.Int64 }

func (s *stuckStreak) hit() int64 { return s.n.Add(1) }
func (s *stuckStreak) reset()     { s.n.Store(0) }

// whisperWaitTickInterval is how often a run reports liveness while one whisper
// request is outstanding. A var so tests do not wait a minute.
var whisperWaitTickInterval = time.Minute

// tickWhileWaiting calls tick(elapsed) every interval until the returned stop
// is called, and waits for the ticker goroutine to finish before stop returns,
// so no tick can run after the caller moved on. It exists because a whisper
// request can legitimately outlast the registry watchdog's progress budget
// (a worker's per-clip deadline times a chunk of clips): a run that is
// plainly waiting on a live request must not be killed as stuck itself.
func tickWhileWaiting(interval time.Duration, tick func(elapsed time.Duration)) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	start := time.Now()
	wg.Go(func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				tick(time.Since(start))
			}
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		wg.Wait()
	}
}
