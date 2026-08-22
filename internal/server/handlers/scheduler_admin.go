// file: internal/server/handlers/scheduler_admin.go
// version: 1.0.0
// guid: c8cffbf7-1356-4211-ad0e-28307563161b
// last-edited: 2026-08-22

// TODO.md scheduler-config item (was line 4563 as of commit 46628240): the
// task-scheduler endpoints (list/run/configure tasks) and the
// maintenance-window endpoints (trigger/inspect/configure) used to live on
// internal/server/handlers/operations.Handler alongside the v1
// operations-record CRUD that is being retired elsewhere in this backlog.
// Extracted into their own SchedulerHandler here so that retirement does not
// read as "delete task scheduling" -- these six routes are scheduler
// configuration/control, not operation records, and were never coupled to
// the v1 operations table.
//
// This split changes no route paths, request/response shapes, or
// permissions (auth.PermSettingsManage is used uniformly across all six,
// both before and after) -- it is purely an internal code-organization move.

package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/gin-gonic/gin"
)

// Scheduler is the narrow *scheduler.TaskScheduler subset used by the task
// and maintenance-window endpoints below. It mirrors
// operations.Scheduler (same five methods) but is declared separately here
// rather than imported: package operations already imports package handlers
// (for MaintenanceWindowConfigReq), so the dependency cannot run the other
// way without an import cycle. Go's structural typing means the same
// concrete *scheduler.TaskScheduler satisfies both declarations.
type Scheduler interface {
	ListTasks() []scheduler.TaskInfo
	RunTaskManual(name string) (*database.Operation, error)
	RunMaintenanceWindow(ctx context.Context) error
	IsMaintenanceRunning() bool
	GetLastMaintenanceRunDate() string
}

// SchedulerHandler hosts the task-scheduler and maintenance-window HTTP
// endpoints: list/run/configure tasks, and trigger/inspect/configure the
// nightly maintenance window.
type SchedulerHandler struct {
	// store is used only to persist config changes (SaveConfigToDatabase).
	// database.SettingsStore is the exact four-method interface that call
	// needs -- see operations.OperationsStore's doc for why it is embedded
	// rather than method-listed elsewhere in this codebase; the same
	// reasoning applies here, one level narrower since this handler needs
	// nothing else from the store.
	store database.SettingsStore

	// getScheduler resolves the scheduler lazily, at request time. The
	// *Server.scheduler field is assigned in Start() -- AFTER NewServer →
	// setupRoutes → wireHandlers runs -- so snapshotting it at wire time would
	// always capture nil (mirrors operations.Handler.getScheduler, which this
	// handler's routes used to share). The provider closure performs the
	// typed-nil guard so a nil *scheduler.TaskScheduler is never boxed into a
	// non-nil interface (which would defeat the in-method nil checks below).
	getScheduler func() Scheduler
}

// NewSchedulerHandler constructs a SchedulerHandler. getScheduler is a lazy
// provider (see the field doc) rather than a plain Scheduler value because
// *Server.scheduler is populated after wire time.
func NewSchedulerHandler(store database.SettingsStore, getScheduler func() Scheduler) *SchedulerHandler {
	return &SchedulerHandler{
		store:        store,
		getScheduler: getScheduler,
	}
}

// resolveScheduler returns the live scheduler via the lazy provider, or nil if
// no provider was supplied (e.g. some unit tests) or the provider yields nil.
func (h *SchedulerHandler) resolveScheduler() Scheduler {
	if h.getScheduler == nil {
		return nil
	}
	return h.getScheduler()
}

// --- Tasks ---

// ListTasks returns all registered tasks with their status and schedule.
// Implements GET /tasks.
func (h *SchedulerHandler) ListTasks(c *gin.Context) {
	sched := h.resolveScheduler()
	if sched == nil {
		httputil.RespondWithInternalError(c, "scheduler not initialized")
		return
	}
	httputil.RespondWithOK(c, sched.ListTasks())
}

// RunTask triggers a task by name. Implements POST /tasks/:name/run.
func (h *SchedulerHandler) RunTask(c *gin.Context) {
	sched := h.resolveScheduler()
	if sched == nil {
		httputil.RespondWithInternalError(c, "scheduler not initialized")
		return
	}
	name := c.Param("name")
	op, err := sched.RunTaskManual(name)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if op == nil {
		httputil.RespondWithSuccess(c, 202, gin.H{"message": "task triggered"})
		return
	}
	httputil.RespondWithSuccess(c, 202, op)
}

// taskConfigBinding names, for one task, the config field that each PUT
// /tasks/:name body field writes to. A nil pointer means the task has no such
// knob, and the request is rejected rather than acknowledged.
//
// The point of the table is that "which fields does this task accept" and
// "where does each field get written" are the same fact, stated once. The
// hand-written switch this replaced stated them separately: the case label
// implied acceptance while the if-bodies did the writing, and nothing tied the
// 200 response to whether an assignment had actually happened. Cases that bound
// only a subset therefore reported success for the fields they dropped.
// Measured 2026-08-16 against production: PUT /tasks/purge_deleted
// {"enabled":false} answered 200 {"message":"task config updated"} and the task
// still read back enabled=true. Same shape as the iTunes backfill done-flag — a
// write-only field that reports success.
type taskConfigBinding struct {
	enabled   *bool
	interval  *int
	onStartup *bool
	inWindow  *bool

	// hints explains, per rejected field, what to set instead. Some knobs are
	// derived rather than absent — purge_deleted's enabled is
	// PurgeSoftDeletedAfterDays > 0 — so the useful answer names the real config
	// key instead of reporting the field as merely unsupported.
	hints map[string]string

	// foldLegacy normalizes a deprecated config key into the per-task fields
	// above, and runs once before any caller write is applied.
	//
	// A binding pointer is only honest if the task's trigger reads that field
	// ALONE. library_scan's does not: IsEnabled is
	// `Scheduled.LibraryScan.Enabled || ScanOnStartup`, kept that way on purpose
	// so nobody who only ever set the legacy scan_on_startup key loses their
	// startup scan. The side effect is that while the legacy key is set, writing
	// enabled=false lands in the struct and the task still reports enabled — a
	// 200 for a change the caller cannot observe, which is the exact defect this
	// endpoint is being fixed for.
	//
	// Folding is lossless by construction: with the legacy key set, both
	// IsEnabled() and RunOnStart() already evaluate true, so writing true into
	// both per-task fields and clearing the legacy key preserves each getter's
	// value while making the per-task fields the only thing that decides them.
	// The caller's write then lands on a field that governs.
	foldLegacy func()
}

// accepted lists the body fields this task can actually apply, so the error
// message is derived from the same pointers that do the writing and cannot
// drift away from them.
func (b taskConfigBinding) accepted() []string {
	var out []string
	if b.enabled != nil {
		out = append(out, "enabled")
	}
	if b.interval != nil {
		out = append(out, "interval_minutes")
	}
	if b.onStartup != nil {
		out = append(out, "run_on_startup")
	}
	if b.inWindow != nil {
		out = append(out, "run_in_maintenance_window")
	}
	return out
}

// describeRejection renders one unsettable field, with its hint when there is
// one.
func (b taskConfigBinding) describeRejection(field string) string {
	if hint := b.hints[field]; hint != "" {
		return fmt.Sprintf("%s (%s)", field, hint)
	}
	return field
}

// fixedScheduleHint covers tasks whose scheduler definition hardcodes
// IsEnabled/GetInterval/RunOnStart as constants rather than reading config.
const fixedScheduleHint = "this task's schedule is fixed in code"

// bindingForTask maps a task name to the config fields its schedule really
// reads, mirroring the TaskDefinition triggers in internal/scheduler/tasks.go.
// The second return is false for a task that has no configurable schedule at
// all.
func bindingForTask(name string) (taskConfigBinding, bool) {
	sched := &config.AppConfig.Scheduled
	maint := &config.AppConfig.Maintenance

	// full is for tasks whose TaskDefinition reads a ScheduledTaskConfig for
	// enabled/interval/on-startup plus a Maintenance flag for the window.
	full := func(t *config.ScheduledTaskConfig, window *bool) taskConfigBinding {
		return taskConfigBinding{
			enabled:   &t.Enabled,
			interval:  &t.Interval,
			onStartup: &t.OnStartup,
			inWindow:  window,
		}
	}

	// windowOnly is for tasks whose only real knob is whether the maintenance
	// window runs them.
	windowOnly := func(window *bool, hints map[string]string) taskConfigBinding {
		return taskConfigBinding{inWindow: window, hints: hints}
	}

	switch name {
	case "dedup_refresh":
		return full(&sched.DedupRefresh, &maint.DedupRefresh), true
	case "author_split_scan":
		return full(&sched.AuthorSplit, &maint.AuthorSplit), true
	case "db_optimize":
		return full(&sched.DbOptimize, &maint.DbOptimize), true
	case "metadata_refresh":
		return full(&sched.MetadataRefresh, &maint.MetadataRefresh), true
	case "series_prune":
		return full(&sched.SeriesPrune, &maint.SeriesPrune), true
	case "library_scan":
		// All four knobs are real: the task reads Scheduled.LibraryScan for
		// enabled/interval/on-startup. The previous switch bound only the
		// maintenance-window flag and silently dropped the other three.
		b := full(&sched.LibraryScan, &maint.LibraryScan)
		// ...but two of those three are OR'd with the legacy scan_on_startup key
		// in tasks.go, so they only govern once it is folded away. See foldLegacy.
		b.foldLegacy = func() {
			if !config.AppConfig.ScanOnStartup {
				return
			}
			sched.LibraryScan.Enabled = true
			sched.LibraryScan.OnStartup = true
			config.AppConfig.ScanOnStartup = false
		}
		return b, true
	case "reconcile_scan":
		// interval_minutes and run_on_startup are read by the task definition
		// but were not bound before, so they were dropped silently too.
		return full(&sched.Reconcile, &maint.Reconcile), true
	case "itunes_sync":
		return taskConfigBinding{
			enabled:  &config.AppConfig.ITunes.SyncEnabled,
			interval: &config.AppConfig.ITunes.SyncInterval,
			hints: map[string]string{
				"run_on_startup":            "iTunes sync does not run on startup",
				"run_in_maintenance_window": "iTunes sync is not part of the maintenance window",
			},
		}, true
	case "purge_deleted":
		return windowOnly(&maint.PurgeDeleted, map[string]string{
			"enabled":          "derived from purge_soft_deleted_after_days; set that key via PUT /config, 0 disables the purge",
			"interval_minutes": "fixed at 6h while purge_soft_deleted_after_days > 0",
			"run_on_startup":   "derived from purge_soft_deleted_after_days",
		}), true
	case "purge_old_logs":
		return windowOnly(&maint.PurgeOldLogs, map[string]string{
			"enabled":          "derived from log_retention_days; set that key via PUT /config, 0 disables the purge",
			"interval_minutes": "fixed at 7d",
			"run_on_startup":   fixedScheduleHint,
		}), true
	case "tombstone_cleanup":
		return windowOnly(&maint.TombstoneCleanup, map[string]string{
			"enabled":          fixedScheduleHint,
			"interval_minutes": "fixed at 24h",
			"run_on_startup":   fixedScheduleHint,
		}), true
	case "library_organize":
		return windowOnly(&maint.LibraryOrganize, map[string]string{
			"enabled":          fixedScheduleHint,
			"interval_minutes": "library_organize runs only inside the maintenance window",
			"run_on_startup":   fixedScheduleHint,
		}), true
	}
	return taskConfigBinding{}, false
}

// UpdateTaskConfig updates schedule config for a task. Implements PUT
// /tasks/:name.
//
// A field the named task cannot apply is a 400 naming what the task does
// accept, never a 200. See taskConfigBinding for why.
func (h *SchedulerHandler) UpdateTaskConfig(c *gin.Context) {
	name := c.Param("name")

	var req struct {
		Enabled                *bool `json:"enabled"`
		IntervalMinutes        *int  `json:"interval_minutes"`
		RunOnStartup           *bool `json:"run_on_startup"`
		RunInMaintenanceWindow *bool `json:"run_in_maintenance_window"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	binding, ok := bindingForTask(name)
	if !ok {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("task %q config is not configurable", name))
		return
	}

	// Resolve every provided field against the binding BEFORE applying any of
	// them, so a rejected field cannot leave a half-written config behind.
	var writes []func()
	var rejected []string
	bindBool := func(field string, v, target *bool) {
		if v == nil {
			return
		}
		if target == nil {
			rejected = append(rejected, binding.describeRejection(field))
			return
		}
		writes = append(writes, func() { *target = *v })
	}
	bindInt := func(field string, v, target *int) {
		if v == nil {
			return
		}
		if target == nil {
			rejected = append(rejected, binding.describeRejection(field))
			return
		}
		writes = append(writes, func() { *target = *v })
	}

	bindBool("enabled", req.Enabled, binding.enabled)
	bindInt("interval_minutes", req.IntervalMinutes, binding.interval)
	bindBool("run_on_startup", req.RunOnStartup, binding.onStartup)
	bindBool("run_in_maintenance_window", req.RunInMaintenanceWindow, binding.inWindow)

	if len(rejected) > 0 {
		accepted := "no schedule fields"
		if fields := binding.accepted(); len(fields) > 0 {
			accepted = strings.Join(fields, ", ")
		}
		httputil.RespondWithBadRequest(c, fmt.Sprintf(
			"task %q cannot set %s; it accepts %s",
			name, strings.Join(rejected, "; "), accepted))
		return
	}

	// Fold before applying, and only once a write is actually going to land, so
	// a rejected or empty request never rewrites config as a side effect.
	if len(writes) > 0 && binding.foldLegacy != nil {
		binding.foldLegacy()
	}
	for _, write := range writes {
		write()
	}

	// Persist to database. A failure here means the change is live in memory but
	// will not survive a restart, which is not "task config updated" — report it
	// rather than logging a warning under a 200.
	if h.store != nil {
		if err := config.SaveConfigToDatabase(h.store); err != nil {
			slog.Error("Failed to save task config", "task", name, "err", err)
			httputil.RespondWithInternalError(c, fmt.Sprintf(
				"task %q config applied in memory but could not be persisted: %v", name, err))
			return
		}
	}

	httputil.RespondWithOK(c, gin.H{"message": "task config updated"})
}

// --- Maintenance window ---

// RunMaintenanceWindowNow triggers the full maintenance window sequence
// immediately. Implements POST /maintenance-window/run.
func (h *SchedulerHandler) RunMaintenanceWindowNow(c *gin.Context) {
	sched := h.resolveScheduler()
	if sched == nil {
		httputil.RespondWithInternalError(c, "scheduler not initialized")
		return
	}
	ctx := context.WithValue(c.Request.Context(), scheduler.IgnoreWindowKey, true)
	if err := sched.RunMaintenanceWindow(ctx); err != nil {
		httputil.InternalError(c, "failed to run maintenance", err)
		return
	}
	httputil.RespondWithSuccess(c, 202, gin.H{"message": "maintenance window triggered"})
}

// GetMaintenanceWindowStatus returns current schedule config and live running
// status. Implements GET /maintenance-window/status.
func (h *SchedulerHandler) GetMaintenanceWindowStatus(c *gin.Context) {
	sched := h.resolveScheduler()
	if sched == nil {
		httputil.RespondWithInternalError(c, "scheduler not initialized")
		return
	}
	cfg := config.AppConfig
	httputil.RespondWithOK(c, gin.H{
		"enabled":           cfg.Maintenance.Enabled,
		"window_start":      cfg.Maintenance.WindowStart,
		"window_end":        cfg.Maintenance.WindowEnd,
		"last_run_date":     sched.GetLastMaintenanceRunDate(),
		"next_run_estimate": calculateNextWindowRun(cfg.Maintenance.WindowStart),
		"currently_running": sched.IsMaintenanceRunning(),
	})
}

// calculateNextWindowRun returns the next RFC3339 timestamp when startHour
// occurs locally.
func calculateNextWindowRun(startHour int) string {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), startHour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Format(time.RFC3339)
}

// UpdateMaintenanceWindowConfig persists maintenance window schedule settings.
// Implements PUT /maintenance-window/config.
func (h *SchedulerHandler) UpdateMaintenanceWindowConfig(c *gin.Context) {
	var req MaintenanceWindowConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if req.WindowStart < 0 || req.WindowStart > 23 || req.WindowEnd < 0 || req.WindowEnd > 23 {
		httputil.RespondWithBadRequest(c, "window_start and window_end must be 0-23")
		return
	}
	config.AppConfig.Maintenance.Enabled = req.Enabled
	config.AppConfig.Maintenance.WindowStart = req.WindowStart
	config.AppConfig.Maintenance.WindowEnd = req.WindowEnd
	if h.store != nil {
		if err := config.SaveConfigToDatabase(h.store); err != nil {
			httputil.InternalError(c, "failed to save maintenance window config", err)
			return
		}
	}
	httputil.RespondWithOK(c, gin.H{"ok": true})
}
