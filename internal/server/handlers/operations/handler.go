// file: internal/server/handlers/operations/handler.go
// version: 1.10.0
// guid: 1b7fbd86-cdda-4921-b2d0-786f5cadb438
// last-edited: 2026-08-23

// Package operations hosts the background-operation HTTP handlers extracted
// from the server package: the long-running scan / organize / optimize /
// transcode starters, generic operation status / cancel / listing / logs /
// result / changes / revert, and maintenance chores (optimize DB, sweep
// tombstones, audit file consistency, clear stale, delete history, set
// internal flag).
//
// The task-scheduler endpoints (list/run/configure tasks) and the
// maintenance-window endpoints (trigger/inspect/configure) used to live here
// too; they moved to handlers.SchedulerHandler (TODO.md scheduler-config
// item) so that retiring the v1 operations-record surface below does not read
// as "delete task scheduling" — those routes are scheduler
// configuration/control, not operation records. The getScheduler dependency
// and Scheduler interface below are unused by any handler method as of that
// move; left as-is (not touched here — see the PR that made this comment
// change for why) since removing them would mean changing New's signature and
// every call site, which is out of scope for a purely mechanical extraction.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (OperationsStore, OperationsRegistry, Scheduler, ScanCanceler,
// AIScanLister) and three injected funcs (collectStale, preflightUndo, revert)
// that wrap server-private helpers, so package operations never imports package
// server. preflightUndo wraps undo.PreflightUndoConflicts and revert wraps
// audiobooks.NewRevertService(...).RevertOperation; both consume a full
// database.Store opaquely, so the controller closes over s.Ops() rather than
// the handler enumerating "methods used".

package operations

import (
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

	// preflightUndo wraps undo.PreflightUndoConflicts(s.Ops(), id). The undo
	// report type is an importable alias, but PreflightUndoConflicts consumes a
	// full database.Store opaquely, so the controller closes over s.Ops().
	preflightUndo func(id string) (*undo.UndoConflictReport, error)

	// revert wraps audiobooks.NewRevertService(s.Ops()).RevertOperation(id).
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
//
// Unused since the six task/maintenance-window methods that called it moved to
// handlers.SchedulerHandler (TODO.md scheduler-config item, 2026-08-22).
// Removing it would mean narrowing New's getScheduler param too, which
// cascades to every operations.New call site (wire_handlers.go,
// handlers_integration_test.go) -- out of scope for this mechanical
// extraction per the task brief. Follow-up: drop getScheduler/resolveScheduler/
// Scheduler from this package when the v1-operations-record retirement work
// elsewhere in this backlog touches New's signature anyway.
//
//lint:ignore U1000 kept for constructor signature compatibility, see doc above (2026-08-22)
func (h *Handler) resolveScheduler() Scheduler {
	if h.getScheduler == nil {
		return nil
	}
	return h.getScheduler()
}

// --- Operation starters ---

// --- Operation status / cancel ---

// operationV2ToLegacy WAS HERE AND HAS BEEN DELETED (2026-08-19).
//
// It converted a v2 registry row into the legacy Operation shape at READ time.
// Nothing called it, and nothing should: internal/operations/registry/
// legacy_op_status.go keeps the legacy row in step by WRITING it as the v2 run
// progresses, so the legacy shape is materialised, not derived. A read-time
// converter alongside a write-time bridge is two sources of truth for the same
// row -- worth saying out loud while the kill-v1 migration is still in flight.

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
							// BookID is REQUIRED: memdb's book_authors primary
							// index is a non-AllowMissing compound on
							// {BookID, AuthorID}, so omitting it aborts the
							// memdb sync while the Pebble write still succeeds
							// and SetBookAuthors still returns nil. The split
							// authors then count 0 everywhere and get purged
							// while the book keeps unresolvable author_ids.
							// This was the only one of 13 non-test call sites
							// that left it empty.
							BookID:   book.ID,
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
//
// Two keyspaces hold result payloads and the id alone does not say which. Ops
// that still mint a v1 row write theirs with UpdateOperationResultData
// ("operation:<id>"); ops that have gone v2-native write theirs with
// ReporterSetResult, which lands on the v2 row ("opv2:op:<id>"). v2 is checked
// first because for a twinned op — one that wrote both — the v2 row is the
// authority; the v1 twin is a mirror that the retirement lane is removing.
//
// A v1-only read here is what made a v2-native op's output unreachable: nothing
// else serves ResultData, since rowToResponse omits it (it also renders the op
// LIST, where result blobs would balloon every row). Do not narrow this back to
// one keyspace until the last UpdateOperationResultData caller is gone —
// maintenance/dedup_ops.go, maintenance/reconcile.go, itunes/path_repair.go,
// batch_poller.go and diagnostics.go were all still writing v1 on 2026-08-23.
func (h *Handler) GetOperationResult(c *gin.Context) {
	id := c.Param("id")

	if row, err := h.store.GetOperationV2(id); err == nil && row != nil {
		h.respondWithResult(c, row.ResultData)
		return
	}

	op, err := h.store.GetOperationByID(id)
	if err != nil {
		httputil.InternalError(c, "failed to get operation", err)
		return
	}
	if op == nil {
		httputil.RespondWithNotFound(c, "operation", id)
		return
	}
	h.respondWithResult(c, op.ResultData)
}

// respondWithResult renders a stored result payload, which is a *string of JSON
// in both keyspaces. Unparseable data is echoed as the raw string rather than
// erroring: the payload is whatever the op chose to store, and a caller that
// can read a mangled result is better off than one that gets a 500.
func (h *Handler) respondWithResult(c *gin.Context, stored *string) {
	if stored == nil {
		httputil.RespondWithOK(c, gin.H{"result_data": nil})
		return
	}
	var resultData json.RawMessage
	if err := json.Unmarshal([]byte(*stored), &resultData); err != nil {
		httputil.RespondWithOK(c, gin.H{"result_data": *stored})
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
