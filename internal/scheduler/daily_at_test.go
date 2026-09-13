// file: internal/scheduler/daily_at_test.go
// version: 1.0.0
// guid: 36e12f90-f876-4348-a89b-ae1438620143
// last-edited: 2026-09-13

package scheduler

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	return loc
}

// assertWall pins both the local wall clock and the absolute instant, so a
// result an hour off (the "+24h across DST" defect) fails either way.
func assertWall(t *testing.T, got time.Time, loc *time.Location, y int, mo time.Month, d, h, mi int, wantUTC string) {
	t.Helper()
	g := got.In(loc)
	assert.Equal(t, [5]int{y, int(mo), d, h, mi}, [5]int{g.Year(), int(g.Month()), g.Day(), g.Hour(), g.Minute()},
		"local wall clock; got %s", g.Format(time.RFC3339))
	assert.Equal(t, wantUTC, got.UTC().Format(time.RFC3339), "absolute instant")
}

// 2026 spring-forward in America/New_York: Sunday 2026-03-08, 02:00 EST -> 03:00 EDT.
func TestNextDailyAt_SpringForward(t *testing.T) {
	ny := newYork(t)

	// Ran at 00:10 on the transition day; the next run is 00:10 the day after,
	// which is on the far side of the jump (23h later, not 24h).
	last := time.Date(2026, 3, 8, 0, 10, 30, 0, ny)
	assertWall(t, nextDailyAt(last, 0, 10, ny), ny, 2026, 3, 9, 0, 10, "2026-03-09T04:10:00Z")

	// A time after the jump, computed from the day before it.
	noonBefore := time.Date(2026, 3, 7, 12, 30, 0, 0, ny)
	assertWall(t, nextDailyAt(noonBefore, 12, 0, ny), ny, 2026, 3, 8, 12, 0, "2026-03-08T16:00:00Z")

	// 02:30 does not exist on 2026-03-08. time.Date resolves it to an instant
	// on that date (06:30Z, wall 01:30 EST), so the task still runs that day...
	gap := nextDailyAt(time.Date(2026, 3, 7, 2, 30, 30, 0, ny), 2, 30, ny)
	assert.Equal(t, "2026-03-08T06:30:00Z", gap.UTC().Format(time.RFC3339))
	assert.Equal(t, 8, gap.In(ny).Day())
	// ...exactly once: the stamp's wall clock (01:30) reads BEFORE 02:30, and
	// only the strictly-after guard stops it re-scheduling the same instant.
	assertWall(t, nextDailyAt(gap.Add(20*time.Second), 2, 30, ny), ny, 2026, 3, 9, 2, 30, "2026-03-09T06:30:00Z")
}

// 2026 fall-back in America/New_York: Sunday 2026-11-01, 02:00 EDT -> 01:00 EST.
func TestNextDailyAt_FallBack(t *testing.T) {
	ny := newYork(t)

	// Ran at 00:10 EDT on the transition day; the next run is 00:10 EST on
	// 11-02 (25h later). "+24h" would land on 11-01 23:10 — same date, early.
	last := time.Date(2026, 11, 1, 0, 10, 30, 0, ny)
	assertWall(t, nextDailyAt(last, 0, 10, ny), ny, 2026, 11, 2, 0, 10, "2026-11-02T05:10:00Z")

	// 01:30 happens twice on 11-01. A run at the FIRST 01:30 (EDT, 05:30Z)
	// must not schedule the second one an hour later.
	firstPass := time.Date(2026, 11, 1, 5, 30, 30, 0, time.UTC).In(ny)
	require.Equal(t, 1, firstPass.Hour())
	assertWall(t, nextDailyAt(firstPass, 1, 30, ny), ny, 2026, 11, 2, 1, 30, "2026-11-02T06:30:00Z")

	// Same from a stamp in the SECOND copy of the hour.
	secondPass := time.Date(2026, 11, 1, 6, 30, 30, 0, time.UTC).In(ny)
	require.Equal(t, 1, secondPass.Hour())
	assertWall(t, nextDailyAt(secondPass, 1, 30, ny), ny, 2026, 11, 2, 1, 30, "2026-11-02T06:30:00Z")
}

// TestNextDailyAt_OrdinaryDays covers the non-DST shape: later today when the
// time has not passed, tomorrow when it has or is exactly now, and month/year
// rollover.
func TestNextDailyAt_OrdinaryDays(t *testing.T) {
	ny := newYork(t)
	assertWall(t, nextDailyAt(time.Date(2026, 6, 10, 0, 5, 0, 0, ny), 0, 10, ny), ny, 2026, 6, 10, 0, 10, "2026-06-10T04:10:00Z")
	assertWall(t, nextDailyAt(time.Date(2026, 6, 10, 0, 10, 0, 0, ny), 0, 10, ny), ny, 2026, 6, 11, 0, 10, "2026-06-11T04:10:00Z")
	assertWall(t, nextDailyAt(time.Date(2026, 12, 31, 23, 0, 0, 0, ny), 0, 10, ny), ny, 2027, 1, 1, 0, 10, "2027-01-01T05:10:00Z")
}

// TestDailyAt_MissedRunFiresOnceThenReanchors: the server was down across
// several 00:10s. The first check after start fires once — not once per missed
// day — and the next run is the next 00:10 by the clock, not 24h after the
// catch-up. The clock is read by a second scheduler to include the restart.
func TestDailyAt_MissedRunFiresOnceThenReanchors(t *testing.T) {
	ny := newYork(t)
	settings := map[string]string{}
	const name = "nightly_activity_compaction"

	before := settingsBackedScheduler(t, settings)
	before.stampIntervalTick(name, time.Date(2026, 9, 10, 0, 10, 20, 0, ny))

	ts := settingsBackedScheduler(t, settings) // the restarted process
	bootAt := time.Date(2026, 9, 13, 8, 0, 0, 0, ny)
	assert.True(t, ts.claimDailyAtRun(name, 0, 10, ny, bootAt), "missed runs must fire at startup")
	assert.False(t, ts.claimDailyAtRun(name, 0, 10, ny, bootAt.Add(time.Minute)),
		"three missed days fire ONCE, not three times")
	assert.False(t, ts.claimDailyAtRun(name, 0, 10, ny, time.Date(2026, 9, 14, 0, 9, 0, 0, ny)),
		"not due before the next 00:10")
	assert.True(t, ts.claimDailyAtRun(name, 0, 10, ny, time.Date(2026, 9, 14, 0, 10, 5, 0, ny)),
		"re-anchored to the clock: due at the next 00:10, not 24h after the 08:00 catch-up")
}

// TestDailyAt_FirstObservationSeedsAndDeclines: a new deployment does not run
// the task at boot; it seeds the clock and first runs at the next 00:10.
func TestDailyAt_FirstObservationSeedsAndDeclines(t *testing.T) {
	ny := newYork(t)
	settings := map[string]string{}
	ts := settingsBackedScheduler(t, settings)
	const name = "nightly_activity_compaction"

	boot := time.Date(2026, 9, 13, 15, 0, 0, 0, ny)
	assert.False(t, ts.claimDailyAtRun(name, 0, 10, ny, boot), "first observation must seed, not fire")
	_, stored := settings[intervalLastTickKey(name)]
	assert.True(t, stored, "the seed must persist")
	assert.False(t, ts.claimDailyAtRun(name, 0, 10, ny, time.Date(2026, 9, 13, 23, 59, 0, 0, ny)))
	assert.True(t, ts.claimDailyAtRun(name, 0, 10, ny, time.Date(2026, 9, 14, 0, 10, 0, 0, ny)))
}

// TestIntervalTasksUnaffectedByDailyAt: an interval task is still due purely by
// elapsed time since its last check, whatever the wall clock says, and Start
// still gives it the interval ticker rather than the daily one.
func TestIntervalTasksUnaffectedByDailyAt(t *testing.T) {
	ny := newYork(t)
	ts := settingsBackedScheduler(t, map[string]string{})
	const name = "cleanup_activity_log"

	last := time.Date(2026, 9, 12, 15, 37, 0, 0, ny)
	ts.stampIntervalTick(name, last)
	assert.False(t, ts.claimIntervalRun(name, 24*time.Hour, last.Add(23*time.Hour)),
		"23h elapsed across a 00:10 is not due for a 24h interval")
	assert.True(t, ts.claimIntervalRun(name, 24*time.Hour, last.Add(24*time.Hour)),
		"due at 24h elapsed, at 15:37, not at any clock time")

	out := startScheduler(t, slog.LevelInfo, TaskDefinition{
		Name:        "purge_deleted",
		IsEnabled:   func() bool { return true },
		GetInterval: func() time.Duration { return 6 * time.Hour },
		RunOnStart:  func() bool { return false },
	})
	assert.Contains(t, out, "Scheduled task interval")
	assert.NotContains(t, out, "Scheduled task daily")
}

// TestStart_DailyAtTaskGetsDailyTimerAndNoWarning: a DailyAt task has interval
// 0, which must not trip the "can NEVER run" warning or get an interval ticker.
func TestStart_DailyAtTaskGetsDailyTimerAndNoWarning(t *testing.T) {
	out := startScheduler(t, slog.LevelInfo, TaskDefinition{
		Name:        "nightly_activity_compaction",
		IsEnabled:   func() bool { return true },
		GetInterval: func() time.Duration { return 0 },
		RunOnStart:  func() bool { return false },
		DailyAt:     "00:10",
	})
	assert.Contains(t, out, "Scheduled task daily")
	assert.NotContains(t, out, "Scheduled task interval")
	assert.NotContains(t, out, "level=WARN")
}

// TestStart_UnparseableDailyAtWarns: a typo in DailyAt must be loud.
func TestStart_UnparseableDailyAtWarns(t *testing.T) {
	out := startScheduler(t, slog.LevelInfo, TaskDefinition{
		Name:        "nightly_activity_compaction",
		IsEnabled:   func() bool { return true },
		GetInterval: func() time.Duration { return 0 },
		RunOnStart:  func() bool { return false },
		DailyAt:     "12:10am",
	})
	assert.Contains(t, out, "level=WARN")
	assert.True(t, strings.Contains(out, "nightly_activity_compaction"), out)
}

func TestParseDailyAt(t *testing.T) {
	h, m, err := parseDailyAt("00:10")
	require.NoError(t, err)
	assert.Equal(t, [2]int{0, 10}, [2]int{h, m})
	for _, bad := range []string{"", "24:00", "7pm", "12:60", "00:10:00"} {
		_, _, err := parseDailyAt(bad)
		assert.Error(t, err, "%q must be rejected", bad)
	}
}

// TestRegisteredDailyAtValuesParse keeps a bad literal in tasks.go from
// reaching Start, where it would only log.
func TestRegisteredDailyAtValuesParse(t *testing.T) {
	ts := NewTaskScheduler(reachabilityTestDeps())
	seen := 0
	for name, task := range ts.tasks {
		if task.DailyAt == "" {
			continue
		}
		seen++
		_, _, err := parseDailyAt(task.DailyAt)
		assert.NoError(t, err, "task %s", name)
	}
	require.Positive(t, seen, "no DailyAt task registered — this check would pass on nothing")
}
