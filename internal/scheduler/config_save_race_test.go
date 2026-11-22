// file: internal/scheduler/config_save_race_test.go
// version: 1.0.0
// guid: 9d3a6e48-1b27-4c85-b0f6-7a2e5c9d1f43
// last-edited: 2026-11-21

package scheduler

import (
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestSchedulerReads_RaceAgainstSettingsSave: a settings save replaces the whole
// global Config (config.Project, or config.Mutate) while the scheduler's
// goroutines ask it whether the maintenance window is open and what each task's
// enabled / interval / run-on-start / in-window answers are. Those readers must
// go through config.Snapshot; a direct config.AppConfig read is a data race with
// the save, which `-race` reports. The save toggles Maintenance.Enabled and the
// fields every task closure reads.
func TestSchedulerReads_RaceAgainstSettingsSave(t *testing.T) {
	prev := config.Snapshot()
	t.Cleanup(func() { config.Project(prev) })

	ts := NewTaskScheduler(testDeps())

	const (
		saves   = 300
		readers = 12
	)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := range readers {
		wg.Go(func() {
			for hour := r; ; hour++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = IsInMaintenanceWindowAt(hour % 24)
				// ListTasks runs every task's IsEnabled, GetInterval,
				// RunOnStart and RunInMaintenanceWindow closure.
				_ = ts.ListTasks()
			}
		})
	}

	for i := range saves {
		next := prev
		on := i%2 == 0
		next.Maintenance.Enabled = on
		next.Maintenance.WindowStart = i % 24
		next.Maintenance.WindowEnd = (i + 5) % 24
		next.Maintenance.LibraryScan = on
		next.Maintenance.DbOptimize = on
		next.Scheduled.LibraryScan.Enabled = on
		next.Scheduled.LibraryScanFull.Enabled = on
		next.Scheduled.DedupRefresh.Enabled = on
		next.Scheduled.Reconcile.Interval = i % 7
		next.PurgeSoftDeletedAfterDays = i % 3
		next.EnableAIParsing = on
		next.OpenAIAPIKey = map[bool]string{true: "joker-key", false: ""}[on]
		if i%2 == 0 {
			config.Project(next)
		} else {
			config.Mutate(func(c *config.Config) { *c = next })
		}
	}
	close(stop)
	wg.Wait()
}
