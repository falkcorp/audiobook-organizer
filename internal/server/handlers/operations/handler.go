// file: internal/server/handlers/operations/handler.go
// version: 1.7.0
// guid: 1b7fbd86-cdda-4921-b2d0-786f5cadb438
// last-edited: 2026-08-16

// Package operations hosts the background-operation HTTP handlers extracted
// from the server package: the long-running scan / organize / optimize /
// transcode starters, generic operation status / cancel / listing / logs /
// result / changes / revert, maintenance chores (optimize DB, sweep tombstones,
// audit file consistency, clear stale, delete history, set internal flag), the
// task-scheduler endpoints, and the maintenance-window endpoints.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (OperationsStore, OperationsRegistry, Scheduler, ScanCanceler,
// AIScanLister) and three injected funcs (collectStale, preflightUndo, revert)
// that wrap server-private helpers, so package operations never imports package
// server. preflightUndo wraps undo.PreflightUndoConflicts and revert wraps
// audiobooks.NewRevertService(...).RevertOperation; both consume a full
// database.Store opaquely, so the controller closes over s.Store() rather than
// the handler enumerating "methods used".

package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/sweep"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/gin-gonic/gin"
)

// Handler hosts the operations-domain HTTP endpoints.
type Handler struct {
	store    OperationsStore
	registry OperationsRegistry
	// getScheduler resolves the scheduler lazily, at request time. The
	// *Server.scheduler field is assigned in Start() — AFTER NewServer →
	// setupRoutes → wireHandlers runs — so snapshotting it at wire time would
	// always capture nil (the old s.listTasks/s.runTask methods read s.scheduler
	// at call time, which this preserves). The provider closure performs the
	// typed-nil guard so a nil *scheduler.TaskScheduler is never boxed into a
	// non-nil interface (which would defeat the in-method nil checks).
	getScheduler func() Scheduler
	pipeline     ScanCanceler
	scanStore    AIScanLister

	// collectStale wraps the server-private *Server.collectStaleOperations,
	// which also stays in package server (called from server_lifecycle.go). The
	// controller passes s.collectStaleOperations.
	collectStale func(timeout time.Duration) ([]database.Operation, error)

	// preflightUndo wraps undo.PreflightUndoConflicts(s.Store(), id). The undo
	// report type is an importable alias, but PreflightUndoConflicts consumes a
	// full database.Store opaquely, so the controller closes over s.Store().
	preflightUndo func(id string) (*undo.UndoConflictReport, error)

	// revert wraps audiobooks.NewRevertService(s.Store()).RevertOperation(id).
	// Same opaque-store rationale as preflightUndo.
	revert func(id string) error
}

// New constructs an operations Handler from its dependencies. getScheduler is a
// lazy provider (see the field doc) rather than a plain Scheduler value because
// *Server.scheduler is populated after wire time.
func New(
	store OperationsStore,
	registry OperationsRegistry,
	getScheduler func() Scheduler,
	pipeline ScanCanceler,
	scanStore AIScanLister,
	collectStale func(timeout time.Duration) ([]database.Operation, error),
	preflightUndo func(id string) (*undo.UndoConflictReport, error),
	revert func(id string) error,
) *Handler {
	return &Handler{
		store:         store,
		registry:      registry,
		getScheduler:  getScheduler,
		pipeline:      pipeline,
		scanStore:     scanStore,
		collectStale:  collectStale,
		preflightUndo: preflightUndo,
		revert:        revert,
	}
}

// resolveScheduler returns the live scheduler via the lazy provider, or nil if
// no provider was supplied (e.g. some unit tests) or the provider yields nil.
func (h *Handler) resolveScheduler() Scheduler {
	if h.getScheduler == nil {
		return nil
	}
	return h.getScheduler()
}

// --- Operation starters ---

// --- Operation status / cancel ---

// operationV2ToLegacy converts a v2 registry row to the legacy Operation shape
// that the frontend's pollOperation helper expects (id, status, progress, etc.).
func operationV2ToLegacy(v2 *database.OperationV2Row) database.Operation {
	op := database.Operation{
		ID:           v2.ID,
		Type:         v2.DefID,
		Status:       v2.Status,
		Progress:     v2.ProgressCurrent,
		Total:        v2.ProgressTotal,
		Message:      v2.ProgressMessage,
		CreatedAt:    v2.QueuedAt,
		StartedAt:    v2.StartedAt,
		CompletedAt:  v2.CompletedAt,
		ErrorMessage: v2.ErrorMessage,
	}
	return op
}

// CancelOperation implements DELETE /operations/:id.
func (h *Handler) CancelOperation(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	id := c.Param("id")

	// Check if this is an AI scan operation — cancel via pipeline manager
	if h.pipeline != nil && h.scanStore != nil {
		scans, _ := h.scanStore.ListScans()
		for _, scan := range scans {
			if scan.OperationID == id {
				if err := h.pipeline.CancelScan(scan.ID); err != nil {
					slog.Info("canceloperation AI scan cancel warning", "scan", scan.ID, "err", err)
				}
				httputil.RespondWithNoContent(c)
				return
			}
		}
	}

	// Try cancel via v2 registry (running and queued v2 ops).
	if h.registry != nil {
		if err := h.registry.Cancel(id); err == nil {
			httputil.RespondWithNoContent(c)
			return
		}
	}

	// Fallback: force-update DB status (e.g., stale after restart)
	if dbErr := h.store.UpdateOperationStatus(id, "canceled", 0, 0, "force canceled (stale operation)"); dbErr != nil {
		httputil.InternalError(c, "failed to cancel operation", dbErr)
		return
	}
	httputil.RespondWithNoContent(c)
}

// ClearStaleOperations force-marks all pending/running/queued operations as
// failed. Implements POST /operations/clear-stale.
func (h *Handler) ClearStaleOperations(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	ops, err := h.store.GetRecentOperations(500)
	if err != nil {
		httputil.InternalError(c, "failed to get operations", err)
		return
	}

	cleared := 0
	for _, op := range ops {
		if op.Status == "pending" || op.Status == "running" || op.Status == "queued" {
			_ = h.store.UpdateOperationStatus(op.ID, "failed", 0, 0, "force cleared by user")
			cleared++
		}
	}

	httputil.RespondWithOK(c, gin.H{"cleared": cleared})
}

// DeleteOperationHistory deletes operations matching the given status(es).
// Query param: ?status=completed or ?status=failed or ?status=completed,failed
// Implements DELETE /operations/history.
func (h *Handler) DeleteOperationHistory(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	statusParam := c.Query("status")
	if statusParam == "" {
		httputil.RespondWithBadRequest(c, "status parameter required")
		return
	}

	statuses := strings.Split(statusParam, ",")
	// Only allow deleting terminal statuses
	allowed := map[string]bool{"completed": true, "failed": true, "canceled": true}
	for _, st := range statuses {
		if !allowed[st] {
			httputil.RespondWithBadRequest(c, fmt.Sprintf("cannot delete operations with status %q", st))
			return
		}
	}

	deleted, err := h.store.DeleteOperationsByStatus(statuses)
	if err != nil {
		httputil.InternalError(c, "failed to delete operations", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{"deleted": deleted})
}

// --- Maintenance chores ---

// OptimizeDatabase splits &-delimited author/narrator strings and re-extracts
// empty media info. Implements POST /operations/optimize-database.
func (h *Handler) OptimizeDatabase(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	books, err := h.store.GetAllBooksCore(0, 0)
	if err != nil {
		httputil.InternalError(c, "failed to get audiobooks", err)
		return
	}

	authorsSplit := 0
	narratorsSplit := 0

	for _, book := range books {
		// Split compound author names into individual book_authors
		if book.AuthorID != nil {
			author, err := h.store.GetAuthorByID(*book.AuthorID)
			if err == nil && author != nil && util.IsCompoundCreditName(author.Name) {
				names := splitMultipleNames(author.Name)
				if len(names) > 1 {
					var bookAuthors []database.BookAuthor
					for _, name := range names {
						a, err := h.store.GetAuthorByName(name)
						if err != nil || a == nil {
							a, err = h.store.CreateAuthor(name)
							if err != nil {
								continue
							}
						}
						bookAuthors = append(bookAuthors, database.BookAuthor{
							AuthorID: a.ID,
							Role:     "author",
						})
					}
					if len(bookAuthors) > 0 {
						if err := h.store.SetBookAuthors(book.ID, bookAuthors); err == nil {
							authorsSplit++
						}
					}
				}
			}
		}

		// Split compound narrator names into individual book_narrators
		if book.Narrator != nil && util.IsCompoundCreditName(*book.Narrator) {
			names := splitMultipleNames(*book.Narrator)
			if len(names) > 1 {
				var bookNarrators []database.BookNarrator
				for _, name := range names {
					n, err := h.store.GetNarratorByName(name)
					if err != nil || n == nil {
						n, err = h.store.CreateNarrator(name)
						if err != nil {
							continue
						}
					}
					bookNarrators = append(bookNarrators, database.BookNarrator{
						NarratorID: n.ID,
					})
				}
				if len(bookNarrators) > 0 {
					if err := h.store.SetBookNarrators(book.ID, bookNarrators); err == nil {
						narratorsSplit++
					}
				}
			}
		}
	}

	httputil.RespondWithOK(c, gin.H{
		"books_processed": len(books),
		"authors_split":   authorsSplit,
		"narrators_split": narratorsSplit,
	})
}

// splitMultipleNames splits an "A & B & C" string into its trimmed parts. It
// mirrors the server-package helper of the same name (a trivial pure function
// that was only used by this domain).
// splitMultipleNames delegates to util.SplitCreditNames. It was a verbatim
// second copy of the audiobooks package's " & "-only splitter; package
// operations deliberately does not import package audiobooks, so the shared
// implementation lives in the leaf package internal/util.
func splitMultipleNames(name string) []string {
	return util.SplitCreditNames(name)
}

// SweepTombstones implements POST /operations/sweep-tombstones.
func (h *Handler) SweepTombstones(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	result, err := sweep.SweepTombstones(h.store)
	if err != nil {
		httputil.InternalError(c, "failed to sweep tombstones", err)
		return
	}
	httputil.RespondWithOK(c, result)
}

// SetInternalFlag sets an arbitrary internal settings flag in PebbleDB. Useful
// for injecting skip/done flags without direct DB access. Implements POST
// /operations/set-internal-flag.
func (h *Handler) SetInternalFlag(c *gin.Context) {
	var req struct {
		Key   string `json:"key" binding:"required"`
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if err := h.store.SetSetting(req.Key, req.Value, "string", false); err != nil {
		httputil.InternalError(c, "failed to set flag", err)
		return
	}
	slog.Info("setInternalFlag", "key", req.Key, "value", req.Value)
	httputil.RespondWithOK(c, gin.H{"key": req.Key, "value": req.Value})
}

// AuditFileConsistency implements GET /operations/audit-files.
func (h *Handler) AuditFileConsistency(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	result, err := sweep.AuditFileConsistency(h.store)
	if err != nil {
		httputil.InternalError(c, "failed to audit file consistency", err)
		return
	}
	httputil.RespondWithOK(c, result)
}

// --- Operation listing / logs / result / changes ---

// ListStaleOperations implements GET /operations/stale.
func (h *Handler) ListStaleOperations(c *gin.Context) {
	timeoutMinutes := config.AppConfig.OperationTimeoutMinutes
	if timeoutMinutes <= 0 {
		timeoutMinutes = 30
	}
	if raw := strings.TrimSpace(c.Query("timeout_minutes")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			timeoutMinutes = parsed
		}
	}

	stale, err := h.collectStale(time.Duration(timeoutMinutes) * time.Minute)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to list stale operations")
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"timeout_minutes": timeoutMinutes,
		"count":           len(stale),
		"operations":      stale,
	})
}

// GetOperationResult implements GET /operations/:id/result.
func (h *Handler) GetOperationResult(c *gin.Context) {
	id := c.Param("id")
	op, err := h.store.GetOperationByID(id)
	if err != nil {
		httputil.InternalError(c, "failed to get operation", err)
		return
	}
	if op == nil {
		httputil.RespondWithNotFound(c, "operation", id)
		return
	}

	if op.ResultData == nil {
		httputil.RespondWithOK(c, gin.H{"result_data": nil})
		return
	}

	// Parse the JSON result data to return as structured JSON
	var resultData json.RawMessage
	if err := json.Unmarshal([]byte(*op.ResultData), &resultData); err != nil {
		httputil.RespondWithOK(c, gin.H{"result_data": *op.ResultData})
		return
	}

	httputil.RespondWithOK(c, gin.H{"result_data": resultData})
}

// GetOperationChanges returns change tracking records for an operation.
// Implements GET /operations/:id/changes.
func (h *Handler) GetOperationChanges(c *gin.Context) {
	id := c.Param("id")
	changes, err := h.store.GetOperationChanges(id)
	if err != nil {
		httputil.InternalError(c, "failed to get operation changes", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"changes": changes})
}

// UndoPreflightHandler checks for conflicts before executing an undo.
// Implements GET /operations/:id/undo/preflight.
func (h *Handler) UndoPreflightHandler(c *gin.Context) {
	id := c.Param("id")
	report, err := h.preflightUndo(id)
	if err != nil {
		httputil.InternalError(c, "failed to check conflicts", err)
		return
	}
	httputil.RespondWithOK(c, report)
}

// RevertOperation undoes all changes from a given operation. Implements POST
// /operations/:id/revert.
func (h *Handler) RevertOperation(c *gin.Context) {
	id := c.Param("id")
	if err := h.revert(id); err != nil {
		httputil.InternalError(c, "failed to revert operation", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"message": "operation reverted successfully"})
}

// --- Tasks ---

// ListTasks returns all registered tasks with their status and schedule.
// Implements GET /tasks.
func (h *Handler) ListTasks(c *gin.Context) {
	sched := h.resolveScheduler()
	if sched == nil {
		httputil.RespondWithInternalError(c, "scheduler not initialized")
		return
	}
	httputil.RespondWithOK(c, sched.ListTasks())
}

// RunTask triggers a task by name. Implements POST /tasks/:name/run.
func (h *Handler) RunTask(c *gin.Context) {
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
func (h *Handler) UpdateTaskConfig(c *gin.Context) {
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
func (h *Handler) RunMaintenanceWindowNow(c *gin.Context) {
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
func (h *Handler) GetMaintenanceWindowStatus(c *gin.Context) {
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
func (h *Handler) UpdateMaintenanceWindowConfig(c *gin.Context) {
	var req handlers.MaintenanceWindowConfigReq
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
