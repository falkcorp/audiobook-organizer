// file: core/kernel/core_schedule_defaults.go
// version: 1.0.0
// guid: 3ade25b9-c939-402e-b9cb-3e6919618c11
// last-edited: 2026-11-11

package kernel

import (
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// The default shapes of the schedule keys and windows the core-section list
// declares (core_sections.go). They live in their own file so that every
// mention of tasks.ScheduleConfig in core_sections.go is a key declaration:
// its acceptance check counts them (F2-04).

// scheduleEvery is an interval schedule key's default, {enabled: true,
// every: d}: the shape of every owner-table default except
// core.storage.schedules.compact's and core.log.schedules.journal_prune's.
func scheduleEvery(d time.Duration) tasks.ScheduleConfig {
	return tasks.ScheduleConfig{Enabled: true, Every: dur(d)}
}

// journalPruneOrder is core.log.schedules.journal_prune's position in the
// nightly window: HEAD's log purge ran at position 9 of its maintenance order,
// after the soft-delete purge and before the activity-log cleanup. The
// journal-prune parity golden (testdata/journal_prune_parity.json, written
// from HEAD's own order) pins it.
const journalPruneOrder = 9

// scheduleEveryInWindow is an interval schedule key that is also a member of
// window name at position order: core.log.schedules.journal_prune's (HEAD's
// log purge: a weekly timer and a nightly window run).
func scheduleEveryInWindow(d time.Duration, name string, order int) tasks.ScheduleConfig {
	return tasks.ScheduleConfig{Enabled: true, Every: dur(d), Window: name, Order: order}
}

// scheduleWindow is a window member's default, {enabled: true, window: name}.
func scheduleWindow(name string) tasks.ScheduleConfig {
	return tasks.ScheduleConfig{Enabled: true, Window: name}
}

// nightlyWindow is core.tasks.windows.nightly's default: hours 1 to 4 (HEAD's
// maintenance window, config.go:3003-3005), enabled.
func nightlyWindow() tasksWindow {
	w := tasks.NightlyWindow()
	return tasksWindow{Start: w.Start, End: w.End, Enabled: true}
}
