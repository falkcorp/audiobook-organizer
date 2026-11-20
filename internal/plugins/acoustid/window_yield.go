// file: internal/plugins/acoustid/window_yield.go
// version: 0.1.0
// guid: 5c0e6b42-2f0b-4f3e-9a67-7d1f3a0c9e18
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
)

// windowTotalBudget is HEAD's Timeout for the whole window backfill. The native
// kind's attempt is capped at tasks.MaxTimeout (24h), so a run that outlives one
// attempt yields before the deadline and the resumed attempts share this budget
// (the run's StartedAt rides every checkpoint).
const windowTotalBudget = 72 * time.Hour

// windowYieldMargin is how close to the attempt's deadline the run stops
// admitting work. It must cover settling what is in flight: a lease a worker
// vanished from comes back after workerapi.LeaseTTL plus a sweep, and a file the
// server lane is cutting finishes in minutes.
const windowYieldMargin = workerapi.LeaseTTL + 15*time.Minute

// windowSettleSafety is how long before the attempt deadline a yielding run stops
// waiting for in-flight work. Settling is bounded because draining to zero is not
// needed for correctness: the cursor is capped below every item a worker holds
// (hub.checkpointMark) and every item skipped, and the writer is fenced when the
// attempt ends. It must be well under windowYieldMargin or the settle window is gone.
const windowSettleSafety = 5 * time.Minute

// windowYieldPoll is how often the watcher re-checks the clock: a remote-only
// run can sit in awaitRemote with no item starting, so nothing else would notice
// the margin. A variable so tests do not wait.
var windowYieldPoll = 30 * time.Second

// windowYield decides when a native run stops admitting work and hands the rest
// to a fresh attempt. Once stopping, items not yet started are skipped (left
// pending, the cursor kept below them), the hub is quiesced so no new lease goes
// out and its waiters are released, and the run settles the leases still out
// before it saves the cursor and returns the continue-in-place sentinel.
type windowYield struct {
	deadline  func() time.Time
	margin    time.Duration
	budget    time.Duration
	now       func() time.Time
	cont      error
	startedAt time.Time
	quiesce   func()
	abandon   func() // stop waiting on leased items (hub.abandon)
	// settleSafety is how long before the attempt deadline the run stops waiting
	// on leases and in-flight waits and yields anyway (see settleAt).
	settleSafety time.Duration

	stop       atomic.Bool
	overBudget atomic.Bool
	// progressed counts items finished this attempt: the executor honours a
	// yield only for an attempt that made progress, so the deadline alone never
	// triggers one before the first item (the run runs on and, at worst, times
	// out as a run that never advanced would). The budget is not gated by it.
	progressed atomic.Int64

	mu      sync.Mutex
	minSkip int // lowest item index skipped because of the yield; -1: none
}

// newWindowYield returns nil (never yields) unless the env can yield, and stamps
// params.Resume.StartedAt when it can so every checkpoint carries the run start.
func newWindowYield(env windowEnv, params *WindowBackfillParams, hub *WorkerHub) *windowYield {
	if env.Deadline == nil || env.ContinueInPlace == nil || env.Checkpoint == nil {
		return nil
	}
	y := &windowYield{deadline: env.Deadline, margin: env.YieldMargin, budget: env.TotalBudget,
		now: env.Now, cont: env.ContinueInPlace, quiesce: hub.quiesce, abandon: hub.abandon, minSkip: -1,
		settleSafety: env.SettleSafety}
	if y.settleSafety <= 0 {
		y.settleSafety = windowSettleSafety
	}
	if y.now == nil {
		y.now = time.Now
	}
	if y.budget <= 0 {
		y.budget = windowTotalBudget
	}
	var started time.Time
	if params.Resume != nil {
		started = params.Resume.StartedAt
	}
	if started.IsZero() {
		started = y.now()
	}
	y.startedAt = started
	// Stamping an empty cursor changes nothing the skip logic reads (tier 0,
	// no AfterFileID), so a first attempt keeps its behaviour.
	c := WindowBackfillCursor{}
	if params.Resume != nil {
		c = *params.Resume
	}
	c.StartedAt = started
	params.Resume = &c
	return y
}

// stopping reports whether the run must stop admitting work, latching the
// decision and quiescing the hub the first time it holds.
func (y *windowYield) stopping() bool {
	if y == nil {
		return false
	}
	if y.stop.Load() {
		return true
	}
	now := y.now()
	over := now.Sub(y.startedAt) >= y.budget
	near := false
	if dl := y.deadline(); !dl.IsZero() && y.progressed.Load() > 0 {
		near = dl.Sub(now) < y.margin
	}
	if !over && !near {
		return false
	}
	if y.stop.CompareAndSwap(false, true) {
		y.overBudget.Store(over)
		y.quiesce()
	}
	return true
}

// stopped is the latched decision, without evaluating the clock.
func (y *windowYield) stopped() bool { return y != nil && y.stop.Load() }

// settleAt is when a yielding run stops waiting on what is in flight; zero when
// there is no deadline to bound it by.
func (y *windowYield) settleAt() time.Time {
	if y == nil {
		return time.Time{}
	}
	dl := y.deadline()
	if dl.IsZero() {
		return time.Time{}
	}
	return dl.Add(-y.settleSafety)
}

// settleExpired reports that a stopped run must stop waiting and yield now.
func (y *windowYield) settleExpired() bool {
	at := y.settleAt()
	return y.stopped() && !at.IsZero() && !y.now().Before(at)
}

// settleWait is how long a wait that re-checks every poll may sleep: the poll,
// cut to the time left before the settle bound once the run is stopping.
func (y *windowYield) settleWait(poll time.Duration) time.Duration {
	at := y.settleAt()
	if !y.stopped() || at.IsZero() {
		return poll
	}
	return min(poll, max(at.Sub(y.now()), time.Millisecond))
}

// completed notes an item finished (written, tombstoned or deferred) this attempt.
func (y *windowYield) completed() {
	if y != nil {
		y.progressed.Add(1)
	}
}

// skip records that item idx was passed over because of the yield. It must be
// called before the item's pool slot returns, so the pool's watermark never
// moves past an item skip has not seen.
func (y *windowYield) skip(idx int) {
	if y == nil {
		return
	}
	y.mu.Lock()
	if y.minSkip < 0 || idx < y.minSkip {
		y.minSkip = idx
	}
	y.mu.Unlock()
}

// cap lowers a watermark below the first skipped item.
func (y *windowYield) cap(mark int) int {
	if y == nil {
		return mark
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.minSkip >= 0 && y.minSkip < mark {
		return y.minSkip
	}
	return mark
}

// cursorMark is how many of the tier's n items are finished for good: below the
// first skipped item and below every item a remote worker touched and did not
// finish (hub.checkpointMark: a requeued handed-back file, say). Called after
// the tier's pool and lease drain returned.
func (y *windowYield) cursorMark(hub *WorkerHub, n int) int {
	return y.cap(hub.checkpointMark(n))
}

// watch re-checks the clock until ctx ends, so a run that is only waiting still
// notices the margin.
func (y *windowYield) watch(ctx context.Context) {
	t := time.NewTicker(windowYieldPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !y.stopping() {
				continue
			}
			// Stopping: the pool's waiters sit on items leased to workers, and only
			// the hub can release them. At the settle bound it stops waiting on the
			// holders, so the attempt yields before its deadline instead of after.
			if y.settleExpired() {
				y.abandon()
				return
			}
		}
	}
}

// errWindowBudget is returned when the run's total budget is used up.
var errWindowBudget = errors.New("window-backfill: total run budget used")

// finish saves the cursor at mark finished items of tier and ends the attempt: the
// continue-in-place sentinel (the next attempt resumes from the cursor), or, with
// the total budget used up, an error that says so (a new run plans what is left).
// A cursor that cannot be saved fails the run: yielding without it would restart
// the tier.
func (y *windowYield) finish(res *windowRunResult, tally *windowTally, params WindowBackfillParams,
	checkpoint func(state any) error, progress func(current, total int, msg string),
	tier int, items []windowItem, done, total, mark int) (*windowRunResult, error) {
	fillWindowResult(res, tally)
	after := ""
	switch {
	case mark > 0:
		after = items[mark-1].FileID
	case params.Resume != nil && params.Resume.Tier == tier:
		after = params.Resume.AfterFileID // nothing new finished here: keep the old cursor
	}
	next := params
	next.Resume = windowCursorAt(params, tier, after, done+mark)
	// Every lease is settled or abandoned and the pool has returned, so the tally
	// is final for this attempt: hand it on, or a write error below the cursor
	// (never retried) would not be seen by the attempt that ends the run.
	next.Resume.Carry = tally.carry()
	if err := checkpoint(next); err != nil {
		return res, fmt.Errorf("window-backfill: save the cursor before yielding: %w", err)
	}
	remaining := len(items) - mark
	if y.overBudget.Load() {
		return res, fmt.Errorf("%w (%s since the run began): %d file(s) of %s not reached; start a new run to continue",
			errWindowBudget, y.budget, remaining, windowTierNames[tier])
	}
	// RunItems counted the items it skipped as handled; the bar goes back to the
	// exact cursor so the next attempt's progress proof is measured from it.
	progress(done+mark, total, fmt.Sprintf("Yielding before the attempt deadline: %s, %d file(s) left; continuing in place",
		windowTierNames[tier], remaining))
	wbLog.Info("yielding before the attempt deadline: tier=%d finished=%d left=%d", tier, mark, remaining)
	return res, y.cont
}
