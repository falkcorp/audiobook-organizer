// file: internal/scheduler/on_startup_dropped_test.go
// version: 1.0.0
// guid: 5d1e8b73-9a42-4c06-8f37-2b6c0a94e1d8
// last-edited: 2026-11-20

package scheduler

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// setEveryOnStartup sets OnStartup on every ScheduledTaskConfig under
// config.Scheduled and reports how many it set.
func setEveryOnStartup(t *testing.T, v bool) int {
	t.Helper()
	sv := reflect.ValueOf(&config.AppConfig.Scheduled).Elem()
	n := 0
	for i := 0; i < sv.NumField(); i++ {
		if f := sv.Field(i).FieldByName("OnStartup"); f.IsValid() && f.Kind() == reflect.Bool {
			f.SetBool(v)
			n++
		}
	}
	return n
}

// TestRunOnStartIgnoresTheDroppedOnStartupFields: a flipped Config holds every
// scheduled.<task>.on_startup at false, because core's schedule value has no such
// field. No task may read it, so setting them all changes nothing.
func TestRunOnStartIgnoresTheDroppedOnStartupFields(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaultsForTest()

	runs := func() map[string]bool {
		out := map[string]bool{}
		for _, info := range NewTaskScheduler(testDeps()).ListTasks() {
			out[info.Name] = info.RunOnStartup
		}
		return out
	}
	base := runs()
	require.NotEmpty(t, base)
	require.GreaterOrEqual(t, setEveryOnStartup(t, true), 10, "the reflection found too few entries to prove anything")
	assert.Equal(t, base, runs(), "a task's start trigger followed scheduled.<task>.on_startup")
}

// TestLibraryScanRunsOnStartFromTheCarriedKeyOnly: scan_on_startup is the start
// setting core carries (modules.books.scan.on_startup).
func TestLibraryScanRunsOnStartFromTheCarriedKeyOnly(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.ResetToDefaultsForTest()
	task, ok := NewTaskScheduler(testDeps()).GetTask("library_scan")
	require.True(t, ok)

	config.AppConfig.Scheduled.LibraryScan.OnStartup = true
	assert.False(t, task.RunOnStart())
	config.AppConfig.ScanOnStartup = true
	assert.True(t, task.RunOnStart())
}
