// file: internal/scheduler/tasks.go
// version: 1.1.0
// guid: 9b4c7e21-a5f3-4d08-b2e6-3c8d1f7a0e54
// last-edited: 2026-06-16

// Package scheduler — task registrations.
// All 22 registered tasks are defined here. Each task's TriggerFn and
// IsEnabled read from SchedulerDeps (not *Server) so the scheduler package
// remains independent of the server package.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
)

// ---- param types -------------------------------------------------------
// These types mirror the JSON wire shapes defined in server/{library_core_ops,
// duplicates_ops}.go. They are intentionally minimal — only the fields used
// when the scheduler triggers the operations.

type libraryScanParams struct{}

type libraryOrganizeParams struct{}

type librarySizeRefreshParams struct{}

type authorDedupScanOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
}

type seriesPruneOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
}

type seriesNormalizeOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
}

// schedulerExtraOpParams carries the v1 operation ID into the Run func.
type schedulerExtraOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
}

// ---- registration -------------------------------------------------------

func (ts *TaskScheduler) registerAllTasks() {
	// --- Library tasks ---

	ts.registerTask(TaskDefinition{
		Name:        "library_scan",
		Description: "Scan library for new/changed audiobooks (incremental by default, use force_update for full rescan)",
		Category:    "library",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "scan", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.scan", libraryScanParams{}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue library.scan: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return config.AppConfig.ScanOnStartup },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return config.AppConfig.ScanOnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.LibraryScan },
	})

	ts.registerTask(TaskDefinition{
		Name:        "library_organize",
		Description: "Organize audiobooks into folder structure",
		Category:    "library",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "organize", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.organize", libraryOrganizeParams{}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue library.organize: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.LibraryOrganize },
	})

	ts.registerTask(TaskDefinition{
		Name:        "library_size_refresh",
		Description: "Walk library + import-path trees to refresh on-disk size cache",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "library-size-refresh", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.size-refresh", librarySizeRefreshParams{}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue library.size-refresh: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.LibrarySizeRefresh },
	})

	ts.registerTask(TaskDefinition{
		Name:        "transcode",
		Description: "Transcode audiobooks to target format",
		Category:    "library",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "transcode", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			// Transcode requires specific params — cannot be triggered from the scheduler
			// without book_id. Mark the operation as failed immediately.
			_ = store.UpdateOperationError(op.ID, "transcode requires parameters — use the operations API directly")
			slog.Warn("transcode task triggered from scheduler () without params — use the operations API", "source", source)
			return op, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// --- Sync tasks ---
	// iTunes sync and import are now registered via UOS plugin (UOS-10)

	// --- Maintenance tasks ---

	ts.registerTask(TaskDefinition{
		Name:        "dedup_refresh",
		Description: "Refresh author & series dedup cache",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "author-dedup-scan", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			params := authorDedupScanOpParams{LegacyOpID: op.ID}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.author-scan", params); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.author-scan: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled: func() bool { return config.AppConfig.ScheduledDedupRefreshEnabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.ScheduledDedupRefreshInterval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.ScheduledDedupRefreshOnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.DedupRefresh },
	})

	ts.registerTask(TaskDefinition{
		Name:        "dedup_llm_review",
		Description: "Run LLM review on ambiguous dedup candidates",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "dedup-llm-review", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.dedup-llm-review", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.dedup-llm-review: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return ts.deps.HasDedupEngine() },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})

	ts.registerTask(TaskDefinition{
		Name:        "series_prune",
		Description: "Merge duplicate series and delete orphans",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "series-prune", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			params := seriesPruneOpParams{LegacyOpID: op.ID}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.series-prune", params); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.series-prune: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled: func() bool { return config.AppConfig.ScheduledSeriesPruneEnabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.ScheduledSeriesPruneInterval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.ScheduledSeriesPruneOnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.SeriesPrune },
	})

	ts.registerTask(TaskDefinition{
		Name:        "series_normalize",
		Description: "Strip title/position contamination from series names and run write-back + organize for affected books",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "series-normalize", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			params := seriesNormalizeOpParams{LegacyOpID: op.ID}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.series-normalize", params); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.series-normalize: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	ts.registerTask(TaskDefinition{
		Name:        "isbn_enrichment",
		Description: "Enrich missing ISBN identifiers from external metadata sources",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "isbn-enrichment", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			params := schedulerExtraOpParams{LegacyOpID: op.ID}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.isbn-enrichment", params); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.isbn-enrichment: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:   func() bool { return ts.deps.HasMetadataFetchSvc() },
		GetInterval: func() time.Duration { return 6 * time.Hour },
		// ISBN enrichment scans 100 books per run and frequently returns
		// "nothing to enrich". Running it on every startup adds 0 value
		// and clutters Active Operations. The maintenance window still
		// picks it up periodically; the 6h interval keeps it from going
		// completely silent.
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.MetadataRefresh },
	})

	// iTunes position sync is now registered via UOS plugin (UOS-10)

	ts.registerTask(TaskDefinition{
		Name:        "temp_file_cleanup",
		Description: "Remove orphaned *.tmp.m4b / *.tmp.m4a files left by crashed ffmpeg operations",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "temp-file-cleanup", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			params := schedulerExtraOpParams{LegacyOpID: op.ID}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.temp-file-cleanup", params); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.temp-file-cleanup: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return true },
		RunInMaintenanceWindow: func() bool { return true },
	})

	ts.registerTask(TaskDefinition{
		Name:        "trash_cleanup",
		Description: "Purge trashed book versions past their 14-day TTL",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "trash-cleanup", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.trash-cleanup", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.trash-cleanup: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})

	ts.registerTask(TaskDefinition{
		Name:        "archive_sweep",
		Description: "Remove soft-deleted books past the 30-day retention window",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "archive-sweep", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.archive-sweep", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.archive-sweep: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})

	ts.registerTask(TaskDefinition{
		Name:        "metadata_upgrade",
		Description: "Upgrade metadata from lower-quality sources (Google Books, Wikipedia) to richer ones (Hardcover, Audible) when a high-confidence match is available",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "metadata-upgrade", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.metadata-upgrade", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.metadata-upgrade: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return ts.deps.HasMetadataFetchSvc() },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.MetadataRefresh },
	})

	ts.registerTask(TaskDefinition{
		Name:        "author_split_scan",
		Description: "Find & split composite author names",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "author-split-scan", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.author-split-scan", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.author-split-scan: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled: func() bool { return config.AppConfig.ScheduledAuthorSplitEnabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.ScheduledAuthorSplitInterval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.ScheduledAuthorSplitOnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.AuthorSplit },
	})

	ts.registerTask(TaskDefinition{
		Name:        "db_optimize",
		Description: "Optimize database (VACUUM/compact)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "db-optimize", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.db-optimize", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.db-optimize: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled: func() bool { return config.AppConfig.ScheduledDbOptimizeEnabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.ScheduledDbOptimizeInterval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.ScheduledDbOptimizeOnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.DbOptimize },
	})

	// Nightly AcoustID online lookup. Off unless the user explicitly
	// enables MaintenanceWindowAcoustIDOnlineLookup. The op itself
	// refuses to run without ACOUSTID_API_KEY, so flipping the toggle
	// without setting the env is a no-op.
	ts.registerTask(TaskDefinition{
		Name:        "acoustid_online_lookup",
		Description: "Look up un-queried fingerprints on acoustid.org (rate-limited, bounded)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "acoustid-online-lookup", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			params := map[string]any{
				"limit": config.AppConfig.Maintenance.AcoustIDNightlyLimit,
				"force": false,
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "acoustid.lookup-online", params); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue acoustid.lookup-online: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return config.AppConfig.Maintenance.AcoustIDOnlineLookup },
		GetInterval:            func() time.Duration { return 0 }, // run only inside the maintenance window
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.AcoustIDOnlineLookup },
	})

	ts.registerTask(TaskDefinition{
		Name:        "cleanup_old_backups",
		Description: "Remove old .bak-* backup files past retention",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "cleanup-old-backups", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.cleanup-old-backups", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.cleanup-old-backups: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return config.AppConfig.Maintenance.DbOptimize },
		GetInterval:            func() time.Duration { return 0 },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.DbOptimize },
	})

	ts.registerTask(TaskDefinition{
		Name:        "purge_deleted",
		Description: "Purge soft-deleted books past retention",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "purge-deleted", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			params := schedulerExtraOpParams{LegacyOpID: op.ID}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.purge-deleted", params); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.purge-deleted: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled: func() bool { return config.AppConfig.PurgeSoftDeletedAfterDays > 0 },
		GetInterval: func() time.Duration {
			if config.AppConfig.PurgeSoftDeletedAfterDays > 0 {
				return 6 * time.Hour
			}
			return 0
		},
		RunOnStart:             func() bool { return config.AppConfig.PurgeSoftDeletedAfterDays > 0 },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.PurgeDeleted },
	})

	ts.registerTask(TaskDefinition{
		Name:        "tombstone_cleanup",
		Description: "Resolve author tombstone chains (A→B→C becomes A→C)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "tombstone-cleanup", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.tombstone-cleanup", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.tombstone-cleanup: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return true },
		GetInterval:            func() time.Duration { return 24 * time.Hour },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.TombstoneCleanup },
	})

	ts.registerTask(TaskDefinition{
		Name:        "resolve_production_authors",
		Description: "Resolve real authors for production company entries",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "resolve-production-authors", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.resolve-production-authors", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.resolve-production-authors: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled: func() bool { return config.AppConfig.ScheduledResolveProductionAuthorsEnabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.ScheduledResolveProductionAuthorsInterval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return false },
	})

	ts.registerTask(TaskDefinition{
		Name:        "metadata_refresh",
		Description: "Re-fetch metadata for incomplete books",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "metadata-refresh", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.metadata-refresh", schedulerExtraOpParams{LegacyOpID: op.ID}); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.metadata-refresh: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled: func() bool { return config.AppConfig.ScheduledMetadataRefreshEnabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.ScheduledMetadataRefreshInterval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.ScheduledMetadataRefreshOnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.MetadataRefresh },
	})

	// Reconcile — find broken file paths and match to untracked files on disk
	ts.registerTask(TaskDefinition{
		Name:        "reconcile_scan",
		Description: "Find books with missing files and match to untracked files on disk",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "reconcile_scan", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.reconcile-scan", nil); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue reconcile scan: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled: func() bool { return config.AppConfig.ScheduledReconcileEnabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.ScheduledReconcileInterval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.ScheduledReconcileOnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.Reconcile },
	})

	// AI Dedup Batch — uses OpenAI Batch API at 50% cost
	ts.registerTask(TaskDefinition{
		Name:        "ai_dedup_batch",
		Description: "Run AI author dedup via Batch API (50% cheaper, up to 24h)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "ai-dedup-batch", nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create operation: %w", err)
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.ai-dedup-batch", nil); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue ai-dedup-batch: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled: func() bool {
			return config.AppConfig.ScheduledAIDedupBatchEnabled && config.AppConfig.EnableAIParsing
		},
		GetInterval: func() time.Duration {
			mins := config.AppConfig.ScheduledAIDedupBatchInterval
			if mins <= 0 {
				return 24 * time.Hour
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.ScheduledAIDedupBatchOnStartup },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// Unified Batch Poller — discovers all project-tagged OpenAI batches and routes
	// completed ones to the appropriate handler (author_dedup, author_review,
	// diagnostics, pipeline, etc.)
	ts.registerTask(TaskDefinition{
		Name:        "batch_poller",
		Description: "Poll OpenAI for completed batch jobs",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			if ts.deps.PollBatches == nil {
				return nil, nil
			}
			processed, err := ts.deps.PollBatches(context.Background())
			if err != nil {
				slog.Warn("batch_poller", "error", err)
			}
			if processed > 0 {
				slog.Info("batch_poller processed completed batches", "processed", processed)
			}
			return nil, nil
		},
		IsEnabled: func() bool {
			return config.AppConfig.OpenAIAPIKey != "" && ts.deps.HasBatchPoller()
		},
		GetInterval: func() time.Duration {
			return 5 * time.Minute
		},
		RunOnStart:             func() bool { return true },
		RunInMaintenanceWindow: func() bool { return false },
	})

	// Log Retention Pruning — prune old operation logs and system activity logs
	ts.registerTask(TaskDefinition{
		Name:        "purge_old_logs",
		Description: "Prune operation logs and system activity logs older than retention period",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "purge_old_logs", nil)
			if err != nil {
				return nil, err
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.purge-old-logs", nil); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue purge-old-logs: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return config.AppConfig.LogRetentionDays > 0 },
		GetInterval:            func() time.Duration { return 7 * 24 * time.Hour },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.PurgeOldLogs },
	})

	// Activity Log Cleanup — summarize old change entries and prune old debug entries
	ts.registerTask(TaskDefinition{
		Name:        "cleanup_activity_log",
		Description: "Summarize old change entries and prune old debug entries from activity log",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			opID := ulid.Make().String()
			op, err := store.CreateOperation(opID, "cleanup_activity_log", nil)
			if err != nil {
				return nil, err
			}
			if _, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.cleanup-activity-log", nil); enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue cleanup-activity-log: %w", enqErr)
			}
			return op, nil
		},
		IsEnabled:              func() bool { return ts.deps.HasActivitySvc() },
		GetInterval:            func() time.Duration { return 24 * time.Hour },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})
}
