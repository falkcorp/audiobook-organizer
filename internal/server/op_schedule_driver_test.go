// file: internal/server/op_schedule_driver_test.go
// version: 1.0.1
// guid: c47e92b1-5d08-4a3f-9e61-1b2f8d0a7c34
// last-edited: 2026-10-10

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
	"deluge.protected-paths-sync":     "protected list loads at boot and on Deluge changes; a half-hourly op row adds noise (revisit with D52)",
	"maintenance.author-dedup-scan":   "to be retired by D27 C1; its twin dedup.author-scan runs on the dedup_refresh task",
	"maintenance.author-split-scan":   "twin of the scheduled scheduler.author-split-scan; after P4e the task points here",
	"maintenance.batch-poller":        "to be deleted by D26 / P12; the inline loop is the poller",
	"maintenance.cleanup-old-backups": "twin of the scheduled scheduler.cleanup-old-backups; after P4f the task points here",
	"maintenance.db-optimize":         "twin of the scheduled scheduler.db-optimize; after P4a the task points here",
	"maintenance.metadata-refresh":    "twin of the scheduled scheduler.metadata-refresh; after P4c the task points here",
	"maintenance.purge-deleted":       "twin of the scheduled scheduler.purge-deleted; after P4b the task points here (it deletes)",
	"maintenance.series-prune":        "twin of dedup.series-prune, which the series_prune task runs weekly (P5 aliases it)",
	"maintenance.temp-file-cleanup":   "twin of the scheduled scheduler.temp-file-cleanup; after P4a the task points here",
	"maintenance.tombstone-cleanup":   "twin of the scheduled scheduler.tombstone-cleanup; after P4a the task points here",
	"maintenance.trash-cleanup":       "twin of the scheduled scheduler.trash-cleanup; after P4a the task points here",
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
	if _, ok := scheduleDriverAllowList["maintenance.reconcile-scan"]; ok {
		t.Error("maintenance.reconcile-scan is driven and must not be allow-listed")
	}
	for _, id := range []string{"maintenance.file-integrity-check", "maintenance.orphan-book-files-cleanup", "maintenance.reconcile-scan"} {
		if !driven[id] {
			t.Errorf("%s should be driven by a scheduler task", id)
		}
		if _, ok := scheduleDriverAllowList[id]; ok {
			t.Errorf("%s must not be on the allow-list", id)
		}
	}
}
