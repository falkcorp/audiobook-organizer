// file: internal/server/handlers/split_book.go
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678901
// last-edited: 2026-06-02

// Package handlers contains extracted HTTP handler types for the audiobook
// organizer server. SplitBookHandler covers the split-book deduplication
// endpoints.

package handlers

import (
	"context"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
)

// SplitBookOpEnqueuer is the narrow interface required to trigger a
// split-book scan operation via the UOS registry.
type SplitBookOpEnqueuer interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// SplitBookCandidateStore is the narrow interface for reading and managing
// persisted split-book candidate clusters.
type SplitBookCandidateStore interface {
	List() ([]dedupengine.SplitBookCandidate, error)
	Get(id string) (*dedupengine.SplitBookCandidate, error)
	Delete(id string) error
}

// SplitBookHandler handles the split-book deduplication HTTP endpoints.
//
// opEnqueuer and candStore may be nil when not wired (e.g. in tests or
// when the embedding store is unavailable). Handlers check for nil and
// return 503 gracefully.
type SplitBookHandler struct {
	opEnqueuer SplitBookOpEnqueuer     // may be nil
	candStore  SplitBookCandidateStore // may be nil
	mergeStore database.Store          // required by MergeSplitBookCluster
}

// NewSplitBookHandler constructs a SplitBookHandler.
func NewSplitBookHandler(op SplitBookOpEnqueuer, cands SplitBookCandidateStore, store database.Store) *SplitBookHandler {
	return &SplitBookHandler{opEnqueuer: op, candStore: cands, mergeStore: store}
}

// TriggerSplitBookScan handles POST /api/v1/dedup/split-book-scan.
// Delegates to the UOS registry to enqueue the dedup.split-book-scan op.
func (h *SplitBookHandler) TriggerSplitBookScan(c *gin.Context) {
	if h.opEnqueuer == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opEnqueuer.EnqueueOp(c.Request.Context(), "dedup.split-book-scan", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue split-book scan", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// ListSplitBookCandidates handles GET /api/v1/dedup/split-book-candidates.
// Returns all persisted split-book candidate clusters.
func (h *SplitBookHandler) ListSplitBookCandidates(c *gin.Context) {
	if h.candStore == nil {
		httputil.RespondWithServiceUnavailable(c, "split-book store not available")
		return
	}
	cands, err := h.candStore.List()
	if err != nil {
		httputil.InternalError(c, "failed to list split-book candidates", err)
		return
	}
	if cands == nil {
		cands = []dedupengine.SplitBookCandidate{}
	}
	httputil.RespondWithOK(c, gin.H{
		"candidates": cands,
		"total":      len(cands),
	})
}

// MergeSplitBookCandidate handles POST /api/v1/dedup/split-book-candidates/:id/merge.
//
// Optional JSON body: { "keep_id": "<bookID>" }. If keep_id is omitted, the
// first BookID in the candidate (earliest ULID) is used as the keep target.
// On success, the candidate row is deleted so it is not surfaced again.
func (h *SplitBookHandler) MergeSplitBookCandidate(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "candidate id required")
		return
	}
	if h.candStore == nil {
		httputil.RespondWithServiceUnavailable(c, "split-book store not available")
		return
	}
	cand, err := h.candStore.Get(id)
	if err != nil {
		httputil.InternalError(c, "failed to load split-book candidate", err)
		return
	}
	if cand == nil {
		httputil.RespondWithNotFound(c, "split_book_candidate", id)
		return
	}
	if len(cand.BookIDs) < 2 {
		httputil.RespondWithBadRequest(c, "candidate has fewer than 2 books")
		return
	}

	var body struct {
		KeepID string `json:"keep_id"`
	}
	_ = c.ShouldBindJSON(&body) // body is optional

	keepID := body.KeepID
	if keepID == "" {
		keepID = cand.BookIDs[0]
	}

	// Validate keepID is in the candidate's BookIDs.
	srcIDs := make([]string, 0, len(cand.BookIDs)-1)
	keepFound := false
	for _, bid := range cand.BookIDs {
		if bid == keepID {
			keepFound = true
			continue
		}
		srcIDs = append(srcIDs, bid)
	}
	if !keepFound {
		httputil.RespondWithBadRequest(c, "keep_id not in candidate book_ids")
		return
	}

	result, err := dedupengine.MergeSplitBookCluster(h.mergeStore, keepID, srcIDs, cand.SuggestedTitle)
	if err != nil {
		httputil.InternalError(c, "split-book merge failed", err)
		return
	}

	// Delete candidate so it is not surfaced again. Best-effort: a stale row
	// only causes a re-attempt that no-ops on already-soft-deleted srcs.
	if delErr := h.candStore.Delete(id); delErr != nil {
		c.Error(delErr) //nolint:errcheck
	}

	httputil.RespondWithOK(c, result)
}
