// file: internal/server/handlers/metadata_cache.go
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-06-02

// Package handlers contains extracted HTTP handler types for the audiobook
// organizer server. MetadataCacheHandler covers the persistent metadata-cache
// query endpoints (cached candidates list, cache review, batch-apply-cached,
// clear-no-match).

package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/gin-gonic/gin"
)

// MetadataCacheBookStore is the narrow persistence interface required by
// MetadataCacheHandler. It also satisfies metabatch.BookFilesGetter so that
// BuildCandidateBookInfo can be called with the same store value.
type MetadataCacheBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	// GetBookFiles is required to satisfy metabatch.BookFilesGetter.
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// MetadataCacheFetchService is the narrow interface required for the
// metadata cache query and apply operations.
type MetadataCacheFetchService interface {
	ListCachedSummaries(ctx context.Context) ([]metafetch.MetadataCacheSummary, error)
	GetCachedCandidates(bookID string) (*metafetch.MetadataCandidateCache, bool, error)
	ApplyMetadataCandidate(id string, candidate metafetch.MetadataCandidate, fields []string) (*metafetch.FetchMetadataResponse, error)
	InvalidateCachedCandidates(bookID string) error
}

// MetadataCacheWriteBackEnqueuer is an alias for the shared WriteBackEnqueuer;
// kept here so existing call sites continue to compile without change.
type MetadataCacheWriteBackEnqueuer = WriteBackEnqueuer

// MetadataCacheHandler handles the persistent metadata-cache HTTP endpoints.
type MetadataCacheHandler struct {
	store   MetadataCacheBookStore
	svc     MetadataCacheFetchService
	batcher WriteBackEnqueuer // may be nil
}

// NewMetadataCacheHandler constructs a MetadataCacheHandler.
func NewMetadataCacheHandler(store MetadataCacheBookStore, svc MetadataCacheFetchService, batcher WriteBackEnqueuer) *MetadataCacheHandler {
	return &MetadataCacheHandler{store: store, svc: svc, batcher: batcher}
}

// ListCachedCandidates handles GET /api/v1/audiobooks/metadata/cached.
//
// Optional query param: status=pending|matched
func (h *MetadataCacheHandler) ListCachedCandidates(c *gin.Context) {
	if h.store == nil || h.svc == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}

	summaries, err := h.svc.ListCachedSummaries(c.Request.Context())
	if err != nil {
		httputil.InternalError(c, "failed to list metadata cache", err)
		return
	}

	statusFilter := c.Query("status")
	freshCutoff := time.Now().Add(-database.MetadataCacheTTL)

	out := make([]gin.H, 0, len(summaries))
	for _, sum := range summaries {
		book, err := h.store.GetBookByID(sum.BookID)
		if err != nil || book == nil {
			continue
		}
		var reviewStatus string
		if book.MetadataReviewStatus != nil {
			reviewStatus = *book.MetadataReviewStatus
		}
		switch statusFilter {
		case "pending":
			if reviewStatus != "" && reviewStatus != "pending" {
				continue
			}
		case "matched":
			if reviewStatus != "matched" {
				continue
			}
		}
		out = append(out, gin.H{
			"book_id":         sum.BookID,
			"fetched_at":      sum.FetchedAt,
			"candidate_count": sum.CandidateCount,
			"is_fresh":        sum.FetchedAt.After(freshCutoff),
			"title":           book.Title,
			"review_status":   reviewStatus,
		})
	}

	httputil.RespondWithOK(c, gin.H{"entries": out, "total": len(out)})
}

// GetCacheReviewResults handles GET /api/v1/audiobooks/metadata/cache/review.
//
// Returns a paginated list of CandidateResult items sourced from the
// persistent metadata cache. limit=0 means "return all rows".
func (h *MetadataCacheHandler) GetCacheReviewResults(c *gin.Context) {
	if h.store == nil || h.svc == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}

	limit := httputil.ParseQueryInt(c, "limit", 0)
	offset := httputil.ParseQueryInt(c, "offset", 0)
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}

	summaries, err := h.svc.ListCachedSummaries(c.Request.Context())
	if err != nil {
		httputil.InternalError(c, "failed to list metadata cache", err)
		return
	}

	total := len(summaries)

	type entryWithStatus struct {
		sum    metafetch.MetadataCacheSummary
		status string // "matched" | "no_match" | "applied"
	}
	prepared := make([]entryWithStatus, 0, total)
	for _, sum := range summaries {
		book, err := h.store.GetBookByID(sum.BookID)
		if err != nil || book == nil {
			continue
		}
		st := "matched"
		if book.MetadataReviewStatus != nil {
			switch *book.MetadataReviewStatus {
			case "no_match":
				st = "no_match"
			case "matched":
				st = "applied"
			}
		}
		prepared = append(prepared, entryWithStatus{sum: sum, status: st})
	}
	// Stable sort: matched (pending review) first, then no_match, then applied.
	statusRank := map[string]int{"matched": 0, "no_match": 1, "applied": 2}
	sort.SliceStable(prepared, func(i, j int) bool {
		return statusRank[prepared[i].status] < statusRank[prepared[j].status]
	})

	start := offset
	if start > len(prepared) {
		start = len(prepared)
	}
	end := len(prepared)
	if limit > 0 {
		end = start + limit
		if end > len(prepared) {
			end = len(prepared)
		}
	}
	page := prepared[start:end]

	var matched, noMatch, applied int
	for _, p := range prepared {
		switch p.status {
		case "no_match":
			noMatch++
		case "applied":
			applied++
		default:
			matched++
		}
	}

	results := make([]metabatch.CandidateResult, 0, len(page))
	for _, p := range page {
		sum := p.sum
		book, err := h.store.GetBookByID(sum.BookID)
		if err != nil || book == nil {
			continue
		}
		entry, _, err := h.svc.GetCachedCandidates(sum.BookID)
		if err != nil || entry == nil || len(entry.Candidates) == 0 {
			continue
		}
		var cand metafetch.MetadataCandidate
		if err := json.Unmarshal(entry.Candidates[0], &cand); err != nil {
			slog.Warn("GetCacheReviewResults decode candidate", "bookID", sum.BookID, "err", err)
			continue
		}

		results = append(results, metabatch.CandidateResult{
			Book:      metabatch.BuildCandidateBookInfo(h.store, book),
			Candidate: &cand,
			Status:    p.status,
		})
	}

	httputil.RespondWithOK(c, gin.H{
		"results":       results,
		"total_count":   total,
		"matched":       matched,
		"no_match":      noMatch,
		"errors":        0,
		"total_applied": applied,
	})
}

// BatchApplyFromCache handles POST /api/v1/audiobooks/metadata/batch-apply-cached.
//
// Applies the highest-scored cached candidate for each book_id in the request.
func (h *MetadataCacheHandler) BatchApplyFromCache(c *gin.Context) {
	if h.store == nil || h.svc == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}
	var body struct {
		BookIDs []string `json:"book_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}

	applied := 0
	for _, bookID := range body.BookIDs {
		entry, _, err := h.svc.GetCachedCandidates(bookID)
		if err != nil || entry == nil || len(entry.Candidates) == 0 {
			slog.Warn("BatchApplyFromCache no cached candidates", "bookID", bookID)
			continue
		}
		var cand metafetch.MetadataCandidate
		if err := json.Unmarshal(entry.Candidates[0], &cand); err != nil {
			slog.Warn("BatchApplyFromCache decode candidate", "bookID", bookID, "err", err)
			continue
		}
		if _, err := h.svc.ApplyMetadataCandidate(bookID, cand, nil); err != nil {
			slog.Warn("BatchApplyFromCache apply", "bookID", bookID, "err", err)
			continue
		}
		_ = h.svc.InvalidateCachedCandidates(bookID)
		if h.batcher != nil {
			h.batcher.Enqueue(bookID)
		}
		applied++
	}

	httputil.RespondWithOK(c, gin.H{"applied": applied})
}

// ClearMetadataNoMatch handles POST /api/v1/audiobooks/:id/clear-no-match.
//
// Clears a book's MetadataReviewStatus back to null so it re-surfaces in the
// Review dialog. Does not create a rejection record.
func (h *MetadataCacheHandler) ClearMetadataNoMatch(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	book, err := h.store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}
	book.MetadataReviewStatus = nil
	if _, err := h.store.UpdateBook(id, book); err != nil {
		httputil.InternalError(c, "failed to clear review status", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"message": "Review status cleared"})
}
