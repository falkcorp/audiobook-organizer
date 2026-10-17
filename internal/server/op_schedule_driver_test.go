// file: internal/server/op_schedule_driver_test.go
// version: 1.9.0
// guid: c47e92b1-5d08-4a3f-9e61-1b2f8d0a7c34
// last-edited: 2026-10-17

package server

import (
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
)

// scheduleDriverAllowList names the ops that declare a cron Schedule but are
// intentionally NOT driven by any scheduler task (owner decision D24). The
// declared cron is documentation until a task enqueues the op. Each PR that
// wires, merges or retires one of these removes its own row here; the checks
// below fail if a row outlives its reason.
var scheduleDriverAllowList = map[string]string{
	"deluge.protected-paths-sync": "protected list loads at boot and on Deluge changes; a half-hourly op row adds noise (revisit with D52)",
}

// TestScheduledOpsHaveADriver fails any op that declares a cron Schedule with
// neither a scheduler task that enqueues it nor an explicit allow-list row. Two
// report-only checks declared a nightly cron for weeks and never ran because
// nothing drove them; nothing noticed.
func TestScheduledOpsHaveADriver(t *testing.T) {
	srv, registered, aliases := bootRegisteredOpIDs(t)

	driven := map[string]bool{}
	for _, defID := range scheduler.TaskV2DefIDs() {
		driven[defID] = true
	}

	scheduled := map[string]bool{}
	var undriven []string
	for _, d := range srv.opRegistry.ActiveDefs() {
		if d.Schedule == nil {
			continue
		}
		scheduled[d.ID] = true
		if driven[d.ID] {
			continue
		}
		if _, ok := scheduleDriverAllowList[d.ID]; ok {
			continue
		}
		undriven = append(undriven, d.ID)
	}
	sort.Strings(undriven)
	if len(undriven) > 0 {
		t.Errorf("ops declare a Schedule but no scheduler task enqueues them (add a task + taskV2DefIDs entry, or an allow-list row with a reason): %v", undriven)
	}

	if len(scheduled) == 0 {
		t.Fatal("no op declares a Schedule; the guard would pass vacuously")
	}

	// The allow-list cannot rot.
	for id := range scheduleDriverAllowList {
		canonical := id
		if a, ok := aliases[id]; ok {
			canonical = a
		}
		if !registered[canonical] {
			t.Errorf("allow-list row %q no longer names a registered op; remove it", id)
			continue
		}
		if !scheduled[canonical] {
			t.Errorf("allow-list row %q no longer declares a Schedule; remove it", id)
		}
		if driven[canonical] {
			t.Errorf("allow-list row %q is now driven by a scheduler task; remove it", id)
		}
	}
}

// Anti-over-suppression: the allow-list must not hide a driven op, and the two
// newly scheduled checks must be counted as driven.
func TestScheduledOpsHaveADriver_NotOverSuppressed(t *testing.T) {
	driven := map[string]bool{}
	for _, defID := range scheduler.TaskV2DefIDs() {
		driven[defID] = true
	}
	if _, ok := scheduleDriverAllowList["reconcile.scan"]; ok {
		t.Error("reconcile.scan is driven and must not be allow-listed")
	}
	for _, id := range []string{"maintenance.file-integrity-check", "maintenance.orphan-book-files-cleanup", "reconcile.scan"} {
		if !driven[id] {
			t.Errorf("%s should be driven by a scheduler task", id)
		}
		if _, ok := scheduleDriverAllowList[id]; ok {
			t.Errorf("%s must not be on the allow-list", id)
		}
	}
}

// TestScheduleStatusesListUndrivenCronsAsDisabled runs the F1 join through the
// scheduler's own registration (05-PR8): every def that declares a cron and
// has a driving task is an active schedule, every one without a driver is
// "declared, not active" and never fires, and the second set is exactly the
// allow-list above.
func TestScheduleStatusesListUndrivenCronsAsDisabled(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	yes := func() bool { return true }
	ts := scheduler.NewTaskScheduler(scheduler.SchedulerDeps{
		Store:               func() scheduler.SchedulerStore { return nil },
		OpRegistry:          srv.opRegistry,
		HasDedupPlugin:      yes,
		HasMetadataFetchSvc: yes,
		HasActivitySvc:      yes,
		HasBatchPoller:      yes,
	})
	if err := ts.RegisterSchedules(srv.opRegistry.Catalog()); err != nil {
		t.Fatalf("RegisterSchedules: %v", err)
	}
	var disabled []string
	active := 0
	for _, st := range ts.ScheduleStatuses() {
		if st.Kind == "disabled" {
			if st.Active {
				t.Errorf("%s is disabled but reports Active", st.DefID)
			}
			if _, ok := ts.NextFire(st.DefID); ok {
				t.Errorf("%s is disabled but has a next fire", st.DefID)
			}
			disabled = append(disabled, st.DefID)
			continue
		}
		if !st.Active {
			t.Errorf("%s is driven by a task but reports inactive", st.DefID)
		}
		active++
	}
	sort.Strings(disabled)
	var want []string
	for id := range scheduleDriverAllowList {
		want = append(want, id)
	}
	sort.Strings(want)
	if len(disabled) != len(want) {
		t.Fatalf("declared-not-active = %v, want the allow-list %v", disabled, want)
	}
	for i := range want {
		if disabled[i] != want[i] {
			t.Fatalf("declared-not-active = %v, want the allow-list %v", disabled, want)
		}
	}
	if active == 0 {
		t.Fatal("no driven schedule listed; the check would pass vacuously")
	}
	t.Logf("F1 at HEAD: %d cron-declared defs driven by a task, %d declared-not-active: %v", active, len(disabled), disabled)
}
