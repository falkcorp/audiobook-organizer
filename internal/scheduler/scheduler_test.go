// file: internal/scheduler/scheduler_test.go
// version: 1.1.0
// guid: 4e8b2f1c-9a3d-4c07-b5e8-6f2a0d7c3b94
// last-edited: 2026-06-16

package scheduler

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
)

// testDeps returns a SchedulerDeps with nil store (safe for unit tests that
// don't touch the database).
func testDeps() SchedulerDeps {
	return SchedulerDeps{
		Store:               func() database.Store { return nil },
		HasDedupEngine:      func() bool { return false },
		HasMetadataFetchSvc: func() bool { return false },
		HasActivitySvc:      func() bool { return false },
		HasBatchPoller:      func() bool { return false },
	}
}

func TestIsInMaintenanceWindowAt(t *testing.T) {
	tests := []struct {
		name    string
		start   int
		end     int
		hour    int
		enabled bool
		want    bool
	}{
		{"disabled", 1, 4, 2, false, false},
		{"in window", 1, 4, 2, true, true},
		{"at start (inclusive)", 1, 4, 1, true, true},
		{"before window", 1, 4, 0, true, false},
		{"at end (exclusive)", 1, 4, 4, true, false},
		{"midnight span in midnight", 23, 2, 0, true, true},
		{"midnight span in 1am", 23, 2, 1, true, true},
		{"midnight span at start", 23, 2, 23, true, true},
		{"midnight span out 2am", 23, 2, 2, true, false},
		{"midnight span out 3am", 23, 2, 3, true, false},
		{"midnight span out noon", 23, 2, 12, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.AppConfig.Maintenance.Enabled = tt.enabled
			config.AppConfig.Maintenance.WindowStart = tt.start
			config.AppConfig.Maintenance.WindowEnd = tt.end
			got := IsInMaintenanceWindowAt(tt.hour)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasRunToday(t *testing.T) {
	ts := &TaskScheduler{lastMaintenanceRun: time.Time{}}
	assert.False(t, ts.hasRunToday(), "zero time should not count as today")

	ts.lastMaintenanceRun = time.Now()
	assert.True(t, ts.hasRunToday(), "current time should count as today")

	ts.lastMaintenanceRun = time.Now().AddDate(0, 0, -1)
	assert.False(t, ts.hasRunToday(), "yesterday should not count as today")
}

func TestGetLastMaintenanceRunDate_Zero(t *testing.T) {
	ts := &TaskScheduler{lastMaintenanceRun: time.Time{}}
	assert.Equal(t, "", ts.GetLastMaintenanceRunDate(), "zero time should return empty string")
}

func TestGetLastMaintenanceRunDate_Set(t *testing.T) {
	fixed := time.Date(2026, 4, 27, 3, 0, 0, 0, time.UTC)
	ts := &TaskScheduler{lastMaintenanceRun: fixed}
	assert.Equal(t, "2026-04-27", ts.GetLastMaintenanceRunDate())
}

func TestIsMaintenanceRunning_NilStore(t *testing.T) {
	ts := &TaskScheduler{deps: testDeps()}
	assert.False(t, ts.IsMaintenanceRunning(), "nil store should return false")
}

func TestNewTaskScheduler_RegistersAllTasks(t *testing.T) {
	ts := NewTaskScheduler(testDeps())
	assert.NotEmpty(t, ts.order, "task order should not be empty after construction")
	assert.NotEmpty(t, ts.maintenanceOrder, "maintenance order should be set after construction")
	// All maintenance-order entries must exist in tasks map
	for _, name := range ts.maintenanceOrder {
		_, ok := ts.tasks[name]
		assert.Truef(t, ok, "maintenance task %q not registered", name)
	}
}

func TestRunTask_UnknownTask(t *testing.T) {
	ts := NewTaskScheduler(testDeps())
	_, err := ts.RunTask("nonexistent_task_xyz")
	assert.Error(t, err, "running unknown task should return an error")
	assert.Contains(t, err.Error(), "unknown task")
}

func TestListTasks_ReturnsAllRegistered(t *testing.T) {
	ts := NewTaskScheduler(testDeps())
	infos := ts.ListTasks()
	assert.Equal(t, len(ts.order), len(infos), "ListTasks should return one entry per registered task")
}

func TestGetTask_KnownTask(t *testing.T) {
	ts := NewTaskScheduler(testDeps())
	def, ok := ts.GetTask("library_scan")
	assert.True(t, ok, "library_scan should be registered")
	assert.Equal(t, "library_scan", def.Name)
	assert.Equal(t, "library", def.Category)
}

func TestGetTask_UnknownTask(t *testing.T) {
	ts := NewTaskScheduler(testDeps())
	_, ok := ts.GetTask("totally_made_up")
	assert.False(t, ok, "unknown task should return not found")
}

func TestIsInMaintenanceWindowAt_FullDayCoverage(t *testing.T) {
	// A window of start=0, end=0: start < end is false, so falls into the
	// midnight-span branch: hour >= 0 || hour < 0 — always true for valid hours.
	config.AppConfig.Maintenance.Enabled = true
	config.AppConfig.Maintenance.WindowStart = 0
	config.AppConfig.Maintenance.WindowEnd = 0
	got := IsInMaintenanceWindowAt(12)
	assert.True(t, got, "0==0 wraps to always-open")
}
