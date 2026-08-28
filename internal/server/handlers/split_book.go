// file: internal/server/handlers/split_book.go
// version: 1.3.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678901
// last-edited: 2026-08-28

// Package handlers contains extracted HTTP handler types for the audiobook
// organizer server. SplitBookHandler covers the split-book deduplication
// endpoints.

package handlers

import (
	"context"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/dedup"
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
	List() ([]dedup.SplitBookCandidate, error)
	Get(id string) (*dedup.SplitBookCandidate, error)
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
	mergeStore dedup.Store             // required by MergeSplitBookCluster
}

// NewSplitBookHandler constructs a SplitBookHandler.
func NewSplitBookHandler(op SplitBookOpEnqueuer, cands SplitBookCandidateStore, store dedup.Store) *SplitBookHandler {
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
		cands = []dedup.SplitBookCandidate{}
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

	result, err := dedup.MergeSplitBookCluster(h.mergeStore, keepID, srcIDs, cand.SuggestedTitle)
	if err != nil {
		httputil.InternalError(c, "split-book merge failed", err)
		return
	}

	// A partial result must remain reviewable. Deleting it would turn a failed
	// source-file move into an invisible, non-retryable repair gap.
	complete := len(result.Errors) == 0 && result.MergedSrcCount == len(srcIDs)
	if complete {
		if delErr := h.candStore.Delete(id); delErr != nil {
			c.Error(delErr) //nolint:errcheck
		}
	}

	httputil.RespondWithOK(c, result)
}

type bulkSplitBookMergeRequest struct {
	CandidateIDs []string          `json:"candidate_ids" binding:"required"`
	KeepIDs      map[string]string `json:"keep_ids"`
	DryRun       *bool             `json:"dry_run"`
}

// BulkMergeSplitBookCandidates preflights reviewed persisted candidates and
// queues exactly one durable batch. The operation receives snapshots rather
// than bare IDs so a rescan cannot change queued repair work.
func (h *SplitBookHandler) BulkMergeSplitBookCandidates(c *gin.Context) {
	if h.opEnqueuer == nil || h.candStore == nil {
		httputil.RespondWithServiceUnavailable(c, "split-book operation or candidate store not available")
		return
	}
	var body bulkSplitBookMergeRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid bulk split-book merge request: "+err.Error())
		return
	}
	if len(body.CandidateIDs) == 0 {
		httputil.RespondWithBadRequest(c, "candidate_ids must not be empty")
		return
	}
	seenCandidates := make(map[string]struct{}, len(body.CandidateIDs))
	seenBooks := make(map[string]string)
	items := make([]dedup.BulkSplitBookMergeItem, 0, len(body.CandidateIDs))
	for _, candidateID := range body.CandidateIDs {
		if candidateID == "" {
			httputil.RespondWithBadRequest(c, "candidate_ids must not contain empty IDs")
			return
		}
		if _, exists := seenCandidates[candidateID]; exists {
			httputil.RespondWithBadRequest(c, "candidate_ids must be unique")
			return
		}
		seenCandidates[candidateID] = struct{}{}
		candidate, err := h.candStore.Get(candidateID)
		if err != nil {
			httputil.InternalError(c, "failed to load split-book candidate", err)
			return
		}
		if candidate == nil {
			httputil.RespondWithNotFound(c, "split_book_candidate", candidateID)
			return
		}
		if len(candidate.BookIDs) < 2 {
			httputil.RespondWithBadRequest(c, "candidate has fewer than 2 books")
			return
		}
		keepID := candidate.BookIDs[0]
		if body.KeepIDs != nil && body.KeepIDs[candidateID] != "" {
			keepID = body.KeepIDs[candidateID]
		}
		keepFound := false
		for _, bookID := range candidate.BookIDs {
			if priorCandidate, exists := seenBooks[bookID]; exists {
				httputil.RespondWithBadRequest(c, "submitted candidates overlap on book "+bookID+" ("+priorCandidate+" and "+candidateID+")")
				return
			}
			seenBooks[bookID] = candidateID
			if bookID == keepID {
				keepFound = true
			}
		}
		if !keepFound {
			httputil.RespondWithBadRequest(c, "keep_id not in candidate book_ids")
			return
		}
		items = append(items, dedup.BulkSplitBookMergeItem{CandidateID: candidateID, BookIDs: append([]string(nil), candidate.BookIDs...), KeepID: keepID, SuggestedTitle: candidate.SuggestedTitle})
	}
	// Safety by default: omission produces a dry run. A later apply must send
	// an explicit false, and production invocation is separately operator-gated.
	dryRun := true
	if body.DryRun != nil {
		dryRun = *body.DryRun
	}
	opID, err := h.opEnqueuer.EnqueueOp(c.Request.Context(), "dedup.split-book-bulk-merge", dedup.BulkSplitBookMergeParams{Items: items, DryRun: dryRun})
	if err != nil {
		httputil.InternalError(c, "failed to enqueue split-book bulk merge", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{"op_id": opID, "dry_run": dryRun, "candidate_count": len(items)})
}
