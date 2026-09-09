// file: internal/scheduler/interval_clock.go
// version: 1.0.0
// guid: 8c3d1f57-92ab-4e60-b1d4-7a5e0c9f2b83
// last-edited: 2026-09-09

package scheduler

import (
	"errors"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The interval clock makes a scheduled task's cadence survive a process
// restart.
//
// WHY THIS EXISTS. Every interval task used to be driven by a bare
// `time.NewTicker(interval)` created at Start(). A ticker measures from process
// start, so its deadline resets to zero on every restart, and the only record
// of a task's last run lived in the in-memory `lastRun` map, which is discarded
// with the process. The consequence is arithmetic, not subtle: a task whose
// interval exceeds the process's mean uptime can NEVER fire.
//
// Measured on production 2026-09-09. `maintenance.cleanup-activity-log` has a
// 24h interval. systemd recorded consecutive service lifetimes of 1h02m and
// 37m on 2026-09-09, and the journal shows the op re-registering 3+ times a day
// through Sep 7-9. In the 30 days to 2026-09-09 the task fired exactly twice
// outside the maintenance window — 2026-08-27 and 2026-08-30, the only two days
// the process stayed up past 24 hours. Meanwhile the activity table grew to
// 13,256,825 rows. Nothing logged an error: a ticker that never ticks is
// indistinguishable from a healthy task that is simply not due yet.
//
// TWO CLOCKS, DELIBERATELY. `lastRun` keeps its existing meaning — the last
// time a tick actually ENQUEUED an operation — because that is the
// operator-facing "Last Run" on the tasks page, and stamping it on declined
// ticks is what once made a task that had not run in months display a
// timestamp minutes old. The durable clock written here is a separate value:
// the last time the task was DUE and its TriggerFn was called, regardless of
// whether that call enqueued anything.
//
// Advancing the durable clock on every due check (not only on a successful
// enqueue) is what reproduces `time.NewTicker` semantics exactly. A task that
// legitimately declines — library_scan skips while a scan is active — is
// re-checked one interval later, not on every poll. Stamping only on success
// would turn a declining task into a once-per-poll retry, which for
// library_scan_full (which declines 167 of every 168 due checks by design)
// means calling its TriggerFn every minute forever.
const intervalLastTickPrefix = "scheduled_task_last_tick."

// intervalPollInterval bounds how often the durable clock is re-examined. The
// due check is a settings read plus a subtraction, so a one-minute poll costs
// 1440 reads/day per task and buys worst-case one-minute lateness on any
// interval. It is the same cadence the maintenance-window checker already uses.
const intervalPollInterval = time.Minute

// intervalLastTickKey is the settings key holding the RFC3339 UTC timestamp of
// the last due check for a task.
func intervalLastTickKey(name string) string { return intervalLastTickPrefix + name }

// loadIntervalLastTick reads a task's durable clock. It returns ok=false when
// no usable timestamp is stored, which the caller must treat as "seed it now",
// never as "due immediately" — RunOnStart is a separate, explicit opt-in and a
// deploy must not stampede every interval task at boot.
//
// A MISSING KEY ARRIVES AS AN ERROR, not as (nil, nil): PebbleStore.GetSetting
// wraps ErrSettingNotFound. Treating every error as a backend failure would
// make first boot re-seed and decline forever — the same never-fires shape this
// file exists to remove — so the two are told apart by type, as full_sweep.go
// already does.
func (ts *TaskScheduler) loadIntervalLastTick(name string) (time.Time, bool) {
	store := ts.deps.Store()
	if store == nil {
		// No settings store (not yet initialised, or a deployment without one).
		// Fall back to the in-memory clock so the task keeps the OLD ticker
		// behaviour rather than never firing. Returning "not stored" here would
		// make the caller seed-and-decline on every poll forever — the exact
		// never-fires shape this file exists to remove, reintroduced through the
		// degraded path.
		return ts.memIntervalTick(name)
	}
	setting, err := store.GetSetting(intervalLastTickKey(name))
	if err != nil && !errors.Is(err, database.ErrSettingNotFound) {
		// A real backend failure. Report "not stored" so the caller seeds and
		// the task keeps a live clock in memory for this process; it is better
		// to behave like the old ticker than to wedge.
		slog.Warn("scheduler: could not read the durable interval clock; falling back to "+
			"process-lifetime scheduling for this task",
			"taskName", name, "err", err)
		return ts.memIntervalTick(name)
	}
	if setting == nil {
		return ts.memIntervalTick(name)
	}
	t, perr := time.Parse(time.RFC3339, setting.Value)
	if perr != nil {
		slog.Warn("scheduler: stored interval clock is unparseable; re-seeding",
			"taskName", name, "value", setting.Value, "err", perr)
		return time.Time{}, false
	}
	return t, true
}

// memIntervalTick reads the in-process copy of a task's clock. It is the
// degraded-mode fallback, and it is exactly as good as the time.NewTicker this
// replaced: correct while the process lives, lost on restart.
func (ts *TaskScheduler) memIntervalTick(name string) (time.Time, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	t, ok := ts.intervalTick[name]
	return t, ok
}

// stampIntervalTick persists t as the task's last due check.
//
// A write failure is NOT self-limiting and must not be silent: loadIntervalLastTick
// keeps reporting "not stored", so the task re-seeds from the in-memory value on
// the next boot and its cadence silently reverts to process-lifetime — the exact
// defect this file removes, reintroduced without a symptom. The in-memory
// fallback keeps the current process scheduling correctly, so this warns rather
// than failing the tick.
func (ts *TaskScheduler) stampIntervalTick(name string, t time.Time) {
	// The in-memory copy is written unconditionally and FIRST, so the schedule
	// keeps working even when the settings store is unavailable.
	ts.mu.Lock()
	if ts.intervalTick == nil {
		ts.intervalTick = make(map[string]time.Time)
	}
	ts.intervalTick[name] = t
	ts.mu.Unlock()

	store := ts.deps.Store()
	if store == nil {
		return
	}
	if err := store.SetSetting(intervalLastTickKey(name), t.UTC().Format(time.RFC3339), "string", false); err != nil {
		slog.Warn("scheduler: failed to persist the interval clock; this task's cadence will "+
			"reset on the next restart and can be starved by frequent restarts",
			"taskName", name, "err", err)
	}
}

// intervalPollFor returns how often to re-examine a task's durable clock: the
// poll cadence, or the interval itself when that is shorter, so a sub-minute
// interval is not silently stretched to a minute.
func intervalPollFor(interval time.Duration) time.Duration {
	if interval > 0 && interval < intervalPollInterval {
		return interval
	}
	return intervalPollInterval
}
