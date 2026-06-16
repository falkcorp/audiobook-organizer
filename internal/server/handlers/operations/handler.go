// file: internal/server/handlers/operations/handler.go
// version: 1.1.0
// guid: 1b7fbd86-cdda-4921-b2d0-786f5cadb438
// last-edited: 2026-06-16

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
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/sweep"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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

// StartScan implements POST /operations/scan.
func (h *Handler) StartScan(c *gin.Context) {
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	body, _ := c.GetRawData()
	if len(body) == 0 {
		body = []byte("{}")
	}
	opID, err := h.registry.EnqueueOp(c.Request.Context(), "library.scan", body)
	if err != nil {
		httputil.InternalError(c, "enqueue failed", err)
		return
	}
	c.JSON(202, gin.H{"op_id": opID, "id": opID})
}

// StartOrganize implements POST /operations/organize.
func (h *Handler) StartOrganize(c *gin.Context) {
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	body, _ := c.GetRawData()
	if len(body) == 0 {
		body = []byte("{}")
	}
	opID, err := h.registry.EnqueueOp(c.Request.Context(), "library.organize", body)
	if err != nil {
		httputil.InternalError(c, "enqueue failed", err)
		return
	}
	c.JSON(202, gin.H{"op_id": opID, "id": opID})
}

// StartOptimize implements POST /operations/optimize.
func (h *Handler) StartOptimize(c *gin.Context) {
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	opID, err := h.registry.EnqueueOp(c.Request.Context(), "library.optimize", nil)
	if err != nil {
		httputil.InternalError(c, "enqueue failed", err)
		return
	}
	c.JSON(202, gin.H{"op_id": opID, "id": opID})
}

// StartTranscode implements POST /operations/transcode.
func (h *Handler) StartTranscode(c *gin.Context) {
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	body, _ := c.GetRawData()
	var check struct {
		BookID string `json:"book_id"`
	}
	if err := json.Unmarshal(body, &check); err != nil || check.BookID == "" {
		httputil.RespondWithBadRequest(c, "book_id is required")
		return
	}
	opID, err := h.registry.EnqueueOp(c.Request.Context(), "library.transcode", body)
	if err != nil {
		httputil.InternalError(c, "enqueue failed", err)
		return
	}
	c.JSON(202, gin.H{"op_id": opID, "id": opID})
}

// --- Operation status / cancel ---

// GetOperationStatus implements GET /operations/:id/status.
func (h *Handler) GetOperationStatus(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	id := c.Param("id")

	// Try v2 registry first; fall back to legacy table.
	if v2, err := h.store.GetOperationV2(id); err == nil && v2 != nil {
		httputil.RespondWithOK(c, operationV2ToLegacy(v2))
		return
	}

	op, err := h.store.GetOperationByID(id)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", id)
		return
	}
	httputil.RespondWithOK(c, op)
}

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

	books, err := h.store.GetAllBooks(10000, 0)
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
			if err == nil && author != nil && strings.Contains(author.Name, " & ") {
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
		if book.Narrator != nil && strings.Contains(*book.Narrator, " & ") {
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
func splitMultipleNames(name string) []string {
	parts := strings.Split(name, " & ")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return []string{name}
	}
	return result
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

// ListOperations returns a snapshot of currently queued/running operations with
// basic progress. Implements GET /operations.
func (h *Handler) ListOperations(c *gin.Context) {
	params := httputil.ParsePaginationParams(c)
	if h.store == nil {
		httputil.RespondWithOK(c, gin.H{"items": []database.Operation{}, "total": 0, "limit": params.Limit, "offset": params.Offset})
		return
	}
	ops, total, err := h.store.ListOperations(params.Limit, params.Offset)
	if err != nil {
		httputil.InternalError(c, "failed to list operations", err)
		return
	}
	if ops == nil {
		ops = []database.Operation{}
	}
	httputil.RespondWithOK(c, gin.H{"items": ops, "total": total, "limit": params.Limit, "offset": params.Offset})
}

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

// GetOperationLogs returns logs for a given operation. UOS v2 ops persist their
// log lines to op_logs_v2 via dbReporter; v1 ops used operation_logs. We query
// v2 first (canonical for all currently-enqueued ops) and fall back to v1 only
// if v2 has nothing — legacy rows from before the v2 cutover. Implements GET
// /operations/:id/logs.
func (h *Handler) GetOperationLogs(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	id := c.Param("id")
	limit := 1000
	if tailStr := c.Query("tail"); tailStr != "" {
		if n, convErr := strconv.Atoi(tailStr); convErr == nil && n > 0 {
			limit = n
		}
	}
	type logItem struct {
		Level     string    `json:"level"`
		Message   string    `json:"message"`
		Attrs     string    `json:"attrs,omitempty"`
		CreatedAt time.Time `json:"created_at"`
	}
	var items []logItem
	v2Logs, err := h.store.GetOpLogsV2(id, limit)
	if err != nil {
		httputil.InternalError(c, "failed to get operation logs", err)
		return
	}
	for _, l := range v2Logs {
		items = append(items, logItem{Level: l.Level, Message: l.Message, Attrs: l.Attrs, CreatedAt: l.CreatedAt})
	}
	if len(items) == 0 {
		v1Logs, err := h.store.GetOperationLogs(id)
		if err == nil {
			for _, l := range v1Logs {
				items = append(items, logItem{Level: l.Level, Message: l.Message, CreatedAt: l.CreatedAt})
			}
			if len(items) > limit {
				items = items[len(items)-limit:]
			}
		}
	}
	httputil.RespondWithOK(c, gin.H{"items": items, "count": len(items)})
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

// UpdateTaskConfig updates schedule config for a task. Implements PUT
// /tasks/:name.
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

	// Map task name to config fields and apply
	switch name {
	case "dedup_refresh":
		if req.Enabled != nil {
			config.AppConfig.ScheduledDedupRefreshEnabled = *req.Enabled
		}
		if req.IntervalMinutes != nil {
			config.AppConfig.ScheduledDedupRefreshInterval = *req.IntervalMinutes
		}
		if req.RunOnStartup != nil {
			config.AppConfig.ScheduledDedupRefreshOnStartup = *req.RunOnStartup
		}
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.DedupRefresh = *req.RunInMaintenanceWindow
		}
	case "author_split_scan":
		if req.Enabled != nil {
			config.AppConfig.ScheduledAuthorSplitEnabled = *req.Enabled
		}
		if req.IntervalMinutes != nil {
			config.AppConfig.ScheduledAuthorSplitInterval = *req.IntervalMinutes
		}
		if req.RunOnStartup != nil {
			config.AppConfig.ScheduledAuthorSplitOnStartup = *req.RunOnStartup
		}
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.AuthorSplit = *req.RunInMaintenanceWindow
		}
	case "db_optimize":
		if req.Enabled != nil {
			config.AppConfig.ScheduledDbOptimizeEnabled = *req.Enabled
		}
		if req.IntervalMinutes != nil {
			config.AppConfig.ScheduledDbOptimizeInterval = *req.IntervalMinutes
		}
		if req.RunOnStartup != nil {
			config.AppConfig.ScheduledDbOptimizeOnStartup = *req.RunOnStartup
		}
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.DbOptimize = *req.RunInMaintenanceWindow
		}
	case "metadata_refresh":
		if req.Enabled != nil {
			config.AppConfig.ScheduledMetadataRefreshEnabled = *req.Enabled
		}
		if req.IntervalMinutes != nil {
			config.AppConfig.ScheduledMetadataRefreshInterval = *req.IntervalMinutes
		}
		if req.RunOnStartup != nil {
			config.AppConfig.ScheduledMetadataRefreshOnStartup = *req.RunOnStartup
		}
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.MetadataRefresh = *req.RunInMaintenanceWindow
		}
	case "itunes_sync":
		if req.Enabled != nil {
			config.AppConfig.ITunes.SyncEnabled = *req.Enabled
		}
		if req.IntervalMinutes != nil {
			config.AppConfig.ITunes.SyncInterval = *req.IntervalMinutes
		}
	case "series_prune":
		if req.Enabled != nil {
			config.AppConfig.ScheduledSeriesPruneEnabled = *req.Enabled
		}
		if req.IntervalMinutes != nil {
			config.AppConfig.ScheduledSeriesPruneInterval = *req.IntervalMinutes
		}
		if req.RunOnStartup != nil {
			config.AppConfig.ScheduledSeriesPruneOnStartup = *req.RunOnStartup
		}
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.SeriesPrune = *req.RunInMaintenanceWindow
		}
	case "purge_deleted":
		if req.IntervalMinutes != nil {
			// purge interval is fixed at 6h, but we can update retention days
		}
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.PurgeDeleted = *req.RunInMaintenanceWindow
		}
	case "tombstone_cleanup":
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.TombstoneCleanup = *req.RunInMaintenanceWindow
		}
	case "reconcile_scan":
		if req.Enabled != nil {
			config.AppConfig.ScheduledReconcileEnabled = *req.Enabled
		}
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.Reconcile = *req.RunInMaintenanceWindow
		}
	case "purge_old_logs":
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.PurgeOldLogs = *req.RunInMaintenanceWindow
		}
	case "library_scan":
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.LibraryScan = *req.RunInMaintenanceWindow
		}
	case "library_organize":
		if req.RunInMaintenanceWindow != nil {
			config.AppConfig.Maintenance.LibraryOrganize = *req.RunInMaintenanceWindow
		}
	default:
		httputil.RespondWithBadRequest(c, fmt.Sprintf("task %q config is not configurable", name))
		return
	}

	// Persist to database
	if h.store != nil {
		if err := config.SaveConfigToDatabase(h.store); err != nil {
			slog.Warn("Failed to save task config", "err", err)
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
