// file: internal/server/handlers/metadata_cache.go
// version: 1.1.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-08-11

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

	// ApplyMetadataFileIO runs the slow post-apply file work: cover-art
	// embedding, tag writing and (when enabled) renaming. Gated in prod by
	// auto_write_tags_on_apply / auto_rename_on_apply.
	ApplyMetadataFileIO(id string)
	// WriteBackMetadataForBook writes the book's current DB metadata into the
	// audio files themselves and returns the number of files written.
	WriteBackMetadataForBook(id string, segmentFilter ...[]string) (int, error)
}

// MetadataCacheWriteBackEnqueuer is an alias for the shared WriteBackEnqueuer;
// kept here so existing call sites continue to compile without change.
type MetadataCacheWriteBackEnqueuer = WriteBackEnqueuer

// MetadataCacheHandler handles the persistent metadata-cache HTTP endpoints.
type MetadataCacheHandler struct {
	store   MetadataCacheBookStore
	svc     MetadataCacheFetchService
	batcher WriteBackEnqueuer // may be nil — iTunes library sync, NOT audio tags
	// fileIOPool schedules the audio-tag / cover-art file work off the request
	// path. May be nil; when it is, BatchApplyFromCache logs at warn rather
	// than skipping silently (see the comment in BatchApplyFromCache).
	fileIOPool FileIOPool
}

// NewMetadataCacheHandler constructs a MetadataCacheHandler.
//
// fileIOPool may be nil (tests, or a server built without a pool). Callers must
// pass a nil INTERFACE, not a typed-nil pointer, or the nil guards below become
// false-negatives — see the wiring in wire_handlers.go.
func NewMetadataCacheHandler(store MetadataCacheBookStore, svc MetadataCacheFetchService, batcher WriteBackEnqueuer, fileIOPool FileIOPool) *MetadataCacheHandler {
	return &MetadataCacheHandler{store: store, svc: svc, batcher: batcher, fileIOPool: fileIOPool}
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

// Skip reasons reported per book by BatchApplyFromCache. Stable string values —
// the Metadata Review UI keys off them to decide whether to leave a row in the
// queue and what to tell the user.
const (
	batchSkipNoCachedCandidates = "no_cached_candidates"
	batchSkipDecodeFailed       = "decode_failed"
	batchSkipApplyFailed        = "apply_failed"
)

// BatchApplySkip describes one book that was requested but not applied.
type BatchApplySkip struct {
	BookID string `json:"book_id"`
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

// BatchApplyFromCache handles POST /api/v1/audiobooks/metadata/batch-apply-cached.
//
// Applies the highest-scored cached candidate for each book_id in the request.
//
// FILE I/O — KEEP IN STEP WITH THE SINGLE-BOOK PATH.
//
// This handler has a sibling: applyAudiobookMetadataImpl in
// internal/server/handlers/metadata/handler.go (POST /audiobooks/:id/apply-metadata).
// The two drifted apart. The sibling submits ApplyMetadataFileIO +
// WriteBackMetadataForBook to the file-I/O pool, which is what actually writes
// tags and embeds cover art INTO the audio files; this handler did neither. It
// updated the database and enqueued h.batcher — but h.batcher is the *iTunes*
// write-back batcher (itunesservice.WriteBackBatcher), which syncs to the iTunes
// library and never touches audio tags.
//
// The visible symptom: metadata applied from the Metadata Review screen existed
// in the database but was never written to the files, and no cover art was
// embedded. It looked like success because nothing logged a failure.
//
// If you add file-side work to either path, add it to both.
//
// The work goes through the pool, never inline: "Apply All" can carry hundreds
// of book IDs and rewriting tags for each synchronously would hold the request
// open for minutes.
func (h *MetadataCacheHandler) BatchApplyFromCache(c *gin.Context) {
	if h.store == nil || h.svc == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}
	var body struct {
		BookIDs []string `json:"book_ids" binding:"required"`
		// WriteBack defaults to TRUE when absent — identical semantics to the
		// single-book path's body.WriteBack.
		WriteBack *bool `json:"write_back"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}

	shouldWriteBack := body.WriteBack == nil || *body.WriteBack

	appliedIDs := make([]string, 0, len(body.BookIDs))
	skipped := make([]BatchApplySkip, 0)

	for _, bookID := range body.BookIDs {
		entry, _, err := h.svc.GetCachedCandidates(bookID)
		if err != nil || entry == nil || len(entry.Candidates) == 0 {
			slog.Warn("BatchApplyFromCache no cached candidates", "bookID", bookID, "err", err)
			skipped = append(skipped, BatchApplySkip{
				BookID: bookID,
				Reason: batchSkipNoCachedCandidates,
				Error:  errString(err),
			})
			continue
		}
		var cand metafetch.MetadataCandidate
		if err := json.Unmarshal(entry.Candidates[0], &cand); err != nil {
			slog.Warn("BatchApplyFromCache decode candidate", "bookID", bookID, "err", err)
			skipped = append(skipped, BatchApplySkip{
				BookID: bookID,
				Reason: batchSkipDecodeFailed,
				Error:  errString(err),
			})
			continue
		}
		if _, err := h.svc.ApplyMetadataCandidate(bookID, cand, nil); err != nil {
			slog.Warn("BatchApplyFromCache apply", "bookID", bookID, "err", err)
			skipped = append(skipped, BatchApplySkip{
				BookID: bookID,
				Reason: batchSkipApplyFailed,
				Error:  errString(err),
			})
			continue
		}
		_ = h.svc.InvalidateCachedCandidates(bookID)

		// iTunes library sync. Enqueued before the pool submission (matching the
		// single-book path) so a panic in the background file job cannot lose it.
		if shouldWriteBack && h.batcher != nil {
			h.batcher.Enqueue(bookID)
		}

		if shouldWriteBack {
			if pool := h.fileIOPool; pool != nil {
				id := bookID
				svc := h.svc
				pool.Submit(id, func() {
					svc.ApplyMetadataFileIO(id)
					if _, wbErr := svc.WriteBackMetadataForBook(id); wbErr != nil {
						slog.Warn("BatchApplyFromCache background write-back", "bookID", id, "err", wbErr)
					}
				})
			} else {
				// Never skip silently. A silent skip here is exactly the shape of
				// the defect this code path is fixing: the DB says applied, the
				// files were never touched, and nothing in the logs says so.
				slog.Warn("BatchApplyFromCache: no file-I/O pool wired, audio tags and cover art NOT written",
					"bookID", bookID, "reason", "fileIOPool is nil")
			}
		}

		appliedIDs = append(appliedIDs, bookID)
	}

	// "applied" stays an int at the top level: the Metadata Review UI already
	// reads it, so changing its type would break the frontend.
	httputil.RespondWithOK(c, gin.H{
		"applied":     len(appliedIDs),
		"applied_ids": appliedIDs,
		"skipped":     skipped,
		"requested":   len(body.BookIDs),
		"write_back":  shouldWriteBack,
	})
}

// errString renders an error for the JSON response, tolerating nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
