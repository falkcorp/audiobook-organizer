// file: core/tasks/taskstest/schedule/gates_test.go
// version: 0.1.0
// guid: 6045a436-322f-47e3-8878-6bd6250cb46e
// last-edited: 2026-11-21

package schedule_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest/schedule"
)

// TestScheduleHarness_TriggerGates (F5-14): the harness records what the two
// trigger gates do. gotham.cave-scan has a start gate (HEAD's library_scan
// with scan_on_startup) on a key that is off: it runs at boot and writes no
// clock. gotham.tombstone ticks every 24h and sits in the nightly window
// behind a window gate (HEAD's maintenance toggle): while the gate is closed
// the window opens without it and its interval keeps firing; once open, the
// next opening lists it.
func TestScheduleHarness_TriggerGates(t *testing.T) {
	const (
		scanKey = "modules.gotham.cave.schedules.scan"
		tombKey = "modules.gotham.night.schedules.tombstone"
	)
	scanOnStartup, toggle := true, false
	defs := []tasks.Definition{
		gothamTask("gotham.cave-scan", tasks.ScheduleFromConfig(scanKey).OnStartWhen(func() bool { return scanOnStartup })),
		gothamTask("gotham.tombstone", tasks.ScheduleFromConfig(tombKey).WindowWhen(func() bool { return toggle })),
	}
	cfg := schedule.Config{
		Schedules: map[string]tasks.ScheduleConfig{
			scanKey: {Every: config.Duration(6 * time.Hour)}, // interval off
			tombKey: {Enabled: true, Every: config.Duration(24 * time.Hour), Window: "nightly", Order: 7},
		},
		Windows: map[string]schedule.Window{"nightly": {Start: 1, End: 4, Enabled: true}},
	}
	steps := []schedule.Step{
		{At: at(1, 12, 0)}, // boot: the start gate fires on the disabled key; tombstone seeds
		{At: at(2, 1, 0), Finish: []tasks.Kind{"gotham.cave-scan"}}, // window opens without tombstone
		{At: at(2, 12, 0), Finish: []tasks.Kind{tasks.WindowNightly}, Edit: func(*schedule.Config) { toggle = true }},
		{At: at(3, 1, 0), Finish: []tasks.Kind{"gotham.tombstone"}}, // next opening lists it
		{At: at(3, 6, 0), Boot: true, Edit: func(*schedule.Config) { scanOnStartup = false }}, // restart, start gate closed: nothing
	}
	got := schedule.Strings(schedule.Run(t, defs, cfg, steps))
	want := []string{
		"2026-01-01T12:00:00Z enqueue gotham.cave-scan trigger=on_start",
		"2026-01-01T12:00:00Z seed gotham.tombstone last_due=2026-01-01T12:00:00Z",
		"2026-01-01T12:00:00Z seed core.window.nightly last_due=2026-01-01T01:00:00Z",
		"2026-01-02T01:00:00Z advance core.window.nightly last_due=2026-01-02T01:00:00Z",
		"2026-01-02T01:00:00Z window core.window.nightly members=[]",
		"2026-01-02T01:00:00Z enqueue core.window.nightly trigger=window",
		"2026-01-02T12:00:00Z advance gotham.tombstone last_due=2026-01-02T12:00:00Z",
		"2026-01-02T12:00:00Z enqueue gotham.tombstone trigger=schedule",
		"2026-01-03T01:00:00Z advance core.window.nightly last_due=2026-01-03T01:00:00Z",
		"2026-01-03T01:00:00Z window core.window.nightly members=[gotham.tombstone]",
		"2026-01-03T01:00:00Z enqueue core.window.nightly trigger=window",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sequence:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
