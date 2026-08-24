// file: internal/scheduler/full_sweep.go
// version: 1.1.0
// guid: a0bad6dd-4723-4b3b-8a78-9072a26b4bcd
// last-edited: 2026-08-24

package scheduler

import (
	"time"

	"log/slog"
)

// fullSweepLastRunSetting is the settings key holding the RFC3339 timestamp of
// the last completed FULL library sweep.
//
// It is persisted rather than kept in memory because of a durable property of
// this scheduler: scheduler.Start drives every task from an in-memory
// time.NewTicker with no last-run state, so PROCESS UPTIME IS A SILENT CEILING
// ON EVERY SCHEDULE. A ticker whose period exceeds the uptime never fires, and
// it never fires silently -- it logs the same healthy "Scheduled task interval"
// line as a task that runs fine.
//
// That is the invariant. The measurement that made it concrete, which will go
// stale and should be read as dated evidence rather than a live fact: on
// 2026-08-24 production had recorded 146 service starts in the preceding 30
// days -- mean uptime near 5 hours, longest observed gap ~15h -- so a 168h
// ticker would have fired exactly zero sweeps while looking perfectly
// configured. Even if that cadence changes, uptime remains a lower bound
// nobody guarantees, so the invariant outlives the number.
//
// Keying off a stored timestamp makes the schedule restart-safe: the due-check
// compares wall-clock against durable state, so process lifetime stops
// mattering.
const fullSweepLastRunSetting = "library_scan_full_last_run"

// fullSweepDue reports whether a full sweep should start now.
//
// A ZERO last time means "no sweep has ever been recorded", and that case
// deliberately returns FALSE rather than true. Treating never-run as due would
// make the very first tick after a deploy start an unannounced multi-hour
// force_update re-hash of the whole library -- and per the scan's own hazard
// note a running scan clobbers metadata applied while it is in flight. The
// caller seeds the timestamp instead and the first real sweep lands one full
// period later. Flip this only together with that caller.
func fullSweepDue(last, now time.Time, period time.Duration) bool {
	if last.IsZero() {
		return false
	}
	if period <= 0 {
		return false
	}
	return now.Sub(last) >= period
}

// loadLastFullSweep reads the persisted sweep timestamp.
//
// The THREE outcomes are reported separately, and collapsing any two of them
// reintroduces a silent never-runs bug:
//
//   - (t, true, nil)          a usable timestamp; compare it and decide.
//   - (zero, false, nil)      genuinely never run, or the stored value was
//     garbage. The caller seeds the clock.
//   - (zero, false, err)      the store could not be read. The caller must
//     DECLINE WITHOUT SEEDING.
//
// That last distinction is the whole point. An earlier version returned
// (zero, false) for a store error too, so the caller treated a transient read
// failure as "never run" and wrote a fresh timestamp -- pushing the sweep out
// another full period every time it happened. A GetSetting failure rate around
// 1-in-168 was then enough to stop the weekly sweep from EVER firing, and the
// only log line said "no prior sweep recorded", actively misattributing a store
// error as a first run.
func (ts *TaskScheduler) loadLastFullSweep() (time.Time, bool, error) {
	store := ts.deps.Store()
	if store == nil {
		return time.Time{}, false, nil
	}
	setting, err := store.GetSetting(fullSweepLastRunSetting)
	if err != nil {
		return time.Time{}, false, err
	}
	if setting == nil {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, setting.Value)
	if err != nil {
		// Garbage rather than absent, but re-seeding is still right: there is
		// no timestamp to compare against and leaving it unreadable would
		// wedge the schedule permanently.
		slog.Warn("library_scan_full: stored last-run timestamp is unparseable; re-seeding",
			"value", setting.Value, "err", err)
		return time.Time{}, false, nil
	}
	return t, true, nil
}

// setFullSweepTimestamp persists t as the last sweep time. Callers should use
// seedFullSweepClock or stampFullSweepRun instead: a write failure means
// opposite things to each of them, and one shared warning necessarily gets one
// of the two backwards.
func (ts *TaskScheduler) setFullSweepTimestamp(t time.Time) error {
	store := ts.deps.Store()
	if store == nil {
		return nil
	}
	return store.SetSetting(fullSweepLastRunSetting, t.UTC().Format(time.RFC3339), "string", false)
}

// seedFullSweepClock records the current time WITHOUT running a sweep, on the
// first tick after a deploy when no timestamp exists yet.
//
// A failure here is the more dangerous of the two: loadLastFullSweep keeps
// reporting "not found", so the task re-seeds and declines on every tick and
// the sweep NEVER becomes due. The symptom is a job that never runs, which is
// the exact silence this whole task was designed against -- so it must not
// share the enqueue path's "may repeat" wording.
func (ts *TaskScheduler) seedFullSweepClock(t time.Time) {
	if err := ts.setFullSweepTimestamp(t); err != nil {
		slog.Warn("library_scan_full: failed to SEED the last-run timestamp; the sweep will re-seed and "+
			"decline on every due check, so it can NEVER become due until the settings store accepts a write",
			"err", err)
	}
}

// stampFullSweepRun records a sweep that was just enqueued.
//
// Stamped at ENQUEUE, not at completion: stamping on completion would
// re-trigger the sweep on every due-check for as long as a sweep kept failing,
// turning one broken run into an hourly retry of a multi-hour job. A failure
// here is bounded -- at worst the sweep is enqueued again next check, and
// library.scan's ConcurrencyKey serializes it.
func (ts *TaskScheduler) stampFullSweepRun(t time.Time) {
	if err := ts.setFullSweepTimestamp(t); err != nil {
		slog.Warn("library_scan_full: failed to record the sweep just enqueued; "+
			"it may be enqueued again on the next due check", "err", err)
	}
}
