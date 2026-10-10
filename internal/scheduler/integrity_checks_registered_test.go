// file: internal/scheduler/integrity_checks_registered_test.go
// version: 1.0.0
// guid: 3e8a1c52-7b94-4d06-a1f3-5c2d9e60b718
// last-edited: 2026-10-10

package scheduler

import "testing"

// The two report-only health checks declared a nightly cron on their defs but
// no scheduler task existed, so neither ever ran. They must be registered, in
// the maintenance window, mapped to their def (or isTaskRunning lies), and in
// the window's iteration order (or the window never reaches them).
func TestRegisteredTasks_IncludeIntegrityChecks(t *testing.T) {
	ts := NewTaskScheduler(reachabilityTestDeps())
	want := map[string]string{
		"file_integrity_check":      "maintenance.file-integrity-check",
		"orphan_book_files_cleanup": "maintenance.orphan-book-files-cleanup",
	}
	for name, defID := range want {
		task, ok := ts.tasks[name]
		if !ok {
			t.Errorf("task %q is not registered", name)
			continue
		}
		if task.Category != "maintenance" {
			t.Errorf("%s: category = %q, want maintenance", name, task.Category)
		}
		if !task.IsEnabled() || !task.RunInMaintenanceWindow() {
			t.Errorf("%s: want enabled and in the maintenance window", name)
		}
		if task.RunOnStart() {
			t.Errorf("%s: must not run on start", name)
		}
		if !ts.inMaintenanceOrder(name) {
			t.Errorf("%s: missing from maintenanceOrder, the window would never run it", name)
		}
		if got := TaskV2DefIDs()[name]; got != defID {
			t.Errorf("%s: taskV2DefIDs = %q, want %q", name, got, defID)
		}
	}
}

// TaskV2DefIDs hands out a copy: mutating it must not change the live map.
func TestTaskV2DefIDs_ReturnsACopy(t *testing.T) {
	m := TaskV2DefIDs()
	m["file_integrity_check"] = "tampered"
	if got := TaskV2DefIDs()["file_integrity_check"]; got != "maintenance.file-integrity-check" {
		t.Fatalf("live map was mutated through the accessor: %q", got)
	}
}
