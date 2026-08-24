// file: internal/scheduler/full_sweep.go
// version: 1.0.0
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
// It is persisted rather than kept in memory because scheduler.Start drives
// every task from an in-memory time.NewTicker with no durable last-run state.
// A ticker whose period exceeds the process's uptime never fires -- and it
// never fires SILENTLY, logging the same healthy "Scheduled task interval"
// line as a task that runs fine. Production restarted 2026-08-24 07:24 EDT, so
// a 168h ticker would have been reset roughly every deploy and a weekly sweep
// would have been structurally impossible while looking perfectly configured.
//
// Keying off a stored timestamp instead makes the schedule restart-safe: the
// due-check compares wall-clock against durable state, so process lifetime
// stops mattering.
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

// loadLastFullSweep reads the persisted sweep timestamp. The bool reports
// whether a usable timestamp was found; an unreadable or unparseable value is
// reported as absent so the caller re-seeds rather than sweeping on every tick.
func (ts *TaskScheduler) loadLastFullSweep() (time.Time, bool) {
	store := ts.deps.Store()
	if store == nil {
		return time.Time{}, false
	}
	setting, err := store.GetSetting(fullSweepLastRunSetting)
	if err != nil || setting == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, setting.Value)
	if err != nil {
		slog.Warn("library_scan_full: stored last-run timestamp is unparseable; re-seeding",
			"value", setting.Value, "err", err)
		return time.Time{}, false
	}
	return t, true
}

// saveLastFullSweep persists t as the last sweep time.
//
// The caller stamps this at ENQUEUE, not at completion. Stamping on completion
// would re-trigger the sweep on every due-check for as long as a sweep kept
// failing, turning one broken run into an hourly retry of a multi-hour job.
func (ts *TaskScheduler) saveLastFullSweep(t time.Time) {
	store := ts.deps.Store()
	if store == nil {
		return
	}
	if err := store.SetSetting(fullSweepLastRunSetting, t.UTC().Format(time.RFC3339), "string", false); err != nil {
		slog.Warn("library_scan_full: failed to persist last-run timestamp; "+
			"the sweep may repeat on the next due check", "err", err)
	}
}
