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
	ulid "github.com/oklog/ulid/v2"
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
func (ts *TaskScheduler) IsMaintenanceRunning() bool {
	store := ts.deps.Store()
	if store == nil {
		return false
	}
	ops, _, err := store.ListOperations(20, 0)
	if err != nil {
		return false
	}
	for _, op := range ops {
		if op.Type == "maintenance-window" && (op.Status == "running" || op.Status == "pending") {
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

// isTaskRunning is the internal implementation.
func (ts *TaskScheduler) isTaskRunning(name string) bool {
	store := ts.deps.Store()
	if store == nil {
		return false
	}
	ops, _, err := store.ListOperations(100, 0)
	if err != nil {
		return false
	}
	opTypeMap := map[string]string{
		"library_scan": "scan", "library_organize": "organize",
		"dedup_refresh": "author-dedup-scan", "dedup_llm_review": "dedup-llm-review", "series_prune": "series-prune",
		"isbn_enrichment":   "isbn-enrichment",
		"author_split_scan": "author-split-scan", "db_optimize": "db-optimize",
		"purge_deleted": "purge-deleted", "tombstone_cleanup": "tombstone-cleanup",
		"reconcile_scan": "reconcile_scan", "purge_old_logs": "purge_old_logs",
		"cleanup_old_backups": "cleanup-old-backups",
		"metadata_refresh":    "metadata-refresh",
	}
	opType, ok := opTypeMap[name]
	if !ok {
		return false
	}
	for _, op := range ops {
		if op.Type == opType && (op.Status == "running" || op.Status == "pending") {
			return true
		}
	}
	return false
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

	opID := ulid.Make().String()
	if _, err := store.CreateOperation(opID, "maintenance-window", nil); err != nil {
		return fmt.Errorf("failed to create maintenance-window operation: %w", err)
	}

	// Mark as run NOW to prevent the 60s ticker from re-enqueuing
	// while the async operation is still running.
	ts.saveLastMaintenanceRun()

	ignoreWindow := ctx.Value(IgnoreWindowKey) != nil
	if _, err := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.window", MaintenanceWindowOpParams{
		LegacyOpID:   opID,
		IgnoreWindow: ignoreWindow,
	}); err != nil {
		return fmt.Errorf("failed to enqueue maintenance-window: %w", err)
	}
	return nil
}
