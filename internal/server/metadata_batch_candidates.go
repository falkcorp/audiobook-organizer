// file: internal/server/metadata_batch_candidates.go
// version: 4.1.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-e1f2a3b4c5d6
// last-edited: 2026-09-02
//
// HTTP handlers for the metadata candidate batch fetch / apply pipeline.
// Pure service types and logic live in internal/metabatch.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// Re-export metabatch types under server-local aliases so existing
// JSON serialisation and test references continue to compile unchanged.
type CandidateBookInfo = metabatch.CandidateBookInfo
type CandidateResult = metabatch.CandidateResult

// batchFetchRequest is the JSON body for handleBatchFetchCandidates.
// Either BookIDs or Selection must be provided; OnlyUnmatched can be combined
// with either to exclude books that already have a "matched" candidate.
type batchFetchRequest = metabatch.BatchFetchRequest

// batchApplyRequest is the JSON body for handleBatchApplyCandidates.
type batchApplyRequest = metabatch.BatchApplyRequest

// handleBatchFetchCandidates creates a background operation that spawns parallel
// workers to fetch metadata candidates for the given book IDs.
func (s *Server) handleBatchFetchCandidates(c *gin.Context) {
	var req batchFetchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}

	store := s.Ops()

	// Resolve the target book IDs — from either explicit list or SelectionSpec.
	candidateIDs := req.BookIDs
	if len(candidateIDs) == 0 && req.Selection != nil {
		resolved, err := operations.ResolveBookIDs(*req.Selection, func(f operations.FilterSpec) ([]string, error) {
			return s.resolveFilterToBookIDs(c.Request.Context(), f)
		})
		if err != nil {
			httputil.RespondWithBadRequest(c, "failed to resolve selection: "+err.Error())
			return
		}
		candidateIDs = resolved
	}
	if len(candidateIDs) == 0 {
		httputil.RespondWithBadRequest(c, "book_ids or selection is required")
		return
	}

	// Optionally exclude books already having a "matched" candidate.
	if req.OnlyUnmatched {
		matched := metabatch.LatestMatchedBookIDs(store)
		filtered := candidateIDs[:0]
		for _, id := range candidateIDs {
			if !matched[id] {
				filtered = append(filtered, id)
			}
		}
		candidateIDs = filtered
		if len(candidateIDs) == 0 {
			httputil.RespondWithOK(c, gin.H{
				"message":      "all selected books already have matched candidates",
				"operation_id": "",
				"book_count":   0,
			})
			return
		}
	}

	// Exclude books already in an active metadata fetch to avoid duplicate API calls.
	//
	// This is a PER-BOOK guard and EnqueueOp's param dedup does not replace it:
	// that merges byte-identical requests, so asking for {C,D,E} while {A,B,C} is
	// running queues a second run that re-fetches C. This removes C instead and
	// proceeds with {D,E}.
	//
	// The book list now comes off the v2 row's own params, so the separate
	// GetOperationParams read the v1 path needed is gone.
	alreadyFetching := make(map[string]bool)
	for _, op := range metabatch.CandidateFetchOps(store, 200) {
		if !metabatch.IsActiveFetchStatus(op.Status) {
			continue
		}
		for _, id := range metabatch.CandidateFetchBookIDs(store, op) {
			alreadyFetching[id] = true
		}
	}

	var bookIDs []string
	var skippedCount int
	for _, id := range candidateIDs {
		if alreadyFetching[id] {
			skippedCount++
		} else {
			bookIDs = append(bookIDs, id)
		}
	}

	if len(bookIDs) == 0 {
		httputil.RespondWithOK(c, struct {
			Message     string `json:"message"`
			OperationID string `json:"operation_id"`
			BookCount   int    `json:"book_count"`
			Skipped     int    `json:"skipped"`
		}{
			Message:     fmt.Sprintf("All %d books are already being fetched in another operation", skippedCount),
			OperationID: "",
			BookCount:   0,
			Skipped:     skippedCount,
		})
		return
	}

	totalBooks := len(bookIDs)

	// Return the id EnqueueOp minted. This used to mint a v1 operations row,
	// stamp its id into the params, and DISCARD the v2 id — on a comment saying
	// three v1 readers depended on it. One of the three, handleGetPendingReview,
	// did not exist anywhere in the repo; the real readers are below and they all
	// take whichever id the response carries.
	//
	// SaveOperationParams went with it. It existed so the restart path could
	// recover the book list, and the v2 row already persists these params — Run
	// receives them on a resumed run without anyone writing them twice.
	params := metadataCandidateFetchOpParams{
		BookIDs:    bookIDs,
		TotalBooks: totalBooks,
	}
	opID, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "metadata.candidate-fetch", params)
	if enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, struct {
		OperationID string `json:"operation_id"`
		TotalBooks  int    `json:"total_books"`
		Message     string `json:"message"`
	}{
		OperationID: opID,
		TotalBooks:  totalBooks,
		Message:     "metadata candidate fetch started",
	})
}

// fetchCandidateForBook fetches metadata candidates for a single book, respecting
// the rate limiter. Returns a CandidateResult.
func (s *Server) fetchCandidateForBook(
	ctx context.Context,
	mfs *metafetch.Service,
	store candidateFetchStore,
	limiter *rate.Limiter,
	opID, bookID string,
) CandidateResult {
	book, err := store.GetBookByID(bookID)
	if err != nil || book == nil {
		return CandidateResult{
			Book:   CandidateBookInfo{ID: bookID},
			Status: "error",
			Error:  fmt.Sprintf("book not found: %v", err),
		}
	}

	bookInfo := metabatch.BuildCandidateBookInfo(store, book)

	// Skip obvious chapter fragments of shattered audiobooks (e.g. a book
	// titled "06 Chapter 6"). Searching a catalog for these matches a random
	// entry at ~100%+ confidence and writes garbage onto every chapter, so we
	// short-circuit BEFORE any external search and surface a clear skipped
	// status instead of a bogus "matched" candidate.
	if metadata.IsLikelyChapterFragment(book.Title) {
		return CandidateResult{
			Book:   bookInfo,
			Status: "skipped",
			Error:  "skipped: chapter fragment",
		}
	}

	var authorHint []string
	if book.Author != nil && book.Author.Name != "" {
		authorHint = append(authorHint, book.Author.Name)
	}

	// METADATA-CACHED-MATCHER: batch fetch always invalidates + writes
	// the persistent cache for each book. FetchAndCacheLimited runs the same
	// search chain and replaces the cache row in one call, so the
	// per-book Review UI hits a fresh top-10 next render.
	//
	// The shared limiter is threaded into the search core so it throttles ACTUAL
	// outbound requests (one token per live source call), not books — previously a
	// single limiter.Wait per book let each book fan out to many HTTP calls, so
	// "10/s" permitted 10 books/s = a large multiple of the intended request rate.
	authorForHash := ""
	if len(authorHint) > 0 {
		authorForHash = authorHint[0]
	}
	entry, err := mfs.FetchAndCacheLimited(ctx, limiter, bookID, book.Title, authorForHash, "", "", metafetch.SearchOptions{})
	if err != nil {
		return CandidateResult{
			Book:   bookInfo,
			Status: "error",
			Error:  fmt.Sprintf("search failed: %v", err),
		}
	}
	// Decode cached []json.RawMessage back into MetadataCandidate
	// for the OperationResult payload (back-compat with the progress UI).
	results := make([]metafetch.MetadataCandidate, 0, len(entry.Candidates))
	for _, raw := range entry.Candidates {
		var c metafetch.MetadataCandidate
		if jerr := json.Unmarshal(raw, &c); jerr == nil {
			results = append(results, c)
		}
	}
	resp := &metafetch.SearchMetadataResponse{Results: results, Query: book.Title}

	if len(resp.Results) == 0 {
		return CandidateResult{
			Book:   bookInfo,
			Status: "no_match",
		}
	}

	// Load previously rejected candidates for this book (across all operations)
	// and filter them out so we pick the next best match.
	rejectedKeys := metabatch.LoadRejectedCandidateKeys(store, bookID)
	var filtered []metafetch.MetadataCandidate
	for _, c := range resp.Results {
		key := c.Source + "|" + c.Title
		if !rejectedKeys[key] {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return CandidateResult{
			Book:   bookInfo,
			Status: "no_match",
			Error:  "all candidates previously rejected",
		}
	}

	// Pick the top-scoring non-rejected candidate.
	topCandidate := filtered[0]
	return CandidateResult{
		Book:      bookInfo,
		Candidate: &topCandidate,
		Status:    "matched",
	}
}

// handleGetOperationResults returns a paginated page of candidate results for an operation.
// Query params: limit (default 100, 0=all), offset (default 0).
// Response includes total_count so the frontend can render correct pagination controls
// without loading all results.
func (s *Server) handleGetOperationResults(c *gin.Context) {
	opID := c.Param("id")
	if opID == "" {
		httputil.RespondWithBadRequest(c, "operation id is required")
		return
	}

	params := httputil.ParsePaginationParams(c)
	limit := params.Limit
	offset := params.Offset

	store := s.Ops()

	// Resolve from EITHER keyspace. A v1-only lookup 404s on every run started
	// since the handler stopped minting a v1 row — the client would hold an id
	// the results endpoint refuses to acknowledge.
	op := metabatch.ResolveCandidateFetch(store, opID)
	if op == nil {
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	}

	allRaw, err := store.GetOperationResults(opID)
	if err != nil {
		httputil.InternalError(c, "failed to get operation results", err)
		return
	}
	totalCount := len(allRaw)

	// Global counts by Status field — no JSON unmarshal needed.
	var totalMatched, totalNoMatch, totalErrors int
	for _, r := range allRaw {
		switch r.Status {
		case "matched":
			totalMatched++
		case "no_match":
			totalNoMatch++
		case "error":
			totalErrors++
		}
	}

	// Slice for the requested page.
	end := totalCount
	if limit > 0 && offset+limit < totalCount {
		end = offset + limit
	}
	var pageRaw []database.OperationResult
	if offset < totalCount {
		pageRaw = allRaw[offset:end]
	}

	candidateResults := make([]CandidateResult, 0, len(pageRaw))
	for _, r := range pageRaw {
		var cr CandidateResult
		if err := json.Unmarshal([]byte(r.ResultJSON), &cr); err != nil {
			slog.Warn("failed to unmarshal result for book in op", "r", r.BookID, "opID", opID, "err", err)
			continue
		}
		candidateResults = append(candidateResults, cr)
	}

	httputil.RespondWithOK(c, struct {
		Operation    *database.Operation `json:"operation"`
		Results      []CandidateResult   `json:"results"`
		Total        int                 `json:"total"`
		TotalCount   int                 `json:"total_count"`
		Matched      int                 `json:"matched"`
		NoMatch      int                 `json:"no_match"`
		Errors       int                 `json:"errors"`
		TotalMatched int                 `json:"total_matched"`
		TotalNoMatch int                 `json:"total_no_match"`
		TotalErrors  int                 `json:"total_errors"`
		Limit        int                 `json:"limit"`
		Offset       int                 `json:"offset"`
	}{
		Operation:    op,
		Results:      candidateResults,
		Total:        totalCount,
		TotalCount:   totalCount,
		Matched:      metabatch.CountByStatus(candidateResults, "matched"),
		NoMatch:      metabatch.CountByStatus(candidateResults, "no_match"),
		Errors:       metabatch.CountByStatus(candidateResults, "error"),
		TotalMatched: totalMatched,
		TotalNoMatch: totalNoMatch,
		TotalErrors:  totalErrors,
		Limit:        limit,
		Offset:       offset,
	})
}

// handleGetLatestMetadataFetch returns recent metadata candidate-fetch
// operations that have persisted results.
//
// The name in this comment used to be handleListMetadataFetchOperations, which
// exists nowhere in the repo — the same phantom-reader defect as the
// handleGetPendingReview one removed from this file.
//
// Returns up to the last 10 operations where:
//   - it is a metadata candidate fetch, in either keyspace
//   - status is completed OR running (the bullet here said "completed" only,
//     which the code below has contradicted deliberately since partial-review
//     was added)
//   - at least one persisted result row exists
//
// The frontend Resume Review dialog displays these so the user can
// pick which fetch to review. Without this, firing two fetches
// back-to-back without reviewing the first leaves the first's
// results invisible in the UI — the operation id is only held in
// React state, and only the latest gets tracked.
//
// 10 is a soft cap chosen as "enough to cover back-to-back fetches
// plus a review backlog without overwhelming the dialog".
func (s *Server) handleGetLatestMetadataFetch(c *gin.Context) {
	const maxOps = 10
	store := s.Ops()
	// Scan more than maxOps from history because the filter (completed/running
	// + non-empty results) can reject many rows. The limit is deliberately large:
	// both listings load into memory and sort anyway, so raising the cap is free,
	// and without it background maintenance/organize/scan ops push older
	// metadata-fetch runs out of the window.
	//
	// CandidateFetchOps spans BOTH keyspaces. A v2-only scan here would empty this
	// picker of every fetch that ran before the v1 row was retired — the exact
	// "results are invisible" failure this endpoint was written to fix.
	ops := metabatch.CandidateFetchOps(store, 5000)
	type fetchOpSummary struct {
		ID           string    `json:"id"`
		Type         string    `json:"type"`
		Status       string    `json:"status"`
		CreatedAt    time.Time `json:"created_at"`
		CompletedAt  time.Time `json:"completed_at,omitempty"`
		ResultCount  int       `json:"result_count"`
		MatchedCount int       `json:"matched_count"`
		NoMatchCount int       `json:"no_match_count"`
		ErrorCount   int       `json:"error_count"`
	}
	var out []fetchOpSummary
	for _, op := range ops {
		if len(out) >= maxOps {
			break
		}
		// Include both completed AND running operations so the
		// user can review partial results while a bulk fetch is
		// still in progress. Before this change, only completed
		// operations appeared in the picker — the user had to
		// wait for the full 10K-book fetch to finish before they
		// could start reviewing anything.
		if op.Status != "completed" && op.Status != "running" {
			continue
		}
		results, err := store.GetOperationResults(op.ID)
		if err != nil {
			slog.Warn("list-metadata-fetches get results for", "op", op.ID, "err", err)
			continue
		}
		if len(results) == 0 {
			continue
		}
		var matched, noMatch, errCount int
		for _, r := range results {
			switch r.Status {
			case "matched":
				matched++
			case "no_match":
				noMatch++
			case "error":
				errCount++
			}
		}
		// Type is still reported as the v1 string. The frontend keys the Resume
		// Review dialog off it, and a run's kind did not change when its id did.
		summary := fetchOpSummary{
			ID:           op.ID,
			Type:         "metadata_candidate_fetch",
			Status:       op.Status,
			CreatedAt:    op.CreatedAt,
			ResultCount:  len(results),
			MatchedCount: matched,
			NoMatchCount: noMatch,
			ErrorCount:   errCount,
		}
		if op.CompletedAt != nil {
			summary.CompletedAt = *op.CompletedAt
		}
		out = append(out, summary)
	}
	httputil.RespondWithOK(c, struct {
		Operations []fetchOpSummary `json:"operations"`
		Count      int              `json:"count"`
	}{Operations: out, Count: len(out)})
}

// batchApplyConcurrency bounds how many books handleBatchApplyCandidates applies
// at once. Mirrors the const of the same name in internal/server/handlers, which
// bounds the cache-backed sibling endpoint doing the same DB-bound apply work.
const batchApplyConcurrency = 4

// handleBatchApplyCandidates applies stored metadata candidates for the selected books.
func (s *Server) handleBatchApplyCandidates(c *gin.Context) {
	// The list this feeds is memoised; a status change must not keep offering a
	// candidate the user just acted on.
	defer invalidateMetadataResultsCache()

	var req batchApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "operation_id and book_ids are required")
		return
	}
	if len(req.BookIDs) == 0 {
		httputil.RespondWithBadRequest(c, "book_ids must not be empty")
		return
	}
	// Fail-safe cap (internal/applycap): refuse an implausibly large selection
	// before a single candidate is applied. Refusal, not truncation.
	if ex := applycap.Refuse("batch-apply-candidates", len(req.BookIDs), config.AppConfig.BulkApplyMaxItems); ex != nil {
		httputil.RespondWithApplyCapExceeded(c, ex)
		return
	}

	store := s.Ops()
	mfs := s.metadataFetchService

	// Load all operation results for the given operation.
	results, err := store.GetOperationResults(req.OperationID)
	if err != nil {
		httputil.InternalError(c, "failed to load operation results", err)
		return
	}

	// Index results by book ID for fast lookup.
	resultsByBook := make(map[string]database.OperationResult, len(results))
	for _, r := range results {
		resultsByBook[r.BookID] = r
	}

	// Apply in parallel with a bounded pool, assembling the response strictly in
	// request order. Each goroutine owns exactly one slot in `outcomes` and the
	// slice is read only after Wait, so the slots need no lock. Building the
	// counters and the error list from that ordered slice afterwards — rather
	// than appending from the workers — is what keeps `errors` deterministic:
	// appending concurrently would both race and reorder the messages the user
	// sees between two identical requests.
	type applyOutcome struct {
		applied bool
		skipped bool
		errMsg  string
	}
	outcomes := make([]applyOutcome, len(req.BookIDs))

	g, gctx := errgroup.WithContext(c.Request.Context())
	// Deliberately NOT writeBackWorkers(): this handler does not write back.
	// The per-book work left on the request path is DB-bound
	// (ApplyMetadataCandidate + CreateOperationResult); the file work already
	// goes to s.fileIOPool. Tying it to the write-back knob would mean an
	// operator raising write_back_workers to speed up DISK writes also widened
	// the in-request DB fan-out here, which is not what that knob says it does.
	// Matches batchApplyConcurrency in the sibling handler, which does the same
	// work for the cache-backed endpoint.
	g.SetLimit(batchApplyConcurrency)

	for i, bookID := range req.BookIDs {
		g.Go(func() error {
			if gctx.Err() != nil {
				outcomes[i] = applyOutcome{errMsg: fmt.Sprintf("%s: canceled: %v", bookID, gctx.Err())}
				return nil
			}

			opResult, ok := resultsByBook[bookID]
			if !ok {
				outcomes[i] = applyOutcome{skipped: true}
				return nil
			}

			var cr CandidateResult
			if err := json.Unmarshal([]byte(opResult.ResultJSON), &cr); err != nil {
				outcomes[i] = applyOutcome{errMsg: fmt.Sprintf("%s: failed to parse result", bookID)}
				return nil
			}
			if cr.Candidate == nil || cr.Status != "matched" {
				outcomes[i] = applyOutcome{skipped: true}
				return nil
			}

			candidate := *cr.Candidate
			if _, err := mfs.ApplyMetadataCandidate(bookID, candidate, nil); err != nil {
				outcomes[i] = applyOutcome{errMsg: fmt.Sprintf("%s: apply failed: %v", bookID, err)}
				return nil
			}

			// Persist "applied" status so re-opens of the dialog don't show
			// this book as still needing review. Mirrors the reject handler.
			cr.Status = "applied"
			if updatedJSON, err := json.Marshal(cr); err == nil {
				_ = store.CreateOperationResult(&database.OperationResult{
					OperationID: req.OperationID,
					BookID:      bookID,
					ResultJSON:  string(updatedJSON),
					Status:      "applied",
				})
			}

			// Queue file I/O through the worker pool (bounded concurrency).
			if pool := s.fileIOPool; pool != nil {
				bid := bookID
				pool.Submit(bid, func() {
					// Logged, not returned: this runs in the pool AFTER the
					// handler has already answered, so outcomes[i] is long
					// since written. The response cannot report it; the log is
					// the only channel left.
					if err := mfs.ApplyMetadataFileIO(bid); err != nil {
						slog.Warn("background apply file I/O failed", "bid", bid, "err", err)
					}
					if _, err := mfs.WriteBackMetadataForBook(bid); err != nil {
						slog.Warn("write-back failed for", "bid", bid, "err", err)
					}
					if s.writeBackBatcher != nil {
						s.writeBackBatcher.Enqueue(bid)
					}
				})
			}

			outcomes[i] = applyOutcome{applied: true}
			return nil
		})
	}
	// No worker returns a non-nil error — every per-book failure is recorded in
	// outcomes[i].errMsg so it reaches the user in the `errors` array — but the
	// result is checked rather than discarded in case that ever changes.
	if err := g.Wait(); err != nil {
		httputil.InternalError(c, "failed to apply candidates", err)
		return
	}

	applied := 0
	skipped := 0
	var errors []string
	for _, o := range outcomes {
		switch {
		case o.applied:
			applied++
		case o.skipped:
			skipped++
		case o.errMsg != "":
			errors = append(errors, o.errMsg)
		}
	}

	httputil.RespondWithOK(c, struct {
		Applied     int      `json:"applied"`
		Skipped     int      `json:"skipped"`
		Errors      []string `json:"errors"`
		ErrorCount  int      `json:"error_count"`
		OperationID string   `json:"operation_id"`
	}{
		Applied:     applied,
		Skipped:     skipped,
		Errors:      errors,
		ErrorCount:  len(errors),
		OperationID: req.OperationID,
	})
}

// handleRejectCandidates stores rejected candidates so future fetches exclude them.
// The rejection is stored as an operation_result with status "rejected".
func (s *Server) handleRejectCandidates(c *gin.Context) {
	// The list this feeds is memoised; a status change must not keep offering a
	// candidate the user just acted on.
	defer invalidateMetadataResultsCache()

	var req struct {
		OperationID string   `json:"operation_id" binding:"required"`
		BookIDs     []string `json:"book_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	store := s.Ops()

	// For each book, update the stored result status to "rejected"
	results, err := store.GetOperationResults(req.OperationID)
	if err != nil {
		httputil.InternalError(c, "failed to load results", err)
		return
	}

	rejectSet := make(map[string]bool, len(req.BookIDs))
	for _, id := range req.BookIDs {
		rejectSet[id] = true
	}

	rejected := 0
	for _, r := range results {
		if !rejectSet[r.BookID] {
			continue
		}
		// Update the result JSON to set status to rejected
		var cr CandidateResult
		if err := json.Unmarshal([]byte(r.ResultJSON), &cr); err != nil {
			continue
		}
		cr.Status = "rejected"
		updatedJSON, _ := json.Marshal(cr)

		// Store as a new result with rejected status (overwrites by key in PebbleDB)
		_ = store.CreateOperationResult(&database.OperationResult{
			OperationID: req.OperationID,
			BookID:      r.BookID,
			ResultJSON:  string(updatedJSON),
			Status:      "rejected",
		})

		// Store a fast-lookup rejection key for the batch fetch dedup
		if cr.Candidate != nil {
			rejectKey := fmt.Sprintf("rejected_candidate:%s:%s|%s", r.BookID, cr.Candidate.Source, cr.Candidate.Title)
			_ = store.SetRaw(rejectKey, []byte("1"))
		}
		rejected++
	}

	httputil.RespondWithOK(c, struct {
		Rejected int `json:"rejected"`
	}{Rejected: rejected})
}

// handleUnrejectCandidates reverses a rejection — restores the candidate to "matched" status
// and removes the fast-lookup rejection key so it can be fetched again.
func (s *Server) handleUnrejectCandidates(c *gin.Context) {
	// The list this feeds is memoised; a status change must not keep offering a
	// candidate the user just acted on.
	defer invalidateMetadataResultsCache()

	var req struct {
		OperationID string   `json:"operation_id"`
		BookIDs     []string `json:"book_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	store := s.Ops()

	results, err := store.GetOperationResults(req.OperationID)
	if err != nil {
		httputil.InternalError(c, "failed to load results", err)
		return
	}

	unrejectSet := make(map[string]bool, len(req.BookIDs))
	for _, id := range req.BookIDs {
		unrejectSet[id] = true
	}

	unrejected := 0
	for _, r := range results {
		if !unrejectSet[r.BookID] {
			continue
		}
		var cr CandidateResult
		if err := json.Unmarshal([]byte(r.ResultJSON), &cr); err != nil {
			continue
		}
		if cr.Status != "rejected" {
			continue
		}
		cr.Status = "matched"
		updatedJSON, _ := json.Marshal(cr)

		_ = store.CreateOperationResult(&database.OperationResult{
			OperationID: req.OperationID,
			BookID:      r.BookID,
			ResultJSON:  string(updatedJSON),
			Status:      "matched",
		})

		// Remove the fast-lookup rejection key
		if cr.Candidate != nil {
			rejectKey := fmt.Sprintf("rejected_candidate:%s:%s|%s", r.BookID, cr.Candidate.Source, cr.Candidate.Title)
			_ = store.DeleteRaw(rejectKey)
		}
		unrejected++
	}

	httputil.RespondWithOK(c, struct {
		Unrejected int `json:"unrejected"`
	}{Unrejected: unrejected})
}

// latestMetadataResultsByBook scans the recent metadata_candidate_fetch
// operations and returns the LATEST OperationResult per book_id, plus a
// status histogram across the deduplicated set. The same helper backs both
// the unified GET /library/metadata-results endpoint and the legacy
// POST /metadata/pending-review endpoint, so the filter logic stays in
// one place.
//
// Returns (results-by-bookID, status-counts, error).
func latestMetadataResultsByBook(store metadataResultsReader) (map[string]database.OperationResult, map[string]int, error) {
	type bookEntry struct {
		result    database.OperationResult
		createdAt time.Time
	}
	latest := map[string]bookEntry{}
	// Spans both keyspaces — see metabatch.CandidateFetchOps. A v2-only scan
	// would hide every result produced before the v1 row was retired, and this
	// helper backs the review endpoints, so those books would read as never
	// fetched and be re-fetched.
	for _, op := range metabatch.CandidateFetchOps(store, 5000) {
		results, err := store.GetOperationResults(op.ID)
		if err != nil {
			continue
		}
		for _, r := range results {
			existing, ok := latest[r.BookID]
			if !ok || r.CreatedAt.After(existing.createdAt) {
				latest[r.BookID] = bookEntry{result: r, createdAt: r.CreatedAt}
			}
		}
	}

	out := make(map[string]database.OperationResult, len(latest))
	counts := map[string]int{}
	for bookID, entry := range latest {
		out[bookID] = entry.result
		counts[entry.result.Status]++
	}
	return out, counts, nil
}

// handleListMetadataResults implements GET /api/v1/library/metadata-results.
// Returns every book's latest metadata-fetch result joined with book
// metadata, plus a by_status histogram for filter-toggle counts.
//
// Query params:
//
//	status= (repeatable) — filter to specific status values
//	                       (matched / no_match / applied / rejected / error / unfetched).
//	                       If omitted, all books with any result are returned.
//	limit / offset       — pagination (defaults: limit=100, offset=0; limit=0 → all).
//	include_unfetched=true — include books that have NEVER been fetched
//	                         (status=unfetched). Off by default to keep the
//	                         payload focused on the review-relevant set.
func (s *Server) handleListMetadataResults(c *gin.Context) {
	store := s.Ops()

	// Parse filters.
	statusFilter := map[string]bool{}
	for _, v := range c.QueryArray("status") {
		if v != "" {
			statusFilter[v] = true
		}
	}
	includeUnfetched := c.Query("include_unfetched") == "true"
	pp := httputil.ParsePaginationParams(c)

	latest, counts, err := latestMetadataResultsByBookCached(store)
	if err != nil {
		httputil.InternalError(c, "failed to load metadata results", err)
		return
	}

	// Optionally add an `unfetched` synthetic bucket. We populate the count
	// without loading every book record (that's expensive); the actual rows
	// only get streamed when the caller asks for include_unfetched=true.
	var unfetchedBookIDs []string
	if includeUnfetched || statusFilter["unfetched"] {
		// Use ListBookIDs (key-only projection) instead of GetAllBooks —
		// we only need the ID set to diff against `latest`. Avoids
		// materializing ~50K Book structs (~50x memory reduction). H4.
		allIDs, err := store.ListBookIDs()
		if err == nil {
			for _, id := range allIDs {
				if _, ok := latest[id]; !ok {
					unfetchedBookIDs = append(unfetchedBookIDs, id)
				}
			}
			counts["unfetched"] = len(unfetchedBookIDs)
		}
	}

	// Build response item list, applying status filter.
	type item struct {
		BookID      string `json:"book_id"`
		Status      string `json:"status"`
		ResultJSON  string `json:"result_json,omitempty"`
		OperationID string `json:"operation_id,omitempty"`
		FetchedAt   string `json:"fetched_at,omitempty"`
	}
	keep := func(status string) bool {
		if len(statusFilter) == 0 {
			return status != "unfetched" || includeUnfetched
		}
		return statusFilter[status]
	}

	all := make([]item, 0, len(latest))
	for bookID, r := range latest {
		if !keep(r.Status) {
			continue
		}
		all = append(all, item{
			BookID:      bookID,
			Status:      r.Status,
			ResultJSON:  r.ResultJSON,
			OperationID: r.OperationID,
			FetchedAt:   r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	if includeUnfetched || statusFilter["unfetched"] {
		for _, id := range unfetchedBookIDs {
			all = append(all, item{BookID: id, Status: "unfetched"})
		}
	}

	total := len(all)

	// Apply pagination.
	start := min(pp.Offset, total)
	end := total
	if pp.Limit > 0 {
		end = min(start+pp.Limit, total)
	}
	page := all[start:end]

	httputil.RespondWithOK(c, gin.H{
		"items":     page,
		"total":     total,
		"by_status": counts,
		"limit":     pp.Limit,
		"offset":    pp.Offset,
	})
}
