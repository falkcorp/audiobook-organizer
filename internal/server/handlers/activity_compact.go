// file: internal/server/handlers/activity_compact.go
// version: 1.2.0
// guid: b5d2e8a4-7c19-4f6b-a3e0-1d8f4c7b9e26
// last-edited: 2026-09-13

package handlers

import (
	"context"
	"encoding/json"
	"errors"
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

// ActivityCompactHandler serves the activity-maintenance enqueue endpoints:
// POST /api/v1/activity/compact and POST /api/v1/admin/recompact-digests.
//
// Until 2026-09-10 both ran their whole pass inside the request and returned
// its counters (ActivityHandler.CompactActivity and .RecompactDigests). On a
// production-sized activity store that took longer than any browser waits, so
// the user saw a timeout and could not tell whether the work was still going.
// Each now enqueues its maintenance op and answers 202 with the op id; the op
// appears in the operations list with a live log, which is where the counters
// are read.
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
//   - 400 — "older_than_days must be a whole number of days" when the value
//     is not an integer (1.75, "3", 1e3); "must be zero or positive" when it
//     is negative; a body-shape message when the JSON itself is malformed.
//   - 500 — registry unavailable or the enqueue failed.
func (h *ActivityCompactHandler) CompactActivity(c *gin.Context) {
	if h.enqueuer == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}

	var req maintenance.CompactActivityLogParams
	if err := c.ShouldBindJSON(&req); err != nil {
		// A fractional, exponent, string or out-of-range value fails to decode
		// into the int field. Until 2026-09-13 this answered "must be zero or
		// positive" for 1.75, which is both. Say what is actually wrong.
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			httputil.RespondWithBadRequest(c, "older_than_days must be a whole number of days")
			return
		}
		httputil.RespondWithBadRequest(c, `request body must be a JSON object like {"older_than_days": N}`)
		return
	}
	if req.OlderThanDays < 0 {
		httputil.RespondWithBadRequest(c, "older_than_days must be zero or positive")
		return
	}

	h.enqueue(c, maintenance.CompactActivityLogDefID, "activity compaction", req)
}

// RecompactDigests handles POST /api/v1/admin/recompact-digests.
//
// Re-derives type, tier and tags on every stored daily-digest entry, on every
// activity backend. No body. Until 2026-09-10 it ran inside the request and
// returned {touched, skipped}; it now enqueues
// maintenance.recompact-activity-digests and responds exactly as
// CompactActivity does (202 with the op id, 409 if a run is already live, 500
// without a registry). The counters are the op's persisted result.
func (h *ActivityCompactHandler) RecompactDigests(c *gin.Context) {
	if h.enqueuer == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	h.enqueue(c, maintenance.RecompactActivityDigestsDefID, "digest recompaction", nil)
}

// enqueue is the shared tail of both endpoints: enqueue defID with params,
// answer 409 if the registry deduped the request onto a run that was already
// active, else 202 with the new op id.
func (h *ActivityCompactHandler) enqueue(c *gin.Context, defID, what string, params any) {
	before := h.activeOpIDs(defID)

	opID, err := h.enqueuer.EnqueueOp(c.Request.Context(), defID, params)
	if err != nil {
		httputil.InternalError(c, "enqueue "+what+" failed", err)
		return
	}
	if status, seen := before[opID]; seen {
		httputil.RespondWithConflict(c, fmt.Sprintf("%s is already %s (operation %s)", what, status, opID))
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"data": CompactStartedResponse{
		OperationID: opID,
		DefID:       defID,
		Status:      "queued",
	}})
}

// activeOpIDs returns id → status for every queued/running row of defID.
// Empty when there is no lister or the listing fails: a listing failure must
// not block the enqueue, and the worst outcome of an empty map is a 202 for a
// deduped id — the op is still running either way.
func (h *ActivityCompactHandler) activeOpIDs(defID string) map[string]string {
	out := map[string]string{}
	if h.active == nil {
		return out
	}
	rows, err := h.active.ListActiveOperationsV2()
	if err != nil {
		return out
	}
	for _, row := range rows {
		if row.DefID == defID {
			out[row.ID] = row.Status
		}
	}
	return out
}
