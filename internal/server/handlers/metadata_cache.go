// file: internal/server/handlers/metadata_cache.go
// version: 1.3.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-08-15

// Package handlers contains extracted HTTP handler types for the audiobook
// organizer server. MetadataCacheHandler covers the persistent metadata-cache
// query endpoints (cached candidates list, cache review, batch-apply-cached,
// clear-no-match).

package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
)

// MetadataCacheBookStore is the narrow persistence interface required by
// MetadataCacheHandler. It also satisfies metabatch.BookFilesGetter so that
// BuildCandidateBookInfo can be called with the same store value.
// reviewListConcurrency bounds the concurrent cached-candidate reads when
// building the review listing. These are store reads, not network calls, and
// this runs inline on a user-facing request — a small fixed pool, not
// runtime.NumCPU(), so one large listing cannot starve the rest of the server.
const reviewListConcurrency = 8

type MetadataCacheBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	// GetBooksByIDs fetches many books in one store call, preserving input
	// order. The review listing is served with limit=0 and previously did two
	// GetBookByID point reads per entry over the whole pending set.
	GetBooksByIDs(ids []string) ([]database.Book, error)
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
	// ops enqueues the background apply op. When nil, BatchApplyFromCache
	// reports 503 rather than silently falling back to an inline apply: an
	// inline fallback would be a second implementation that only ever ran in
	// tests, so the tested path and the shipped path would diverge.
	ops OpEnqueuer
}

// OpEnqueuer is the slice of the v2 operations registry this handler needs:
// enqueue a definition with params, get an op id back immediately.
type OpEnqueuer interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// NewMetadataCacheHandler constructs a MetadataCacheHandler.
//
// fileIOPool may be nil (tests, or a server built without a pool). Callers must
// pass a nil INTERFACE, not a typed-nil pointer, or the nil guards below become
// false-negatives — see the wiring in wire_handlers.go.
func NewMetadataCacheHandler(store MetadataCacheBookStore, svc MetadataCacheFetchService, batcher WriteBackEnqueuer, fileIOPool FileIOPool, ops OpEnqueuer) *MetadataCacheHandler {
	return &MetadataCacheHandler{store: store, svc: svc, batcher: batcher, fileIOPool: fileIOPool, ops: ops}
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
	// Fetch every book in ONE batch read instead of a GetBookByID per summary.
	//
	// This endpoint is called with limit=0 ("return all rows"), so the loops below
	// run over the entire pending set. It previously did GetBookByID once here to
	// compute status counts and AGAIN further down to build each row — 2N
	// sequential point reads. Production served it in 21.7s and 35.2s, which is
	// what was timing the UI out.
	//
	// GetBooksByIDs preserves input order and is a single store call.
	bookIDs := make([]string, 0, total)
	for _, sum := range summaries {
		bookIDs = append(bookIDs, sum.BookID)
	}
	booksByID := make(map[string]*database.Book, total)
	if fetched, berr := h.store.GetBooksByIDs(bookIDs); berr == nil {
		for i := range fetched {
			booksByID[fetched[i].ID] = &fetched[i]
		}
	} else {
		slog.Warn("GetCacheReviewResults batch book fetch failed; falling back to per-book reads", "err", berr)
	}

	// lookupBook serves from the batch result and falls back to a point read only
	// when the batch missed the row (or the batch call itself failed), so a
	// partial batch degrades in behavior-preserving fashion rather than dropping
	// entries.
	lookupBook := func(id string) *database.Book {
		if b, ok := booksByID[id]; ok {
			return b
		}
		b, err := h.store.GetBookByID(id)
		if err != nil || b == nil {
			return nil
		}
		booksByID[id] = b
		return b
	}

	prepared := make([]entryWithStatus, 0, total)
	for _, sum := range summaries {
		book := lookupBook(sum.BookID)
		if book == nil {
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

	// Read each page entry's cached candidates CONCURRENTLY. This was a serial
	// GetCachedCandidates per entry, on top of a second GetBookByID per entry
	// that is now gone (lookupBook serves it from the batch above).
	//
	// Results are written to a pre-sized slot per index and assembled in order
	// below, so the response ordering is byte-identical to the serial version —
	// this is a paginated listing and reordering it would scramble the UI.
	cachedByIdx := make([]*metafetch.MetadataCandidateCache, len(page))
	var cg errgroup.Group
	cg.SetLimit(reviewListConcurrency)
	for i := range page {
		i := i
		cg.Go(func() error {
			entry, _, cerr := h.svc.GetCachedCandidates(page[i].sum.BookID)
			if cerr == nil {
				cachedByIdx[i] = entry
			}
			return nil // a per-entry failure skips that row, never the whole page
		})
	}
	_ = cg.Wait()

	results := make([]metabatch.CandidateResult, 0, len(page))
	for pageIdx, p := range page {
		sum := p.sum
		book := lookupBook(sum.BookID)
		if book == nil {
			continue
		}
		entry := cachedByIdx[pageIdx]
		if entry == nil || len(entry.Candidates) == 0 {
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
// This is a DISPATCHER. It enqueues the "metadata.batch-apply-cached" op and
// returns 202 with an op id; it applies nothing itself.
//
// It used to apply the whole batch inline. That was already parallel and
// already pushed the file work to a pool, so the problem was never a missing
// worker pool — it was the REQUEST DURATION. A 250-book apply measured 2m0s on
// production. Go's HTTP server does not kill a handler when the client
// disconnects, and ApplyMetadataCandidate takes no context, so the browser
// timed out, the UI reported "session expired, nothing was applied", and the
// server went on applying for another minute. The user was told the opposite of
// what happened.
//
// The per-book logic now lives in applyCachedCandidateForBook
// (internal/server/batch_apply_one.go), which the op calls. There is
// deliberately NO inline fallback here: a fallback would be a second
// implementation reachable only when the registry is absent, so the tested path
// and the shipped path would diverge.
//
// FILE I/O — KEEP IN STEP WITH THE SINGLE-BOOK PATH. The sibling is
// applyAudiobookMetadataImpl in internal/server/handlers/metadata/handler.go.
// The two drifted apart once: the sibling wrote tags and embedded cover art
// while this path only updated the database and enqueued h.batcher — which is
// the *iTunes* library batcher, not the tag writer. Applied metadata never
// reached the files and nothing logged a failure. If you add file-side work to
// either path, add it to both.
func (h *MetadataCacheHandler) BatchApplyFromCache(c *gin.Context) {
	if h.store == nil || h.svc == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}
	if h.ops == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
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

	opID, err := h.ops.EnqueueOp(c.Request.Context(), "metadata.batch-apply-cached", map[string]any{
		"book_ids":   body.BookIDs,
		"write_back": shouldWriteBack,
	})
	if err != nil {
		httputil.InternalError(c, "failed to enqueue metadata apply", err)
		return
	}

	// 202 with an op id, NOT a completed result. The response no longer carries
	// applied_ids/skipped because nothing has been applied yet — the caller polls
	// the op and then re-reads the review list, which is the only description of
	// what actually happened that cannot go stale.
	c.JSON(http.StatusAccepted, gin.H{
		"data": gin.H{
			"op_id":      opID,
			"requested":  len(body.BookIDs),
			"write_back": shouldWriteBack,
		},
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
