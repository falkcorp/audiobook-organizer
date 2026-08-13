// file: internal/scheduler/task_reachability_test.go
// version: 1.0.0
// guid: 1d84f2b7-6c03-4e91-a7d5-9b02e6f41c38
// last-edited: 2026-08-13

package scheduler

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// reachabilityTestDeps builds a fully-capable SchedulerDeps.
//
// Every capability probe returns TRUE on purpose. Several IsEnabled closures are
// gated on them (HasActivitySvc, HasDedupEngine, HasMetadataFetchSvc), so
// stubbing them false would report those tasks as disabled and the reachability
// invariant would skip exactly the tasks most likely to be misconfigured. True
// models a fully-provisioned production server, which is the case that matters.
//
// A bare SchedulerDeps{} is not usable here: the probes are nil funcs and
// registerAllTasks' closures dereference them, so the first IsEnabled() call
// panics.
//
// Store returns nil, which is safe — task registration only builds closures, and
// nothing on the reachability path invokes the store.
func reachabilityTestDeps() SchedulerDeps {
	return SchedulerDeps{
		Store:               func() database.Store { return nil },
		HasDedupEngine:      func() bool { return true },
		HasMetadataFetchSvc: func() bool { return true },
		HasActivitySvc:      func() bool { return true },
		HasBatchPoller:      func() bool { return true },
	}
}

// TestEveryEnabledTaskIsReachable is the invariant that closes this bug class.
//
// A registered task can start itself in exactly two ways: an interval ticker, or
// the nightly maintenance window (which iterates MaintenanceOrder() and requires
// RunInMaintenanceWindow). A task that is reported ENABLED while satisfying
// neither is dead — it shows up on the tasks page as on, and does nothing,
// forever, with no error anywhere.
//
// This is not hypothetical and it is not a one-off. Measured on production
// 2026-08-13, of 18 registered tasks:
//
//   - 5 had a working ticker;
//   - 7 were correctly reachable through the maintenance window;
//   - 6 were enabled and could never run.
//
// Four of those six (library_organize, temp_file_cleanup, trash_cleanup,
// archive_sweep) declared RunInMaintenanceWindow but were MISSING from
// maintenanceOrder — the identical dead-config shape already documented in the
// maintenanceOrder comment for library_scan, recurring four more times because
// nothing checked. Three of them are unbounded on-disk leaks: orphaned ffmpeg
// temp files, trashed versions past their 14-day TTL, and soft-deleted books
// past the 30-day retention window.
//
// Adding a task now forces a choice: give it an interval, wire it into the
// window, or mark it disabled. All three are fine. Silently doing none is not.
func TestEveryEnabledTaskIsReachable(t *testing.T) {
	ts := NewTaskScheduler(reachabilityTestDeps())

	if len(ts.tasks) == 0 {
		t.Fatal("no tasks registered — the invariant below would vacuously pass, " +
			"which is the failure mode this guard exists to prevent")
	}

	var dead []string
	for name, task := range ts.tasks {
		if task.IsEnabled == nil || !task.IsEnabled() {
			continue // deliberately off — an explicit, visible choice
		}
		if task.GetInterval != nil && task.GetInterval() > 0 {
			continue // timer-driven
		}
		// Deliberately inMaintenanceOrder, NOT reachableViaMaintenanceWindow.
		//
		// This test asserts WIRING — can the task run if an operator turns it
		// on? RunInMaintenanceWindow usually reads config.AppConfig.Maintenance.*,
		// which is zero-valued under test, so every toggle is false here. Using
		// the full reachability check failed metadata_upgrade,
		// library_size_refresh and library_organize purely because nobody had
		// set a config field in a unit test — an operator choice, not a defect.
		// A structural invariant that fails on configuration gets muted, and a
		// muted invariant protects nothing.
		//
		// The runtime warning in Start() correctly uses the stricter
		// reachableViaMaintenanceWindow, because there "enabled but the toggle
		// is off" is exactly what the operator needs told.
		if ts.inMaintenanceOrder(name) {
			continue // wired into the nightly window; whether it runs is config
		}
		dead = append(dead, name)
	}

	if len(dead) > 0 {
		t.Errorf("%d task(s) are ENABLED but can never run: %v\n"+
			"Each needs exactly one of:\n"+
			"  - a non-zero GetInterval (a ticker), or\n"+
			"  - membership in maintenanceOrder AND RunInMaintenanceWindow() true, or\n"+
			"  - IsEnabled() false, if it is manual/API-only.\n"+
			"Declaring RunInMaintenanceWindow without adding the name to "+
			"maintenanceOrder is dead config: the window op only iterates that list.",
			len(dead), dead)
	}
}

// TestMaintenanceOrderNamesRealTasks catches the other half of the wiring
// mistake. inMaintenanceOrder is a string lookup, so a typo or a renamed task
// leaves an entry that matches nothing and silently drops that task from the
// nightly run — the same invisible failure, arrived at from the opposite side.
func TestMaintenanceOrderNamesRealTasks(t *testing.T) {
	ts := NewTaskScheduler(reachabilityTestDeps())

	if len(ts.maintenanceOrder) == 0 {
		t.Fatal("maintenanceOrder is empty — nothing would be checked below")
	}

	for _, name := range ts.maintenanceOrder {
		if _, ok := ts.tasks[name]; !ok {
			t.Errorf("maintenanceOrder contains %q, which is not a registered task; "+
				"the window op will iterate past it and run nothing", name)
		}
	}
}

// TestTasksMissingFromMaintenanceOrderDoNotClaimTheWindow asserts the specific
// inconsistency directly, so a failure names the wiring error rather than only
// its downstream effect.
//
// A task may legitimately opt out of the window (RunInMaintenanceWindow false).
// What it may not do is return true while absent from the list, because that
// reads as "runs nightly" everywhere it is surfaced and never does.
func TestTasksMissingFromMaintenanceOrderDoNotClaimTheWindow(t *testing.T) {
	ts := NewTaskScheduler(reachabilityTestDeps())

	for name, task := range ts.tasks {
		if task.RunInMaintenanceWindow == nil || !task.RunInMaintenanceWindow() {
			continue
		}
		if !ts.inMaintenanceOrder(name) {
			t.Errorf("task %q returns RunInMaintenanceWindow() true but is absent from "+
				"maintenanceOrder, so the nightly window never reaches it — add it to the "+
				"list in NewTaskScheduler, or return false", name)
		}
	}
}

// TestReachabilityHelpersRequireBothConditions pins the helper itself. Without
// this, reachableViaMaintenanceWindow could degrade to a plain list-membership
// check and TestEveryEnabledTaskIsReachable would keep passing while quietly
// excusing tasks whose toggle is off.
func TestReachabilityHelpersRequireBothConditions(t *testing.T) {
	ts := NewTaskScheduler(reachabilityTestDeps())

	const listedOptOut = "reachability_listed_but_opted_out"
	ts.tasks[listedOptOut] = &TaskDefinition{
		Name:                   listedOptOut,
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunInMaintenanceWindow: func() bool { return false },
	}
	ts.maintenanceOrder = append(ts.maintenanceOrder, listedOptOut)

	if ts.reachableViaMaintenanceWindow(listedOptOut) {
		t.Error("a task in maintenanceOrder whose RunInMaintenanceWindow() is false " +
			"must NOT count as reachable — the window op checks the toggle too")
	}

	const unlistedOptIn = "reachability_opted_in_but_unlisted"
	ts.tasks[unlistedOptIn] = &TaskDefinition{
		Name:                   unlistedOptIn,
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunInMaintenanceWindow: func() bool { return true },
	}

	if ts.reachableViaMaintenanceWindow(unlistedOptIn) {
		t.Error("a task returning RunInMaintenanceWindow() true but absent from " +
			"maintenanceOrder must NOT count as reachable — this is exactly the dead " +
			"config that hid four broken tasks in production")
	}
}
