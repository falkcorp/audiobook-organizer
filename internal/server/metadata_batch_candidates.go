// file: internal/server/metadata_batch_candidates.go
// version: 4.24.2
// guid: a1b2c3d4-e5f6-7a8b-9c0d-e1f2a3b4c5d6
// last-edited: 2026-10-06
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
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// Re-export metabatch types under server-local aliases so existing
// JSON serialisation and test references continue to compile unchanged.
type CandidateBookInfo = metabatch.CandidateBookInfo
type CandidateResult = metabatch.CandidateResult

// batchFetchRequest is the JSON body for handleBatchFetchCandidates.
// One of BookIDs, Selection or Stale must be provided; OnlyUnmatched can be
// combined with any of them to exclude books that already have a "matched"
// candidate.
type batchFetchRequest = metabatch.BatchFetchRequest

// batchApplyRequest is the JSON body for handleBatchApplyCandidates.
type batchApplyRequest = metabatch.BatchApplyRequest

var batchApplyCandidatesLog = logger.New("server.batch-apply-candidates")

var batchFetchCandidatesLog = logger.New("server.batch-fetch-candidates")

// handleBatchFetchCandidates creates a background operation that spawns parallel
// workers to fetch metadata candidates for the given book IDs.
func (s *Server) handleBatchFetchCandidates(c *gin.Context) {
	var req batchFetchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}

	store := s.Ops()

	// Resolve the target book IDs — from an explicit list, a SelectionSpec, or
	// the stale set.
	candidateIDs := req.BookIDs
	force := req.Force
	if len(candidateIDs) == 0 && req.Selection == nil && req.Stale {
		// Resolved here, not by the client, because the client only holds the
		// reviewable bucket: the rail read "3,511 stale" while the button
		// refetched the 10 stale rows it could see. StaleCachedBookIDs shares
		// its loader and predicate with the summary count, so the two cannot
		// drift.
		if s.metadataFetchService == nil {
			httputil.RespondWithInternalError(c, "metadata service not initialized")
			return
		}
		staleIDs, err := handlers.StaleCachedBookIDs(c.Request.Context(), s.storeForWiring(), s.metadataFetchService)
		if err != nil {
			httputil.InternalError(c, "failed to resolve stale metadata cache rows", err)
			return
		}
		if len(staleIDs) == 0 {
			// Not a bad request: the backlog is simply clear.
			httputil.RespondWithOK(c, gin.H{
				"message":      "no stale books to refetch",
				"operation_id": "",
				"book_count":   0,
				"skipped":      0,
			})
			return
		}
		candidateIDs = staleIDs
		// Every one of these is past MetadataCacheTTL and is meant to be
		// re-queried. Without force, a zero-candidate row every provider
		// answered empty 30-90 days ago is served as known-empty
		// (MetadataKnownEmptyTTL is 90 days), is not re-dated, and would stay
		// stale forever. Fresh-candidate rows cannot be in this set: the fetch's
		// fresh check and cacheRowStale use the same last-checked rule.
		force = true
		batchFetchCandidatesLog.Info("stale refetch resolved %d books", len(staleIDs))
	}
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
		httputil.RespondWithBadRequest(c, "book_ids, selection or stale is required")
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
	//
	// Read off the ACTIVE-operations index, not the capped history listing it
	// used to use: a long fetch could fall out of a 200-row history window
	// while still running, and the guard then waved every one of its books
	// through again. A failed read refuses rather than guessing "nothing is
	// running".
	//
	// force does NOT bypass this: forcing re-queries books whose cached answer
	// is still valid, but two runs fetching the same book at once would race
	// on its cache entry and pay the providers twice. A forced request for a
	// book already in flight waits for that run.
	alreadyFetching, err := metabatch.ActiveCandidateFetchBookIDs(store, s.opRegistry.IsRunning)
	if err != nil {
		httputil.InternalError(c, "failed to check running metadata fetches", err)
		return
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
		Force:      force,
		// "Search again" on ONE book is a person waiting on that book:
		// interactive for the daily quota budgets (owner decision
		// 2026-10-06). A selection, a stale refetch or several books stay
		// background.
		Interactive: singleBookSearch(req, bookIDs),
	}
	opID, enqErr := s.opRegistry.EnqueueOp(c.Request.Context(), "metadata.candidate-fetch", params)
	if enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	// book_count and skipped are what the caller should report: the books this
	// op will fetch and the ones left to a fetch already running. total_books
	// repeats book_count for existing readers.
	httputil.RespondWithSuccess(c, http.StatusAccepted, struct {
		OperationID string `json:"operation_id"`
		TotalBooks  int    `json:"total_books"`
		BookCount   int    `json:"book_count"`
		Skipped     int    `json:"skipped"`
		Message     string `json:"message"`
	}{
		OperationID: opID,
		TotalBooks:  totalBooks,
		BookCount:   totalBooks,
		Skipped:     skippedCount,
		Message:     "metadata candidate fetch started",
	})
}

// Values of CandidateResult.Cached: the fetch answered the book from the
// candidate cache without asking any provider.
const (
	candidateCachedCandidates = "candidates"
	candidateCachedKnownEmpty = "known_empty"
)

// fetchCandidateForBook fetches metadata candidates for a single book, respecting
// the rate limiter. Returns a CandidateResult.
//
// Unless force is set, a book the candidate cache can already answer for its
// CURRENT inputs costs no provider call (metafetch.CachedBatchVerdict): fresh
// cached candidates are served as-is, and a book every enabled provider has
// already answered with nothing is reported as no_match straight away. Before
// this, every run of "search providers" re-asked all four providers the full
// query ladder for the same ~8,000 books they had answered with nothing on
// every previous run -- those books are never "matched", so the only-unmatched
// selection hands them back every time, and the per-provider fetch cache does
// not store empty answers.
func (s *Server) fetchCandidateForBook(
	ctx context.Context,
	mfs *metafetch.Service,
	store candidateFetchStore,
	limiter *rate.Limiter,
	opID, bookID string,
	force bool,
	googleCapped bool,
	folderMemo *metabatch.FolderMemo,
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

	// The owner marked this book "no match": do not spend provider quota
	// searching for, or offer, a match they rejected. "skipped", not
	// "no_match": that status here means "the search found nothing".
	if metafetch.IsMarkedNoMatch(book.MetadataReviewStatus) {
		return CandidateResult{
			Book:   bookInfo,
			Status: "skipped",
			Error:  "skipped: marked no match",
		}
	}

	// A title not worth searching -- empty, a placeholder, a chapter number
	// or a chapter fragment of a shattered book ("06 Chapter 6";
	// metadata.IsUnsearchableTitle) -- is never searched as-is: the catalogs
	// answer it with whatever they rank first (two books titled "" matched
	// Audible's "Bad in Bed" while their intro said "Marvel's Planet Hulk";
	// "06 Chapter 6" matched a random entry at ~100%). The transcribed title
	// stands in when there is one, then the folder name; otherwise the book
	// is skipped. The stand-in is only a query: it is never written onto the
	// book. The resolved query is used for EVERY step below -- the cache
	// verdict, the fetch and the result -- because the cache verdict is keyed
	// on the query: checking it with the raw title would re-serve the junk
	// row an earlier "" search cached.
	//
	// This replaced a separate chapter-fragment skip here: every fragment is
	// unsearchable, and the resolver never returns one as a stand-in, so a
	// fragment with no fallback is now this skip, named by kind.
	query := metabatch.ResolveCandidateSearchQueryMemo(store, book, folderMemo)
	if !query.Usable {
		kind, _ := unsearchableQueryKind(query, book.Title)
		return CandidateResult{
			Book:   bookInfo,
			Status: "skipped",
			Error:  "skipped: " + kind + ", " + metabatch.SkipDetailNoUsableTitle,
		}
	}
	// searchAuthor is the author the ladder narrows by (set below, once the
	// hint is known); "" when the book has none or only a placeholder.
	searchAuthor := ""
	withQuery := func(r CandidateResult) CandidateResult {
		r.SearchQuery = query.Title
		r.SearchQuerySource = query.Source
		r.SearchAuthor = searchAuthor
		return r
	}

	// The hint is the book's LIVE primary author (database.LiveBookAuthorNames:
	// AuthorID, then the join), not the Book.Author snapshot, which
	// GetBookByID does not fill: hashing that empty snapshot recorded "no
	// author" for nearly every book, and the apply gate could then not tell a
	// row fetched for the book's author from one fetched before it changed.
	// The author is recorded on the result too (bookInfo.Author), for the
	// op-result path's fetchTimeIdentity. A read failure fails the book
	// rather than hashing it as authorless.
	//
	// A placeholder author ("Unknown Author", "read by narrator") is not a
	// hint: it is never sent, and the row is hashed without it
	// (metafetch.SearchAuthorHint, which the ladder applies again to the
	// author it resolves from AuthorID).
	liveAuthors, lerr := database.LiveBookAuthorNames(store, book)
	if lerr != nil {
		return CandidateResult{Book: bookInfo, Status: "error", Error: "read book authors: " + lerr.Error()}
	}
	author := ""
	if book.Author != nil {
		author = book.Author.Name
	}
	if len(liveAuthors) > 0 {
		author = liveAuthors[0]
		if bookInfo.Author == "" {
			bookInfo.Author = author
		}
	}
	var authorHint []string
	if a := metafetch.SearchAuthorHint(author); a != "" {
		authorHint = append(authorHint, a)
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
	searchAuthor = mfs.SearchAuthorFor(book, query.Title, authorForHash)
	// PROVIDER FALLBACK (owner decisions 2026-10-06, candidate_fallback.go).
	// Open Library and Google Books are not asked alongside the rest of the
	// chain: they are asked, in that order, only when the chain left the book
	// without a usable candidate (metabatch.NoUsableCandidate) -- Google under the
	// shared daily budget. With neither enabled, plan is empty and this is
	// the single search it always was.
	nameByID := mfs.ActiveSourceNamesByID()
	plan := candidateFallbackPlan(nameByID)
	active := activeFallbackPlan(plan)
	idByName := sourceIDsByName(nameByID)
	// carryFrom is the SourceHash of the book's row when it was vouched for
	// the book as it is now (metafetch.Service.VouchedCachedRow, or the
	// batch verdict's fresh row): every write of this fetch, the chain's and
	// the fallback's, carries that row's candidates as a same-inputs row's
	// are carried (SearchOptions.CarryFromSourceHash). Without it a row
	// hashed under other inputs (a raw author credit, a pre-2026-09-28
	// no-author row) was replaced by an empty chain answer -- every
	// scheduled tick, library-wide, for each book whose candidates were all
	// unusable.
	carryFrom := ""
	fallback := func(entry *metafetch.MetadataCandidateCache, why, cached string) CandidateResult {
		return s.runCandidateFallback(ctx, mfs, candidateFallbackInput{
			store: store, limiter: limiter, book: book, bookInfo: bookInfo, query: query.Title, author: authorForHash,
			force: force, pending: fallbackOwed(entry, active), entry: entry, why: why, cached: cached, withQuery: withQuery,
			googleCapped: googleCapped, carryFrom: carryFrom,
		})
	}

	// askOnly: providers without a valid answer for these inputs. When the
	// cache holds an empty result that some providers already answered, only
	// the rest are asked (a quota-starved provider no longer drags the others
	// into every run); nil asks every provider.
	var askOnly []string
	if !force {
		cached, verdict, ask := mfs.CachedBatchVerdict(book, query.Title, authorForHash)
		askOnly = ask
		switch verdict {
		case metafetch.BatchVerdictFreshCandidates:
			// Fresh candidates the owner cannot use (all rejected, all refused
			// by the ASIN checks, or below the apply floor) are not an answer:
			// a fallback provider that still owes one is asked. A row like this
			// was stuck before -- served from the cache forever, never selected.
			if len(active) > 0 {
				v := metabatch.NoUsableCandidate(store, book, cached)
				if !v.Usable && len(fallbackOwed(cached, active)) > 0 {
					carryFrom = cached.SourceHash // the verdict vouched for it
					return fallback(cached, v.Why, candidateCachedCandidates)
				}
				if v.Usable {
					// A usable candidate landed since a fallback lookup was
					// deferred (a dialog search, a chain refresh): the book
					// no longer waits on it.
					clearDeferral(mfs, bookID, cached)
				}
			}
			result := candidateResultFromEntry(store, bookInfo, bookID, query.Title, cached)
			result.Cached = candidateCachedCandidates
			return withQuery(result)
		case metafetch.BatchVerdictKnownEmpty:
			checked := "earlier"
			if cached.LastEmptyFetchAt != nil {
				checked = cached.LastEmptyFetchAt.UTC().Format("2006-01-02")
			}
			return withQuery(CandidateResult{
				Book:   bookInfo,
				Status: "no_match",
				Error: fmt.Sprintf("not refetched: every enabled provider returned nothing for this title/author (last checked %s); "+
					"edit the title or author, enable another provider, or force a refetch to ask again", checked),
				Cached: candidateCachedKnownEmpty,
			})
		}
	}

	ask := askOnly
	if len(ask) == 0 {
		ask = mfs.ActiveSourceNames()
	}
	primary := splitFallback(ask, plan)

	// The row this fetch writes over, when it still belongs to the book --
	// read here, not from the verdict: a forced refetch never asked the
	// verdict, and a None verdict also returns a row it did NOT vouch for
	// (another identity), whose candidates must be neither carried nor
	// merged into.
	entry := mfs.VouchedCachedRow(book, query.Title)
	if entry != nil {
		carryFrom = entry.SourceHash
	}
	if len(plan) == 0 || len(primary) > 0 {
		onlySources := askOnly
		if len(plan) > 0 {
			onlySources = primary
		}
		fetched, resp, ferr := mfs.FetchAndCacheWithResponse(ctx, limiter, bookID, query.Title, authorForHash, "", "",
			metafetch.SearchOptions{OnlySources: onlySources, BypassFetchCache: force, CarryFromSourceHash: carryFrom,
				// A refetch that keeps the fallback providers' candidates
				// ranks the union like the fallback's merge does.
				MergeRank: metabatch.MergeRanker(store, book)})
		if ferr != nil {
			// A primary chain that failed is not a "no match": the fallback
			// is not asked, and the next run asks the chain again.
			return withQuery(CandidateResult{
				Book:   bookInfo,
				Status: "error",
				Error:  fmt.Sprintf("search failed: %v", ferr),
			})
		}
		entry = fetched
		if len(active) == 0 {
			return withQuery(candidateResultFromEntry(store, bookInfo, bookID, query.Title, entry))
		}
		v := metabatch.NoUsableCandidate(store, book, entry)
		if v.Usable {
			clearDeferral(mfs, bookID, entry)
		}
		if v.Usable || len(fallbackOwed(entry, active)) == 0 {
			return withQuery(candidateResultFromEntry(store, bookInfo, bookID, query.Title, entry))
		}
		// A title-searching source of the chain FAILED (Audible down while
		// the ASIN-only Audnexus answered "no such ASIN"): the chain's
		// question went unanswered, so the fallback is not asked -- the next
		// run asks the chain again.
		if failed := failedTitleSource(resp, idByName); failed != "" {
			r := candidateResultFromEntry(store, bookInfo, bookID, query.Title, entry)
			if r.Status != "matched" {
				r.Status = "error"
				r.Error = "fallback not asked: the chain's " + failed
			}
			return withQuery(r)
		}
		return fallback(entry, v.Why, "")
	}
	if len(active) == 0 {
		return withQuery(candidateResultFromEntry(store, bookInfo, bookID, query.Title, entry))
	}
	return fallback(entry, metabatch.NoUsableCandidate(store, book, entry).Why, "")
}

// singleBookSearch reports the review page's "Search again" on one book: a
// request marked interactive for exactly one explicit book id (book_ids, no
// selection filter, not the stale refetch). The mark is required, not
// inferred from the count: a large selection's last chunk can hold one book.
func singleBookSearch(req metabatch.BatchFetchRequest, bookIDs []string) bool {
	return req.Interactive && len(req.BookIDs) == 1 && req.Selection == nil && !req.Stale && len(bookIDs) == 1
}

// candidateResultFromEntry turns a candidate-cache entry into the op's result
// row: the top candidate the owner has not already rejected, or no_match.
func candidateResultFromEntry(
	store candidateFetchStore,
	bookInfo CandidateBookInfo,
	bookID, title string,
	entry *metafetch.MetadataCandidateCache,
) CandidateResult {
	// Decode cached []json.RawMessage back into MetadataCandidate
	// for the OperationResult payload (back-compat with the progress UI).
	results := make([]metafetch.MetadataCandidate, 0, len(entry.Candidates))
	for _, raw := range entry.Candidates {
		var c metafetch.MetadataCandidate
		if jerr := json.Unmarshal(raw, &c); jerr == nil {
			results = append(results, c)
		}
	}
	resp := &metafetch.SearchMetadataResponse{Results: results, Query: title}

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
	var totalMatched, totalNoMatch, totalErrors, totalDeferred, totalSkipped int
	for _, r := range allRaw {
		switch r.Status {
		case "matched":
			totalMatched++
		case "no_match":
			totalNoMatch++
		case "error":
			totalErrors++
		case candidateStatusDeferred:
			totalDeferred++
		case "skipped":
			totalSkipped++
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
			slog.Warn("failed to unmarshal result for book in op", "r", r.BookID, "opID", logger.SanitizeLogValue(opID), "err", err)
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
		Deferred     int                 `json:"deferred"`
		Skipped      int                 `json:"skipped"`
		TotalMatched int                 `json:"total_matched"`
		TotalNoMatch int                 `json:"total_no_match"`
		TotalErrors  int                 `json:"total_errors"`
		// TotalDeferred: books whose fallback lookup was put off (budget
		// spent, a throttle hold, a passing failure), waiting on a later run;
		// TotalSkipped: books the fetch did not search (marked no match, no
		// usable title). Neither is a no_match or an error.
		TotalDeferred int `json:"total_deferred"`
		TotalSkipped  int `json:"total_skipped"`
		Limit         int `json:"limit"`
		Offset        int `json:"offset"`
	}{
		Operation:     op,
		Results:       candidateResults,
		Total:         totalCount,
		TotalCount:    totalCount,
		Matched:       metabatch.CountByStatus(candidateResults, "matched"),
		NoMatch:       metabatch.CountByStatus(candidateResults, "no_match"),
		Errors:        metabatch.CountByStatus(candidateResults, "error"),
		Deferred:      metabatch.CountByStatus(candidateResults, candidateStatusDeferred),
		Skipped:       metabatch.CountByStatus(candidateResults, "skipped"),
		TotalMatched:  totalMatched,
		TotalNoMatch:  totalNoMatch,
		TotalErrors:   totalErrors,
		TotalDeferred: totalDeferred,
		TotalSkipped:  totalSkipped,
		Limit:         limit,
		Offset:        offset,
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
		// DeferredCount / SkippedCount: see handleGetOperationResults.
		DeferredCount int `json:"deferred_count"`
		SkippedCount  int `json:"skipped_count"`
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
		var matched, noMatch, errCount, deferred, skipped int
		for _, r := range results {
			switch r.Status {
			case "matched":
				matched++
			case "no_match":
				noMatch++
			case "error":
				errCount++
			case candidateStatusDeferred:
				deferred++
			case "skipped":
				skipped++
			}
		}
		// Type is still reported as the v1 string. The frontend keys the Resume
		// Review dialog off it, and a run's kind did not change when its id did.
		summary := fetchOpSummary{
			ID:            op.ID,
			Type:          "metadata_candidate_fetch",
			Status:        op.Status,
			CreatedAt:     op.CreatedAt,
			ResultCount:   len(results),
			MatchedCount:  matched,
			NoMatchCount:  noMatch,
			ErrorCount:    errCount,
			DeferredCount: deferred,
			SkippedCount:  skipped,
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
	var req batchApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "operation_id and book_ids are required")
		return
	}
	if len(req.BookIDs) == 0 {
		httputil.RespondWithBadRequest(c, "book_ids must not be empty")
		return
	}
	// Dry run unless the caller says dry_run:false: the preview reports, for
	// each selected book, the gate verdict, field changes and rename, and
	// applies nothing. It is read-only, so it takes neither the apply cap nor
	// the scan stand-down hold below.
	if req.DryRun == nil || *req.DryRun {
		s.enqueueBulkApplyPreview(c, bulkApplyPreviewParams{
			BookIDs: req.BookIDs, Source: previewSourceOpResults, OperationID: req.OperationID,
		})
		return
	}
	// Fail-safe cap (internal/applycap): refuse an implausibly large selection
	// before a single candidate is applied. Refusal, not truncation.
	if ex := applycap.Refuse("batch-apply-candidates", len(req.BookIDs), config.AppConfig.BulkApplyMaxItems); ex != nil {
		httputil.RespondWithApplyCapExceeded(c, ex)
		return
	}

	// Scan coordination is per BOOK (internal/scanlock), never library-wide:
	// each book is applied under its own scan lock, so only a book the library
	// scan is reading right now waits, and one it holds past the bound is
	// queued rather than refused. Until 2026-09-30 this whole request answered
	// 409 SCAN_RUNNING while any scan ran. See applyOpResultBooks.

	// The list this feeds is memoised; a status change must not keep offering a
	// candidate the user just acted on.
	defer invalidateMetadataResultsCache()

	resultsByBook, claims, loadErr := s.loadOpResultApplyInputs(c.Request.Context(), req.OperationID)
	if loadErr != nil {
		httputil.InternalError(c, "failed to load the operation's candidates", loadErr)
		return
	}

	outcomes := s.applyOpResultBooks(c.Request.Context(), req.OperationID, req.BookIDs, resultsByBook, claims)

	applied := 0
	skipped := 0
	var errors, blocked, queued, queuedOps []string
	for i, o := range outcomes {
		switch {
		case o.applied:
			applied++
		case o.skipped:
			skipped++
		case o.queued:
			queued = append(queued, req.BookIDs[i])
			queuedOps = append(queuedOps, o.queuedOpID)
		case o.blocked:
			blocked = append(blocked, o.blockMsg)
		case o.errMsg != "":
			errors = append(errors, o.errMsg)
		}
	}

	httputil.RespondWithOK(c, struct {
		Applied int `json:"applied"`
		Skipped int `json:"skipped"`
		// Blocked lists books refused before any write, with the reason: the
		// certainty gate (the match was not certain enough to apply), or
		// file_work_would_fail (the rename after the apply is known to fail).
		// Not errors: nothing failed and nothing was written.
		Blocked      []string `json:"blocked"`
		BlockedCount int      `json:"blocked_count"`
		Errors       []string `json:"errors"`
		ErrorCount   int      `json:"error_count"`
		OperationID  string   `json:"operation_id"`
		// UnreadableBooks counts books of the operation the sibling-part
		// index could not read; the same field as the preview summary's.
		UnreadableBooks int `json:"unreadable_books"`
		// QueuedBookIDs lists books the library scan was reading for longer
		// than the wait bound. Nothing was refused: each is handed to a
		// metadata.apply-when-scanned operation (QueuedOperationIDs, same
		// order) that applies it as soon as the scan moves on.
		QueuedBookIDs      []string `json:"queued_book_ids"`
		QueuedCount        int      `json:"queued_count"`
		QueuedOperationIDs []string `json:"queued_operation_ids"`
	}{
		Applied:            applied,
		Skipped:            skipped,
		Blocked:            blocked,
		BlockedCount:       len(blocked),
		Errors:             errors,
		ErrorCount:         len(errors),
		OperationID:        req.OperationID,
		UnreadableBooks:    claims.Unreadable(),
		QueuedBookIDs:      queued,
		QueuedCount:        len(queued),
		QueuedOperationIDs: queuedOps,
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
//	                       (matched / no_match / applied / rejected / error /
//	                       deferred / skipped / unfetched).
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

	// latest is a map, so without a sort every request walked it in a new
	// random order and offset paging returned an arbitrary slice each time:
	// pages repeated some books and never reached others. Book ID is the
	// key, not fetch time: the cached set is refreshed in the background
	// while a client pages, and a newest-first order would shift every later
	// offset each time a fetch landed. A new result for a book already
	// listed keeps its place.
	sort.Slice(all, func(i, j int) bool { return all[i].BookID < all[j].BookID })

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
