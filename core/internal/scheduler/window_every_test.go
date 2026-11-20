// file: core/internal/scheduler/window_every_test.go
// version: 0.1.0
// guid: 0b7e4d2a-6f13-4c8e-9a51-3d2c8e7f1a64
// last-edited: 2026-11-20

package scheduler

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
)

// A schedule with an interval and a window slot (owner ruling 2026-11-20,
// seams.d/F5-14.md: "Allow both in core"). HEAD's tombstone_cleanup,
// reports_prune, purge_deleted, cleanup_activity_log and ai_journal_prune
// tick on an interval and are also members of maintenanceOrder, and fire on
// either; a fire that finds a run of the task active is skipped (HEAD's
// isTaskRunning), which core's executor fold reproduces.

const tombstone tasks.Kind = "gotham.tombstone"

const tombstoneKey = "modules.gotham.night.schedules.tombstone"

func bothCfg(d time.Duration, order int) tasks.ScheduleConfig {
	return tasks.ScheduleConfig{Enabled: true, Every: config.Duration(d), Window: "nightly", Order: order}
}

func nightly() map[string]WindowConfig {
	return map[string]WindowConfig{"nightly": {Start: 1, End: 4, Enabled: true}}
}

// expectAbout checks the events naming k (a window's member list included)
// recorded since the last expect; other events are dropped.
func (f *fixture) expectAbout(k tasks.Kind, want ...string) {
	f.t.Helper()
	var got []string
	for _, e := range f.rec.take() {
		if strings.Contains(e, string(k)) {
			got = append(got, e)
		}
	}
	if !slices.Equal(got, want) {
		f.t.Fatalf("%s events:\n  %s\nwant:\n  %s", k, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// windowRun runs the window's body for the queued window run of this opening
// at now, through the fake host (members' runs stay queued), then finishes
// the window run.
func (f *fixture) windowRun(now time.Time) *fakeHost {
	f.t.Helper()
	rs := runsOf(f.t, f.st, tasks.WindowNightly)
	if len(rs) == 0 {
		f.t.Fatal("no window run to run")
	}
	f.setNow(now)
	h := &fakeHost{ctx: context.Background(), id: rs[len(rs)-1].ID, trigger: tasks.TriggerWindow, st: f.st}
	if err := f.scheduler().runWindow(h); err != nil {
		f.t.Fatalf("runWindow: %v", err)
	}
	f.finish(tasks.WindowNightly, tasks.StateSucceeded) // the body returned
	return h
}

// TestEffectiveOf_EveryAndWindow: an enabled value with Every and Window is
// both a window member and an interval timing, under a hash of its own; the
// combinations that stay invalid are still refused.
func TestEffectiveOf_EveryAndWindow(t *testing.T) {
	both, err := effectiveOf(bothCfg(time.Hour, 3), time.Minute, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !both.enabled || both.window != "nightly" || both.t != interval(time.Hour) {
		t.Fatalf("effectiveOf(every+window) = %+v", both)
	}
	alone, _ := effectiveOf(every(time.Hour), time.Minute, time.UTC)
	win, _ := effectiveOf(tasks.ScheduleConfig{Enabled: true, Window: "nightly"}, time.Minute, time.UTC)
	if both.hash == alone.hash || both.hash == win.hash {
		t.Fatalf("every+window shares a hash: %s / %s / %s", both.hash, alone.hash, win.hash)
	}
	other, _ := effectiveOf(tasks.ScheduleConfig{Enabled: true, Every: config.Duration(time.Hour), Window: "weekend"}, time.Minute, time.UTC)
	if other.hash == both.hash {
		t.Fatal("the window is not part of an every+window hash")
	}
	if win.t != nil {
		t.Fatalf("a window-only member has a timing: %+v", win)
	}
	if _, err := effectiveOf(bothCfg(30*time.Second, 0), time.Minute, time.UTC); err == nil {
		t.Fatal("every below the poll was accepted beside a window")
	}
}

// TestEveryAndWindow_IntervalOnly: an interval-only kind is no window member
// and fires on its interval inside and outside the window.
func TestEveryAndWindow_IntervalOnly(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: every(24 * time.Hour)}, windows: nightly()})
	f.boot(day(1, 1, 0))
	f.poll(day(1, 1, 0))
	f.poll(day(2, 1, 0))
	if got := f.scheduler().Members("nightly"); len(got) != 0 {
		t.Fatalf("Members = %v, want none", got)
	}
	f.expectAbout(tombstone,
		"01-01T01:00 seed gotham.tombstone last_due=01-01T01:00",
		"01-02T01:00 advance gotham.tombstone last_due=01-02T01:00",
		"01-02T01:00 enqueue gotham.tombstone schedule",
	)
}

// TestEveryAndWindow_WindowOnly: a window-only member's own clock is not
// polled; the window starts it once per opening.
func TestEveryAndWindow_WindowOnly(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: {Enabled: true, Window: "nightly", Order: 7}}, windows: nightly()})
	f.boot(day(1, 12, 0))
	f.poll(day(1, 12, 0))
	f.poll(day(2, 1, 0))
	f.expectAbout(tombstone, "01-02T01:00 window nightly [gotham.tombstone]")
	if _, ok := f.clockOf(tombstone); ok {
		t.Fatal("a window-only member's clock was polled")
	}
	f.windowRun(day(2, 1, 1))
	f.expectAbout(tombstone, "01-02T01:01 enqueue gotham.tombstone window")
	f.finish(tombstone, tasks.StateSucceeded)
	f.poll(day(2, 12, 0))
	f.poll(day(3, 0, 0))
	f.expectAbout(tombstone)
}

// TestEveryAndWindow_BothSameDueMoment: the interval and the window come due
// at the same poll. The interval enqueues; the window's start of the member
// folds into that run, so the due moment starts one run, not two.
func TestEveryAndWindow_BothSameDueMoment(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: bothCfg(24*time.Hour, 7)}, windows: nightly()})
	f.boot(day(1, 1, 0))
	f.poll(day(1, 1, 0))
	f.expectAbout(tombstone, "01-01T01:00 seed gotham.tombstone last_due=01-01T01:00")
	f.poll(day(2, 1, 0))
	f.expectAbout(tombstone,
		"01-02T01:00 advance gotham.tombstone last_due=01-02T01:00",
		"01-02T01:00 enqueue gotham.tombstone schedule",
		"01-02T01:00 window nightly [gotham.tombstone]",
	)
	h := f.windowRun(day(2, 1, 1))
	f.expectAbout(tombstone, "01-02T01:01 fold gotham.tombstone window")
	if len(h.awaited) != 0 {
		t.Fatalf("the window awaited %v; a folded member is skipped", h.awaited)
	}
	if rs := runsOf(t, f.st, tombstone); len(rs) != 1 || rs[0].Trigger != tasks.TriggerSchedule {
		t.Fatalf("runs %+v, want the one scheduled run", rs)
	}
}

// TestEveryAndWindow_BothEitherFires: each trigger fires on its own when the
// other is not due, and an interval fire that comes due while the window's
// run of the member is active folds into it. A member whose interval run has
// already finished runs again in the window, as HEAD's did.
func TestEveryAndWindow_BothEitherFires(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: bothCfg(3*time.Hour, 7)}, windows: nightly()})
	f.boot(day(1, 12, 0))
	f.poll(day(1, 12, 0))
	f.poll(day(2, 0, 0)) // 4 instants due since 12:00: one fire, three missed
	f.finish(tombstone, tasks.StateSucceeded)
	f.poll(day(2, 1, 0)) // the window opens; the interval is not due until 03:00
	f.windowRun(day(2, 1, 1))
	f.poll(day(2, 3, 0)) // due while the window's run is active
	f.expectAbout(tombstone,
		"01-01T12:00 seed gotham.tombstone last_due=01-01T12:00",
		"01-02T00:00 advance gotham.tombstone last_due=01-02T00:00",
		"01-02T00:00 enqueue gotham.tombstone schedule missed=3",
		"01-02T01:00 window nightly [gotham.tombstone]",
		"01-02T01:01 enqueue gotham.tombstone window",
		"01-02T03:00 advance gotham.tombstone last_due=01-02T03:00",
		"01-02T03:00 fold gotham.tombstone schedule",
	)
	rs := runsOf(t, f.st, tombstone)
	if len(rs) != 2 || rs[0].Trigger != tasks.TriggerSchedule || rs[1].Trigger != tasks.TriggerWindow {
		t.Fatalf("runs %+v, want one scheduled and one window run", rs)
	}
}

// TestEveryAndWindow_RestartAcrossBoth: a restart inside the window neither
// re-fires the opening nor re-seeds the interval clock; both continue from
// their stored clocks, and the next opening and the next interval fire.
func TestEveryAndWindow_RestartAcrossBoth(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: bothCfg(3*time.Hour, 7)}, windows: nightly()})
	f.boot(day(1, 23, 0))
	f.poll(day(1, 23, 0))
	f.poll(day(2, 1, 0))
	f.windowRun(day(2, 1, 1))
	f.finish(tombstone, tasks.StateSucceeded)
	f.rec.take()

	f.boot(day(2, 1, 30)) // restart inside the opening
	f.poll(day(2, 1, 30))
	f.poll(day(2, 1, 59))
	f.expect()
	f.poll(day(2, 2, 0)) // 23:00 + 3h: the interval continues from its stored clock
	f.expectAbout(tombstone,
		"01-02T02:00 advance gotham.tombstone last_due=01-02T02:00",
		"01-02T02:00 enqueue gotham.tombstone schedule",
	)
	f.finish(tombstone, tasks.StateSucceeded)
	f.boot(day(2, 4, 30)) // restart outside the window
	f.poll(day(2, 4, 30))
	f.poll(day(2, 5, 0))
	f.finish(tombstone, tasks.StateSucceeded)
	f.poll(day(3, 1, 0))
	f.expectAbout(tombstone,
		"01-02T05:00 advance gotham.tombstone last_due=01-02T05:00",
		"01-02T05:00 enqueue gotham.tombstone schedule",
		"01-03T01:00 advance gotham.tombstone last_due=01-03T01:00",
		"01-03T01:00 enqueue gotham.tombstone schedule missed=5",
		"01-03T01:00 window nightly [gotham.tombstone]",
	)
	if rs := runsOf(t, f.st, tasks.WindowNightly); len(rs) != 2 {
		t.Fatalf("window runs %d, want 2 (one per opening)", len(rs))
	}
}

// TestEveryAndWindow_BootOnStartFiresForMembers: OnStart fires at boot for a
// window member, window-only or with an interval, as HEAD's RunOnStart did
// for temp_file_cleanup (window only) and purge_deleted (interval and
// window).
func TestEveryAndWindow_BootOnStartFiresForMembers(t *testing.T) {
	const tempKey = "modules.gotham.night.schedules.temp"
	const temp tasks.Kind = "gotham.temp"
	f := newFixture(t, []tasks.Definition{
		gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey).OnStart()),
		gotham(temp, tasks.ScheduleFromConfig(tempKey).OnStart()),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{
		tombstoneKey: bothCfg(6*time.Hour, 8),
		tempKey:      {Enabled: true, Window: "nightly", Order: 13},
	}, windows: nightly()})
	f.boot(day(1, 12, 0))
	f.expect(
		"01-01T12:00 seed gotham.temp last_due=01-01T12:00",
		"01-01T12:00 enqueue gotham.temp on_start",
		"01-01T12:00 seed gotham.tombstone last_due=01-01T12:00",
		"01-01T12:00 enqueue gotham.tombstone on_start",
	)
	if got := f.scheduler().Members("nightly"); !slices.Equal(got, []tasks.Kind{tombstone, temp}) {
		t.Fatalf("Members = %v", got)
	}
}

// TestEveryAndWindow_FinishedIntervalRunDoesNotSuppressWindow pins HEAD's
// dedup exactly: the interval fires at the poll the window opens and its run
// finishes before the window reaches the member, so the window starts a
// second run for the same moment. HEAD's window checked only isTaskRunning
// and never read or stamped the interval clock
// (internal/server/scheduler_maintenance_window_op.go); the fold covers an
// active run and nothing more. seams.d/F5-14.md names this reading.
func TestEveryAndWindow_FinishedIntervalRunDoesNotSuppressWindow(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: bothCfg(24*time.Hour, 7)}, windows: nightly()})
	f.boot(day(1, 1, 0))
	f.poll(day(1, 1, 0))
	f.poll(day(2, 1, 0))
	f.finish(tombstone, tasks.StateSucceeded)
	f.windowRun(day(2, 1, 1))
	f.expectAbout(tombstone,
		"01-01T01:00 seed gotham.tombstone last_due=01-01T01:00",
		"01-02T01:00 advance gotham.tombstone last_due=01-02T01:00",
		"01-02T01:00 enqueue gotham.tombstone schedule",
		"01-02T01:00 window nightly [gotham.tombstone]",
		"01-02T01:01 enqueue gotham.tombstone window",
	)
	if c, _ := f.clockOf(tombstone); !c.LastDue.Equal(day(2, 1, 0)) {
		t.Fatalf("the window moved the interval clock: last_due %s", c.LastDue)
	}
	if rs := runsOf(t, f.st, tombstone); len(rs) != 2 {
		t.Fatalf("runs %d, want 2 (HEAD skips only an active run)", len(rs))
	}
}

// TestEveryAndWindow_HotReloadKeepsTheIntervalClock: adding a window slot to
// an interval schedule keeps its last_due (the interval continues; nothing is
// counted missed across the schedule change), and dropping the slot again
// leaves a plain interval kind that is no window member.
func TestEveryAndWindow_HotReloadKeepsTheIntervalClock(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(tombstone, tasks.ScheduleFromConfig(tombstoneKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{tombstoneKey: every(6 * time.Hour)}, windows: nightly()})
	f.boot(day(1, 12, 0))
	f.poll(day(1, 12, 0))
	f.cfg.set(tombstoneKey, bothCfg(6*time.Hour, 7))
	if got := f.scheduler().Members("nightly"); !slices.Equal(got, []tasks.Kind{tombstone}) {
		t.Fatalf("Members = %v after adding the slot", got)
	}
	f.poll(day(1, 17, 0))
	f.poll(day(1, 18, 0))
	f.expectAbout(tombstone,
		"01-01T12:00 seed gotham.tombstone last_due=01-01T12:00",
		"01-01T18:00 advance gotham.tombstone last_due=01-01T18:00",
		"01-01T18:00 enqueue gotham.tombstone schedule",
	)
	if f.rec.missed[tombstone] != 0 {
		t.Fatalf("a schedule change counted %d missed", f.rec.missed[tombstone])
	}
	f.cfg.set(tombstoneKey, every(6*time.Hour))
	if got := f.scheduler().Members("nightly"); len(got) != 0 {
		t.Fatalf("Members = %v after dropping the slot", got)
	}
}
