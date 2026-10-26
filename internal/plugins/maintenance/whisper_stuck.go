// file: internal/plugins/maintenance/whisper_stuck.go
// version: 1.2.0
// guid: c4e81a07-6d3f-4b52-a9e8-2f7d05b3c61a
// last-edited: 2026-10-26

package maintenance

import (
	"errors"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
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

// transcribeStrikeMinGap is how long after one strike on a file the next one may
// count. Run ids cannot make strikes independent: a worker that stalls for
// twenty minutes can fail the same file in two ops, or in two pages of one op,
// and two strikes from one stall would make the file permanent. A strike counts
// only when the previous one is at least this old, so the second needs a second
// occasion (a different night, a different load), not a second request in the
// same bad hour. A var only so tests can run two "nights" back to back.
var transcribeStrikeMinGap = 12 * time.Hour

// isTimeoutResult reports whether a per-file error is one of the worker's
// strike-type answers about a clip: transcribe_timeout (its decoder ran out the
// clip's deadline) or decoder_crashed (the decode process died holding it).
func isTimeoutResult(errText string) bool {
	return transcribe.IsStrikeError(errText)
}

// dropPoolStrikes removes, from the results of a dispatch that ENDED IN ERROR,
// every strike-type answer the dispatcher did not itself condemn, and returns
// what is left. A pool that is failing (a worker fault, an outage, a stuck
// worker) produced those answers under conditions that say nothing about the
// clips, so they are read as no answer: the clip is deferred, never struck. The
// transport already withholds the faulted responses of one worker (see
// transcribe.WorkerFaultError); this covers the timeouts that came back from
// healthy-looking responses elsewhere in a dispatch that failed anyway. A
// Condemned verdict is the exception: the dispatcher got it by sending the file
// ALONE to several workers, which is the strongest evidence there is.
func dropPoolStrikes[V any](res map[string]V, strike func(V) (isStrike, condemned bool)) map[string]V {
	var out map[string]V
	for id, r := range res {
		if s, c := strike(r); s && !c {
			if out == nil {
				out = maps.Clone(res)
			}
			delete(out, id)
		}
	}
	if out == nil {
		return res
	}
	return out
}

func batchStrike(r transcribe.BatchResult) (bool, bool) {
	return transcribe.IsStrikeError(r.Error), r.Condemned
}

// whisperStuckMaxBatches is how many batches in a row may come back "worker
// stuck" before a run stops. Every one of them was deferred with nothing
// written; without a stop a pool that stays wedged would defer the whole
// library one batch at a time and report a clean run.
const whisperStuckMaxBatches = 3

// errWhisperStuck marks a run stopped because the pool kept answering "worker
// stuck": the workers are reachable but wedged, which is for an operator to
// clear (restart them), not for the run to wait out.
var errWhisperStuck = errors.New("whisper workers stuck")

// whisperUnavailableMaxBatches is how many pages in a row the book op may get no
// answer at all because the pool was unreachable (a recognised network failure)
// or unavailable (transcribe.ErrPoolUnavailable: every endpoint benched, or every
// decoder failing to start, for longer than the dispatcher's pacing allowance)
// before the run stops. Each of those pages already waited: the dispatcher paces
// a benched pool for up to six minutes before giving up. Without a stop, a dead
// pool would defer the whole library one slow page at a time; the per-file op
// stops the same way after fileIntroOutageMaxTries of one batch.
const whisperUnavailableMaxBatches = 5

// errWhisperUnavailable marks a book-op run stopped because the pool stayed
// unreachable or unavailable.
var errWhisperUnavailable = errors.New("whisper endpoints unreachable or unavailable")

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
