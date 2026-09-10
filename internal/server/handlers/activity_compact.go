// file: internal/server/handlers/activity_compact.go
// version: 1.0.0
// guid: b5d2e8a4-7c19-4f6b-a3e0-1d8f4c7b9e26
// last-edited: 2026-09-10

package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
)

// CompactionEnqueuer is the one registry method the compaction handler needs.
type CompactionEnqueuer interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// ActiveOperationsLister is the one ops-store method the compaction handler
// needs: the set of queued/running op rows, to tell a fresh enqueue from a
// dedupe onto a run already in flight.
type ActiveOperationsLister interface {
	ListActiveOperationsV2() ([]database.OperationV2Row, error)
}

// ActivityCompactHandler serves POST /api/v1/activity/compact.
//
// Until 2026-09-10 this endpoint (then ActivityHandler.CompactActivity) ran the
// whole compaction inside the request and returned its counters. On a
// production-sized activity store that took longer than any browser waits, so
// the user saw a timeout and could not tell whether the work was still going.
// It now enqueues maintenance.compact-activity-log and answers 202 with the op
// id; the op appears in the operations list with a live log, which is where
// the counters are read.
type ActivityCompactHandler struct {
	enqueuer CompactionEnqueuer     // nil when the registry is not initialised
	active   ActiveOperationsLister // nil when the store does not expose ops v2
}

// NewActivityCompactHandler constructs the handler. Either dependency may be
// nil; the handler answers 500 (registry) or degrades to never reporting 409
// (lister) rather than panicking.
func NewActivityCompactHandler(enqueuer CompactionEnqueuer, active ActiveOperationsLister) *ActivityCompactHandler {
	return &ActivityCompactHandler{enqueuer: enqueuer, active: active}
}

// CompactStartedResponse is the 202 body's data envelope.
type CompactStartedResponse struct {
	OperationID string `json:"operation_id"`
	DefID       string `json:"def_id"`
	Status      string `json:"status"`
}

// CompactActivity handles POST /api/v1/activity/compact.
//
// Body: {"older_than_days": N}; 0 means everything up to now. Responds:
//
//   - 202 {"data": {"operation_id", "def_id", "status": "queued"}} — enqueued.
//   - 409 — an op for this def is already queued or running; the message
//     carries its id. Detected by comparing the id the registry hands back
//     with the active rows seen BEFORE the enqueue: the registry dedupes a
//     same-params request onto the live run (and skips zombie rows with no
//     live handle), so an id that was already active is that run, not a new
//     one. A different-params request while one is running is queued behind
//     it (ConcurrencyKey serialises runs) and answered 202, because the
//     caller asked for different work and will get it.
//   - 400 — negative day count or malformed body.
//   - 500 — registry unavailable or the enqueue failed.
func (h *ActivityCompactHandler) CompactActivity(c *gin.Context) {
	if h.enqueuer == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}

	var req maintenance.CompactActivityLogParams
	if err := c.ShouldBindJSON(&req); err != nil || req.OlderThanDays < 0 {
		httputil.RespondWithBadRequest(c, "older_than_days must be zero or positive")
		return
	}

	before := h.activeCompactionIDs()

	opID, err := h.enqueuer.EnqueueOp(c.Request.Context(), maintenance.CompactActivityLogDefID, req)
	if err != nil {
		httputil.InternalError(c, "enqueue activity compaction failed", err)
		return
	}
	if status, seen := before[opID]; seen {
		httputil.RespondWithConflict(c, fmt.Sprintf("activity compaction is already %s (operation %s)", status, opID))
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"data": CompactStartedResponse{
		OperationID: opID,
		DefID:       maintenance.CompactActivityLogDefID,
		Status:      "queued",
	}})
}

// activeCompactionIDs returns id → status for every queued/running row of the
// compaction def. Empty when there is no lister or the listing fails: a
// listing failure must not block the enqueue, and the worst outcome of an
// empty map is a 202 for a deduped id — the op is still running either way.
func (h *ActivityCompactHandler) activeCompactionIDs() map[string]string {
	out := map[string]string{}
	if h.active == nil {
		return out
	}
	rows, err := h.active.ListActiveOperationsV2()
	if err != nil {
		return out
	}
	for _, row := range rows {
		if row.DefID == maintenance.CompactActivityLogDefID {
			out[row.ID] = row.Status
		}
	}
	return out
}
