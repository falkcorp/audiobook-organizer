// file: internal/scheduler/daily_at.go
// version: 1.1.0
// guid: 965c3488-7ec4-4dd5-bad3-4edc9c3c1fb0
// last-edited: 2026-09-13

package scheduler

import (
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// schedLog is the logger for code added after the log-injection guard
// (internal/logger/slog_guard_test.go); new log calls go through it rather
// than calling log/slog directly. Its methods are printf-style
// (fmt.Sprintf(msg, args...)), not slog key/value pairs.
var schedLog = logger.New("scheduler")

// DailyAt scheduling: run a task once a day at a fixed wall-clock time in the
// server's local zone ("00:10"), instead of once per durable interval.
//
// WHY A SECOND TRIGGER. The durable interval clock (interval_clock.go) repeats
// a task N after its last due check, so a 24h task fires 24h after the deploy
// that first seeded it — at whatever time of day that happened to be. The
// activity-log compaction the owner asked for is "at the end of each day",
// which is a clock time, not a period. OperationDef.Schedule cron strings are
// never read (there is no cron library in the module), so this is the only
// path that can put a task at a time of day.
//
// DST. The next occurrence is always built with time.Date in the local
// location, never by adding 24h to the previous one. A local day is 23h on the
// spring-forward date and 25h on the fall-back date, so "+24h" drifts the run
// an hour off its wall-clock time for good after each transition (and on the
// fall-back day lands on the SAME local date, an hour early). time.Date also
// normalises a day-of-month overflow, so Day()+1 is the whole "tomorrow"
// computation, month and year ends included.
//
// A time inside the spring-forward gap (02:30 in America/New_York on that
// date) does not exist; time.Date resolves it to an instant on that date
// (measured: 06:30Z, whose wall clock reads 01:30 EST), so the task still runs
// that day. The run's stamp then reads BEFORE 02:30 on the wall clock, so it is
// nextDailyAt's strictly-after guard that moves the next run to tomorrow
// rather than back onto the same instant. A time inside the fall-back repeat (01:30)
// exists twice; nextDailyAt picks the day by wall clock (see there), so a run
// stamped at the first 01:30 schedules the next one for the NEXT local date
// and cannot fire again an hour later.
//
// PERSISTENCE. The last run is stored in the same durable clock key as
// interval tasks (scheduled_task_last_tick.<name>), with the same meaning: the
// last time the task was due and its TriggerFn was called. A task switched from
// an interval to DailyAt therefore keeps its history, and a restart does not
// reset anything.
//
// MISSED RUNS. If the server was down across the day's time, the first check
// after start (made immediately, not one poll later) sees that an occurrence
// after the stored last run has already passed and fires once. The stamp is
// then "now", so the next due time is the next occurrence of the clock time —
// the schedule re-anchors to the clock rather than replaying every missed day.

// parseDailyAt parses a 24-hour "HH:MM" local time.
func parseDailyAt(s string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("daily-at time %q is not a 24-hour HH:MM value: %w", s, err)
	}
	return t.Hour(), t.Minute(), nil
}

// nextDailyAt returns the first occurrence of hour:minute in loc strictly
// after `after`. See the DST note above: both candidates come from time.Date,
// and "tomorrow" is Day()+1, never a 24h duration.
//
// The day is chosen by comparing WALL-CLOCK hour:minute, not instants. On the
// fall-back date a time in the repeated hour exists twice and Go does not
// specify which one time.Date returns; comparing instants there could pick the
// second copy after a run at the first and fire twice in one night. The wall
// clock says "01:30 has happened today" either way. The strictly-after guard
// covers the remaining repeated-hour corner (a stamp in the second copy of the
// hour, before the target's wall time).
func nextDailyAt(after time.Time, hour, minute int, loc *time.Location) time.Time {
	a := after.In(loc)
	day := a.Day()
	if a.Hour() > hour || (a.Hour() == hour && a.Minute() >= minute) {
		day++ // today's occurrence is at or before `after`: tomorrow
	}
	next := time.Date(a.Year(), a.Month(), day, hour, minute, 0, 0, loc)
	if !next.After(a) {
		next = time.Date(a.Year(), a.Month(), day+1, hour, minute, 0, 0, loc)
	}
	return next
}

// dailyAtDue reports whether an occurrence of hour:minute has come due since
// the task last ran.
func dailyAtDue(lastRun, now time.Time, hour, minute int, loc *time.Location) bool {
	return !now.Before(nextDailyAt(lastRun, hour, minute, loc))
}

// claimDailyAtRun is one due check of a DailyAt task. It returns true when the
// task should run now, and in that case has already stamped the durable clock.
//
// First observation (nothing stored) seeds the clock with now and declines,
// exactly like the interval path: the task first runs at the next occurrence
// of its time, and a deploy never stampedes it at boot. RunOnStart is the
// separate, explicit opt-in for that.
//
// The stamp is written BEFORE TriggerFn is called, as the interval path does,
// so a TriggerFn that declines or fails is retried at the next occurrence,
// not on every poll.
func (ts *TaskScheduler) claimDailyAtRun(name string, hour, minute int, loc *time.Location, now time.Time) bool {
	last, ok := ts.loadIntervalLastTick(name)
	if !ok {
		ts.stampIntervalTick(name, now)
		return false
	}
	if !dailyAtDue(last, now, hour, minute, loc) {
		return false
	}
	ts.stampIntervalTick(name, now)
	return true
}

// runDailyAtCheck performs one due check and runs the task if it is due.
func (ts *TaskScheduler) runDailyAtCheck(name string, hour, minute int, loc *time.Location) {
	if !ts.claimDailyAtRun(name, hour, minute, loc, time.Now()) {
		return
	}
	if op, err := ts.RunTask(name); err != nil {
		schedLog.Warn("Scheduled task failed: taskName=%s err=%v", name, err)
	} else if op != nil {
		schedLog.Info("Scheduled task started operation: taskName=%s op=%s", name, op.ID)
	}
}

// selfScheduled reports whether the task has a timer of its own: a daily
// wall-clock time or a positive interval. A task without one can run only via
// the maintenance window, a manual trigger, or RunOnStart.
func (d *TaskDefinition) selfScheduled() bool {
	if d.DailyAt != "" {
		return true
	}
	return d.GetInterval != nil && d.GetInterval() > 0
}
