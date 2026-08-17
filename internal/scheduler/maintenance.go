// file: internal/scheduler/maintenance.go
// version: 1.1.0
// guid: 7d2e8f4a-c3b1-4a09-8e5f-2d6c0b9a3e71
// last-edited: 2026-06-16

package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// --- Maintenance Window ---

// MaintenanceCtxKey is a typed context key to avoid string-key collisions.
type MaintenanceCtxKey string

// IgnoreWindowKey is the context key used to bypass the maintenance window check.
const IgnoreWindowKey MaintenanceCtxKey = "ignore_window"

// IsInMaintenanceWindowAt checks if a given hour falls within the configured window.
// Supports midnight-spanning windows (e.g., start=23, end=2).
func IsInMaintenanceWindowAt(hour int) bool {
	if !config.AppConfig.Maintenance.Enabled {
		return false
	}
	start := config.AppConfig.Maintenance.WindowStart
	end := config.AppConfig.Maintenance.WindowEnd

	if start < end {
		return hour >= start && hour < end
	}
	// Midnight spanning: e.g., start=23, end=2 → 23,0,1 are in window
	return hour >= start || hour < end
}

// IsInMaintenanceWindow checks if the current time falls within the configured window.
func IsInMaintenanceWindow() bool {
	return IsInMaintenanceWindowAt(time.Now().Hour())
}

// loadLastMaintenanceRun reads the persisted last-run date from the database.
func (ts *TaskScheduler) loadLastMaintenanceRun() {
	store := ts.deps.Store()
	if store == nil {
		return
	}
	setting, err := store.GetSetting("maintenance_window_last_run")
	if err != nil || setting == nil {
		return
	}
	t, err := time.Parse("2006-01-02", setting.Value)
	if err != nil {
		return
	}
	ts.lastMaintenanceRun = t
}

// saveLastMaintenanceRun persists today's date as the last-run date.
func (ts *TaskScheduler) saveLastMaintenanceRun() {
	store := ts.deps.Store()
	if store == nil {
		return
	}
	today := time.Now().Format("2006-01-02")
	_ = store.SetSetting("maintenance_window_last_run", today, "string", false)
	ts.lastMaintenanceRun = time.Now()
}

// GetLastMaintenanceRunDate returns the last-run date as "2006-01-02", or "" if never run.
func (ts *TaskScheduler) GetLastMaintenanceRunDate() string {
	if ts.lastMaintenanceRun.IsZero() {
		return ""
	}
	return ts.lastMaintenanceRun.Format("2006-01-02")
}

// IsMaintenanceRunning returns true if a maintenance-window operation is active.
//
// Reads the v2 record. It used to scan the newest 20 rows of the legacy
// operations table for type "maintenance-window" at status running/pending —
// which stopped being able to return true the moment the scheduler stopped
// writing those rows. A guard that silently answers "no" forever is worse than
// no guard, because callers keep trusting it.
func (ts *TaskScheduler) IsMaintenanceRunning() bool {
	return ts.hasActiveV2Op("maintenance.window")
}

// hasActiveV2Op reports whether the registry currently has a queued or running
// operation for defID.
//
// ListActiveOperationsV2 is defined as exactly the queued/running set, so there
// is no status list to keep in step here — the previous implementation carried
// its own ("running", "pending") and had already drifted from the v1 vocabulary
// it was matching against.
func (ts *TaskScheduler) hasActiveV2Op(defID string) bool {
	store := ts.deps.Store()
	if store == nil || defID == "" {
		return false
	}
	ops, err := store.ListActiveOperationsV2()
	if err != nil {
		return false
	}
	for _, op := range ops {
		if op.DefID == defID {
			return true
		}
	}
	return false
}

// hasRunToday checks if the maintenance window has already run today.
func (ts *TaskScheduler) hasRunToday() bool {
	today := time.Now().Format("2006-01-02")
	return ts.lastMaintenanceRun.Format("2006-01-02") == today
}

// IsTaskRunning checks if a task's operation is currently in progress.
func (ts *TaskScheduler) IsTaskRunning(name string) bool {
	return ts.isTaskRunning(name)
}

// taskV2DefIDs maps a scheduled task's name to the v2 operation def it enqueues.
//
// This is the same lookup the old opTypeMap performed against the legacy
// operations table, retargeted at the v2 record. It is not decoration: the
// maintenance window skips a task when isTaskRunning says it is already going,
// and the tasks page renders IsRunning from it.
//
// Only tasks that actually enqueue an operation appear. transcode,
// label_refinement and batch_poller do not — transcode fails by design without
// a book_id, and the other two dispatch through their own paths.
//
// A test asserts this map against what each task's TriggerFn really enqueues, so
// it cannot drift the way opTypeMap did. That one covered 14 of the 24 tasks
// that enqueue an operation; the other 10 — temp_file_cleanup, trash_cleanup,
// archive_sweep, metadata_upgrade, series_normalize, acoustid_online_lookup,
// ai_dedup_batch, cleanup_activity_log, resolve_production_authors,
// library_size_refresh — were absent, and a missing key is indistinguishable
// from "not running". Nothing announces a map entry that was never added.
var taskV2DefIDs = map[string]string{
	"library_scan":               "library.scan",
	"library_organize":           "library.organize",
	"library_size_refresh":       "library.size-refresh",
	"dedup_refresh":              "dedup.author-scan",
	"dedup_llm_review":           "scheduler.dedup-llm-review",
	"series_prune":               "dedup.series-prune",
	"series_normalize":           "dedup.series-normalize",
	"isbn_enrichment":            "scheduler.isbn-enrichment",
	"temp_file_cleanup":          "scheduler.temp-file-cleanup",
	"trash_cleanup":              "scheduler.trash-cleanup",
	"archive_sweep":              "scheduler.archive-sweep",
	"metadata_upgrade":           "scheduler.metadata-upgrade",
	"author_split_scan":          "scheduler.author-split-scan",
	"db_optimize":                "scheduler.db-optimize",
	"acoustid_online_lookup":     "acoustid.lookup-online",
	"cleanup_old_backups":        "scheduler.cleanup-old-backups",
	"purge_deleted":              "scheduler.purge-deleted",
	"tombstone_cleanup":          "scheduler.tombstone-cleanup",
	"resolve_production_authors": "scheduler.resolve-production-authors",
	"metadata_refresh":           "scheduler.metadata-refresh",
	"reconcile_scan":             "maintenance.reconcile-scan",
	"ai_dedup_batch":             "maintenance.ai-dedup-batch",
	"purge_old_logs":             "maintenance.purge-old-logs",
	"cleanup_activity_log":       "maintenance.cleanup-activity-log",
}

// isTaskRunning is the internal implementation.
func (ts *TaskScheduler) isTaskRunning(name string) bool {
	return ts.hasActiveV2Op(taskV2DefIDs[name])
}

// MaintenanceWindowOpParams carries parameters to the maintenance.window operation.
type MaintenanceWindowOpParams struct {
	LegacyOpID   string `json:"legacy_op_id"`
	IgnoreWindow bool   `json:"ignore_window"`
}

// RunMaintenanceWindow enqueues the maintenance-window operation via the v2 registry.
// Step 1: auto-update (if enabled). Step 2+: maintenance tasks in fixed order.
func (ts *TaskScheduler) RunMaintenanceWindow(ctx context.Context) error {
	store := ts.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if ts.deps.OpRegistry == nil {
		return fmt.Errorf("operation registry not initialized")
	}

	// Mark as run NOW to prevent the 60s ticker from re-enqueuing
	// while the async operation is still running.
	ts.saveLastMaintenanceRun()

	// No legacy operations row. This used to create one and pass its id as
	// LegacyOpID; the op used that id only to tag activity-log entries, and it
	// now takes its own operation id from the reporter instead. 28 of the 44
	// stuck-op cancellations measured over 30 days were maintenance.window.
	ignoreWindow := ctx.Value(IgnoreWindowKey) != nil
	if _, err := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.window", MaintenanceWindowOpParams{
		IgnoreWindow: ignoreWindow,
	}); err != nil {
		return fmt.Errorf("failed to enqueue maintenance-window: %w", err)
	}
	return nil
}
