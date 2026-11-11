// file: core/tasks/taskstest/schedule/schedule_test.go
// version: 1.0.0
// guid: b511c270-bb41-4218-b39b-a0b0bec4a046
// last-edited: 2026-11-11

package schedule_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest/schedule"
)

// gothamTask is a read-only gotham kind scheduled on key.
func gothamTask(kind tasks.Kind, s *tasks.Schedule) tasks.Definition {
	return tasks.Task(kind, tasks.TaskSpec[struct{}, struct{}]{
		Title: string(kind), Effects: tasks.ReadOnly(), Resume: tasks.ResumeDrop,
		Perms: tasks.Permissions{Start: rbac.Need("gotham:run")}, Schedule: s,
		Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
	})
}

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 1, day, hour, minute, 0, 0, time.UTC)
}

// TestScheduleHarness_RecordsSequence (name coined by F2-04): two kinds, one
// on an interval and one in the nightly window, over a fake day; the
// recorded sequence matches the hand-written golden below.
func TestScheduleHarness_RecordsSequence(t *testing.T) {
	defs := []tasks.Definition{
		gothamTask("gotham.patrol", tasks.ScheduleFromConfig("modules.gotham.watch.schedules.patrol")),
		gothamTask("gotham.batcave-sweep", tasks.ScheduleFromConfig("modules.gotham.cave.schedules.sweep")),
	}
	cfg := schedule.Config{
		Schedules: map[string]tasks.ScheduleConfig{
			"modules.gotham.watch.schedules.patrol": {Enabled: true, Every: config.Duration(6 * time.Hour)},
			"modules.gotham.cave.schedules.sweep":   {Enabled: true, Window: "nightly", Order: 1},
		},
		Windows: map[string]schedule.Window{"nightly": {Start: 1, End: 4, Enabled: true}},
	}
	steps := []schedule.Step{
		{At: at(1, 12, 0)},  // first boot: every clock seeds, nothing fires
		{At: at(1, 17, 59)}, // patrol not due yet
		{At: at(1, 18, 0)},  // patrol due
		{At: at(2, 0, 0)},   // due again, but its run is still queued: folds
		{At: at(2, 1, 0)},   // the window opens: it runs once
		{At: at(2, 2, 30)},  // still open, already run this opening
		{At: at(2, 6, 0), Finish: []tasks.Kind{"gotham.patrol"}},                      // patrol's run finished: a new one
		{At: at(3, 1, 5), Finish: []tasks.Kind{"gotham.patrol", tasks.WindowNightly}}, // the next opening; patrol is 3 periods late
	}
	got := schedule.Strings(schedule.Run(t, defs, cfg, steps))
	want := []string{
		"2026-01-01T12:00:00Z seed gotham.patrol last_due=2026-01-01T12:00:00Z",
		"2026-01-01T12:00:00Z seed core.window.nightly last_due=2026-01-01T12:00:00Z",
		"2026-01-01T18:00:00Z advance gotham.patrol last_due=2026-01-01T18:00:00Z",
		"2026-01-01T18:00:00Z enqueue gotham.patrol trigger=schedule",
		"2026-01-02T00:00:00Z advance gotham.patrol last_due=2026-01-02T00:00:00Z",
		"2026-01-02T00:00:00Z fold gotham.patrol trigger=schedule",
		"2026-01-02T01:00:00Z advance core.window.nightly last_due=2026-01-02T01:00:00Z",
		"2026-01-02T01:00:00Z window core.window.nightly members=[gotham.batcave-sweep]",
		"2026-01-02T01:00:00Z enqueue core.window.nightly trigger=window",
		"2026-01-02T06:00:00Z advance gotham.patrol last_due=2026-01-02T06:00:00Z",
		"2026-01-02T06:00:00Z enqueue gotham.patrol trigger=schedule",
		"2026-01-03T01:05:00Z advance gotham.patrol last_due=2026-01-03T01:05:00Z",
		"2026-01-03T01:05:00Z enqueue gotham.patrol trigger=schedule missed=2",
		"2026-01-03T01:05:00Z advance core.window.nightly last_due=2026-01-03T01:05:00Z",
		"2026-01-03T01:05:00Z window core.window.nightly members=[gotham.batcave-sweep]",
		"2026-01-03T01:05:00Z enqueue core.window.nightly trigger=window",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sequence:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestScheduleHarness_RestartHeldImportEdit: the harness's other Step fields.
// An imported clock continues from its LastDue; an OnStart kind runs at each
// boot; a held kind records nothing; a hot edit applies at the next poll; a
// key missing from the config records nothing.
func TestScheduleHarness_RestartHeldImportEdit(t *testing.T) {
	defs := []tasks.Definition{
		gothamTask("gotham.patrol", tasks.ScheduleFromConfig("modules.gotham.watch.schedules.patrol")),
		gothamTask("gotham.cave-lights", tasks.ScheduleFromConfig("modules.gotham.cave.schedules.lights").OnStart()),
		gothamTask("gotham.ghost", tasks.ScheduleFromConfig("modules.gotham.watch.schedules.ghost")),
	}
	cfg := schedule.Config{Schedules: map[string]tasks.ScheduleConfig{
		"modules.gotham.watch.schedules.patrol": {Enabled: true, Every: config.Duration(time.Hour)},
		"modules.gotham.cave.schedules.lights":  {Enabled: true, Every: config.Duration(24 * time.Hour)},
	}}
	steps := []schedule.Step{
		{At: at(1, 12, 0), NoPoll: true, Held: []tasks.Kind{"gotham.patrol"},
			Clocks: map[tasks.Kind]tasks.Clock{"gotham.patrol": {LastDue: at(1, 11, 30)}}},
		{At: at(1, 12, 30)}, // held: nothing
		{At: at(1, 12, 31), Held: []tasks.Kind{}, Boot: true, Finish: []tasks.Kind{"gotham.cave-lights"}},
		{At: at(1, 12, 40), Edit: func(c *schedule.Config) {
			c.Schedules["modules.gotham.watch.schedules.patrol"] = tasks.ScheduleConfig{Enabled: true, Every: config.Duration(5 * time.Minute)}
		}, Finish: []tasks.Kind{"gotham.patrol"}},
	}
	got := schedule.Strings(schedule.Run(t, defs, cfg, steps))
	want := []string{
		"2026-01-01T12:00:00Z seed gotham.cave-lights last_due=2026-01-01T12:00:00Z",
		"2026-01-01T12:00:00Z enqueue gotham.cave-lights trigger=on_start",
		"2026-01-01T12:31:00Z advance gotham.cave-lights last_due=2026-01-01T12:31:00Z",
		"2026-01-01T12:31:00Z enqueue gotham.cave-lights trigger=on_start",
		"2026-01-01T12:31:00Z advance gotham.patrol last_due=2026-01-01T12:31:00Z",
		"2026-01-01T12:31:00Z enqueue gotham.patrol trigger=schedule",
		"2026-01-01T12:40:00Z advance gotham.patrol last_due=2026-01-01T12:40:00Z",
		"2026-01-01T12:40:00Z enqueue gotham.patrol trigger=schedule",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sequence:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
