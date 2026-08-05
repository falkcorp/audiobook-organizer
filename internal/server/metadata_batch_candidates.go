// file: internal/server/metadata_batch_candidates.go
// version: 3.3.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-e1f2a3b4c5d6
// last-edited: 2026-07-13
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
	"github.com/oklog/ulid/v2"
	"golang.org/x/time/rate"

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

	store := s.Store()

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
	activeOps, _ := store.GetRecentOperations(50)
	alreadyFetching := make(map[string]bool)
	for _, op := range activeOps {
		if op.Type != "metadata_candidate_fetch" {
			continue
		}
		if op.Status != "pending" && op.Status != "running" && op.Status != "queued" {
			continue
		}
		params, err := store.GetOperationParams(op.ID)
		if err != nil || len(params) == 0 {
			continue
		}
		var ids []string
		if err := json.Unmarshal(params, &ids); err == nil {
			for _, id := range ids {
				alreadyFetching[id] = true
			}
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

	opID := ulid.Make().String()
	_, err := store.CreateOperation(opID, "metadata_candidate_fetch", nil)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	totalBooks := len(bookIDs)

	// Save book IDs as operation params for recovery on restart
	if paramsJSON, err := json.Marshal(bookIDs); err == nil {
		_ = store.SaveOperationParams(opID, paramsJSON)
	}

	// Enqueue via v2 opRegistry. The Run func in metadata_candidate_op.go
	// handles all fetch work and writes OperationResult rows under opID so that
	// all existing v1 readers (handleGetPendingReview, handleGetOperationResults,
	// handleGetLatestMetadataFetch) continue working without changes.
	// The v2 run ID returned by EnqueueOp is intentionally discarded — clients
	// track progress via the v1 opID returned in the response below.
	params := metadataCandidateFetchOpParams{
		LegacyOpID: opID,
		BookIDs:    bookIDs,
		TotalBooks: totalBooks,
	}
	if _, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "metadata.candidate-fetch", params); enqErr != nil {
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
	store database.Store,
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

	store := s.Store()

	op, err := store.GetOperationByID(opID)
	if err != nil || op == nil {
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

// handleListMetadataFetchOperations returns recent completed
// metadata_candidate_fetch operations that have persisted results.
//
// Returns up to the last 10 operations where:
//   - type = metadata_candidate_fetch
//   - status = completed
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
	store := s.Store()
	// Scan more than maxOps from recent history because the filter
	// (type + completed + non-empty results) can reject many rows.
	// Use a large limit: GetRecentOperations loads all ops into memory anyway
	// (PebbleDB scans all keys, sorts, then slices), so increasing the cap is
	// free. Without a high limit, background maintenance/organize/scan ops
	// quickly push older metadata-fetch operations out of the top 200.
	ops, err := store.GetRecentOperations(5000)
	if err != nil {
		httputil.InternalError(c, "failed to list recent operations", err)
		return
	}
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
		if op.Type != "metadata_candidate_fetch" {
			continue
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
		summary := fetchOpSummary{
			ID:           op.ID,
			Type:         op.Type,
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

	store := s.Store()
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

	applied := 0
	skipped := 0
	var errors []string

	for _, bookID := range req.BookIDs {
		opResult, ok := resultsByBook[bookID]
		if !ok {
			skipped++
			continue
		}

		var cr CandidateResult
		if err := json.Unmarshal([]byte(opResult.ResultJSON), &cr); err != nil {
			errors = append(errors, fmt.Sprintf("%s: failed to parse result", bookID))
			continue
		}
		if cr.Candidate == nil || cr.Status != "matched" {
			skipped++
			continue
		}

		candidate := *cr.Candidate
		_, err := mfs.ApplyMetadataCandidate(bookID, candidate, nil)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: apply failed: %v", bookID, err))
			continue
		}

		applied++

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
				mfs.ApplyMetadataFileIO(bid)
				if _, err := mfs.WriteBackMetadataForBook(bid); err != nil {
					slog.Warn("write-back failed for", "bid", bid, "err", err)
				}
				if s.writeBackBatcher != nil {
					s.writeBackBatcher.Enqueue(bid)
				}
			})
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

	store := s.Store()

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

	store := s.Store()

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

// resumeInterruptedMetadataFetch checks for metadata_candidate_fetch operations
// that were interrupted (status=running) and re-enqueues the remaining books.
func (s *Server) resumeInterruptedMetadataFetch() {
	store := s.Store()
	if store == nil {
		return
	}

	interrupted, err := store.GetInterruptedOperations()
	if err != nil {
		return
	}

	for _, op := range interrupted {
		if op.Type != "metadata_candidate_fetch" {
			continue
		}

		// Load the original book IDs from saved params
		paramsJSON, err := store.GetOperationParams(op.ID)
		if err != nil || len(paramsJSON) == 0 {
			slog.Warn("no saved params for interrupted metadata fetch , marking failed", "op", op.ID)
			_ = store.UpdateOperationStatus(op.ID, "failed", op.Progress, op.Total, "interrupted, no params to resume")
			continue
		}

		var allBookIDs []string
		if err := json.Unmarshal(paramsJSON, &allBookIDs); err != nil {
			slog.Warn("invalid params for interrupted metadata fetch", "op", op.ID, "err", err)
			_ = store.UpdateOperationStatus(op.ID, "failed", op.Progress, op.Total, "interrupted, invalid params")
			continue
		}

		// Find which books already have results
		existingResults, _ := store.GetOperationResults(op.ID)
		completed := make(map[string]bool, len(existingResults))
		for _, r := range existingResults {
			completed[r.BookID] = true
		}

		// Filter to remaining books
		var remaining []string
		for _, id := range allBookIDs {
			if !completed[id] {
				remaining = append(remaining, id)
			}
		}

		if len(remaining) == 0 {
			slog.Info("interrupted metadata fetch has all results, marking completed", "op", op.ID)
			_ = store.UpdateOperationStatus(op.ID, "completed", len(allBookIDs), len(allBookIDs), "recovered — all books fetched")
			continue
		}

		slog.Info("resuming metadata fetch / books remaining", "op", op.ID, "remaining_count", len(remaining), "allBookIDs_count", len(allBookIDs))

		// Re-enqueue the remaining books via v2 opRegistry as a continuation of the
		// same v1 operation. The Run func in metadata_candidate_op.go handles all
		// fetch work and writes OperationResult rows under the original opID.
		opID := op.ID
		totalBooks := len(allBookIDs)
		alreadyDone := len(allBookIDs) - len(remaining)

		// Mark the v1 record as running so handleGetLatestMetadataFetch can surface
		// it during the resume window before the Run func transitions it itself.
		_ = store.UpdateOperationStatus(opID, "running", alreadyDone, totalBooks,
			fmt.Sprintf("resuming: %d/%d already fetched", alreadyDone, totalBooks))

		resumeParams := metadataCandidateFetchOpParams{
			LegacyOpID:  opID,
			BookIDs:     remaining,
			TotalBooks:  totalBooks,
			AlreadyDone: alreadyDone,
		}
		if _, enqErr := s.opRegistry.EnqueueOp(context.Background(), "metadata.candidate-fetch", resumeParams); enqErr != nil {
			slog.Warn("failed to re-enqueue metadata fetch", "opID", opID, "enqErr", enqErr)
			_ = store.UpdateOperationStatus(opID, "failed", alreadyDone, totalBooks, "failed to resume")
		}
	}
}

// latestMetadataResultsByBook scans the recent metadata_candidate_fetch
// operations and returns the LATEST OperationResult per book_id, plus a
// status histogram across the deduplicated set. The same helper backs both
// the unified GET /library/metadata-results endpoint and the legacy
// POST /metadata/pending-review endpoint, so the filter logic stays in
// one place.
//
// Returns (results-by-bookID, status-counts, error).
func latestMetadataResultsByBook(store database.Store) (map[string]database.OperationResult, map[string]int, error) {
	allOps, err := store.GetRecentOperations(5000)
	if err != nil {
		return nil, nil, err
	}

	type bookEntry struct {
		result    database.OperationResult
		createdAt time.Time
	}
	latest := map[string]bookEntry{}
	for _, op := range allOps {
		if op.Type != "metadata_candidate_fetch" {
			continue
		}
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
	store := s.Store()

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
	start := pp.Offset
	if start > total {
		start = total
	}
	end := total
	if pp.Limit > 0 {
		end = start + pp.Limit
		if end > total {
			end = total
		}
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
