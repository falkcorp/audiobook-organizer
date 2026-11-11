// file: core/internal/scheduler/scheduler_test.go
// version: 1.0.1
// guid: 0c0f39f5-0a37-4c1b-a1f0-7d6c5b7b8e52
// last-edited: 2026-11-11

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

const (
	patrol    tasks.Kind = "gotham.patrol"
	signal    tasks.Kind = "gotham.bat-signal"
	sweep     tasks.Kind = "gotham.batcave-sweep"
	patrolKey            = "modules.gotham.watch.schedules.patrol"
	signalKey            = "modules.gotham.roof.schedules.signal"
	sweepKey             = "modules.gotham.cave.schedules.sweep"
)

func day(d, h, m int) time.Time { return time.Date(2026, 1, d, h, m, 0, 0, time.UTC) }

// crashStarter takes a crash clone of the store after a fire's StartIn
// staged its run, before WriteRuns commits the batch that carries it and
// the clock advance: the state a process killed at that instant would
// reopen with.
type crashStarter struct {
	tasks.Stager
	onStaged func()
}

func (c crashStarter) StartIn(ctx context.Context, b *storage.Batch, k tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	ref, err := c.Stager.StartIn(ctx, b, k, req)
	if err == nil && c.onStaged != nil {
		c.onStaged()
	}
	return ref, err
}

// TestSchedule_EnqueueAndClockOneBatch (05-tasks 5T.9): a due fire's clock
// advance and its run's insert are one batch. A crash after both are staged
// and before the commit reopens with neither; after the commit, with both.
func TestSchedule_EnqueueAndClockOneBatch(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(patrol, tasks.ScheduleFromConfig(patrolKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{patrolKey: every(time.Hour)}, crashable: true})
	var crashed *vfs.MemFS
	f.wrap = func(o *Options) {
		o.Starter = crashStarter{Stager: o.Starter, onStaged: func() { crashed = f.fs.CrashClone(vfs.CrashCloneCfg{}) }}
	}
	f.boot(day(1, 12, 0))
	f.poll(day(1, 12, 0))
	f.poll(day(1, 13, 0))
	f.expect(
		"01-01T12:00 seed gotham.patrol last_due=01-01T12:00",
		"01-01T13:00 advance gotham.patrol last_due=01-01T13:00",
		"01-01T13:00 enqueue gotham.patrol schedule",
	)
	if crashed == nil {
		t.Fatal("no crash point: StartIn never staged a run")
	}

	// Killed between staging and commit: neither the advance nor the run.
	mid, _ := openStores(t, crashed)
	c, ok, err := mid.Clocks.Get(context.Background(), patrol)
	if err != nil || !ok || !c.LastDue.Equal(day(1, 12, 0)) || !c.LastFire.IsZero() {
		t.Fatalf("crash before commit: clock %+v %v %v, want the seed alone", c, ok, err)
	}
	if rs := runsOf(t, mid, patrol); len(rs) != 0 {
		t.Fatalf("crash before commit: %d runs, want 0", len(rs))
	}

	// Killed after the commit: both.
	after, _ := openStores(t, f.fs.CrashClone(vfs.CrashCloneCfg{}))
	c, _, err = after.Clocks.Get(context.Background(), patrol)
	if err != nil || !c.LastDue.Equal(day(1, 13, 0)) || !c.LastFire.Equal(day(1, 13, 0)) || !c.LastRun.Equal(day(1, 13, 0)) {
		t.Fatalf("crash after commit: clock %+v %v", c, err)
	}
	rs := runsOf(t, after, patrol)
	if len(rs) != 1 || rs[0].State != tasks.StateQueued || rs[0].Trigger != tasks.TriggerSchedule || rs[0].Mode != tasks.ModeLive {
		t.Fatalf("crash after commit: runs %+v, want one queued live schedule run", rs)
	}

	// The reopened store polls on from where the commit left it: the next
	// instant is not re-fired from the pre-crash clock.
	crashed = nil
	f.poll(day(1, 13, 30))
	f.expect()
	if crashed != nil {
		t.Fatal("a poll before the next instant staged a run")
	}
}

// TestSchedule_NoBootStampede (05-tasks 5T.9): a first boot seeds every
// clock and fires nothing; a restart after downtime fires each kind once,
// however many instants it missed; an OnStart kind runs once at boot and
// not again at the first poll; a clock imported from HEAD with no schedule
// hash continues instead of being re-seeded.
func TestSchedule_NoBootStampede(t *testing.T) {
	const daily, cron, boot tasks.Kind = "gotham.dawn-report", "gotham.quarter-hour", "gotham.cave-lights"
	defs := []tasks.Definition{
		gotham(patrol, tasks.ScheduleFromConfig(patrolKey)),
		gotham(daily, tasks.ScheduleFromConfig("modules.gotham.watch.schedules.dawn")),
		gotham(cron, tasks.ScheduleFromConfig("modules.gotham.watch.schedules.quarter")),
		gotham(boot, tasks.ScheduleFromConfig("modules.gotham.cave.schedules.lights").OnStart()),
	}
	f := newFixture(t, defs, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{
		patrolKey:                                every(time.Hour),
		"modules.gotham.watch.schedules.dawn":    {Enabled: true, DailyAt: "06:00"},
		"modules.gotham.watch.schedules.quarter": {Enabled: true, Cron: "*/15 * * * *"},
		"modules.gotham.cave.schedules.lights":   every(24 * time.Hour),
	}})
	f.boot(day(1, 12, 7))
	f.poll(day(1, 12, 7))
	f.expect(
		"01-01T12:07 seed gotham.cave-lights last_due=01-01T12:07",
		"01-01T12:07 enqueue gotham.cave-lights on_start",
		"01-01T12:07 seed gotham.dawn-report last_due=01-01T12:07",
		"01-01T12:07 seed gotham.patrol last_due=01-01T12:07",
		"01-01T12:07 seed gotham.quarter-hour last_due=01-01T12:07",
	)
	for _, k := range []tasks.Kind{patrol, daily, cron, boot} {
		f.finish(k, tasks.StateSucceeded)
	}

	// Down three days: one run each, the missed instants counted.
	f.boot(day(4, 12, 7))
	f.poll(day(4, 12, 7))
	f.expect(
		"01-04T12:07 advance gotham.cave-lights last_due=01-04T12:07",
		"01-04T12:07 enqueue gotham.cave-lights on_start",
		"01-04T12:07 advance gotham.dawn-report last_due=01-04T12:07",
		"01-04T12:07 enqueue gotham.dawn-report schedule missed=2",
		"01-04T12:07 advance gotham.patrol last_due=01-04T12:07",
		"01-04T12:07 enqueue gotham.patrol schedule missed=71",
		"01-04T12:07 advance gotham.quarter-hour last_due=01-04T12:07",
		"01-04T12:07 enqueue gotham.quarter-hour schedule missed=287",
	)
	for k, want := range map[tasks.Kind]int{patrol: 1, daily: 1, cron: 1, boot: 2} {
		if n := len(runsOf(t, f.st, k)); n != want {
			t.Fatalf("%s: %d runs after the restart, want %d", k, n, want)
		}
	}

	// An imported clock (HEAD's last run, no schedule hash) continues.
	f.finish(patrol, tasks.StateSucceeded)
	f.plantClock(patrol, tasks.Clock{LastDue: day(4, 12, 30), LastRun: day(4, 12, 30)})
	f.poll(day(4, 13, 29))
	f.expectKind(patrol)
	f.poll(day(4, 13, 30))
	f.expectKind(patrol,
		"01-04T13:30 advance gotham.patrol last_due=01-04T13:30",
		"01-04T13:30 enqueue gotham.patrol schedule",
	)
	if c, _ := f.clockOf(patrol); c.ScheduleHash == "" {
		t.Fatal("the fire did not stamp the schedule hash")
	}
}

// TestSchedule_CoalesceMissed (05-tasks 5T.9): Coalesce fires once for any
// number of missed instants and counts them (schedule_missed); SkipMissed
// does not fire for an instant that came due while the process was down (a
// restart after it) and waits for the next on-time one, but does fire for an
// instant that came due while the process was up and its poll was merely
// delayed.
func TestSchedule_CoalesceMissed(t *testing.T) {
	defs := []tasks.Definition{
		gotham(patrol, tasks.ScheduleFromConfig(patrolKey)),
		gotham(signal, tasks.ScheduleFromConfig(signalKey).Missed(tasks.SkipMissed)),
	}
	f := newFixture(t, defs, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{
		patrolKey: every(time.Hour), signalKey: every(time.Hour),
	}})
	f.boot(day(1, 0, 0))
	f.poll(day(1, 0, 0))
	f.rec.take()
	f.boot(day(1, 5, 30)) // down since 00:00: instants 01..05 passed: four missed, one fired
	f.poll(day(1, 5, 30))
	f.expect(
		"01-01T05:30 advance gotham.bat-signal last_due=01-01T05:30",
		"01-01T05:30 advance gotham.patrol last_due=01-01T05:30",
		"01-01T05:30 enqueue gotham.patrol schedule missed=4",
	)
	if got := f.rec.missed[patrol]; got != 4 {
		t.Fatalf("schedule_missed for patrol = %d, want 4", got)
	}
	if got := f.rec.missed[signal]; got != 5 {
		t.Fatalf("schedule_missed for the skipping kind = %d, want 5", got)
	}
	if lags := f.rec.lags[patrol]; len(lags) != 1 || lags[0] != 4*time.Hour+30*time.Minute {
		t.Fatalf("schedule_lag for patrol = %v, want [4h30m] (from the first missed instant)", lags)
	}
	if c, _ := f.clockOf(patrol); c.Missed != 4 || !c.LastFire.Equal(day(1, 1, 0)) {
		t.Fatalf("patrol clock %+v", c)
	}
	f.finish(patrol, tasks.StateSucceeded)
	f.poll(day(1, 6, 29))
	f.expect()
	f.poll(day(1, 6, 30)) // on time for both
	f.expect(
		"01-01T06:30 advance gotham.bat-signal last_due=01-01T06:30",
		"01-01T06:30 enqueue gotham.bat-signal schedule",
		"01-01T06:30 advance gotham.patrol last_due=01-01T06:30",
		"01-01T06:30 enqueue gotham.patrol schedule",
	)

	// A poll that was merely delayed (the process stayed up) is not
	// downtime: 07:30, 08:30 and 09:30 came due while it was up, so the
	// skipping kind fires for 09:30 although that is far older than one poll.
	f.finish(patrol, tasks.StateSucceeded)
	f.finish(signal, tasks.StateSucceeded)
	f.poll(day(1, 7, 0))
	f.rec.take()
	f.poll(day(1, 9, 45))
	f.expect(
		"01-01T09:45 advance gotham.bat-signal last_due=01-01T09:45",
		"01-01T09:45 enqueue gotham.bat-signal schedule missed=2",
		"01-01T09:45 advance gotham.patrol last_due=01-01T09:45",
		"01-01T09:45 enqueue gotham.patrol schedule missed=2",
	)
}

// TestSchedule_DSTSpringForwardFallBack (05-tasks 5T.9): DailyAt and Cron are
// evaluated in core.tasks.timezone. Over a minute-by-minute day in a fake
// zone with US-style rules (GothamZone: UTC-5, UTC-4 from the second Sunday
// of March 02:00 to the first Sunday of November 02:00), a 02:30 schedule
// fires at 03:00 GDT on the spring-forward day, the first instant after the
// skipped 02:30; a 01:30 schedule fires once on the fall-back day, at the
// first 01:30. The scheduler takes its clock as Options.Now, stepped here
// one minute per poll; the synctest bubble makes every other clock on the
// path (the executor's, pebble's) fake as well, so nothing reads wall time.
func TestSchedule_DSTSpringForwardFallBack(t *testing.T) {
	synctest.Test(t, testDSTSpringForwardFallBack)
}

func testDSTSpringForwardFallBack(t *testing.T) {
	const cronKind tasks.Kind = "gotham.cron-patrol"
	loc := GothamZone(t)
	defs := []tasks.Definition{
		gotham(patrol, tasks.ScheduleFromConfig(patrolKey)),
		gotham(cronKind, tasks.ScheduleFromConfig("modules.gotham.watch.schedules.cron")),
	}
	f := newFixture(t, defs, fixtureOpts{zone: loc, schedules: map[string]tasks.ScheduleConfig{
		patrolKey:                             {Enabled: true, DailyAt: "02:30"},
		"modules.gotham.watch.schedules.cron": {Enabled: true, Cron: "30 2 * * *"},
	}})
	utcAt := func(s string) time.Time { return utc(s) }
	sweepDay := func(from, to time.Time) map[tasks.Kind][]time.Time {
		fires := map[tasks.Kind][]time.Time{}
		for now := from; !now.After(to); now = now.Add(time.Minute) {
			f.poll(now)
			for _, e := range f.rec.take() {
				for _, k := range []tasks.Kind{patrol, cronKind} {
					if strings.Contains(e, "enqueue "+string(k)+" ") || strings.Contains(e, "fold "+string(k)+" ") {
						fires[k] = append(fires[k], now)
					}
				}
			}
			for _, k := range []tasks.Kind{patrol, cronKind} {
				f.finish(k, tasks.StateSucceeded)
			}
		}
		return fires
	}
	f.boot(utcAt("2026-03-07T12:00:00Z"))
	f.poll(utcAt("2026-03-07T12:00:00Z")) // seed
	f.rec.take()

	// Spring forward, 2026-03-08: 02:00 EST jumps to 03:00 EDT (07:00Z).
	fires := sweepDay(utcAt("2026-03-08T05:00:00Z"), utcAt("2026-03-08T09:00:00Z"))
	for _, k := range []tasks.Kind{patrol, cronKind} {
		if got := fires[k]; len(got) != 1 || !got[0].Equal(utcAt("2026-03-08T07:00:00Z")) {
			t.Fatalf("spring forward: %s fired at %v, want once at 07:00Z (03:00 GDT)", k, got)
		}
	}
	// The next day is an ordinary 02:30 GDT.
	fires = sweepDay(utcAt("2026-03-09T06:00:00Z"), utcAt("2026-03-09T07:00:00Z"))
	if got := fires[patrol]; len(got) != 1 || !got[0].Equal(utcAt("2026-03-09T06:30:00Z")) {
		t.Fatalf("day after spring forward: %v", got)
	}

	// Fall back, 2026-11-01: 02:00 EDT returns to 01:00 EST (06:00Z); 01:30
	// happens at 05:30Z and again at 06:30Z.
	f.cfg.set(patrolKey, tasks.ScheduleConfig{Enabled: true, DailyAt: "01:30"})
	f.cfg.set("modules.gotham.watch.schedules.cron", tasks.ScheduleConfig{Enabled: true, Cron: "30 1 * * *"})
	f.poll(utcAt("2026-10-31T12:00:00Z")) // the edited schedules catch up once
	f.rec.take()
	f.finish(patrol, tasks.StateSucceeded)
	f.finish(cronKind, tasks.StateSucceeded)
	fires = sweepDay(utcAt("2026-11-01T04:00:00Z"), utcAt("2026-11-01T08:00:00Z"))
	for _, k := range []tasks.Kind{patrol, cronKind} {
		if got := fires[k]; len(got) != 1 || !got[0].Equal(utcAt("2026-11-01T05:30:00Z")) {
			t.Fatalf("fall back: %s fired at %v, want once at 05:30Z (the first 01:30)", k, got)
		}
	}
}

// declaredKeys is a ValidateSchedules oracle over a fixed declaration table.
func declaredKeys(m map[string]struct {
	sched bool
	owner string
}) func(string) (bool, string, bool) {
	return func(key string) (bool, string, bool) {
		d, ok := m[key]
		return d.sched, d.owner, ok
	}
}

type decl = struct {
	sched bool
	owner string
}

// TestScheduleFromConfig_UndeclaredKeyRefused (05-tasks 5T.9): a key no owner
// declared fails registration; at run time a key the config cannot serve
// skips the kind (schedule_errors) and never fires it.
func TestScheduleFromConfig_UndeclaredKeyRefused(t *testing.T) {
	defs := []tasks.Definition{gotham(patrol, tasks.ScheduleFromConfig(patrolKey))}
	err := ValidateSchedules(defs, declaredKeys(map[string]decl{}))
	if err == nil || !strings.Contains(err.Error(), "is not declared") || !strings.Contains(err.Error(), string(patrol)) {
		t.Fatalf("ValidateSchedules = %v, want an undeclared-key refusal", err)
	}
	f := newFixture(t, defs, fixtureOpts{})
	f.boot(day(1, 0, 0))
	f.poll(day(1, 0, 0))
	f.poll(day(2, 0, 0))
	f.expect()
	if n := f.rec.errCount(patrol); n != 2 {
		t.Fatalf("schedule_errors = %d, want 2 (one per poll)", n)
	}
	if _, ok := f.clockOf(patrol); ok {
		t.Fatal("an undeclared key seeded a clock")
	}
}

// TestScheduleFromConfig_ForeignNamespaceRefused (05-tasks 5T.9): every key
// outside the registering owner's namespace, malformed, of the wrong type,
// declared by another owner or shared by two kinds is refused, each with a
// reason.
func TestScheduleFromConfig_ForeignNamespaceRefused(t *testing.T) {
	all := declaredKeys(map[string]decl{
		"modules.gotham.watch.schedules.patrol":  {true, "gotham"},
		"modules.arkham.cells.schedules.lockup":  {true, "arkham"},
		"core.backup.schedules.create":           {true, "core"},
		"modules.gotham.watch.schedules.timeout": {false, "gotham"},
		"modules.gotham.watch.schedules.stolen":  {true, "arkham"},
		"core.backup.schedules.verify":           {true, "gotham"},
	})
	cases := []struct {
		name string
		def  tasks.Definition
		want string
	}{
		{"core kind on a module key", gotham("core.backup-create", tasks.ScheduleFromConfig("modules.gotham.watch.schedules.patrol")), "belongs to module gotham, not to the registering owner core"},
		{"module kind on another module's key", gotham(patrol, tasks.ScheduleFromConfig("modules.arkham.cells.schedules.lockup")), "belongs to module arkham, not to the registering owner module gotham"},
		{"module kind on a core key", gotham(patrol, tasks.ScheduleFromConfig("core.backup.schedules.create")), "is in core's namespace"},
		{"no .schedules. segment", gotham(patrol, tasks.ScheduleFromConfig("modules.gotham.watch.timers.patrol")), "want core.<subsystem>.schedules.<name>"},
		{"schedules as the subsystem", gotham(patrol, tasks.ScheduleFromConfig("modules.gotham.schedules.schedules.patrol")), "want core.<subsystem>.schedules.<name>"},
		{"too short", gotham(patrol, tasks.ScheduleFromConfig("modules.gotham.schedules.patrol")), "want core.<subsystem>.schedules.<name>"},
		{"hyphenated segment", gotham(patrol, tasks.ScheduleFromConfig("modules.gotham.watch.schedules.night-patrol")), "does not match [a-z][a-z0-9_]{0,47}"},
		{"duration-typed key", gotham(patrol, tasks.ScheduleFromConfig("modules.gotham.watch.schedules.timeout")), "is not of type tasks.ScheduleConfig"},
		{"declared by another module", gotham(patrol, tasks.ScheduleFromConfig("modules.gotham.watch.schedules.stolen")), "is declared by module arkham"},
		{"core key declared by a module", gotham("core.backup-verify", tasks.ScheduleFromConfig("core.backup.schedules.verify")), "is declared by module gotham, not by the registering owner core"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateSchedules([]tasks.Definition{c.def}, all)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ValidateSchedules = %v, want %q", err, c.want)
			}
		})
	}
	t.Run("one key on two kinds", func(t *testing.T) {
		err := ValidateSchedules([]tasks.Definition{
			gotham(signal, tasks.ScheduleFromConfig(patrolKey)),
			gotham(patrol, tasks.ScheduleFromConfig(patrolKey)),
		}, all)
		if err == nil || !strings.Contains(err.Error(), "is used by 2 kinds (gotham.bat-signal, gotham.patrol); one key, one kind") {
			t.Fatalf("ValidateSchedules = %v", err)
		}
	})
	t.Run("every refusal reported, sorted", func(t *testing.T) {
		err := ValidateSchedules([]tasks.Definition{
			gotham(signal, tasks.ScheduleFromConfig("modules.arkham.cells.schedules.lockup")),
			gotham(patrol, tasks.ScheduleFromConfig("core.backup.schedules.create")),
			nil,
		}, all)
		lines := strings.Split(err.Error(), "\n")
		if len(lines) != 3 || !strings.Contains(lines[0], "a nil definition") ||
			!strings.HasPrefix(lines[1], "tasks: gotham.bat-signal:") || !strings.HasPrefix(lines[2], "tasks: gotham.patrol:") {
			t.Fatalf("ValidateSchedules = %q", lines)
		}
	})
}

// TestScheduleFromConfig_OwnerKeyAccepted (05-tasks 5T.9): a core kind on its
// core key and a module kind on its own modules.<id> key register.
func TestScheduleFromConfig_OwnerKeyAccepted(t *testing.T) {
	defs := []tasks.Definition{
		gothamWriter("core.backup-create", tasks.ScheduleFromConfig("core.backup.schedules.create").Live()),
		gothamWriter("books.library-scan-full", tasks.ScheduleFromConfig("modules.books.scheduler.schedules.library_scan_full").Preview()),
		gotham(patrol, tasks.ScheduleFromConfig(patrolKey)),
		gotham("gotham.unscheduled", nil),
	}
	err := ValidateSchedules(defs, declaredKeys(map[string]decl{
		"core.backup.schedules.create":                        {true, "core"},
		"modules.books.scheduler.schedules.library_scan_full": {true, "books"},
		patrolKey: {true, "gotham"},
	}))
	if err != nil {
		t.Fatalf("ValidateSchedules = %v", err)
	}
}

// TestScheduleValidate_ModeRules: a scheduled writer must state Live or
// Preview, never both; a schedule needs a timing.
func TestScheduleValidate_ModeRules(t *testing.T) {
	ok := declaredKeys(map[string]decl{patrolKey: {true, "gotham"}})
	for name, c := range map[string]struct {
		def  tasks.Definition
		want string
	}{
		"writer without a mode": {gothamWriter(patrol, tasks.ScheduleFromConfig(patrolKey)), "must state Live() or Preview()"},
		"both modes":            {gotham(patrol, tasks.ScheduleFromConfig(patrolKey).Live().Preview()), "states both Live() and Preview()"},
		"no timing":             {gotham(patrol, tasks.ScheduleFromConfig("")), "has no timing"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSchedules([]tasks.Definition{c.def}, ok); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ValidateSchedules = %v, want %q", err, c.want)
			}
		})
	}
}

// TestScheduleFromConfig_HotReload (05-tasks 5T.9): the scheduler re-reads
// each key every poll. A new interval applies from the last due instant; a
// disable stamps the gate-close edge and stops firing; a re-enable fires
// once with nothing counted missed; joining a window hands the kind to the
// window, and its own clock is no longer polled.
func TestScheduleFromConfig_HotReload(t *testing.T) {
	defs := []tasks.Definition{gotham(patrol, tasks.ScheduleFromConfig(patrolKey))}
	f := newFixture(t, defs, fixtureOpts{
		schedules: map[string]tasks.ScheduleConfig{patrolKey: every(6 * time.Hour)},
		windows:   map[string]WindowConfig{"nightly": {Start: 1, End: 4, Enabled: true}},
	})
	f.boot(day(1, 12, 0))
	f.poll(day(1, 12, 0))
	f.rec.take()
	f.poll(day(1, 13, 0))
	f.expect()
	f.cfg.set(patrolKey, every(time.Hour))
	f.poll(day(1, 13, 0))
	f.expect(
		"01-01T13:00 advance gotham.patrol last_due=01-01T13:00",
		"01-01T13:00 enqueue gotham.patrol schedule",
	)
	f.finish(patrol, tasks.StateSucceeded)

	f.cfg.set(patrolKey, tasks.ScheduleConfig{Every: every(time.Hour).Every})
	f.poll(day(1, 15, 0))
	f.poll(day(1, 18, 0))
	f.expect("01-01T15:00 advance gotham.patrol last_due=01-01T13:00 closed")

	f.cfg.set(patrolKey, every(time.Hour))
	f.poll(day(1, 20, 0))
	f.expect(
		"01-01T20:00 advance gotham.patrol last_due=01-01T20:00",
		"01-01T20:00 enqueue gotham.patrol schedule",
	)
	if f.rec.missed[patrol] != 0 {
		t.Fatalf("a re-enable counted %d missed", f.rec.missed[patrol])
	}
	f.finish(patrol, tasks.StateSucceeded)

	f.cfg.set(patrolKey, tasks.ScheduleConfig{Enabled: true, Window: "nightly"})
	if got := f.scheduler().Members("nightly"); len(got) != 1 || got[0] != patrol {
		t.Fatalf("Members = %v", got)
	}
	f.poll(day(1, 23, 0))
	f.expect()
	f.poll(day(2, 1, 0))
	f.expect(
		"01-02T01:00 advance core.window.nightly last_due=01-02T01:00",
		"01-02T01:00 window nightly [gotham.patrol]",
		"01-02T01:00 enqueue core.window.nightly window",
	)

	f.cfg.set(patrolKey, tasks.ScheduleConfig{Enabled: true, Window: "weekly"})
	f.poll(day(2, 2, 0))
	f.expect()
	if f.rec.errCount(patrol) != 1 {
		t.Fatalf("a member of an unregistered window: schedule_errors %d, want 1", f.rec.errCount(patrol))
	}
	f.cfg.set(patrolKey, tasks.ScheduleConfig{Enabled: true, Every: every(30 * time.Second).Every})
	f.poll(day(2, 3, 0))
	if f.rec.errCount(patrol) != 2 {
		t.Fatalf("an Every below the poll: schedule_errors %d, want 2", f.rec.errCount(patrol))
	}
}

// TestEvery_RefusedExceptRelay (05-tasks 5T.9): tasks.Every is reserved for
// core.outbox.relay; any other kind is refused, and the relay itself runs
// on its fixed interval with no config key.
func TestEvery_RefusedExceptRelay(t *testing.T) {
	none := declaredKeys(map[string]decl{})
	err := ValidateSchedules([]tasks.Definition{gotham(patrol, tasks.Every(5*time.Minute))}, none)
	if err == nil || !strings.Contains(err.Error(), "tasks.Every is reserved for core.outbox.relay") {
		t.Fatalf("ValidateSchedules = %v", err)
	}
	err = ValidateSchedules([]tasks.Definition{gotham("core.backup-create", tasks.Every(time.Hour))}, none)
	if err == nil || !strings.Contains(err.Error(), "reserved for core.outbox.relay") {
		t.Fatalf("a core kind other than the relay: %v", err)
	}
	relay := gotham(relayKind, tasks.Every(5*time.Second))
	if err := ValidateSchedules([]tasks.Definition{relay}, none); err != nil {
		t.Fatalf("the relay: %v", err)
	}
	f := newFixture(t, []tasks.Definition{relay}, fixtureOpts{})
	f.boot(day(1, 0, 0))
	f.poll(day(1, 0, 0))
	f.poll(day(1, 0, 0).Add(4 * time.Second))
	f.poll(day(1, 0, 0).Add(5 * time.Second))
	f.expect(
		"01-01T00:00 seed core.outbox.relay last_due=01-01T00:00",
		"01-01T00:00 advance core.outbox.relay last_due=01-01T00:00",
		"01-01T00:00 enqueue core.outbox.relay schedule",
	)
}

// TestSchedule_HeldGatedAndFolded: a held kind's clock is not polled at all;
// a closed When gate advances the clock and starts nothing; a fire while a
// run is active folds into it, and a stopping run is not folded into.
func TestSchedule_HeldGatedAndFolded(t *testing.T) {
	open := true
	defs := []tasks.Definition{
		gotham(patrol, tasks.ScheduleFromConfig(patrolKey)),
		gotham(signal, tasks.ScheduleFromConfig(signalKey).When(func() bool { return open })),
	}
	f := newFixture(t, defs, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{
		patrolKey: every(time.Hour), signalKey: every(time.Hour),
	}})
	f.hold(patrol, true)
	f.boot(day(1, 0, 0))
	f.poll(day(1, 0, 0))
	f.expect("01-01T00:00 seed gotham.bat-signal last_due=01-01T00:00")
	if _, ok := f.clockOf(patrol); ok {
		t.Fatal("a held kind's clock was seeded")
	}
	open = false
	f.poll(day(1, 1, 0))
	f.expect(
		"01-01T01:00 advance gotham.bat-signal last_due=01-01T01:00",
		"01-01T01:00 gated gotham.bat-signal schedule",
	)
	if rs := runsOf(t, f.st, signal); len(rs) != 0 {
		t.Fatalf("a gated fire started %d runs", len(rs))
	}
	open = true
	f.hold(patrol, false)
	f.poll(day(1, 2, 0))
	f.poll(day(1, 3, 0))
	f.expect(
		"01-01T02:00 advance gotham.bat-signal last_due=01-01T02:00",
		"01-01T02:00 enqueue gotham.bat-signal schedule",
		"01-01T02:00 seed gotham.patrol last_due=01-01T02:00",
		"01-01T03:00 advance gotham.bat-signal last_due=01-01T03:00",
		"01-01T03:00 fold gotham.bat-signal schedule",
		"01-01T03:00 advance gotham.patrol last_due=01-01T03:00",
		"01-01T03:00 enqueue gotham.patrol schedule",
	)
	f.finish(signal, tasks.StateStopping)
	f.poll(day(1, 4, 0))
	if rs := runsOf(t, f.st, signal); len(rs) != 2 {
		t.Fatalf("a fire beside a stopping run: %d runs, want 2", len(rs))
	}
}

// failingClocks fails every Get.
type failingClocks struct{ tasks.ClockStore }

func (failingClocks) Get(context.Context, tasks.Kind) (tasks.Clock, bool, error) {
	return tasks.Clock{}, false, errors.New("the batcomputer is offline")
}

// refusingStarter refuses every StartIn with err.
type refusingStarter struct {
	tasks.Stager
	err error
}

func (r refusingStarter) StartIn(context.Context, *storage.Batch, tasks.Kind, tasks.StartRequest) (tasks.RunRef, error) {
	return tasks.RunRef{}, r.err
}

// flakyStarter fails StartIn with err until ok is set, then passes through.
type flakyStarter struct {
	tasks.Stager
	err error
	ok  *atomic.Bool
}

func (f flakyStarter) StartIn(ctx context.Context, b *storage.Batch, k tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	if !f.ok.Load() {
		return tasks.RunRef{}, f.err
	}
	return f.Stager.StartIn(ctx, b, k, req)
}

// TestSchedule_ClockReadErrorSkips: a clock read error skips the kind for
// the poll (schedule_errors), never "due now".
func TestSchedule_ClockReadErrorSkips(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(patrol, tasks.ScheduleFromConfig(patrolKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{patrolKey: every(time.Hour)}})
	f.boot(day(1, 0, 0))
	f.poll(day(1, 0, 0))
	f.rec.take()
	f.wrap = func(o *Options) { o.Stores.Clocks = failingClocks{o.Stores.Clocks} }
	f.boot(day(2, 0, 0))
	f.poll(day(2, 0, 0))
	f.expect()
	if n := f.rec.errCount(patrol); n != 1 {
		t.Fatalf("schedule_errors = %d, want 1", n)
	}
	if rs := runsOf(t, f.st, patrol); len(rs) != 0 {
		t.Fatalf("a clock read error started %d runs", len(rs))
	}
}

// TestSchedule_StartFailureAdvancesAlone: a start the executor refuses (a
// permission, mode, priority or plan rule) commits the clock advance alone
// and counts the error, so the kind does not retry on every poll.
func TestSchedule_StartFailureAdvancesAlone(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(patrol, tasks.ScheduleFromConfig(patrolKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{patrolKey: every(time.Hour)}})
	f.wrap = func(o *Options) {
		o.Starter = refusingStarter{Stager: o.Starter, err: fmt.Errorf("%w: riddler", executor.ErrNotPermitted)}
	}
	f.boot(day(1, 0, 0))
	f.poll(day(1, 0, 0))
	f.poll(day(1, 1, 0))
	f.poll(day(1, 1, 30))
	f.expect(
		"01-01T00:00 seed gotham.patrol last_due=01-01T00:00",
		"01-01T01:00 advance gotham.patrol last_due=01-01T01:00",
		"01-01T01:00 failed gotham.patrol schedule",
	)
	if n := f.rec.errCount(patrol); n != 1 {
		t.Fatalf("schedule_errors = %d, want 1", n)
	}
	if c, _ := f.clockOf(patrol); !c.LastFire.IsZero() || !c.LastDue.Equal(day(1, 1, 0)) {
		t.Fatalf("clock after a refused start: %+v", c)
	}
	if len(f.rec.lags[patrol]) != 0 {
		t.Fatal("a refused start recorded schedule_lag")
	}
}

// TestSchedule_RefusalClassification: every refusal the executor can give a
// scheduled start advances the clock alone; the tasks-level mode and effects
// sentinels do too.
func TestSchedule_RefusalClassification(t *testing.T) {
	for _, refusal := range []error{
		executor.ErrUnknownKind, executor.ErrHeld, executor.ErrNotPermitted, executor.ErrPlanRequired,
		executor.ErrInvalidPriority, executor.ErrApprovalRequired, tasks.ErrModeUnset, tasks.ErrOutsideEffects,
	} {
		if !refused(fmt.Errorf("start: %w", refusal)) {
			t.Errorf("%v is not classified as a refusal", refusal)
		}
	}
	for _, transient := range []error{
		errors.New("disk: input/output error"), storage.ErrConflict, executor.ErrShuttingDown,
		context.DeadlineExceeded,
	} {
		if refused(transient) {
			t.Errorf("%v is classified as a refusal; it must retry at the next poll", transient)
		}
	}
}

// TestSchedule_StorageErrorKeepsTheFire: a start that fails for a storage
// reason (the executor's fold read or StartIn's own I/O) aborts the whole
// batch, so the clock stays where it was and the next poll fires; only a
// refusal advances the clock alone.
func TestSchedule_StorageErrorKeepsTheFire(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(patrol, tasks.ScheduleFromConfig(patrolKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{patrolKey: every(time.Hour)}})
	var ok atomic.Bool
	f.wrap = func(o *Options) {
		o.Starter = flakyStarter{Stager: o.Starter, err: errors.New("pebble: input/output error"), ok: &ok}
	}
	f.boot(day(1, 0, 0))
	f.poll(day(1, 0, 0))
	f.rec.take()
	f.poll(day(1, 1, 0)) // due; the start's storage read fails
	f.expect()
	if n := f.rec.errCount(patrol); n != 1 {
		t.Fatalf("schedule_errors = %d, want 1", n)
	}
	if c, _ := f.clockOf(patrol); !c.LastDue.Equal(day(1, 0, 0)) || !c.LastFire.IsZero() {
		t.Fatalf("a storage error advanced the clock: %+v", c)
	}
	if rs := runsOf(t, f.st, patrol); len(rs) != 0 {
		t.Fatalf("%d runs after a failed start", len(rs))
	}
	ok.Store(true)
	f.poll(day(1, 1, 1)) // the storage recovered: the same instant fires
	f.expect(
		"01-01T01:01 advance gotham.patrol last_due=01-01T01:01",
		"01-01T01:01 enqueue gotham.patrol schedule",
	)
	if rs := runsOf(t, f.st, patrol); len(rs) != 1 {
		t.Fatalf("%d runs after the recovery, want 1", len(rs))
	}
}

// TestSchedule_FoldIsTheExecutors: a scheduled start folds into an unfinished
// run of the kind through the executor's Dedupe rule (identical params), not a
// rule of the scheduler's own: a manual run with other params does not
// swallow the scheduled fire.
func TestSchedule_FoldIsTheExecutors(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(patrol, tasks.ScheduleFromConfig(patrolKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{patrolKey: every(time.Hour)}})
	f.boot(day(1, 0, 0))
	f.poll(day(1, 0, 0))
	f.rec.take()
	f.poll(day(1, 1, 0))
	f.poll(day(2, 1, 0)) // the first run is still queued: the executor folds it
	f.expect(
		"01-01T01:00 advance gotham.patrol last_due=01-01T01:00",
		"01-01T01:00 enqueue gotham.patrol schedule",
		"01-02T01:00 advance gotham.patrol last_due=01-02T01:00",
		"01-02T01:00 fold gotham.patrol schedule missed=23",
	)
	if rs := runsOf(t, f.st, patrol); len(rs) != 1 {
		t.Fatalf("%d runs, want 1 (a fold)", len(rs))
	}

	// A queued manual run with other params is not a fold target (the zero
	// DedupePolicy needs identical params), so the scheduled fire adds its
	// own run beside it.
	f.finish(patrol, tasks.StateSucceeded)
	if _, err := f.x.Start(context.Background(), patrol, tasks.StartRequest{
		Mode: tasks.ModeRequestLive, Params: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	f.poll(day(2, 2, 0))
	f.expect(
		"01-02T02:00 advance gotham.patrol last_due=01-02T02:00",
		"01-02T02:00 enqueue gotham.patrol schedule",
	)
	if rs := runsOf(t, f.st, patrol); len(rs) != 3 {
		t.Fatalf("%d runs, want 3 (the finished one, the manual one, the scheduled one)", len(rs))
	}
}

// TestSchedule_BootGatedFireStampsNoRun: an OnStart fire whose When gate is
// closed advances LastDue only; LastFire and LastRun record a run that
// started, so they stay unset.
func TestSchedule_BootGatedFireStampsNoRun(t *testing.T) {
	open := false
	f := newFixture(t, []tasks.Definition{
		gotham(patrol, tasks.ScheduleFromConfig(patrolKey).OnStart().When(func() bool { return open })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{patrolKey: every(time.Hour)}})
	f.boot(day(1, 0, 0))
	f.expect(
		"01-01T00:00 seed gotham.patrol last_due=01-01T00:00",
		"01-01T00:00 gated gotham.patrol on_start",
	)
	if c, _ := f.clockOf(patrol); !c.LastDue.Equal(day(1, 0, 0)) || !c.LastFire.IsZero() || !c.LastRun.IsZero() {
		t.Fatalf("a gated boot fire stamped a run: %+v", c)
	}
	if rs := runsOf(t, f.st, patrol); len(rs) != 0 {
		t.Fatalf("a gated boot fire started %d runs", len(rs))
	}
	open = true
	f.boot(day(1, 0, 30))
	f.expect(
		"01-01T00:30 advance gotham.patrol last_due=01-01T00:30",
		"01-01T00:30 enqueue gotham.patrol on_start",
	)
	if c, _ := f.clockOf(patrol); !c.LastFire.Equal(day(1, 0, 30)) || !c.LastRun.Equal(day(1, 0, 30)) {
		t.Fatalf("an admitted boot fire did not stamp its run: %+v", c)
	}
}

// TestSchedule_BootOnStartRules: OnStart skips a disabled key, a window
// member, a held kind and an invalid value, and a writer's schedule runs in
// the mode it states.
func TestSchedule_BootOnStartRules(t *testing.T) {
	const writer tasks.Kind = "gotham.case-file"
	defs := []tasks.Definition{
		gotham(patrol, tasks.ScheduleFromConfig(patrolKey).OnStart()),
		gotham(signal, tasks.ScheduleFromConfig(signalKey).OnStart()),
		gotham(sweep, tasks.ScheduleFromConfig(sweepKey).OnStart()),
		gothamWriter(writer, tasks.ScheduleFromConfig("modules.gotham.cases.schedules.file").OnStart().Preview()),
		gotham("gotham.bad", tasks.ScheduleFromConfig("modules.gotham.bad.schedules.bad").OnStart()),
	}
	f := newFixture(t, defs, fixtureOpts{
		schedules: map[string]tasks.ScheduleConfig{
			patrolKey:                             {Every: every(time.Hour).Every},
			signalKey:                             every(time.Hour),
			sweepKey:                              {Enabled: true, Window: "nightly"},
			"modules.gotham.cases.schedules.file": every(time.Hour),
			"modules.gotham.bad.schedules.bad":    {Enabled: true, Cron: "x"},
		},
		windows: map[string]WindowConfig{"nightly": {Start: 1, End: 4, Enabled: true}},
	})
	f.hold(signal, true)
	f.boot(day(1, 12, 0))
	f.expect(
		"01-01T12:00 seed gotham.case-file last_due=01-01T12:00",
		"01-01T12:00 enqueue gotham.case-file on_start",
	)
	if rs := runsOf(t, f.st, writer); len(rs) != 1 || rs[0].Mode != tasks.ModePreview || rs[0].Trigger != tasks.TriggerOnStart {
		t.Fatalf("writer runs %+v", rs)
	}
	if f.rec.errCount("gotham.bad") != 1 {
		t.Fatal("an invalid OnStart value was not counted")
	}
}

// TestNew_Refusals: New refuses missing stores, starter or config, a
// duplicate kind and a nil definition, and a Run after Stop.
func TestNew_Refusals(t *testing.T) {
	f := newFixture(t, nil, fixtureOpts{})
	base := f.options()
	def := gotham(patrol, tasks.ScheduleFromConfig(patrolKey))
	for name, mut := range map[string]func(o *Options){
		"no stores":  func(o *Options) { o.Stores = tasks.Stores{} },
		"no starter": func(o *Options) { o.Starter = nil },
		"no config":  func(o *Options) { o.Config = nil },
		"nil def":    func(o *Options) { o.Definitions = []tasks.Definition{nil} },
		"duplicate":  func(o *Options) { o.Definitions = []tasks.Definition{def, def} },
		"no batch":   func(o *Options) { o.NewBatch = nil; o.Stores.Runs = struct{ tasks.RunStore }{o.Stores.Runs} },
	} {
		t.Run(name, func(t *testing.T) {
			o := base
			mut(&o)
			if _, err := New(o); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	o := base
	o.NewBatch, o.Poll, o.Location, o.Now, o.Tick, o.Logger = nil, 0, nil, nil, nil, nil
	s, err := New(o)
	if err != nil || s.opts.Poll != time.Minute || s.opts.Location != time.Local || s.open == nil {
		t.Fatalf("defaults: %v %+v", err, s.opts)
	}
}

// TestRunStop: Run boots, polls on each tick and returns on Stop; a second
// Run, and a Run after Stop, refuse; Stop before Run returns at once.
func TestRunStop(t *testing.T) {
	f := newFixture(t, []tasks.Definition{gotham(patrol, tasks.ScheduleFromConfig(patrolKey))},
		fixtureOpts{schedules: map[string]tasks.ScheduleConfig{patrolKey: every(time.Hour)}})
	ticks := make(chan time.Time)
	stopped := make(chan struct{})
	f.wrap = func(o *Options) {
		o.Tick = func(d time.Duration) (<-chan time.Time, func()) {
			if d != time.Minute {
				t.Errorf("tick period %s, want the poll", d)
			}
			return ticks, func() { close(stopped) }
		}
	}
	f.setNow(day(1, 0, 0))
	s, err := New(f.options())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	ticks <- day(1, 0, 0) // first poll seeds
	f.setNow(day(1, 1, 0))
	ticks <- day(1, 1, 0) // second poll fires
	ticks <- day(1, 1, 0) // a third send proves the second poll returned
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	<-stopped
	if rs := runsOf(t, f.st, patrol); len(rs) != 1 {
		t.Fatalf("%d runs, want 1", len(rs))
	}
	if err := s.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "Run after Stop") {
		t.Fatalf("Run after Stop = %v, want the Run after Stop refusal (not \"called twice\")", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal("second Stop:", err)
	}
	s2, _ := New(f.options())
	if err := s2.Stop(context.Background()); err != nil {
		t.Fatal("Stop before Run:", err)
	}

	// Run returns when its context ends.
	s3, _ := New(f.options())
	ctx, cancel := context.WithCancel(context.Background())
	stopped = make(chan struct{})
	go func() { done <- s3.Run(ctx) }()
	ticks <- day(1, 1, 0)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s3.Run(context.Background()); err == nil {
		t.Fatal("second Run accepted")
	}
}

// TestStop_Timeout: Stop gives up when its context ends before Run returns.
func TestStop_Timeout(t *testing.T) {
	f := newFixture(t, nil, fixtureOpts{})
	entered, block := make(chan struct{}), make(chan struct{})
	f.wrap = func(o *Options) {
		o.Tick = func(time.Duration) (<-chan time.Time, func()) { close(entered); <-block; return nil, func() {} }
	}
	s, _ := New(f.options())
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	<-entered // Run has booted and is inside Tick, so Stop must wait for it
	if err := s.Stop(ctx); err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("Stop = %v", err)
	}
	close(block)
	<-done
}
