// file: core/internal/scheduler/schedule_gates_test.go
// version: 0.1.0
// guid: b22734e3-68a6-4b9a-8916-6975217011dc
// last-edited: 2026-11-21

package scheduler

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// The two trigger gates (F5-14). OnStartWhen: HEAD's library_scan ran at
// boot when scan_on_startup was set, whether its interval was enabled or
// not. WindowWhen: HEAD's RunInMaintenanceWindow toggle on a task with an
// interval took it out of the nightly window and left the interval alone.

const cave tasks.Kind = "gotham.cave-scan"

const caveKey = "modules.gotham.cave.schedules.scan"

// TestOnStartWhen_DisabledKeyFiresWithoutClock: an open start gate fires at
// boot on a disabled key and writes no clock; the first poll that finds the
// key enabled seeds it and does not fire (no boot stampede), and the
// interval fires one period after that.
func TestOnStartWhen_DisabledKeyFiresWithoutClock(t *testing.T) {
	f := newFixture(t, []tasks.Definition{
		gotham(cave, tasks.ScheduleFromConfig(caveKey).OnStartWhen(func() bool { return true })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{caveKey: {Every: every(6 * time.Hour).Every}}})
	f.boot(day(1, 12, 0))
	f.expect("01-01T12:00 enqueue gotham.cave-scan on_start")
	if c, ok := f.clockOf(cave); ok {
		t.Fatalf("a start on a disabled key wrote a clock: %+v", c)
	}
	if rs := runsOf(t, f.st, cave); len(rs) != 1 || rs[0].Trigger != tasks.TriggerOnStart {
		t.Fatalf("runs %+v, want one on_start run", rs)
	}
	f.poll(day(1, 12, 0))
	f.expect() // disabled: the poll writes nothing
	f.finish(cave, tasks.StateSucceeded)
	f.cfg.schedules[caveKey] = every(6 * time.Hour)
	f.poll(day(1, 13, 0))
	f.poll(day(1, 19, 0))
	f.expect(
		"01-01T13:00 seed gotham.cave-scan last_due=01-01T13:00",
		"01-01T19:00 advance gotham.cave-scan last_due=01-01T19:00",
		"01-01T19:00 enqueue gotham.cave-scan schedule",
	)
}

// TestOnStartWhen_ClosedKeepsImportedClock: a closed start gate is no start
// and no clock write, so an imported clock keeps its next due instant.
func TestOnStartWhen_ClosedKeepsImportedClock(t *testing.T) {
	f := newFixture(t, []tasks.Definition{
		gotham(cave, tasks.ScheduleFromConfig(caveKey).OnStartWhen(func() bool { return false })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{caveKey: every(6 * time.Hour)}})
	imported := tasks.Clock{LastDue: day(1, 9, 0)}
	f.plantClock(cave, imported)
	f.boot(day(1, 12, 0))
	f.expect()
	if c, _ := f.clockOf(cave); c != imported {
		t.Fatalf("a closed start gate changed the clock: %+v, want %+v", c, imported)
	}
	f.poll(day(1, 14, 59))
	f.poll(day(1, 15, 0))
	f.expect(
		"01-01T15:00 advance gotham.cave-scan last_due=01-01T15:00",
		"01-01T15:00 enqueue gotham.cave-scan schedule",
	)
}

// TestOnStartWhen_IgnoresWhen: an open start gate fires at boot while the
// When gate is closed; the interval is still gated by When.
func TestOnStartWhen_IgnoresWhen(t *testing.T) {
	f := newFixture(t, []tasks.Definition{
		gotham(cave, tasks.ScheduleFromConfig(caveKey).When(func() bool { return false }).OnStartWhen(func() bool { return true })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{caveKey: every(6 * time.Hour)}})
	f.boot(day(1, 12, 0))
	f.expect(
		"01-01T12:00 seed gotham.cave-scan last_due=01-01T12:00",
		"01-01T12:00 enqueue gotham.cave-scan on_start",
	)
	if c, _ := f.clockOf(cave); !c.LastFire.Equal(day(1, 12, 0)) || !c.LastRun.Equal(day(1, 12, 0)) {
		t.Fatalf("an admitted start did not stamp its run: %+v", c)
	}
	f.finish(cave, tasks.StateSucceeded)
	f.poll(day(1, 18, 0))
	f.expect(
		"01-01T18:00 advance gotham.cave-scan last_due=01-01T18:00",
		"01-01T18:00 gated gotham.cave-scan schedule",
	)
}

// TestOnStartWhen_HeldKindNothing: a held kind gets no start run and no
// clock, its start gate open or not.
func TestOnStartWhen_HeldKindNothing(t *testing.T) {
	f := newFixture(t, []tasks.Definition{
		gotham(cave, tasks.ScheduleFromConfig(caveKey).OnStartWhen(func() bool { return true })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{caveKey: every(6 * time.Hour)}})
	f.hold(cave, true)
	f.boot(day(1, 12, 0))
	f.expect()
	if _, ok := f.clockOf(cave); ok {
		t.Fatal("a held kind's clock was written")
	}
}

// TestWindowWhen_ClosedLeavesInterval: an interval-and-window kind whose
// WindowWhen gate is closed still fires on its interval inside the window
// hours, is absent from the window's opening, and is not started by the
// window run; opening the gate puts it back at the next opening.
func TestWindowWhen_ClosedLeavesInterval(t *testing.T) {
	toggle := false
	f := newFixture(t, []tasks.Definition{
		gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey).WindowWhen(func() bool { return toggle })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: bothCfg(24*time.Hour, 7)}, windows: nightly()})
	f.boot(day(1, 1, 30))
	f.poll(day(1, 1, 30))
	f.rec.take()
	f.poll(day(2, 1, 30))
	f.expectSome(
		"01-02T01:30 advance gotham.tombstone last_due=01-02T01:30",
		"01-02T01:30 enqueue gotham.tombstone schedule",
		"01-02T01:30 window nightly []",
	)
	if rs := runsOf(t, f.st, tombstone); len(rs) != 1 || rs[0].Trigger != tasks.TriggerSchedule {
		t.Fatalf("runs %+v, want the interval's run", rs)
	}
	f.finish(tombstone, tasks.StateSucceeded)
	f.windowRun(day(2, 1, 31))
	if rs := runsOf(t, f.st, tombstone); len(rs) != 1 {
		t.Fatalf("the window started a member whose WindowWhen gate is closed: %+v", rs)
	}
	// Members (the executor's permission check) stays pre-gate.
	if got := f.scheduler().Members("nightly"); !slices.Equal(got, []tasks.Kind{tombstone}) {
		t.Fatalf("Members = %v, want the gated member listed", got)
	}
	toggle = true
	f.rec.take()
	f.poll(day(3, 1, 0))
	f.expectWindow("window nightly [gotham.tombstone]")
}

// TestWhen_ClosesIntervalAndWindow: When, unlike WindowWhen, closes both the
// interval and the window.
func TestWhen_ClosesIntervalAndWindow(t *testing.T) {
	f := newFixture(t, []tasks.Definition{
		gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey).When(func() bool { return false })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: bothCfg(24*time.Hour, 7)}, windows: nightly()})
	f.boot(day(1, 1, 30))
	f.poll(day(1, 1, 30))
	f.rec.take()
	f.poll(day(2, 1, 30))
	f.expectSome(
		"01-02T01:30 gated gotham.tombstone schedule",
		"01-02T01:30 window nightly []",
	)
	if rs := runsOf(t, f.st, tombstone); len(rs) != 0 {
		t.Fatalf("a When-closed kind started %d runs", len(rs))
	}
}

// expectWindow checks the window openings recorded since the last take;
// other events are dropped.
func (f *fixture) expectWindow(want ...string) {
	f.t.Helper()
	var got []string
	for _, e := range f.rec.take() {
		if _, rest, ok := strings.Cut(e, " "); ok && strings.HasPrefix(rest, "window ") {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		f.t.Fatalf("window openings %v, want %v", got, want)
	}
}

// expectSome checks that each of want was recorded since the last take, in
// that order; other events are dropped.
func (f *fixture) expectSome(want ...string) {
	f.t.Helper()
	got := f.rec.take()
	i := 0
	for _, e := range got {
		if i < len(want) && e == want[i] {
			i++
		}
	}
	if i != len(want) {
		f.t.Fatalf("events:\n  %s\nwant, in order:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
