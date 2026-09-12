// file: internal/server/handlers/operations/handler.go
// version: 1.15.0
// guid: 1b7fbd86-cdda-4921-b2d0-786f5cadb438
// last-edited: 2026-09-12

// Package operations hosts the background-operation HTTP handlers extracted
// from the server package: the long-running scan / organize / optimize /
// transcode starters, generic operation status / cancel / listing / logs /
// result / changes / revert, and maintenance chores (optimize DB, sweep
// tombstones, audit file consistency, clear stale, set internal flag).
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
	"errors"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
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

	// repairPhantomLive stamps completed_at on v2 operation rows that hold a
	// terminal status with completed_at null. Injected for the same reason
	// collectStale is: this package's store handle is the v1 OperationsStore and
	// must not widen to reach the v2 keyspace, so the controller closes over
	// s.Ops() and passes RepairOpsV2MissingCompletedAt.
	repairPhantomLive func() (int, error)

	// preflightUndo wraps undo.PreflightUndoConflicts(s.Ops(), id). The undo
	// report type is an importable alias, but PreflightUndoConflicts consumes a
	// full database.Store opaquely, so the controller closes over s.Ops().
	preflightUndo func(id string) (*undo.UndoConflictReport, error)

	// revert wraps audiobooks.NewRevertService(s.Ops()).RevertOperation(id).
	// Same opaque-store rationale as preflightUndo. The result carries the
	// restored / failed / not-restorable counts the response reports.
	revert func(id string) (*audiobooks.RevertResult, error)
}

// revertResponse is the POST /operations/:id/revert success body. Partial is
// true when any change row was left un-reverted (failed or not restorable).
type revertResponse struct {
	Message string `json:"message"`
	Partial bool   `json:"partial"`
	*audiobooks.RevertResult
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
	repairPhantomLive func() (int, error),
	preflightUndo func(id string) (*undo.UndoConflictReport, error),
	revert func(id string) (*audiobooks.RevertResult, error),
) *Handler {
	return &Handler{
		store:             store,
		registry:          registry,
		getScheduler:      getScheduler,
		pipeline:          pipeline,
		scanStore:         scanStore,
		collectStale:      collectStale,
		repairPhantomLive: repairPhantomLive,
		preflightUndo:     preflightUndo,
		revert:            revert,
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

	// Try cancel via v2 registry (running and queued v2 ops). Deliberately
	// left as `err == nil` rather than distinguishing registry.ErrOpNotFound
	// from other errors (see TASK-115): this legacy route already tolerates
	// ANY Cancel error by falling through to the force-update below, so an
	// unknown id here still ends up 204 via the fallback path — unlike
	// DELETE /operations/v2/:id, which now answers 404 for an unknown id.
	// This route is being retired separately per other TODO items; its
	// force-update fallback is the intended behavior for a stale/legacy id
	// and is out of scope for this change.
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

// ClearStaleOperations repairs operation rows that are stuck reading as
// in-flight. Implements POST /operations/clear-stale.
//
// What it does: stamps completed_at on v2 rows that already hold a TERMINAL
// status. It does not terminate anything — the operation is already over — it
// only fixes rows the UI cannot otherwise clear.
//
// v2 rows in a LIVE status (queued/running/waiting_deps/interrupted_quiesced)
// are NOT touched. Those belong to the scheduler and the startup resume sweep,
// which decide per-op whether to restart, requeue, or drop them; a button that
// force-failed them would silently discard work that was going to resume.
//
// HISTORY, because this handler has been wrong in both directions. Until
// 2026-09-07 it was v1-only, so pressing it against a stuck v2 row returned
// {"cleared":0} and did nothing at all; #3102 added the v2 half. The v1 half —
// which force-failed pending/running/queued rows in the `operation:` keyspace —
// was removed on 2026-09-07 with the rest of the v1 reads. Nothing has minted a
// v1 row since that minter was retired on 2026-08-23, so it could only ever act
// on pre-retirement rows, and the keyspace holding them is being deleted.
func (h *Handler) ClearStaleOperations(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	v2Repaired := 0
	if h.repairPhantomLive != nil {
		n, repairErr := h.repairPhantomLive()
		if repairErr != nil {
			httputil.InternalError(c, "failed to repair stuck operations", repairErr)
			return
		}
		v2Repaired = n
	}

	// "cleared" is the client-facing field — web/src/services/api.ts declares the
	// response as {cleared: number} and reads nothing else — so it keeps its name
	// even though only one half now contributes to it. "v1_failed" was dropped
	// with the v1 half; "v2_repaired" stays for anyone reading the raw response.
	httputil.RespondWithOK(c, gin.H{
		"cleared":     v2Repaired,
		"v2_repaired": v2Repaired,
	})
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
						// SetBookNarrators now stamps BookID itself; set it
						// here anyway so the literal says what it means. Its
						// omission stored book_id-less rows that stalled every
						// later memdb update of the book (migration 63).
						BookID:     book.ID,
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
// This is the only route that serves a completed run's payload: rowToResponse
// (operations_v2_handlers.go) deliberately omits ResultData because the same
// function renders the op LIST, where result blobs would balloon every row. So
// an op whose result this cannot reach has no reader at all.
//
// THE V1 FALLBACK WAS REMOVED ON 2026-09-07, AND ITS OWN EXIT CONDITION IS WHY.
// The comment that stood here said: "Do not narrow this back to one keyspace
// until the last UpdateOperationResultData caller is gone — maintenance/
// dedup_ops.go, maintenance/reconcile.go, itunes/path_repair.go, batch_poller.go
// and diagnostics.go were all still writing v1 on 2026-08-23." #3103 moved every
// one of those to ReporterSetResult / SetOperationV2Result, and a policy test in
// internal/plugins/maintenance keeps them there. Re-verified before deleting:
// `git grep UpdateOperationResultData` on main returns the interface
// declarations, the PebbleStore implementation, and nothing else — zero
// production writers. batch_poller.go and diagnostics.go do not mention it at
// all, so that half of the claim had already expired unnoticed.
//
// Reading a result stored on a pre-2026-08-23 v1 row is the accepted cost, in
// line with the rest of the v1 purge.
func (h *Handler) GetOperationResult(c *gin.Context) {
	id := c.Param("id")

	row, err := h.store.GetOperationV2(id)
	if err != nil {
		httputil.InternalError(c, "failed to get operation", err)
		return
	}
	if row == nil {
		httputil.RespondWithNotFound(c, "operation", id)
		return
	}
	h.respondWithResult(c, row.ResultData)
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

// RevertOperation undoes the restorable changes of a given operation.
// Implements POST /operations/:id/revert.
//
//   - 200: every restorable row was reversed. The body reports restored /
//     failed / not_restorable counts and partial=true when some rows (e.g.
//     record-only author_delete rows) were left un-reverted.
//   - 409: the operation has no restorable row at all (a purge op's ledger is
//     a record only); nothing was changed.
//   - 500: a restore failed, or the store failed. When restores were
//     attempted the message carries the counts, never the per-row errors
//     (those hold file paths).
func (h *Handler) RevertOperation(c *gin.Context) {
	id := c.Param("id")
	result, err := h.revert(id)
	var notRestorable *audiobooks.NotRestorableError
	switch {
	case errors.As(err, &notRestorable):
		httputil.RespondWithConflict(c, notRestorable.Error())
		return
	case err != nil && result != nil:
		slog.Error("operation revert partially failed", "operation", logger.SanitizeLogValue(id),
			"summary", result.Summary(), "err", logging.SanitizeErr(err))
		httputil.RespondWithError(c, http.StatusInternalServerError,
			"failed to revert operation: "+result.Summary(), "REVERT_PARTIAL_FAILURE")
		return
	case err != nil:
		httputil.InternalError(c, "failed to revert operation", err)
		return
	}
	httputil.RespondWithOK(c, revertResponse{Message: result.Summary(), Partial: result.Partial(), RevertResult: result})
}
