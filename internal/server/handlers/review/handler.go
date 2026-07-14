// file: internal/server/handlers/review/handler.go
// version: 1.0.0
// guid: 2b6f9c14-8e37-4a5d-91c6-0f4a7d2e8b53
// last-edited: 2026-07-13

// Package reviewhandler hosts the universal review-queue HTTP handlers (PR-A1).
//
// The review queue is a generic, producer-agnostic home for everything the
// system has flagged for a human decision. A1 is pure infrastructure: the store
// (database.ReviewStore) plus this HTTP surface plus an apply-handler registry
// that future producers register into. v1's producer is the regroup op (Track
// B, not built yet), so at A1 the queue starts empty and no apply handlers are
// registered.
//
// Apply-handler registry: because producers don't exist yet, approving an item
// dispatches on its Kind to a registered ApplyFunc. When a Kind has a handler,
// approve runs it and then sets the item to "applied". When it does NOT (the A1
// state for every Kind), approve simply sets "approved" and returns OK with a
// note — it never errors. Track B's B2 registers the regroup apply handler.
package reviewhandler

import (
	"context"
	"strconv"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// ApplyFunc executes the real-world action an approved review item represents
// (e.g. the regroup op collapsing shattered books). Registered per Kind.
type ApplyFunc func(ctx context.Context, item database.ReviewItem) error

// Handler hosts the review-queue HTTP endpoints.
type Handler struct {
	// store is the wire-time snapshot of the review-queue store.
	store database.ReviewStore

	// applyHandlers maps a review item's Kind to the action that applies it.
	// Empty in A1; populated by producers (e.g. B2's regroup) via
	// RegisterApplyHandler. Guarded by mu because registration may happen during
	// wiring while requests could already be arriving.
	mu            sync.RWMutex
	applyHandlers map[string]ApplyFunc
}

// New constructs a review Handler from its store dependency.
func New(store database.ReviewStore) *Handler {
	return &Handler{
		store:         store,
		applyHandlers: make(map[string]ApplyFunc),
	}
}

// RegisterApplyHandler registers the apply action for a given review Kind.
// Producers call this at wire time (B2 registers the regroup apply path).
func (h *Handler) RegisterApplyHandler(kind string, fn ApplyFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.applyHandlers[kind] = fn
}

func (h *Handler) applyHandlerFor(kind string) (ApplyFunc, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	fn, ok := h.applyHandlers[kind]
	return fn, ok
}

// GetReviewCount handles GET /api/v1/review/count.
//
// Response: { "count": N, "byKind": { "<kind>": n, ... } } where count and the
// byKind breakdown both cover PENDING items only (decision #1: the badge counts
// intentional holds awaiting a decision, never decided items).
func (h *Handler) GetReviewCount(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	count, err := h.store.CountReviewItems(database.ReviewStatusPending)
	if err != nil {
		httputil.InternalError(c, "failed to count review items", err)
		return
	}
	stats, err := h.store.ReviewStatsByKind()
	if err != nil {
		httputil.InternalError(c, "failed to compute review stats", err)
		return
	}
	byKind := gin.H{}
	for _, s := range stats {
		if s.Status == database.ReviewStatusPending {
			byKind[s.Kind] = s.Count
		}
	}
	httputil.RespondWithOK(c, gin.H{"count": count, "byKind": byKind})
}

// ListReviewItems handles GET /api/v1/review/items.
//
// Query params: status (default "pending"), kind, limit (default 50), offset.
func (h *Handler) ListReviewItems(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	status := c.DefaultQuery("status", database.ReviewStatusPending)
	// "all" is an explicit escape hatch for "every status".
	if status == "all" {
		status = ""
	}
	filter := database.ReviewFilter{
		Status: status,
		Kind:   c.Query("kind"),
		Limit:  atoiDefault(c.Query("limit"), 50),
		Offset: atoiDefault(c.Query("offset"), 0),
	}
	items, total, err := h.store.ListReviewItems(filter)
	if err != nil {
		httputil.InternalError(c, "failed to list review items", err)
		return
	}
	if items == nil {
		items = []database.ReviewItem{}
	}
	httputil.RespondWithList(c, items, total, filter.Limit, filter.Offset)
}

// ApproveReviewItem handles POST /api/v1/review/items/:id/approve.
//
// If a Kind has a registered apply handler, it runs then the item is set
// "applied". Otherwise (A1 for every Kind) the item is set "approved" and a note
// is returned — approving without a handler is never an error.
func (h *Handler) ApproveReviewItem(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	id := c.Param("id")
	updated, note, err := h.approveOne(c.Request.Context(), id)
	if err != nil {
		httputil.InternalError(c, "failed to approve review item", err)
		return
	}
	if updated == nil {
		httputil.RespondWithNotFound(c, "review item", id)
		return
	}
	resp := gin.H{"item": updated}
	if note != "" {
		resp["note"] = note
	}
	httputil.RespondWithOK(c, resp)
}

// approveOne applies + transitions a single item. Returns (nil, "", nil) when
// the item does not exist. note is set when the item was approved without a
// registered apply handler.
func (h *Handler) approveOne(ctx context.Context, id string) (*database.ReviewItem, string, error) {
	item, err := h.store.GetReviewItem(id)
	if err != nil {
		return nil, "", err
	}
	if item == nil {
		return nil, "", nil
	}
	if fn, ok := h.applyHandlerFor(item.Kind); ok {
		if err := fn(ctx, *item); err != nil {
			return nil, "", err
		}
		updated, err := h.store.SetReviewItemStatus(id, database.ReviewStatusApplied)
		return updated, "", err
	}
	updated, err := h.store.SetReviewItemStatus(id, database.ReviewStatusApproved)
	if err != nil {
		return nil, "", err
	}
	return updated, "no apply handler registered for kind " + item.Kind + "; marked approved", nil
}

// RejectReviewItem handles POST /api/v1/review/items/:id/reject.
func (h *Handler) RejectReviewItem(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	id := c.Param("id")
	updated, err := h.store.SetReviewItemStatus(id, database.ReviewStatusRejected)
	if err != nil {
		httputil.InternalError(c, "failed to reject review item", err)
		return
	}
	if updated == nil {
		httputil.RespondWithNotFound(c, "review item", id)
		return
	}
	httputil.RespondWithOK(c, gin.H{"item": updated})
}

// bulkRequest is the POST /api/v1/review/bulk body (decision #4: grouped bulk
// actions). One of Kind or IDs must be set — an unscoped bulk over the whole
// queue is rejected to avoid an accidental approve/reject-all.
type bulkRequest struct {
	Action string   `json:"action"` // "approve" | "reject"
	Kind   string   `json:"kind,omitempty"`
	IDs    []string `json:"ids,omitempty"`
}

// bulkResult reports the outcome of a bulk action.
type bulkResult struct {
	Action    string   `json:"action"`
	Approved  []string `json:"approved,omitempty"`
	Applied   []string `json:"applied,omitempty"`
	Rejected  []string `json:"rejected,omitempty"`
	NotFound  []string `json:"not_found,omitempty"`
	Processed int      `json:"processed"`
}

// BulkReviewAction handles POST /api/v1/review/bulk.
//
// Targets are the explicit IDs when provided, otherwise every PENDING item of
// the given Kind. Volume is bounded (v1 producer holds only), so this runs
// sequentially; a worker pool would be premature until real apply handlers land.
func (h *Handler) BulkReviewAction(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	var req bulkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}
	if req.Action != "approve" && req.Action != "reject" {
		httputil.RespondWithBadRequest(c, "action must be 'approve' or 'reject'")
		return
	}
	if req.Kind == "" && len(req.IDs) == 0 {
		httputil.RespondWithBadRequest(c, "bulk action requires 'kind' or 'ids' — refusing to act on the entire queue")
		return
	}

	ids := req.IDs
	if len(ids) == 0 {
		// Kind-scoped: act on every pending item of that Kind.
		items, _, err := h.store.ListReviewItems(database.ReviewFilter{
			Status: database.ReviewStatusPending,
			Kind:   req.Kind,
			Limit:  bulkScanLimit,
		})
		if err != nil {
			httputil.InternalError(c, "failed to list review items for bulk action", err)
			return
		}
		ids = make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.ID)
		}
	}

	result := bulkResult{Action: req.Action}
	for _, id := range ids {
		switch req.Action {
		case "approve":
			updated, _, err := h.approveOne(c.Request.Context(), id)
			if err != nil {
				httputil.InternalError(c, "failed to approve review item "+id, err)
				return
			}
			if updated == nil {
				result.NotFound = append(result.NotFound, id)
				continue
			}
			if updated.Status == database.ReviewStatusApplied {
				result.Applied = append(result.Applied, id)
			} else {
				result.Approved = append(result.Approved, id)
			}
			result.Processed++
		case "reject":
			updated, err := h.store.SetReviewItemStatus(id, database.ReviewStatusRejected)
			if err != nil {
				httputil.InternalError(c, "failed to reject review item "+id, err)
				return
			}
			if updated == nil {
				result.NotFound = append(result.NotFound, id)
				continue
			}
			result.Rejected = append(result.Rejected, id)
			result.Processed++
		}
	}
	httputil.RespondWithOK(c, result)
}

// bulkScanLimit caps the kind-scoped pending fetch. Comfortably exceeds any
// realistic v1 hold population (intentional holds only, never raw backlogs).
const bulkScanLimit = 100_000

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
