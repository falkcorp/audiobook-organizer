// file: internal/scheduler/scheduler.go
// version: 1.2.0
// guid: 3f7a9c21-b4d8-4e05-a6f2-8c1d0e3b7a94
// last-edited: 2026-08-11

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
		"library_size_refresh",
		"db_optimize",
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

		// Start scheduled ticker if interval > 0 and enabled
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
