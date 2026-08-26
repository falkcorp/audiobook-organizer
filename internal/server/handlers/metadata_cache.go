// file: internal/server/handlers/metadata_cache.go
// version: 1.7.1
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-08-20

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
	"golang.org/x/sync/errgroup"
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
	//
	// A non-nil error means the file work did not fully land -- most often the
	// rename failed. It does NOT mean nothing happened: rows for renames that
	// did succeed are already persisted, so callers report the database apply
	// as successful and flag only the file side.
	ApplyMetadataFileIO(id string) error
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
	// orphaned counts cache rows that outlived their book. Counted here rather
	// than inferred later: this `continue` is the only place that still knows
	// WHY the row is going away, and a subtraction at the end cannot tell it
	// apart from a book that simply has no candidates stored.
	var orphaned int
	for _, sum := range summaries {
		book := lookupBook(sum.BookID)
		if book == nil {
			orphaned++
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

	// Resolve cached candidates for EVERY prepared row, not just the requested
	// page, and let that decide what is reviewable.
	//
	// This ordering is the fix for a real reporting bug. The counts used to be
	// tallied over `prepared` — every row whose BOOK resolved — while `results`
	// additionally dropped any row with no cached candidates or an undecodable
	// one. On production that was 10,952 counted against 5,774 returned, so the
	// review rail advertised "10730 matched" over a list that could never hold
	// more than 5,774 rows, and `errors` was hardcoded 0 so nothing hinted at
	// the ~5,178 missing. A count that includes rows the caller cannot be given
	// is not a summary, it is a lie with a number on it.
	//
	// Doing this for all rows rather than one page also makes `total_count`
	// correct for pagination. It is not new work in the real call path: both
	// callers pass limit=0, so the page has always BEEN every row.
	cachedByIdx := make([]*metafetch.MetadataCandidateCache, len(prepared))
	var cg errgroup.Group
	cg.SetLimit(reviewListConcurrency)
	for i := range prepared {
		cg.Go(func() error {
			entry, _, cerr := h.svc.GetCachedCandidates(prepared[i].sum.BookID)
			if cerr == nil {
				cachedByIdx[i] = entry
			}
			return nil // a per-entry failure skips that row, never the whole batch
		})
	}
	_ = cg.Wait()

	// reviewable is every row this endpoint can actually hand back, in the
	// sorted order established above. Counts and pagination both derive from
	// it, so they cannot disagree with each other or with `results`.
	type reviewableRow struct {
		sum    metafetch.MetadataCacheSummary
		status string
		cand   metafetch.MetadataCandidate
	}
	reviewable := make([]reviewableRow, 0, len(prepared))
	var decodeErrors int
	// noCandidates is the second cause, counted for the same reason as orphaned.
	var noCandidates int
	for i, p := range prepared {
		entry := cachedByIdx[i]
		if entry == nil || len(entry.Candidates) == 0 {
			// No cached candidate means nothing to review. Not an error.
			noCandidates++
			continue
		}
		var cand metafetch.MetadataCandidate
		if err := json.Unmarshal(entry.Candidates[0], &cand); err != nil {
			slog.Warn("GetCacheReviewResults decode candidate", "bookID", p.sum.BookID, "err", err)
			decodeErrors++
			continue
		}
		reviewable = append(reviewable, reviewableRow{sum: p.sum, status: p.status, cand: cand})
	}

	var matched, noMatch, applied int
	// stale counts every reviewable row past the TTL, not just the ones on this
	// page, so the rail can state the size of the problem rather than whatever
	// fraction of it happens to be visible.
	//
	// ONE clock read, used both here and for the per-row is_fresh flag below.
	// Reading time.Now() twice for one predicate lets a row sitting on the
	// boundary be counted stale by the summary and reported fresh by its own
	// flag -- "the chip says 5,771 but I count 5,772 icons", from a race no
	// test can reproduce because both reads land in the same millisecond under
	// test. The two loops read the same FetchedAt (page is a slice of
	// reviewable), so a single cutoff makes them structurally unable to
	// disagree.
	freshCutoff := time.Now().Add(-database.MetadataCacheTTL)
	var stale int
	for _, r := range reviewable {
		if !r.sum.FetchedAt.After(freshCutoff) {
			stale++
		}
		switch r.status {
		case "no_match":
			noMatch++
		case "applied":
			applied++
		default:
			matched++
		}
	}

	start := min(offset, len(reviewable))
	end := len(reviewable)
	if limit > 0 {
		end = min(start+limit, len(reviewable))
	}
	page := reviewable[start:end]

	// BuildCandidateBookInfo runs for the PAGE only. It is the one genuinely
	// per-row-expensive call here, and a paginated caller must not pay for rows
	// it did not ask for.
	results := make([]metabatch.CandidateResult, 0, len(page))
	for i := range page {
		book := lookupBook(page[i].sum.BookID)
		if book == nil {
			continue
		}
		cand := page[i].cand
		// Age travels with the row. MetadataCacheTTL's contract is that stale
		// entries stay readable and the UI flags them -- but this endpoint sent
		// no age at all, so the review surface could not honour it. On the live
		// library that meant 5,771 of 5,774 reviewable rows were past the TTL
		// and every one of them was presented as though freshly fetched.
		fetchedAt := page[i].sum.FetchedAt
		isFresh := fetchedAt.After(freshCutoff)
		results = append(results, metabatch.CandidateResult{
			Book:      metabatch.BuildCandidateBookInfo(h.store, book),
			Candidate: &cand,
			Status:    page[i].status,
			FetchedAt: &fetchedAt,
			IsFresh:   &isFresh,
		})
	}

	httputil.RespondWithOK(c, gin.H{
		"results":     results,
		"total_count": len(reviewable),
		"matched":     matched,
		"no_match":    noMatch,
		// Real decode failures, not a hardcoded zero. A row counted here is one
		// the cache holds but nobody can review until it is repaired.
		"errors":        decodeErrors,
		"total_applied": applied,
		// Cache summaries that exist but are not reviewable. Surfaced so the gap
		// between "the cache has 14,306 entries" and "you can review 5,774" is
		// visible instead of being discovered by subtracting two numbers that
		// never agreed.
		//
		// This is the same value `total - len(reviewable)` produced, since every
		// dropped row passes through exactly one of these three counters -- but
		// summed from the causes rather than inferred, so the causes can be
		// reported alongside it. Knowing the number is 8,532 tells an operator
		// nothing about what to DO; knowing 3,354 of it is rows whose book is
		// gone points straight at a reaper, and the rest at a refetch.
		"unreviewable": orphaned + noCandidates + decodeErrors,
		// Reviewable rows whose cached candidate is past MetadataCacheTTL. They
		// are still returned -- staleness is informational, per the TTL's
		// contract -- but a reviewer applying month-old metadata should be told.
		"stale": stale,
		"unreviewable_by_cause": gin.H{
			// The book the row points at no longer resolves. Only a cleanup
			// pass fixes these; refetching cannot.
			"orphaned": orphaned,
			// The book is fine, the cache just holds no candidate for it.
			// A refetch fixes these.
			"no_candidates": noCandidates,
			// Stored, but the JSON would not decode. Also counted in `errors`;
			// this repeats it so the three causes sum to `unreviewable`.
			"decode_errors": decodeErrors,
		},
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
