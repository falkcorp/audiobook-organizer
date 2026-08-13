// file: internal/scheduler/scheduler.go
// version: 1.4.0
// guid: 3f7a9c21-b4d8-4e05-a6f2-8c1d0e3b7a94
// last-edited: 2026-08-13

// Package scheduler implements the unified task scheduling system.
// TaskScheduler manages all registered tasks, their schedules, and manual
// triggers. It is decoupled from *server.Server via SchedulerDeps.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// SchedulerDeps contains the external dependencies the TaskScheduler needs.
// Pass this to NewTaskScheduler instead of a *Server pointer so the scheduler
// package does not import the server package.
type SchedulerDeps struct {
	// Store returns the live database.Store. May return nil before the DB
	// is fully initialised; callers must nil-check.
	Store func() database.Store

	// OpRegistry is the UOS-02 operation registry used to enqueue background
	// operations. Required; must not be nil when Start() is called.
	OpRegistry *opsregistry.Registry

	// HasDedupEngine returns true when a dedup engine is wired up. Used by the
	// dedup_llm_review task's IsEnabled guard.
	HasDedupEngine func() bool

	// HasMetadataFetchSvc returns true when a metadata fetch service is wired.
	// Used by isbn_enrichment and metadata_upgrade IsEnabled guards.
	HasMetadataFetchSvc func() bool

	// HasActivitySvc returns true when an activity service is wired. Used by
	// the cleanup_activity_log task's IsEnabled guard.
	HasActivitySvc func() bool

	// PollBatches calls the batch poller's Poll method. May be nil when no
	// batch poller is configured — the batch_poller task will no-op.
	PollBatches func(ctx context.Context) (int, error)

	// HasBatchPoller returns true when a batch poller is available.
	HasBatchPoller func() bool
}

// TaskDefinition defines a registered task in the unified task system.
type TaskDefinition struct {
	Name        string // unique key: "library_scan", "itunes_sync", etc.
	Description string // human-readable
	Category    string // "maintenance", "library", "sync"
	// TriggerFn creates and enqueues an operation, returning it.
	TriggerFn func(source string) (*database.Operation, error)
	// Config accessors (read from AppConfig at runtime)
	IsEnabled              func() bool
	GetInterval            func() time.Duration // 0 = manual only
	RunOnStart             func() bool
	RunInMaintenanceWindow func() bool // whether this task runs during the maintenance window
}

// TaskInfo is the API-facing view of a registered task.
type TaskInfo struct {
	Name                   string  `json:"name"`
	Description            string  `json:"description"`
	Category               string  `json:"category"`
	Enabled                bool    `json:"enabled"`
	IntervalMinutes        int     `json:"interval_minutes"`
	RunOnStartup           bool    `json:"run_on_startup"`
	RunInMaintenanceWindow bool    `json:"run_in_maintenance_window"`
	LastRun                *string `json:"last_run,omitempty"`
	IsRunning              bool    `json:"is_running"`
}

// TaskScheduler manages all registered tasks, their schedules, and manual triggers.
type TaskScheduler struct {
	deps               SchedulerDeps
	tasks              map[string]*TaskDefinition
	order              []string // insertion order for listing
	lastRun            map[string]time.Time
	mu                 sync.RWMutex
	shutdown           chan struct{}
	maintenanceOrder   []string
	lastMaintenanceRun time.Time
	// previousRun maps a task name to the v2 operation id it last enqueued.
	// Only tasks that need to skip a tick while their own previous run is
	// still queued/running use it (currently library_scan, whose full walk can
	// outlast its interval on a large library). Guarded by mu.
	previousRun map[string]string
}

// previousRunID returns the v2 operation id this task last enqueued, or "" if
// it has not enqueued one since process start.
func (ts *TaskScheduler) previousRunID(name string) string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.previousRun[name]
}

// setPreviousRunID records the v2 operation id a task just enqueued.
func (ts *TaskScheduler) setPreviousRunID(name, opID string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.previousRun == nil {
		ts.previousRun = make(map[string]string)
	}
	ts.previousRun[name] = opID
}

// NewTaskScheduler creates a scheduler and registers all known tasks.
func NewTaskScheduler(deps SchedulerDeps) *TaskScheduler {
	ts := &TaskScheduler{
		deps:    deps,
		tasks:   make(map[string]*TaskDefinition),
		lastRun: make(map[string]time.Time),
	}
	ts.registerAllTasks()
	ts.maintenanceOrder = []string{
		"reconcile_scan",
		"dedup_refresh",
		"dedup_llm_review",
		"author_split_scan",
		"series_prune",
		"isbn_enrichment",
		"metadata_upgrade",
		"tombstone_cleanup",
		"purge_deleted",
		"purge_old_logs",
		"cleanup_activity_log",
		"cleanup_old_backups",
		// These three declare RunInMaintenanceWindow: true unconditionally but
		// were absent from this list, so the window op never iterated them and
		// they had never run. They are cheap and they reclaim disk, so they sit
		// with the other cleanups rather than behind the expensive walks:
		//   temp_file_cleanup — orphaned *.tmp.m4b/*.tmp.m4a from crashed ffmpeg
		//   trash_cleanup     — trashed versions past their 14-day TTL
		//   archive_sweep     — soft-deleted books past the 30-day retention
		// Every one of those is an unbounded on-disk leak while it never runs.
		"temp_file_cleanup",
		"trash_cleanup",
		"archive_sweep",
		"library_size_refresh",
		"db_optimize",
		// library_organize was the fourth task declaring a maintenance-window
		// toggle (config.Maintenance.LibraryOrganize) while missing from this
		// list — the same dead-config shape documented for library_scan below.
		// It goes near the end because it MUTATES FILES ON DISK and is expensive;
		// putting it earlier would let a long organize run starve the cleanup and
		// dedup work when the window closes. It stays gated behind its own config
		// toggle, so adding it here does not by itself start moving files.
		"library_organize",
		// library_scan is LAST on purpose. It was missing from this list
		// entirely, which made maintenance.library_scan unreachable dead
		// config: the window op iterates MaintenanceOrder(), so a user who
		// flipped the toggle got nothing. It goes at the end rather than the
		// front because the window op breaks out of the loop the moment the
		// window closes — a full library walk parked in front of everything
		// else would starve dedup/purge/optimize on the same night. The
		// interval ticker (scheduled.library_scan) is the primary discovery
		// mechanism now; this entry exists so the toggle actually does
		// something for anyone who prefers scans confined to the window.
		"library_scan",
	}
	return ts
}

// RegisterTask registers a task definition. This is exported so that external
// packages (e.g. plugins) can add tasks after construction.
func (ts *TaskScheduler) RegisterTask(def TaskDefinition) {
	ts.tasks[def.Name] = &def
	ts.order = append(ts.order, def.Name)
}

// registerTask is the internal alias used during construction.
func (ts *TaskScheduler) registerTask(def TaskDefinition) {
	ts.RegisterTask(def)
}

// Start launches background goroutines for all scheduled and startup tasks.
func (ts *TaskScheduler) Start(shutdown chan struct{}, wg *sync.WaitGroup) {
	ts.shutdown = shutdown
	ts.loadLastMaintenanceRun()

	for _, name := range ts.order {
		task := ts.tasks[name]

		// Run on startup if configured
		if task.RunOnStart != nil && task.RunOnStart() && task.IsEnabled() {
			taskName := name
			go func() {
				slog.Info("Running startup task", "taskName", taskName)
				if op, err := ts.RunTask(taskName); err != nil {
					slog.Warn("Startup task failed", "taskName", taskName, "err", err)
				} else if op != nil {
					slog.Info("Startup task started operation", "taskName", taskName, "op", op.ID)
				}
			}()
		}

		// Start scheduled ticker if interval > 0 and enabled.
		//
		// The `else` arms below exist because this condition used to fail
		// SILENTLY. A task that was enabled but whose interval resolved to 0
		// got no ticker, no log line, and no other trace — from the outside it
		// was indistinguishable from a task deliberately turned off.
		//
		// That is not hypothetical. Measured on production 2026-08-12: every
		// task in the `scheduled.*` config block had interval 0, including
		// library_scan, whose shipped defaults are enabled=true / interval=360.
		// Stored zero values were overriding the viper defaults, so the ONLY
		// unattended discovery path for newly added books had never run — and
		// the only evidence was the absence of a log line, which nobody can
		// grep for. Four unrelated tasks did get tickers, which made the
		// scheduler look healthy.
		//
		// A scheduler that drops a task must say which task and why.
		if task.IsEnabled() && task.GetInterval() > 0 {
			interval := task.GetInterval()
			taskName := name
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						if op, err := ts.RunTask(taskName); err != nil {
							slog.Warn("Scheduled task failed", "taskName", taskName, "err", err)
						} else if op != nil {
							slog.Info("Scheduled task started operation", "taskName", taskName, "op", op.ID)
						}
					case <-shutdown:
						return
					}
				}
			}()
			slog.Info("Scheduled task interval", "taskName", taskName, "interval", interval)
		} else if task.IsEnabled() && ts.reachableViaMaintenanceWindow(name) {
			// Enabled with no ticker, but the nightly maintenance window WILL
			// reach it. Not a defect — this is how the cleanup/backfill jobs are
			// meant to run, and warning about them buries the ones that are
			// genuinely dead.
			//
			// The first version of this branch did not exist, and the resulting
			// WARN fired for 13 of 18 tasks on the 2026-08-12 production boot.
			// Seven of those thirteen were healthy maintenance-window tasks. A
			// diagnostic that cries wolf about half the roster is how the real
			// six stayed invisible.
			slog.Info("Scheduled task has no timer; runs in the nightly maintenance window",
				"taskName", name)
		} else if task.IsEnabled() {
			// Enabled, no interval, and NOT reachable by the maintenance window:
			// the task can never run by itself. This is the genuinely silent
			// case — the operator sees it listed as enabled and nothing happens.
			//
			// Note the two distinct ways to land here, because the fix differs:
			//   1. no interval configured  -> set scheduled.<task>.interval
			//   2. declares RunInMaintenanceWindow but is absent from
			//      maintenanceOrder -> the toggle is dead config; the task must
			//      be added to the list in NewTaskScheduler
			// Cause 2 hit library_scan once (see the maintenanceOrder comment),
			// then recurred on four more tasks: library_organize,
			// temp_file_cleanup, trash_cleanup and archive_sweep.
			slog.Warn("Scheduled task is ENABLED but can NEVER run — it has no interval and the "+
				"maintenance window does not reach it; set scheduled.<task>.interval, or add the "+
				"task to maintenanceOrder if it is meant to run in the nightly window",
				"taskName", name,
				"interval", task.GetInterval(),
				"declaresMaintenanceWindow", task.RunInMaintenanceWindow != nil && task.RunInMaintenanceWindow(),
				"inMaintenanceOrder", ts.inMaintenanceOrder(name))
		} else {
			// Deliberately off. Logged at Debug so the roster is complete for
			// anyone diagnosing "why did nothing happen" without adding noise.
			slog.Debug("Scheduled task disabled", "taskName", name)
		}
	}

	// Maintenance window checker — runs every 60 seconds
	if config.AppConfig.Maintenance.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			slog.Info("Maintenance window enabled", "windowStart", config.AppConfig.Maintenance.WindowStart, "windowEnd", config.AppConfig.Maintenance.WindowEnd)
			for {
				select {
				case <-ticker.C:
					if IsInMaintenanceWindow() && !ts.hasRunToday() {
						slog.Info("Maintenance window open — starting maintenance run")
						if err := ts.RunMaintenanceWindow(context.Background()); err != nil {
							slog.Warn("Maintenance window failed", "error", err)
						}
					}
				case <-shutdown:
					return
				}
			}
		}()
	}
}

// RunTask triggers a scheduled task by name (source = TriggerScheduled).
func (ts *TaskScheduler) RunTask(name string) (*database.Operation, error) {
	return ts.runTask(name, operations.TriggerScheduled)
}

// RunTaskManual triggers a task as a user-initiated action (source = TriggerManual).
// Task functions can gate AlwaysShow activity-feed entries on operations.IsManual(ctx).
func (ts *TaskScheduler) RunTaskManual(name string) (*database.Operation, error) {
	return ts.runTask(name, operations.TriggerManual)
}

// RunTaskWithSource triggers a task with an explicit source string. Intended
// for use by the maintenance window operation which needs fine-grained control
// over the trigger source.
func (ts *TaskScheduler) RunTaskWithSource(name, source string) (*database.Operation, error) {
	return ts.runTask(name, source)
}

func (ts *TaskScheduler) runTask(name, source string) (*database.Operation, error) {
	ts.mu.RLock()
	task, ok := ts.tasks[name]
	ts.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown task: %s", name)
	}

	op, err := task.TriggerFn(source)
	if err != nil {
		return nil, err
	}

	ts.mu.Lock()
	ts.lastRun[name] = time.Now()
	ts.mu.Unlock()

	return op, nil
}

// ListTasks returns info about all registered tasks.
func (ts *TaskScheduler) ListTasks() []TaskInfo {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	result := make([]TaskInfo, 0, len(ts.order))
	for _, name := range ts.order {
		task := ts.tasks[name]
		info := TaskInfo{
			Name:            task.Name,
			Description:     task.Description,
			Category:        task.Category,
			Enabled:         task.IsEnabled(),
			IntervalMinutes: int(task.GetInterval() / time.Minute),
			RunOnStartup:    task.RunOnStart(),
		}
		if task.RunInMaintenanceWindow != nil {
			info.RunInMaintenanceWindow = task.RunInMaintenanceWindow()
		}
		if t, ok := ts.lastRun[name]; ok {
			s := t.Format(time.RFC3339)
			info.LastRun = &s
		}
		info.IsRunning = ts.isTaskRunning(info.Name)
		result = append(result, info)
	}
	return result
}

// GetTask returns the definition for a named task.
func (ts *TaskScheduler) GetTask(name string) (*TaskDefinition, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	task, ok := ts.tasks[name]
	return task, ok
}

// Tasks returns the task map (read-only). Used by the maintenance window op.
func (ts *TaskScheduler) Tasks() map[string]*TaskDefinition {
	return ts.tasks
}

// MaintenanceOrder returns the ordered list of maintenance task names.
func (ts *TaskScheduler) MaintenanceOrder() []string {
	return ts.maintenanceOrder
}

// inMaintenanceOrder reports whether the nightly window op will even consider
// this task. The op iterates MaintenanceOrder() (see
// internal/server/scheduler_maintenance_window_op.go), so a task absent from
// that list is unreachable no matter what RunInMaintenanceWindow returns —
// which is exactly how four tasks came to declare a maintenance-window toggle
// that did nothing.
func (ts *TaskScheduler) inMaintenanceOrder(name string) bool {
	for _, n := range ts.maintenanceOrder {
		if n == name {
			return true
		}
	}
	return false
}

// reachableViaMaintenanceWindow reports whether the nightly window can actually
// run this task. BOTH conditions must hold, which is the whole point: the window
// op's guard is `IsEnabled() && RunInMaintenanceWindow()` applied only to names
// it iterates, so declaring the toggle without list membership — or list
// membership without the toggle — runs nothing.
//
// This deliberately does NOT check config.Maintenance.Enabled. A task that is
// correctly wired but sitting behind a disabled maintenance window is an
// operator choice, not a wiring defect, and conflating the two would restore the
// over-reporting this function exists to prevent.
func (ts *TaskScheduler) reachableViaMaintenanceWindow(name string) bool {
	if !ts.inMaintenanceOrder(name) {
		return false
	}
	task, ok := ts.tasks[name]
	if !ok || task.RunInMaintenanceWindow == nil {
		return false
	}
	return task.RunInMaintenanceWindow()
}

// WaitForOperation polls until an operation completes or the context is canceled.
func (ts *TaskScheduler) WaitForOperation(ctx context.Context, opID string) {
	store := ts.deps.Store()
	if store == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			op, err := store.GetOperationByID(opID)
			if err != nil {
				return
			}
			if op.Status == "completed" || op.Status == "failed" || op.Status == "canceled" {
				return
			}
		}
	}
}
