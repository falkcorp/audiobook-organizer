// file: internal/scheduler/tasks.go
// version: 1.9.0
// guid: 9b4c7e21-a5f3-4d08-b2e6-3c8d1f7a0e54
// last-edited: 2026-08-23

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
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

// ---- param types -------------------------------------------------------
// These types mirror the JSON wire shapes defined in server/{library_core_ops,
// duplicates_ops}.go. They are intentionally minimal — only the fields used
// when the scheduler triggers the operations.

type libraryOrganizeParams struct{}

type librarySizeRefreshParams struct{}

// The three dedup ops below take their params types from internal/dedup
// directly rather than mirroring them here. These used to be local copies
// carrying a legacy_op_id the ops stopped reading on 2026-08-22, and nothing
// could catch that: a mirror is coupled to the real type only by JSON tags, so
// it drifts silently. seriesPruneOpParams had drifted twice over — it declared
// legacy_op_id and omitted Detail, the field the real type actually has.

// schedulerExtraOpParams carries the v1 operation ID into the Run func.
type schedulerExtraOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
}

// labelRefinementRebuildParams / labelRefinementCalibrateParams are the params
// the label_refinement scheduled chain passes to dedup.rebuild-gold-labels and
// dedup.calibrate-composite. They are EMPTY ON PURPOSE: with no apply key,
// both ops fall back to their built-in dry-run/report default (Apply=false) —
// rebuild_gold_labels.go takes the guarded `if !params.Apply` no-write return,
// calibrate_composite.go reports without persisting bands. The scheduled path
// has NO way to set apply=true; enabling an apply is an operator AskUserQuestion
// decision only. The Apply==false-on-empty-params guarantee is pinned by
// canaries in internal/plugins/dedup/{rebuild_gold_labels,calibrate_composite}_test.go.
type labelRefinementRebuildParams struct{}

type labelRefinementCalibrateParams struct{}

// ---- registration -------------------------------------------------------

// v2ScheduledOp is what a scheduled task returns now that it no longer writes a
// legacy operations row.
//
// The 5 tasks below used to do this:
//
//	opID := ulid.Make().String()
//	op, _ := store.CreateOperation(opID, "scan", nil)   // legacy row, status=pending
//	v2ID, _ := ts.deps.OpRegistry.EnqueueOp(ctx, "library.scan", ...)  // DIFFERENT id
//	return op, nil
//
// The legacy row and the real v2 operation got SEPARATE ids, nothing linked
// them, and nothing ever updated the legacy row — so it sat at "pending"
// forever. One orphan per scheduled tick, which is precisely the 183-of-200
// pending rows measured against production on 2026-08-16, going back six days:
// purge-deleted, temp-file-cleanup, scan, isbn-enrichment, author-dedup-scan.
//
// The return value is consumed for exactly one thing — the scheduler's
// "started operation" log line (scheduler.go) and the /tasks/:name/run response.
// Both were therefore reporting an id that NO endpoint could resolve, because
// the id that exists in v2 is the other one. Synthesizing the row from the v2 id
// fixes the log and the response as a side effect of removing the orphan.
func v2ScheduledOp(v2ID, opType string) *database.Operation {
	return &database.Operation{
		ID:        v2ID,
		Type:      opType,
		Status:    "queued",
		CreatedAt: time.Now().UTC(),
	}
}

func (ts *TaskScheduler) registerAllTasks() {
	// --- Library tasks ---

	// library_scan is the ONLY unattended discovery path for new books, so it
	// is the one task in this file that ships with a real, default-on interval.
	//
	// It used to be completely inert: GetInterval returned a hard-coded 0 (so
	// scheduler.Start's `IsEnabled() && GetInterval() > 0` guard never created
	// a ticker), IsEnabled/RunOnStart both read scan_on_startup which defaults
	// false, and maintenance.library_scan was unreachable because "library_scan"
	// was missing from maintenanceOrder. Net effect: a book copied into an
	// import path was never noticed until somebody pressed Scan by hand.
	ts.registerTask(TaskDefinition{
		Name:        "library_scan",
		Description: "Scan library for new/changed audiobooks (incremental by default, use force_update for full rescan)",
		Category:    "library",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			// Skip this tick if the previous scan we enqueued has not finished.
			// library.scan's ConcurrencyKey makes the dispatcher SERIALIZE a
			// duplicate rather than reject it (registry/dispatcher.go: "the op
			// stays QUEUED"), so a scan that outruns the interval would other-
			// wise pile up one queued op — plus one orphan legacy operation
			// row — on every tick. Guarding here rather than in the generic
			// ticker keeps the behaviour of the seven other interval tasks
			// unchanged.
			if prev := ts.previousRunID("library_scan"); prev != "" {
				if row, err := store.GetOperationV2(prev); err == nil && row != nil {
					if row.Status == "queued" || row.Status == "running" {
						slog.Info("library_scan: previous scan still active, skipping this tick",
							"op", prev, "status", row.Status, "source", source)
						return nil, nil
					}
				}
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.scan", scanner.LibraryScanParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue library.scan: %w", enqErr)
			}
			ts.setPreviousRunID("library_scan", v2ID)
			return v2ScheduledOp(v2ID, "scan"), nil
		},
		IsEnabled: func() bool {
			// IsEnabled gates the ticker, the startup run AND maintenance-window
			// eligibility, so it must stay true for anyone who only ever set the
			// legacy scan_on_startup flag — otherwise flipping to the new key
			// would silently take away their startup scan.
			return config.AppConfig.Scheduled.LibraryScan.Enabled || config.AppConfig.ScanOnStartup
		},
		GetInterval: func() time.Duration {
			if !config.AppConfig.Scheduled.LibraryScan.Enabled {
				return 0
			}
			mins := config.AppConfig.Scheduled.LibraryScan.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart: func() bool {
			return config.AppConfig.Scheduled.LibraryScan.OnStartup || config.AppConfig.ScanOnStartup
		},
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.LibraryScan },
	})

	// library_scan_full is the WEEKLY FULL SWEEP: a library.scan with both
	// force_update and include_root_dir set, so every file is re-read and
	// re-hashed instead of being skipped as unchanged.
	//
	// Its schedule does NOT come from GetInterval. GetInterval here is only a
	// DUE-CHECK cadence (default hourly); the actual weekly gap is enforced in
	// TriggerFn against a timestamp persisted in the settings store. See
	// full_sweep.go for why: scheduler.Start's ticker is in-memory, so any
	// interval longer than the process uptime silently never fires, and a
	// deploy-per-day cadence makes 168h unreachable.
	//
	// It shares library.scan's ConcurrencyKey with the incremental task. That
	// is deliberate and safe: EnqueueOp's dedupe compares MARSHALLED PARAMS
	// byte-for-byte, and the incremental enqueues {} while this enqueues
	// {"force_update":true,"include_root_dir":true}. Unequal params queue a
	// second row rather than collapsing into the running op, so a sweep that
	// lands mid-incremental is DELAYED, never dropped.
	ts.registerTask(TaskDefinition{
		Name:        "library_scan_full",
		Description: "Weekly full library sweep (force_update: re-reads and re-hashes every file)",
		Category:    "library",
		TriggerFn: func(source string) (*database.Operation, error) {
			store := ts.deps.Store()
			if store == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			period := time.Duration(config.AppConfig.Scheduled.LibraryScanFull.PeriodHours) * time.Hour

			last, found := ts.loadLastFullSweep()
			if !found {
				// Never run (or the stored value was unreadable). Seed the
				// clock and skip: the first tick after a deploy must not kick
				// off an unannounced multi-hour re-hash of the whole library.
				ts.saveLastFullSweep(time.Now())
				slog.Info("library_scan_full: no prior sweep recorded; seeding the clock and waiting a full period",
					"periodHours", config.AppConfig.Scheduled.LibraryScanFull.PeriodHours, "source", source)
				return nil, nil
			}
			if !fullSweepDue(last, time.Now(), period) {
				return nil, nil
			}

			// Same skip-if-active guard as the incremental scan, keyed to this
			// task's own previous run.
			if prev := ts.previousRunID("library_scan_full"); prev != "" {
				if row, err := store.GetOperationV2(prev); err == nil && row != nil {
					if row.Status == "queued" || row.Status == "running" {
						slog.Info("library_scan_full: previous sweep still active, skipping this tick",
							"op", prev, "status", row.Status, "source", source)
						return nil, nil
					}
				}
			}

			forceUpdate, includeRoot := true, true
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.scan", scanner.LibraryScanParams{
				ForceUpdate:    &forceUpdate,
				IncludeRootDir: &includeRoot,
			})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue full library.scan: %w", enqErr)
			}
			ts.setPreviousRunID("library_scan_full", v2ID)
			// Stamped on ENQUEUE, not completion — see saveLastFullSweep.
			ts.saveLastFullSweep(time.Now())
			slog.Info("library_scan_full: enqueued full sweep", "op", v2ID, "since", last, "source", source)
			return v2ScheduledOp(v2ID, "scan"), nil
		},
		IsEnabled: func() bool {
			return config.AppConfig.Scheduled.LibraryScanFull.Enabled
		},
		GetInterval: func() time.Duration {
			if !config.AppConfig.Scheduled.LibraryScanFull.Enabled {
				return 0
			}
			// Due-check cadence, NOT the sweep period.
			mins := config.AppConfig.Scheduled.LibraryScanFull.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.organize", libraryOrganizeParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue library.organize: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "organize"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "library.size-refresh", librarySizeRefreshParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue library.size-refresh: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "library-size-refresh"), nil
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
		// TriggerFn always fails: its only argument is the trigger source, so
		// there is no way to route a book_id through it, and transcoding without
		// one is meaningless.
		//
		// It used to express that by creating a legacy operations row purely to
		// stamp an error on it and hand it back. RunTask answers 202 Accepted for
		// a non-nil op, so every caller got "accepted" for work that had already
		// definitively failed, with the reason buried in the row. Returning the
		// error instead takes the handler's error branch — 400 with the message
		// where the caller can see it — and writes no row at all.
		TriggerFn: func(source string) (*database.Operation, error) {
			slog.Warn("transcode task triggered without params — use the operations API", "source", source)
			return nil, fmt.Errorf("transcode requires parameters — use the operations API directly")
		},
		// NOT enabled, and this is not a regression: the TriggerFn above fails
		// by design because a scheduled trigger has no book_id to transcode.
		// Reporting Enabled=true for a task whose every automatic invocation
		// creates a failed operation is a lie the UI repeats on the tasks page.
		// runTask does not consult IsEnabled, so manual/API invocation with real
		// params is unaffected — only the automatic paths and the displayed
		// state change.
		IsEnabled:              func() bool { return false },
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.author-scan", dedup.AuthorDedupScanParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.author-scan: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "author-dedup-scan"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.DedupRefresh.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.DedupRefresh.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.DedupRefresh.OnStartup },
		RunInMaintenanceWindow: func() bool { return config.AppConfig.Maintenance.DedupRefresh },
	})

	// label_refinement (INIT-1 T6): built-in-DISABLED scheduled chain that, only
	// when an owner sets scheduled.label_refinement.enabled=true, periodically
	// runs dedup.rebuild-gold-labels then dedup.calibrate-composite in DRY-RUN
	// mode and logs a summary. It writes NOTHING — there is no apply on this path.
	// Deliberately minimal; INIT-6 WF-3 is expected to subsume scheduled.* keys.
	ts.registerTask(TaskDefinition{
		Name:        "label_refinement",
		Description: "Dry-run gold-label refinement chain: rebuild-gold-labels → calibrate-composite (report only; applies are operator-gated)",
		Category:    "maintenance",
		TriggerFn: func(source string) (*database.Operation, error) {
			if ts.deps.Store() == nil {
				return nil, fmt.Errorf("database not initialized")
			}
			if ts.deps.OpRegistry == nil {
				return nil, fmt.Errorf("operations registry not initialized")
			}
			// Run the two-op chain detached so the scheduler tick / manual
			// Run-Now returns promptly (each op can run for tens of minutes).
			// No legacy op row is created, so TriggerFn returns a nil op.
			go ts.runLabelRefinementChain()
			return nil, nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.LabelRefinement.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.LabelRefinement.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart: func() bool { return config.AppConfig.Scheduled.LabelRefinement.OnStartup },
		// Intentionally NOT part of the maintenance window (also absent from
		// maintenanceOrder): keeps the task fully inert under default config.
		RunInMaintenanceWindow: func() bool { return false },
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.dedup-llm-review", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.dedup-llm-review: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "dedup-llm-review"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.series-prune", dedup.SeriesPruneParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.series-prune: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "series-prune"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.SeriesPrune.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.SeriesPrune.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.SeriesPrune.OnStartup },
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "dedup.series-normalize", dedup.SeriesNormalizeParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue dedup.series-normalize: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "series-normalize"), nil
		},
		// Manual-only, and now says so. It has no interval and explicitly opts
		// out of the maintenance window, so IsEnabled: true described a task
		// that could never start itself. It also runs write-back + organize,
		// which moves files — turning it into a timer would be a behaviour
		// change nobody asked for, so the honest fix is to stop claiming it is
		// enabled. runTask ignores IsEnabled, so triggering it from the
		// operations API still works exactly as before.
		IsEnabled:              func() bool { return false },
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.isbn-enrichment", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.isbn-enrichment: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "isbn-enrichment"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.temp-file-cleanup", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.temp-file-cleanup: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "temp-file-cleanup"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.trash-cleanup", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.trash-cleanup: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "trash-cleanup"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.archive-sweep", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.archive-sweep: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "archive-sweep"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.metadata-upgrade", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.metadata-upgrade: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "metadata-upgrade"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.author-split-scan", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.author-split-scan: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "author-split-scan"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.AuthorSplit.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.AuthorSplit.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.AuthorSplit.OnStartup },
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.db-optimize", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.db-optimize: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "db-optimize"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.DbOptimize.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.DbOptimize.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.DbOptimize.OnStartup },
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
			params := map[string]any{
				"limit": config.AppConfig.Maintenance.AcoustIDNightlyLimit,
				"force": false,
			}
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "acoustid.lookup-online", params)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue acoustid.lookup-online: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "acoustid-online-lookup"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.cleanup-old-backups", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.cleanup-old-backups: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "cleanup-old-backups"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.purge-deleted", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.purge-deleted: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "purge-deleted"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.tombstone-cleanup", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.tombstone-cleanup: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "tombstone-cleanup"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.resolve-production-authors", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.resolve-production-authors: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "resolve-production-authors"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.ResolveProductionAuthors.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.ResolveProductionAuthors.Interval
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "scheduler.metadata-refresh", schedulerExtraOpParams{})
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue scheduler.metadata-refresh: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "metadata-refresh"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.MetadataRefresh.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.MetadataRefresh.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.MetadataRefresh.OnStartup },
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.reconcile-scan", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue reconcile scan: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "reconcile_scan"), nil
		},
		IsEnabled: func() bool { return config.AppConfig.Scheduled.Reconcile.Enabled },
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.Reconcile.Interval
			if mins <= 0 {
				return 0
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.Reconcile.OnStartup },
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.ai-dedup-batch", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue ai-dedup-batch: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "ai-dedup-batch"), nil
		},
		IsEnabled: func() bool {
			return config.AppConfig.Scheduled.AIDedupBatch.Enabled && config.AppConfig.EnableAIParsing
		},
		GetInterval: func() time.Duration {
			mins := config.AppConfig.Scheduled.AIDedupBatch.Interval
			if mins <= 0 {
				return 24 * time.Hour
			}
			return time.Duration(mins) * time.Minute
		},
		RunOnStart:             func() bool { return config.AppConfig.Scheduled.AIDedupBatch.OnStartup },
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.purge-old-logs", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue purge-old-logs: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "purge_old_logs"), nil
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
			v2ID, enqErr := ts.deps.OpRegistry.EnqueueOp(context.Background(), "maintenance.cleanup-activity-log", nil)
			if enqErr != nil {
				return nil, fmt.Errorf("failed to enqueue cleanup-activity-log: %w", enqErr)
			}
			return v2ScheduledOp(v2ID, "cleanup_activity_log"), nil
		},
		IsEnabled:              func() bool { return ts.deps.HasActivitySvc() },
		GetInterval:            func() time.Duration { return 24 * time.Hour },
		RunOnStart:             func() bool { return false },
		RunInMaintenanceWindow: func() bool { return true },
	})
}

// labelRefinementChainTimeout bounds the whole dry-run chain so a hung op can
// never leak the detached goroutine forever. It exceeds the sum of the two ops'
// own timeouts (rebuild-gold-labels 60m + calibrate-composite 30m) plus slack.
const labelRefinementChainTimeout = 2 * time.Hour

// runLabelRefinementChain enqueues dedup.rebuild-gold-labels then
// dedup.calibrate-composite, both in DRY-RUN mode (empty params ⇒ Apply=false),
// waiting for each to reach a terminal state before starting the next, and logs
// one summary line. It runs detached from TriggerFn and writes NOTHING: neither
// op mutates state without apply=true, which is never sent on this path.
func (ts *TaskScheduler) runLabelRefinementChain() {
	ctx, cancel := context.WithTimeout(context.Background(), labelRefinementChainTimeout)
	defer cancel()
	// Cancel promptly on scheduler shutdown so we don't outlive the process.
	if ts.shutdown != nil {
		go func() {
			select {
			case <-ts.shutdown:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	rebuildID, err := ts.deps.OpRegistry.EnqueueOp(ctx, "dedup.rebuild-gold-labels", labelRefinementRebuildParams{})
	if err != nil {
		slog.Warn("label_refinement: failed to enqueue dedup.rebuild-gold-labels", "err", err)
		return
	}
	if row := ts.WaitForOperation(ctx, rebuildID); row == nil || row.Status != "completed" {
		slog.Warn("label_refinement: rebuild-gold-labels did not complete cleanly; aborting chain", "op", rebuildID)
		return
	}

	calibrateID, err := ts.deps.OpRegistry.EnqueueOp(ctx, "dedup.calibrate-composite", labelRefinementCalibrateParams{})
	if err != nil {
		slog.Warn("label_refinement: failed to enqueue dedup.calibrate-composite", "err", err)
		return
	}
	if row := ts.WaitForOperation(ctx, calibrateID); row == nil || row.Status != "completed" {
		slog.Warn("label_refinement: calibrate-composite did not complete cleanly", "op", calibrateID)
		return
	}

	slog.Info("label_refinement: dry-run chain complete (report only, nothing written)",
		"rebuild_op", rebuildID, "calibrate_op", calibrateID)
}
