// file: core/internal/scheduler/interval_gate_test.go
// version: 0.1.0
// guid: 1ddbde09-3781-4346-ab5e-1bcb38bdbb73
// last-edited: 2026-11-21

package scheduler

import (
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// IntervalWhen (F5-14): the timing trigger's own switch. HEAD's library_scan
// ticked on scheduled.library_scan.enabled while its window eligibility read
// that flag or scan_on_startup, so core keeps the key enabled (a window
// member) and gates the timer with IntervalWhen. A closed gate is the key's
// enabled flag for the timing alone: decide's disabled path, not When's
// gated fire.

// TestIntervalWhen_ClosedAtFirstSightWritesNothing: a closed gate at first
// sight writes no clock and fires nothing; the first poll that finds it open
// seeds, and the timer fires one period after that (HEAD's disabled key).
func TestIntervalWhen_ClosedAtFirstSightWritesNothing(t *testing.T) {
	on := false
	f := newFixture(t, []tasks.Definition{
		gotham(cave, tasks.ScheduleFromConfig(caveKey).IntervalWhen(func() bool { return on })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{caveKey: every(6 * time.Hour)}})
	f.boot(day(1, 12, 0))
	f.poll(day(1, 12, 0))
	f.poll(day(1, 18, 0))
	f.expect()
	if _, ok := f.clockOf(cave); ok {
		t.Fatal("a closed interval gate wrote a clock at first sight")
	}
	on = true
	f.poll(day(1, 19, 0))
	f.poll(day(2, 0, 59))
	f.poll(day(2, 1, 0))
	f.expect(
		"01-01T19:00 seed gotham.cave-scan last_due=01-01T19:00",
		"01-02T01:00 advance gotham.cave-scan last_due=01-02T01:00",
		"01-02T01:00 enqueue gotham.cave-scan schedule",
	)
}

// TestIntervalWhen_CloseAndReopenEdges: closing the gate stamps GateClosedAt
// once and fires nothing however many instants pass; the poll that finds it
// open again is the re-open edge, which fires once (Coalesce) and counts
// nothing as missed. It is not When: no gated fire is recorded.
func TestIntervalWhen_CloseAndReopenEdges(t *testing.T) {
	on := true
	f := newFixture(t, []tasks.Definition{
		gotham(cave, tasks.ScheduleFromConfig(caveKey).IntervalWhen(func() bool { return on })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{caveKey: every(time.Hour)}})
	f.boot(day(1, 12, 0))
	f.poll(day(1, 12, 0))
	f.rec.take()
	on = false
	f.poll(day(1, 13, 0))
	f.poll(day(1, 14, 0))
	f.poll(day(1, 17, 0))
	f.expect("01-01T13:00 advance gotham.cave-scan last_due=01-01T12:00 closed")
	on = true
	f.poll(day(1, 17, 30))
	f.expect(
		"01-01T17:30 advance gotham.cave-scan last_due=01-01T17:30",
		"01-01T17:30 enqueue gotham.cave-scan schedule",
	)
	if n := f.rec.missed[cave]; n != 0 {
		t.Fatalf("missed = %d while the interval gate was closed, want 0", n)
	}
}

// TestIntervalWhen_WindowAndStartUnaffected: with the timer gated off, the
// enabled key is still a window member that the opening lists and the window
// run starts, and an OnStart kind still starts at boot.
func TestIntervalWhen_WindowAndStartUnaffected(t *testing.T) {
	f := newFixture(t, []tasks.Definition{
		gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey).OnStart().IntervalWhen(func() bool { return false })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: bothCfg(6*time.Hour, 19)}, windows: nightly()})
	f.boot(day(1, 12, 0))
	f.expectSome(
		"01-01T12:00 seed gotham.tombstone last_due=01-01T12:00",
		"01-01T12:00 enqueue gotham.tombstone on_start",
	)
	f.finish(tombstone, tasks.StateSucceeded)
	f.poll(day(1, 18, 0))
	f.poll(day(2, 1, 0))
	f.expectSome(
		"01-01T18:00 advance gotham.tombstone last_due=01-01T12:00 closed",
		"01-02T01:00 window nightly [gotham.tombstone]",
	)
	f.windowRun(day(2, 1, 1))
	if rs := runsOf(t, f.st, tombstone); len(rs) != 2 || rs[1].Trigger != tasks.TriggerWindow {
		t.Fatalf("runs %+v, want the on_start run and the window's", rs)
	}
	if got := f.scheduler().Members("nightly"); !slices.Equal(got, []tasks.Kind{tombstone}) {
		t.Fatalf("Members = %v", got)
	}
}
