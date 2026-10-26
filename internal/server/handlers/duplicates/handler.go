// file: internal/server/handlers/duplicates/handler.go
// version: 1.26.1
// guid: 9f41f363-34fc-4ad2-b2f1-46d5ac0ba2f3
// last-edited: 2026-10-26

// Package duplicates hosts the SQL-backed duplicate-detection HTTP handlers
// extracted from the server package's duplicates_handlers.go: author / series
// duplicate listing, async link / refresh triggers, combine and the combine /
// sibling-move journal lists (their undo routes were retired, 01-P81h),
// series prune preview + apply, deduplicate, and dedup-entry metadata
// validation. The book-duplicate listing, scan, link-as-versions and reject
// handlers were retired with the old /dedup page (03-PR12); their routes answer
// 410 Gone from package server.
// (Series normalize has no handler here any more; it runs only as an op.)
// Distinct from the embedding-based dedup flow in handlers/dedup.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (DuplicatesStore, OperationsRegistry, MergeService,
// MetadataFetchService) plus the concrete
// *cache.Cache[gin.H] (the dedup cache — a clean generic type held by pointer,
// the established cache exception) and a set of INJECTED func fields that wrap
// helpers which STAY in package server because they are shared with files that
// did not move (duplicates_ops.go, server_maintenance_deps.go). Those helpers
// were relocated to internal/server/duplicates_helpers.go; the controller
// passes thin closures over them. As a result package duplicates never imports
// package server.
//
// The store is a WIRE-TIME SNAPSHOT (assigned once in New, never swapped after
// that). opRegistry / mergeService / metadataFetchService are
// also interface snapshots taken at wire time (assigned once before setupRoutes,
// never swapped), each guarded against typed-nil boxing by the controller.

package duplicates

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/gin-gonic/gin"
)

// Handler hosts the duplicates-domain HTTP endpoints.
type Handler struct {
	// store is the wire-time snapshot of the database store. All handlers read
	// from this field directly (no lazy provider needed).
	store DuplicatesStore

	// dedupCache is the concrete *cache.Cache[gin.H] the original handlers read /
	// wrote directly (author-duplicates / series-duplicates keys; the
	// book-dedup-scan key is only invalidated here, after a combine,
	// and currently has no reader: see todo.d/2026-10-17-remove-audiobooks-duplicates-410-stubs.md for the pending decision
	// with 04 on the dedup.book-scan op that writes it). Passed concrete (the cache exception) because it is a
	// clean generic db-adjacent type under heavy multi-method use; the handler
	// nil-checks it where the original did.
	dedupCache *cache.Cache[gin.H]

	// opRegistry backs the scan / merge / refresh / dedup / prune
	// triggers. Interface snapshot guarded by the controller against typed-nil
	// boxing so the in-method `== nil` guards (mirroring the old
	// `s.opRegistry == nil` checks) hold.
	opRegistry OperationsRegistry

	// metadataFetchService backs validateDedupEntry (BuildSourceChain). Interface
	// snapshot, typed-nil guarded by the controller.
	metadataFetchService MetadataFetchService

	// getMergeService resolves the merge service for combineBooks and the
	// combine / sibling-move journal list handlers.
	// The original handler used s.mergeService when non-nil, else constructed
	// merge.NewService(s.Ops()). The controller closes over that exact fallback
	// so this package needs neither the merge constructor nor a server import.
	getMergeService func() MergeService

	// --- injected funcs wrapping helpers that stay in package server ---

	// computeSeriesPrunePreview wraps the server-resident computeSeriesPrunePreview
	// (server_title_helpers.go) over the live store, returning the preview payload
	// (any) and an error. seriesPrunePreview serializes the payload verbatim.
	computeSeriesPrunePreview func() (any, error)

	// othersUserData reports whether the request's caller may read other
	// users' listening state (users.manage). The merge journals carry every
	// user's progress on the books a merge folded together; the two list
	// routes are library.view reads, so they strip that for a caller this
	// refuses. Unset (nil) strips it for everyone: see
	// WithOthersUserDataAccess.
	othersUserData func(*gin.Context) bool
}

// WithOthersUserDataAccess installs the check that keeps the merge journals'
// per-user progress (GET /merge/combine-journal, /merge/sibling-journal) to
// callers who may manage users. A setter rather than a New parameter, like
// the other handlers' access checks; omitting it fails closed (the progress
// is stripped for everyone), and the server's wiring is pinned by
// TestMergeJournals_ProgressIsAdminOnly.
func (h *Handler) WithOthersUserDataAccess(f func(*gin.Context) bool) *Handler {
	h.othersUserData = f
	return h
}

// mayReadOthersUserData is othersUserData, false when unset.
func (h *Handler) mayReadOthersUserData(c *gin.Context) bool {
	return h.othersUserData != nil && h.othersUserData(c)
}

// New constructs a duplicates Handler from its dependencies.
func New(
	store DuplicatesStore,
	dedupCache *cache.Cache[gin.H],
	opRegistry OperationsRegistry,
	metadataFetchService MetadataFetchService,
	getMergeService func() MergeService,
	computeSeriesPrunePreview func() (any, error),
) *Handler {
	return &Handler{
		store:                     store,
		dedupCache:                dedupCache,
		opRegistry:                opRegistry,
		metadataFetchService:      metadataFetchService,
		getMergeService:           getMergeService,
		computeSeriesPrunePreview: computeSeriesPrunePreview,
	}
}

// dupOpResponse is the 202 body every async duplicates trigger returns.
//
// The id is the v2 operation id, and that is the whole point: the client polls
// it via GET /operations/v2/:id, which reads the v2 store and has no fallback
// to the retired v1 table. Handing back a v1 id here made every one of these
// endpoints report failure for work that actually succeeded.
//
// ID is the only field here read back from the enqueue. Everything else is a
// PLACEHOLDER for the single render frame before the client's first poll
// returns, and is carried rather than omitted because the web client types this
// response as its Operation interface, whose progress/total/message/created_at
// are non-optional — omitting them would make that declared type a lie and
// blank the progress bar for one interval.
//
// Status is "queued" because that is the status EnqueueOp actually writes on a
// new row (registry/registry.go:691). Do not put a v1 status word like
// "pending" here: the client polls GET /operations/v2/:id and compares against
// the v2 vocabulary, so a v1 word matches nothing on the first frame.
//
// These placeholders can be momentarily wrong in one case, and that is
// accepted: EnqueueOp's ConcurrencyKey dedupe returns an ALREADY-RUNNING op's
// id when one matches, so status/progress/created_at describe a fresh run that
// is really mid-flight. The first poll (≤1s) replaces all of them with the real
// row. Reading the row back to avoid that would mean re-adding the store
// dependency this package just shed, to fix a value that is stale for under a
// second.
type dupOpResponse struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	Total     int    `json:"total"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
	Detail    string `json:"detail,omitempty"`
}

// launchOp enqueues defID with params and responds 202 with the enqueued v2
// operation. opType is the client-facing label for the operation; it is not a
// registry key. On failure it writes the error response itself and returns
// without a 202, so callers have nothing to check.
func (h *Handler) launchOp(c *gin.Context, defID, opType, detail string, params any) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), defID, params)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue operation", err)
		return
	}
	httputil.RespondWithSuccess(c, 202, dupOpResponse{
		ID:        opID,
		Type:      opType,
		Status:    "queued",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Detail:    detail,
	})
}

// respondMergeError maps the merge service's typed refusals onto HTTP. A
// missing book is 404 naming the ID. A refusal of the CALLER'S choice — the
// picked primary has no audio route while another book does, a participant is
// soft-deleted, or one is still awaiting its deep scan — is 409 carrying the service's own message,
// because that message is the only place the user learns why their pick lost;
// until 2026-09-02 both came back as a generic 500 and the reason was only in
// the server log. Anything else is the 500 it always was.
//
// A combine refused only because the library index is still loading is not
// answered here: the combine's holder-list check runs inside the queued
// operation (internal/server/duplicates_ops.go), which ends as "retry later"
// (BookCombineOpResult.RetryLater), so nothing this handler checks can produce it.
func respondMergeError(c *gin.Context, err error, fallback string) {
	var notFound *merge.BookNotFoundError
	switch {
	case errors.As(err, &notFound):
		httputil.RespondWithNotFound(c, "book", notFound.BookID)
	case merge.IsRefusal(err):
		// RespondWithConflict does not log. A soft-deleted participant means
		// the caller picked from a stale view of the library, so operators
		// need the line even though the user got the reason.
		slog.Warn("merge refused", "path", logger.SanitizeLogValue(c.FullPath()), "err", logger.SanitizeLogValue(fmt.Sprint(err)))
		httputil.RespondWithConflict(c, err.Error())
	default:
		httputil.InternalError(c, fallback, err)
	}
}

// ListCombineJournals lists combine undo journals newest-first.
// GET /merge/combine-journal?limit=N (default 50).
func (h *Handler) ListCombineJournals(c *gin.Context) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			httputil.RespondWithBadRequest(c, "limit must be a non-negative integer")
			return
		}
		limit = n
	}
	ms := h.getMergeService()
	if ms == nil {
		httputil.RespondWithInternalError(c, "merge service not initialized")
		return
	}
	journals, err := ms.ListCombineJournals(limit)
	if err != nil {
		httputil.InternalError(c, "failed to list combine journals", err)
		return
	}
	if !h.mayReadOthersUserData(c) {
		journals = combineJournalsWithoutUserData(journals)
	}
	httputil.RespondWithOK(c, gin.H{"journals": journals, "count": len(journals)})
}

// Sibling-journal list paging: limit=0 or absent is the default, and a larger
// limit is capped, so one request never returns the whole journal history.
const (
	siblingJournalListDefault = 50
	siblingJournalListMax     = 500
)

// ListSiblingMoveJournals lists sibling-move journals newest-first.
// GET /merge/sibling-journal?limit=N (default 50, 0 means the default, capped
// at 500). Each pending journal older than merge.SiblingJournalStaleAfter
// carries stale=true: its merge failed or crashed part way.
func (h *Handler) ListSiblingMoveJournals(c *gin.Context) {
	limit := siblingJournalListDefault
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			httputil.RespondWithBadRequest(c, "limit must be a non-negative integer")
			return
		}
		if n > 0 {
			limit = min(n, siblingJournalListMax)
		}
	}
	ms := h.getMergeService()
	if ms == nil {
		httputil.RespondWithInternalError(c, "merge service not initialized")
		return
	}
	journals, err := ms.ListSiblingMoveJournals(limit)
	if err != nil {
		httputil.InternalError(c, "failed to list sibling-move journals", err)
		return
	}
	if !h.mayReadOthersUserData(c) {
		journals = siblingJournalsWithoutUserData(journals)
	}
	httputil.RespondWithOK(c, gin.H{"journals": journals, "count": len(journals)})
}

// combineJournalsWithoutUserData is js without other users' listening state,
// for a caller who may not manage users: each absorbed book's Progress (every
// user's state and positions on both books, before and after) and the
// warnings that name a user (a skipped or kept follow: "progress of user
// <id> on <book> ...", "user <id> progress on survivor ..."). The rest of
// the journal -- books, files, ids, status -- is library data and stays. js
// is not modified.
func combineJournalsWithoutUserData(js []merge.CombineJournal) []merge.CombineJournal {
	out := make([]merge.CombineJournal, len(js))
	for i, j := range js {
		j.Absorbed = slices.Clone(j.Absorbed)
		for k := range j.Absorbed {
			j.Absorbed[k].Progress = nil
		}
		j.Warnings = warningsWithoutUsers(j.Warnings)
		j.LastError = withoutUser(j.LastError)
		out[i] = j
	}
	return out
}

// siblingJournalsWithoutUserData is combineJournalsWithoutUserData for
// sibling-move journals: each state follow's Progress and the warnings that
// name a user go. js is not modified.
func siblingJournalsWithoutUserData(js []merge.SiblingMoveJournal) []merge.SiblingMoveJournal {
	out := make([]merge.SiblingMoveJournal, len(js))
	for i, j := range js {
		j.StateFollows = slices.Clone(j.StateFollows)
		for k := range j.StateFollows {
			j.StateFollows[k].Progress = nil
		}
		j.Warnings = warningsWithoutUsers(j.Warnings)
		j.LastError = withoutUser(j.LastError)
		out[i] = j
	}
	return out
}

// warningsWithoutUsers drops every warning that mentions a user. The merge
// writes a user's id only into its progress warnings, so this errs towards
// withholding: a warning that says "user" for any other reason goes too.
func warningsWithoutUsers(ws []string) []string {
	var out []string
	for _, w := range ws {
		if withoutUser(w) != "" {
			out = append(out, w)
		}
	}
	return out
}

// withoutUser is s, or "" when s mentions a user (see warningsWithoutUsers).
func withoutUser(s string) string {
	if strings.Contains(strings.ToLower(s), "user") {
		return ""
	}
	return s
}

// LinkBooks enqueues an async book-link operation (books become version-group
// members; no files move). POST /audiobooks/link.
//
// Renamed from MergeBooks (naming-audit class "merge vs link verbs",
// docs/audits/2026-09-25-interface-naming-consistency.md class 2): the
// underlying "dedup.book-merge" op routes through merge.Service.MergeBooks,
// which links the losers into the winner's version group and soft-deletes
// their book rows without touching any files on disk. The internal
// merge.Service.MergeBooks method name and the "dedup.book-merge" op ID are
// unchanged (op IDs are a separate audit class with their own resumability
// constraints). The old POST /audiobooks/merge alias was removed (01-P72).
func (h *Handler) LinkBooks(c *gin.Context) {
	var req struct {
		KeepID   string   `json:"keep_id" binding:"required"`
		MergeIDs []string `json:"merge_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.MergeIDs) == 0 {
		httputil.RespondWithBadRequest(c, "merge_ids must not be empty")
		return
	}

	store := h.store
	keepBook, err := store.GetBookByID(req.KeepID)
	if err != nil {
		// The store returns (nil, nil) for a missing row; a non-nil error is
		// I/O or corruption and is not "the book is gone".
		httputil.InternalError(c, "failed to load keep book", err)
		return
	}
	if keepBook == nil {
		httputil.RespondWithNotFound(c, "book", req.KeepID)
		return
	}

	// Refuse here what the operation would refuse after it ran, so a request
	// that cannot succeed fails this call (409 with the reason) instead of
	// queueing an operation whose failure shows only in the operations list.
	// A trashed loser is not refused: the operation skips it.
	ms := h.getMergeService()
	if ms == nil {
		httputil.RespondWithInternalError(c, "merge service not initialized")
		return
	}
	if err := ms.PreflightLink(req.KeepID, req.MergeIDs); err != nil {
		respondMergeError(c, err, "failed to check the merge")
		return
	}

	detail := fmt.Sprintf("merge-books:keep=%s,merge=%d", req.KeepID, len(req.MergeIDs))
	h.launchOp(c, "dedup.book-merge", "book-merge", detail,
		bookMergeOpParams{KeepID: req.KeepID, MergeIDs: req.MergeIDs, Detail: detail})
}

// CombineBooks queues the dedup.book-combine operation: it combines several
// single-file books into ONE multi-file book on the survivor (keep_id) and
// soft-deletes the absorbed shells. Distinct from LinkBooks, which links them as
// alternate VERSIONS in a version group.
// POST /audiobooks/combine.
//
// It does not run the combine. Moving every file and journaling each step is
// not fast on a real library (it hung the page), so the request is checked here
// (merge.PreflightCombine: 400 for a malformed request, 404 for a missing book,
// 409 for a soft-deleted or iTunes-protected one) and answered 202 with the
// operation id. The combine's outcome (files moved, books trashed, the id that
// undoes it, a part-way failure) is the operation's result, read from
// GET /operations/:id/result.
func (h *Handler) CombineBooks(c *gin.Context) {
	var req struct {
		KeepID   string   `json:"keep_id" binding:"required"`
		MergeIDs []string `json:"merge_ids" binding:"required"`
		// Override is optional. Non-empty fields overwrite the survivor's metadata
		// after all files are reassigned. Useful when none of the source books has
		// the correct title/author (e.g. every file was titled after its chapter).
		Override *merge.CombineOverride `json:"override,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.MergeIDs) == 0 {
		httputil.RespondWithBadRequest(c, "merge_ids must not be empty")
		return
	}
	seen := map[string]bool{req.KeepID: true}
	for _, id := range req.MergeIDs {
		if seen[id] {
			httputil.RespondWithBadRequest(c, fmt.Sprintf("book %s is listed more than once (merge_ids must not repeat or include keep_id)", id))
			return
		}
		seen[id] = true
	}

	ms := h.getMergeService()
	if ms == nil {
		httputil.RespondWithInternalError(c, "merge service not initialized")
		return
	}
	// Refuse here what the operation would refuse before it wrote anything, so
	// a request that cannot succeed fails this call instead of queueing an
	// operation whose failure shows only in the operations list. The check does
	// not read the holder list: a combine that needs it and finds the library
	// index still loading ends as "retry later" inside the operation.
	if err := ms.PreflightCombine(req.KeepID, req.MergeIDs); err != nil {
		respondMergeError(c, err, "failed to check the combine")
		return
	}

	detail := fmt.Sprintf("combine-books:keep=%s,merge=%d", req.KeepID, len(req.MergeIDs))
	h.launchOp(c, "dedup.book-combine", "book-combine", detail,
		bookCombineOpParams{KeepID: req.KeepID, MergeIDs: req.MergeIDs, Override: req.Override, Detail: detail})
}

// ListDuplicateAuthors handles GET /authors/duplicates.
func (h *Handler) ListDuplicateAuthors(c *gin.Context) {
	if h.dedupCache != nil {
		if cached, ok := h.dedupCache.Get("author-duplicates"); ok {
			httputil.RespondWithOK(c, cached)
			return
		}
	}

	// No cache — return empty with needs_refresh flag so frontend triggers async scan
	httputil.RespondWithOK(c, gin.H{"groups": []any{}, "count": 0, "needs_refresh": true})
}

// RefreshDuplicateAuthors enqueues an async author-dedup scan.
// POST /authors/duplicates/refresh.
func (h *Handler) RefreshDuplicateAuthors(c *gin.Context) {
	h.launchOp(c, "dedup.author-scan", "author-dedup-scan", "author-dedup-scan",
		authorDedupScanOpParams{})
}

// ListSeriesDuplicates handles GET /series/duplicates.
func (h *Handler) ListSeriesDuplicates(c *gin.Context) {
	if h.dedupCache != nil {
		if cached, ok := h.dedupCache.Get("series-duplicates"); ok {
			httputil.RespondWithOK(c, cached)
			return
		}
	}

	// No cache — return empty with needs_refresh flag so frontend triggers async scan
	httputil.RespondWithOK(c, gin.H{"groups": []any{}, "count": 0, "total_series": 0, "needs_refresh": true})
}

// RefreshSeriesDuplicates enqueues an async series-dedup scan.
// POST /series/duplicates/refresh.
func (h *Handler) RefreshSeriesDuplicates(c *gin.Context) {
	h.launchOp(c, "dedup.series-scan", "series-dedup-scan", "series-dedup-scan",
		seriesDedupScanOpParams{})
}

// ValidateDedupEntry searches metadata sources (OpenLibrary, Audible, etc.) to
// validate a series name, author name, or book title during dedup review.
// POST /dedup/validate.
func (h *Handler) ValidateDedupEntry(c *gin.Context) {
	var req struct {
		Query string `json:"query" binding:"required"`
		Type  string `json:"type"` // "series", "author", "book"
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "query is required")
		return
	}
	if req.Type == "" {
		req.Type = "series"
	}

	if h.metadataFetchService == nil {
		httputil.RespondWithOK(c, gin.H{"results": []any{}, "message": "no metadata sources configured"})
		return
	}

	chain := h.metadataFetchService.BuildSourceChain()
	if len(chain) == 0 {
		httputil.RespondWithOK(c, gin.H{"results": []any{}, "message": "no metadata sources configured"})
		return
	}

	type validationResult struct {
		Source         string `json:"source"`
		Title          string `json:"title"`
		Author         string `json:"author"`
		Series         string `json:"series,omitempty"`
		SeriesPosition string `json:"series_position,omitempty"`
		CoverURL       string `json:"cover_url,omitempty"`
		ISBN           string `json:"isbn,omitempty"`
	}

	var results []validationResult
	ctx := c.Request.Context()
	for _, src := range chain {
		matches, err := src.SearchByTitle(ctx, req.Query)
		if err != nil {
			continue
		}
		for _, m := range matches {
			r := validationResult{
				Source:         src.Name(),
				Title:          m.Title,
				Author:         m.Author,
				Series:         m.Series,
				SeriesPosition: m.SeriesPosition,
				CoverURL:       m.CoverURL,
				ISBN:           m.ISBN,
			}
			// For series validation, prioritize results that have series info
			if req.Type == "series" && m.Series == "" {
				continue
			}
			results = append(results, r)
		}
		// Limit total results
		if len(results) >= 20 {
			results = results[:20]
			break
		}
	}

	if results == nil {
		results = []validationResult{}
	}
	httputil.RespondWithOK(c, gin.H{"results": results, "query": req.Query, "type": req.Type})
}

// DeduplicateSeriesHandler enqueues an async series-dedup operation.
// POST /series/deduplicate.
//
// The optional body {"dry_run": false} opts out of the preview. Absent,
// empty, or unparseable bodies leave DryRun nil, which the op reads as
// dry_run=true (TODO.md L3966) — every failure mode of this bind lands on the
// side that writes nothing, and this endpoint took no body at all before, so
// existing callers get the preview rather than a 400.
func (h *Handler) DeduplicateSeriesHandler(c *gin.Context) {
	var req struct {
		DryRun *bool `json:"dry_run"`
	}
	_ = c.ShouldBindJSON(&req)
	h.launchOp(c, "dedup.series-dedup", "series-dedup", "series-deduplicate",
		seriesDedupOpParams{Detail: "series-deduplicate", DryRun: req.DryRun})
}

// SeriesPrunePreview returns a dry-run preview of the series auto-prune.
// GET /series/prune/preview.
func (h *Handler) SeriesPrunePreview(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	preview, err := h.computeSeriesPrunePreview()
	if err != nil {
		httputil.InternalError(c, "failed to compute series prune preview", err)
		return
	}

	httputil.RespondWithOK(c, preview)
}

// SeriesPrune enqueues an async series-prune operation. POST /series/prune.
func (h *Handler) SeriesPrune(c *gin.Context) {
	h.launchOp(c, "dedup.series-prune", "series-prune", "series-prune",
		seriesPruneOpParams{Detail: "series-prune"})
}

// MergeSeriesGroup enqueues an async series-merge operation, reassigning all
// books from the merge IDs to the keep ID. POST /series/merge.
func (h *Handler) MergeSeriesGroup(c *gin.Context) {
	var req struct {
		KeepID     int    `json:"keep_id" binding:"required"`
		MergeIDs   []int  `json:"merge_ids" binding:"required"`
		CustomName string `json:"custom_name"`
		// HonorLocks is optional; absent means true (see dedup.SeriesMergeParams).
		HonorLocks *bool `json:"honor_locks"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.MergeIDs) == 0 {
		httputil.RespondWithBadRequest(c, "merge_ids must not be empty")
		return
	}

	store := h.store
	keepSeries, err := store.GetSeriesByID(req.KeepID)
	if err != nil || keepSeries == nil {
		httputil.RespondWithNotFound(c, "series", "")
		return
	}

	detail := fmt.Sprintf("merge-series:keep=%d,merge=%v", req.KeepID, req.MergeIDs)
	h.launchOp(c, "dedup.series-merge", "series-merge", detail,
		seriesMergeOpParams{KeepID: req.KeepID, MergeIDs: req.MergeIDs, CustomName: req.CustomName, HonorLocks: req.HonorLocks, Detail: detail})
}
