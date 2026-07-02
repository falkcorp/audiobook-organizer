// file: internal/server/handlers/dedup/handler.go
// version: 1.8.0
// guid: d1b9e024-d28c-4d62-8f90-96d7064559c4
// last-edited: 2026-07-02

// Package deduphandler hosts the dedup-domain HTTP handlers extracted from the
// server package: dedup candidate / cluster / series listing, merge / dismiss /
// remove, bulk merge, stats, CSV/JSON export, and the dedup / embed / acoustid /
// book-signature scan triggers, plus the per-segment acoustid compare endpoint.
//
// Dependencies that lived on the *Server receiver are reached through narrow
// interfaces (DedupStore, OperationsRegistry, MergeService, DedupEngine) plus
// the concrete *database.EmbeddingStore (heavy multi-method use of a clean db
// type) and two injected funcs (publishEvent / markDuplicatesFlaggedDirty) that
// wrap *Server methods which stay in package server (they are shared with other
// domains). As a result package deduphandler never imports package server.
//
// The store and the embedding store are WIRE-TIME SNAPSHOTS (assigned once in
// New, never swapped after that). opRegistry / mergeService / dedupEngine are
// also interface snapshots taken at wire time (assigned once in registry_wire,
// before setupRoutes, and never swapped), each guarded against typed-nil boxing
// by the controller.
//
// NAME NOTE: this package is `deduphandler` (dir internal/server/handlers/dedup/)
// to avoid clashing with the dedup ENGINE package internal/dedup, imported here
// under its normal `dedup` name.

package deduphandler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/gin-gonic/gin"
)

// Handler hosts the dedup-domain HTTP endpoints.
type Handler struct {
	// store is the wire-time snapshot of the database store. All handlers read
	// from this field directly (no lazy provider needed).
	store DedupStore

	// embeddingStore is the wire-time snapshot of the concrete
	// *database.EmbeddingStore. Concrete pointer (not interface) because
	// EmbeddingStore is a clean db type under heavy multi-method use.
	embeddingStore *database.EmbeddingStore

	// opRegistry backs the scan-trigger endpoints. Interface snapshot guarded by
	// the controller against typed-nil boxing so the in-method `== nil` guards
	// (mirroring the old `s.opRegistry == nil` checks) hold.
	opRegistry OperationsRegistry

	// mergeService backs the merge / bulk-merge / cluster / series endpoints.
	// Interface snapshot, typed-nil guarded by the controller.
	mergeService MergeService

	// dedupEngine backs mergeDedupCandidate's post-merge orphan sweep. Interface
	// snapshot, typed-nil guarded by the controller.
	dedupEngine DedupEngine

	// publishEvent wraps *Server.publishEvent, which stays in package server (it
	// is shared with the audiobooks / metadata domains). The controller passes
	// s.publishEvent.
	publishEvent func(ctx context.Context, event plugin.Event)

	// markDuplicatesFlaggedDirty wraps *Server.markDuplicatesFlaggedDirty, which
	// stays in package server (shared elsewhere). The controller passes
	// s.markDuplicatesFlaggedDirty.
	markDuplicatesFlaggedDirty func(reason string)
}

// New constructs a dedup Handler from its dependencies.
func New(
	store DedupStore,
	embeddingStore *database.EmbeddingStore,
	opRegistry OperationsRegistry,
	mergeService MergeService,
	dedupEngine DedupEngine,
	publishEvent func(ctx context.Context, event plugin.Event),
	markDuplicatesFlaggedDirty func(reason string),
) *Handler {
	return &Handler{
		store:                      store,
		embeddingStore:             embeddingStore,
		opRegistry:                 opRegistry,
		mergeService:               mergeService,
		dedupEngine:                dedupEngine,
		publishEvent:               publishEvent,
		markDuplicatesFlaggedDirty: markDuplicatesFlaggedDirty,
	}
}

// ListDedupCandidates handles GET /api/v1/dedup/candidates.
//
// Query params:
//
//	entity_type — filter by entity type (e.g. "book")
//	status      — filter by status (e.g. "pending")
//	layer       — filter by detection layer (e.g. "embedding")
//	min_similarity (float) — lower-bound on raw similarity score
//	band        — filter by unified scoring band: CERTAIN|HIGH|MEDIUM|REVIEW
//	             (T016 extension; empty = no filter; pre-T015 rows without
//	             a stored band will not match any non-empty band filter)
//	include_breakdown=true — include score_breakdown (full signal array)
//	                         in each row; default false (payload savings)
//	limit (int, default 50), offset (int) — pagination
//
// Response shape (T016 contract, frozen for T017):
//
//	{ "candidates": [ candidateListItem, ... ], "total": N }
//
// Each candidateListItem extends DedupCandidate with a top-level "score"
// field (the composite 0–100 value). "band" is already part of
// DedupCandidate. "score_breakdown" is omitted unless include_breakdown=true.
func (h *Handler) ListDedupCandidates(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	filter := database.CandidateFilter{Limit: 50}

	if v := c.Query("entity_type"); v != "" {
		filter.EntityType = v
	}
	if v := c.Query("status"); v != "" {
		filter.Status = v
	}
	if v := c.Query("layer"); v != "" {
		filter.Layer = v
	}
	if v := c.Query("min_similarity"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid min_similarity")
			return
		}
		filter.MinSimilarity = &f
	}
	// T016: band filter — evaluated at the store scan level so pagination
	// totals are accurate even on large datasets.
	if v := c.Query("band"); v != "" {
		filter.Band = v
	}
	includeBreakdown := c.Query("include_breakdown") == "true"
	// include_books surfaces the full book objects (title/author/path/metadata)
	// inline on each candidate row so the unified dedup UI can render rich cards
	// without an N+2 per-book getBook() fan-out. The book lookups already happen
	// below for the dead-row existence filter, so this is nearly free.
	includeBooks := c.Query("include_books") == "true"
	// both_unmatched surfaces only pairs where NEITHER book has matched
	// metadata — the "both low-quality, need manual matching" triage view.
	// The match signal lives on the Book, not the candidate, so the store
	// cannot pre-filter it: when set, we fetch the full status/layer-filtered
	// candidate set and filter + paginate in-handler below.
	bothUnmatched := c.Query("both_unmatched") == "true"

	p := httputil.ParsePaginationParams(c)
	limit, offset := p.Limit, p.Offset
	if bothUnmatched {
		// Fetch everything matching the base filter; the store scans the whole
		// candidate table regardless, so this only widens the returned slice.
		filter.Limit = 1_000_000
		filter.Offset = 0
	} else {
		filter.Limit = limit
		filter.Offset = offset
	}

	candidates, total, err := es.ListCandidates(filter)
	if err != nil {
		httputil.InternalError(c, "failed to list dedup candidates", err)
		return
	}

	// MAYDEPLOY-B4: defensive filter — drop candidate rows whose
	// referenced book IDs no longer exist in the book table. This is
	// the safety net to B3's proactive cleanup: even if a stale row
	// slips through (race, missed delete, crash between cleanup runs),
	// the UI never shows a candidate that would 404 when clicked.
	// Non-book entities (e.g. author) skip the existence check.
	store := h.store
	// bookCache memoises GetBookByID across both referenced IDs of every
	// candidate. A miss is recorded as a nil entry so we never re-query a
	// known-dead ID. The cached *database.Book doubles as the include_books
	// enrichment payload below — no second fetch.
	bookCache := make(map[string]*database.Book, len(candidates)*2)
	lookupBook := func(id string) *database.Book {
		if id == "" {
			return nil
		}
		if v, ok := bookCache[id]; ok {
			return v
		}
		book, gerr := store.GetBookByID(id)
		if gerr != nil {
			book = nil
		}
		bookCache[id] = book
		return book
	}
	// isMetadataMatched reports whether a book has authoritative metadata, so it
	// does NOT need manual matching. A book counts as matched when EITHER a human
	// confirmed the match (MetadataReviewStatus == "matched") OR it carries an
	// external identifier (ASIN / ISBN13 / ISBN10) — having one means it was
	// matched to a provider record. This is the single intended extension point;
	// add further "matched" indicators here.
	nonEmpty := func(s *string) bool { return s != nil && *s != "" }
	isMetadataMatched := func(b *database.Book) bool {
		if b == nil {
			return false
		}
		if b.MetadataReviewStatus != nil && *b.MetadataReviewStatus == "matched" {
			return true
		}
		return nonEmpty(b.ASIN) || nonEmpty(b.ISBN13) || nonEmpty(b.ISBN10)
	}
	dropped := 0
	items := make([]gin.H, 0, len(candidates))
	for _, cand := range candidates {
		if cand.EntityType == "book" {
			ba := lookupBook(cand.EntityAID)
			bb := lookupBook(cand.EntityBID)
			if ba == nil || bb == nil {
				dropped++
				continue
			}
			// both_unmatched: keep only pairs where NEITHER side is matched.
			if bothUnmatched && (isMetadataMatched(ba) || isMetadataMatched(bb)) {
				continue
			}
		} else if bothUnmatched {
			// Non-book entities carry no book metadata; exclude from this view.
			continue
		}
		// Build the response item: always include band + top-level score;
		// conditionally include score_breakdown.
		row := gin.H{
			"id":              cand.ID,
			"entity_type":     cand.EntityType,
			"entity_a_id":     cand.EntityAID,
			"entity_b_id":     cand.EntityBID,
			"layer":           cand.Layer,
			"similarity":      cand.Similarity,
			"llm_verdict":     cand.LLMVerdict,
			"llm_reason":      cand.LLMReason,
			"status":          cand.Status,
			"created_at":      cand.CreatedAt,
			"updated_at":      cand.UpdatedAt,
			"band":            cand.Band,
			"formula_version": cand.FormulaVersion,
		}
		// Surface top-level score (avoids T017 having to unpack score_breakdown).
		if cand.ScoreBreakdown != nil {
			row["score"] = cand.ScoreBreakdown.Score
		}
		if includeBreakdown {
			row["score_breakdown"] = cand.ScoreBreakdown
		}
		// Attach the cached book objects so the unified UI can render rich
		// cards (title/author/path/metadata-quality) inline. nil when the
		// entity is a non-book type or the lookup missed.
		if includeBooks {
			row["book_a"] = lookupBook(cand.EntityAID)
			row["book_b"] = lookupBook(cand.EntityBID)
		}
		items = append(items, row)
	}
	if dropped > 0 {
		slog.Warn("dedup.list_candidates: filtered dead-book candidate rows",
			"dropped", dropped,
			"returned", len(items),
			"page_size", len(candidates),
			"note", "B3 cleanup may be lagging")
		// Reflect the filtered count in the total so pagination
		// hints stay roughly accurate from the client's view.
		if total >= dropped {
			total -= dropped
		}
	}

	// both_unmatched fetched the full candidate set and filtered in-handler, so
	// `items` holds every qualifying pair. Report the filtered total and slice
	// the requested page here.
	if bothUnmatched {
		total = len(items)
		start := offset
		if start > len(items) {
			start = len(items)
		}
		end := len(items)
		if limit > 0 {
			end = start + limit
			if end > len(items) {
				end = len(items)
			}
		}
		items = items[start:end]
	}

	httputil.RespondWithOK(c, gin.H{
		"candidates": items,
		"total":      total,
	})
}

// GetDedupCandidateBreakdown handles GET /api/v1/dedup/candidates/:id/breakdown.
//
// Returns a single candidate's full comparison payload: the candidate row
// (including the full score_breakdown with all signals), plus both books'
// details (title, author, files) so the UI can render a side-by-side
// comparison without issuing additional requests.
//
// T016 contract (frozen for T017):
//
//	{
//	  "candidate": { ...DedupCandidate with score_breakdown... },
//	  "book_a":    { id, title, author_id, series_id, files: [...] },
//	  "book_b":    { id, title, author_id, series_id, files: [...] }
//	}
//
// Returns 404 when the candidate ID is not found.
// Returns 503 when the embedding store is unavailable.
func (h *Handler) GetDedupCandidateBreakdown(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid candidate id")
		return
	}

	candidate, err := es.GetCandidateByID(id)
	if err != nil {
		httputil.InternalError(c, "failed to get candidate", err)
		return
	}
	if candidate == nil {
		httputil.RespondWithNotFound(c, "candidate", idStr)
		return
	}

	// Fetch both books and their files for the side-by-side comparison view.
	store := h.store
	type bookDetail struct {
		*database.Book
		Files []database.BookFile `json:"files"`
	}
	fetchBook := func(bookID string) *bookDetail {
		book, err := store.GetBookByID(bookID)
		if err != nil || book == nil {
			return nil
		}
		files, _ := store.GetBookFiles(bookID)
		return &bookDetail{Book: book, Files: files}
	}

	bookA := fetchBook(candidate.EntityAID)
	bookB := fetchBook(candidate.EntityBID)

	httputil.RespondWithOK(c, gin.H{
		"candidate": candidate,
		"book_a":    bookA,
		"book_b":    bookB,
	})
}

// RescoreDedupCandidates handles POST /api/v1/dedup/rescore.
//
// Re-runs unified.ComposeScore over the stored signal sets of all pending
// candidates and returns a per-band delta summary. Signals are read from the
// stored ScoreBreakdown — no re-collection or re-embedding is performed.
// Pre-T015 candidates with no stored signals are counted as "skipped".
//
// Request body (JSON, optional):
//
//	{ "apply": true }   // persist new scores+bands (default: false = dry-run)
//
// Response:
//
//	{
//	  "inspected":   N,   // total pending candidates examined
//	  "skipped":     N,   // pre-T015 rows with no stored signals
//	  "changed":     N,   // rows whose band or score changed
//	  "applied":     bool,
//	  "band_deltas": { "HIGH→CERTAIN": 3, ... }
//	}
//
// Returns 503 when the dedup engine is unavailable.
func (h *Handler) RescoreDedupCandidates(c *gin.Context) {
	if h.dedupEngine == nil {
		httputil.RespondWithServiceUnavailable(c, "dedup engine not available")
		return
	}

	var body struct {
		Apply bool `json:"apply"`
	}
	// Ignore parse errors; missing body → dry-run (apply=false).
	_ = c.ShouldBindJSON(&body)

	result, err := h.dedupEngine.Rescore(c.Request.Context(), body.Apply)
	if err != nil {
		httputil.InternalError(c, "rescore failed", err)
		return
	}

	httputil.RespondWithOK(c, result)
}

// PurgeAcoustIDConflicts handles POST /api/v1/dedup/purge-acoustid-conflicts.
//
// Finds PENDING non-audio dedup candidates whose two books have clearly
// disagreeing book-level AcoustID signatures — the title/metadata false
// positives the new acoustid veto suppresses at emission time. Dry-run by
// default (returns counts + a sample, mutates nothing); pass {"apply": true} to
// delete the conflicting candidates.
func (h *Handler) PurgeAcoustIDConflicts(c *gin.Context) {
	if h.dedupEngine == nil {
		httputil.RespondWithServiceUnavailable(c, "dedup engine not available")
		return
	}

	var body struct {
		Apply bool `json:"apply"`
	}
	// Ignore parse errors; missing body → dry-run (apply=false).
	_ = c.ShouldBindJSON(&body)

	result, err := h.dedupEngine.ReevaluateAcoustIDConflicts(c.Request.Context(), !body.Apply)
	if err != nil {
		httputil.InternalError(c, "acoustid-conflict reevaluation failed", err)
		return
	}

	httputil.RespondWithOK(c, result)
}

// ExportDedupCandidates handles GET /api/v1/dedup/candidates/export.
//
// Query params:
//
//	format = "csv" (default) or "json"
//	status, layer, min_similarity, entity_type — same as list endpoint
//
// Unlike the list endpoint, export doesn't paginate — it walks every
// matching row up to an internal hard cap (100K) to prevent runaway
// downloads. Each row is enriched with the book titles and author names
// of both sides so the CSV is readable in a spreadsheet without needing
// to cross-reference IDs.
//
// Columns (CSV): candidate_id, status, layer, similarity,
// entity_a_id, entity_a_title, entity_a_author,
// entity_b_id, entity_b_title, entity_b_author,
// llm_verdict, llm_reason, created_at, updated_at.
func (h *Handler) ExportDedupCandidates(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	format := c.DefaultQuery("format", "csv")
	if format != "csv" && format != "json" {
		httputil.RespondWithBadRequest(c, "format must be csv or json")
		return
	}

	filter := database.CandidateFilter{Limit: 100000}
	if v := c.Query("entity_type"); v != "" {
		filter.EntityType = v
	}
	if v := c.Query("status"); v != "" {
		filter.Status = v
	}
	if v := c.Query("layer"); v != "" {
		filter.Layer = v
	}
	if v := c.Query("min_similarity"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid min_similarity")
			return
		}
		filter.MinSimilarity = &f
	}

	candidates, _, err := es.ListCandidates(filter)
	if err != nil {
		httputil.InternalError(c, "failed to list candidates for export", err)
		return
	}

	// Enrich: lookup titles + author names for every entity involved,
	// memoized so a book that appears in multiple candidates is only
	// fetched once. Books-only for now — authors export would need the
	// author table which we can add later if needed.
	store := h.store
	type enriched struct {
		title  string
		author string
	}
	cache := make(map[string]enriched, len(candidates)*2)
	lookup := func(id string) enriched {
		if e, ok := cache[id]; ok {
			return e
		}
		e := enriched{}
		if book, err := store.GetBookByID(id); err == nil && book != nil {
			e.title = book.Title
			if book.AuthorID != nil {
				if a, err := store.GetAuthorByID(*book.AuthorID); err == nil && a != nil {
					e.author = a.Name
				}
			}
		}
		cache[id] = e
		return e
	}

	filename := fmt.Sprintf("dedup-candidates-%s.%s", time.Now().Format("20060102-150405"), format)

	if format == "json" {
		type row struct {
			CandidateID   int64   `json:"candidate_id"`
			Status        string  `json:"status"`
			Layer         string  `json:"layer"`
			Similarity    float64 `json:"similarity"`
			EntityType    string  `json:"entity_type"`
			EntityAID     string  `json:"entity_a_id"`
			EntityATitle  string  `json:"entity_a_title"`
			EntityAAuthor string  `json:"entity_a_author"`
			EntityBID     string  `json:"entity_b_id"`
			EntityBTitle  string  `json:"entity_b_title"`
			EntityBAuthor string  `json:"entity_b_author"`
			LLMVerdict    string  `json:"llm_verdict,omitempty"`
			LLMReason     string  `json:"llm_reason,omitempty"`
			CreatedAt     string  `json:"created_at"`
			UpdatedAt     string  `json:"updated_at"`
		}
		rows := make([]row, 0, len(candidates))
		for _, cand := range candidates {
			a := lookup(cand.EntityAID)
			b := lookup(cand.EntityBID)
			sim := 0.0
			if cand.Similarity != nil {
				sim = *cand.Similarity
			}
			rows = append(rows, row{
				CandidateID:   cand.ID,
				Status:        cand.Status,
				Layer:         cand.Layer,
				Similarity:    sim,
				EntityType:    cand.EntityType,
				EntityAID:     cand.EntityAID,
				EntityATitle:  a.title,
				EntityAAuthor: a.author,
				EntityBID:     cand.EntityBID,
				EntityBTitle:  b.title,
				EntityBAuthor: b.author,
				LLMVerdict:    cand.LLMVerdict,
				LLMReason:     cand.LLMReason,
				CreatedAt:     cand.CreatedAt.Format(time.RFC3339),
				UpdatedAt:     cand.UpdatedAt.Format(time.RFC3339),
			})
		}
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		c.Header("Content-Type", "application/json")
		enc := json.NewEncoder(c.Writer)
		enc.SetIndent("", "  ")
		if err := enc.Encode(gin.H{"count": len(rows), "candidates": rows}); err != nil {
			slog.Info("dedup export json encode", "err", err)
		}
		return
	}

	// CSV path.
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "text/csv")
	w := csv.NewWriter(c.Writer)
	defer w.Flush()
	_ = w.Write([]string{
		"candidate_id", "status", "layer", "similarity",
		"entity_type",
		"entity_a_id", "entity_a_title", "entity_a_author",
		"entity_b_id", "entity_b_title", "entity_b_author",
		"llm_verdict", "llm_reason",
		"created_at", "updated_at",
	})
	for _, cand := range candidates {
		a := lookup(cand.EntityAID)
		b := lookup(cand.EntityBID)
		simStr := ""
		if cand.Similarity != nil {
			simStr = strconv.FormatFloat(*cand.Similarity, 'f', 4, 64)
		}
		_ = w.Write([]string{
			strconv.FormatInt(cand.ID, 10),
			cand.Status,
			cand.Layer,
			simStr,
			cand.EntityType,
			cand.EntityAID,
			a.title,
			a.author,
			cand.EntityBID,
			b.title,
			b.author,
			cand.LLMVerdict,
			cand.LLMReason,
			cand.CreatedAt.Format(time.RFC3339),
			cand.UpdatedAt.Format(time.RFC3339),
		})
	}
	slog.Info("dedup export wrote candidate rows as", "candidates_count", len(candidates), "format", format)
}

// series-aware dedup helpers below. These exist to support "merge
// every cluster in this series" — a common workflow after rescanning
// a whole collection where every book in a series produces its own
// cluster and the user wants to commit all of them with one action
// instead of N clicks.

// dedupSeriesSummary is one entry in the response of
// ListDedupCandidateSeries — one row per series that has pending
// candidates, with counts so the user can pick a series to merge
// without having to drill into each one.
type dedupSeriesSummary struct {
	SeriesID       int    `json:"series_id"`
	SeriesName     string `json:"series_name"`
	ClusterCount   int    `json:"cluster_count"`
	BookCount      int    `json:"book_count"`
	CandidateCount int    `json:"candidate_count"`
}

// ListDedupCandidateSeries handles
// GET /api/v1/dedup/candidates/series-summary.
//
// Walks every pending book candidate, looks up both sides' series_id,
// and returns one row per series where BOTH sides of at least one
// candidate pair belong to that series. Clusters are computed via
// union-find per-series so the count reflects what "merge every
// cluster in this series" would actually touch.
//
// Candidates whose two sides belong to different series are excluded
// from every summary — they're cross-series and don't fit the
// "series-aware bulk merge" workflow.
func (h *Handler) ListDedupCandidateSeries(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Limit:      100000,
	})
	if err != nil {
		httputil.InternalError(c, "failed to list pending candidates", err)
		return
	}

	// Memoize book → series_id lookups across candidates.
	store := h.store
	bookSeries := make(map[string]int, len(cands)*2)
	lookup := func(id string) int {
		if v, ok := bookSeries[id]; ok {
			return v
		}
		book, err := store.GetBookByID(id)
		if err != nil || book == nil || book.SeriesID == nil {
			bookSeries[id] = 0
			return 0
		}
		bookSeries[id] = *book.SeriesID
		return *book.SeriesID
	}

	// Group candidate pairs by series. candsBySeries[seriesID] holds
	// every (a_id, b_id) that's entirely within that series.
	type pair struct{ a, b string }
	candsBySeries := make(map[int][]pair)
	for _, cand := range cands {
		sa := lookup(cand.EntityAID)
		sb := lookup(cand.EntityBID)
		if sa == 0 || sb == 0 || sa != sb {
			continue
		}
		candsBySeries[sa] = append(candsBySeries[sa], pair{cand.EntityAID, cand.EntityBID})
	}

	// For each series, cluster via union-find to compute how many
	// merge operations would actually run.
	summary := make([]dedupSeriesSummary, 0, len(candsBySeries))
	for seriesID, pairs := range candsBySeries {
		parent := make(map[string]string)
		var find func(string) string = func(x string) string {
			for parent[x] != x {
				parent[x] = parent[parent[x]]
				x = parent[x]
			}
			return x
		}
		union := func(a, b string) {
			for _, id := range []string{a, b} {
				if _, ok := parent[id]; !ok {
					parent[id] = id
				}
			}
			ra, rb := find(a), find(b)
			if ra != rb {
				parent[ra] = rb
			}
		}
		for _, p := range pairs {
			union(p.a, p.b)
		}
		roots := make(map[string]struct{})
		books := make(map[string]struct{})
		for id := range parent {
			roots[find(id)] = struct{}{}
			books[id] = struct{}{}
		}

		name := ""
		if series, err := store.GetSeriesByID(seriesID); err == nil && series != nil {
			name = series.Name
		}
		summary = append(summary, dedupSeriesSummary{
			SeriesID:       seriesID,
			SeriesName:     name,
			ClusterCount:   len(roots),
			BookCount:      len(books),
			CandidateCount: len(pairs),
		})
	}

	// Stable sort: highest cluster count first, then series name.
	sort.Slice(summary, func(i, j int) bool {
		if summary[i].ClusterCount != summary[j].ClusterCount {
			return summary[i].ClusterCount > summary[j].ClusterCount
		}
		return summary[i].SeriesName < summary[j].SeriesName
	})

	httputil.RespondWithOK(c, gin.H{"series": summary})
}

// MergeDedupCandidateSeries handles
// POST /api/v1/dedup/candidates/merge-series.
//
// Body: {"series_id": N}
//
// Finds every pending book candidate whose both sides belong to the
// given series, builds clusters via union-find, and merges each
// cluster with MergeService.MergeBooks. Returns a summary of how many
// clusters were touched and how many books were merged in total.
//
// Cross-series candidates (one side in this series, the other
// somewhere else) are deliberately untouched — the series filter is
// a scope, not a selector. If the user wants those pairs merged, they
// can use the regular Merge Filtered action.
func (h *Handler) MergeDedupCandidateSeries(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}
	if h.mergeService == nil {
		httputil.RespondWithServiceUnavailable(c, "merge service not available")
		return
	}

	var body struct {
		SeriesID int `json:"series_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.SeriesID <= 0 {
		httputil.RespondWithBadRequest(c, "series_id must be a positive integer")
		return
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Limit:      100000,
	})
	if err != nil {
		httputil.InternalError(c, "failed to list pending candidates", err)
		return
	}

	// Filter to same-series candidates only.
	store := h.store
	bookSeries := make(map[string]int, len(cands)*2)
	lookup := func(id string) int {
		if v, ok := bookSeries[id]; ok {
			return v
		}
		book, err := store.GetBookByID(id)
		if err != nil || book == nil || book.SeriesID == nil {
			bookSeries[id] = 0
			return 0
		}
		bookSeries[id] = *book.SeriesID
		return *book.SeriesID
	}
	var inScope []database.DedupCandidate
	for _, cand := range cands {
		if lookup(cand.EntityAID) == body.SeriesID && lookup(cand.EntityBID) == body.SeriesID {
			inScope = append(inScope, cand)
		}
	}

	// Union-find cluster build.
	parent := make(map[string]string)
	var find func(string) string = func(x string) string {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b string) {
		for _, id := range []string{a, b} {
			if _, ok := parent[id]; !ok {
				parent[id] = id
			}
		}
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, cand := range inScope {
		union(cand.EntityAID, cand.EntityBID)
	}
	clusters := make(map[string][]string)
	for id := range parent {
		root := find(id)
		clusters[root] = append(clusters[root], id)
	}

	// Merge each cluster. Candidate rows contained in each cluster get
	// marked as merged inside the same loop (same membership check as
	// mergeDedupCluster) so the Merged tab reflects the action.
	mergedClusters := 0
	mergedBooks := 0
	candidatesUpdated := 0
	var failures []string
	for _, bookIDs := range clusters {
		if len(bookIDs) < 2 {
			continue
		}
		if _, err := h.mergeService.MergeBooks(bookIDs, ""); err != nil {
			failures = append(failures, fmt.Sprintf("cluster of %d: %v", len(bookIDs), err))
			continue
		}
		mergedClusters++
		mergedBooks += len(bookIDs)

		inCluster := make(map[string]struct{}, len(bookIDs))
		for _, id := range bookIDs {
			inCluster[id] = struct{}{}
		}
		for _, cand := range inScope {
			_, aIn := inCluster[cand.EntityAID]
			_, bIn := inCluster[cand.EntityBID]
			if !aIn || !bIn {
				continue
			}
			if err := es.UpdateCandidateStatus(cand.ID, "merged"); err != nil {
				slog.Info("dedup series merge status update", "cand", cand.ID, "err", err)
				continue
			}
			candidatesUpdated++
		}
	}

	slog.Info("dedup series merge series clusters_merged books_merged candidates_updated failures", "body", body.SeriesID, "mergedClusters", mergedClusters, "mergedBooks", mergedBooks, "candidatesUpdated", candidatesUpdated, "failures_count", len(failures))

	httputil.RespondWithOK(c, gin.H{
		"series_id":          body.SeriesID,
		"clusters_merged":    mergedClusters,
		"books_merged":       mergedBooks,
		"candidates_updated": candidatesUpdated,
		"failures":           failures,
	})
}

// GetDedupStats handles GET /api/v1/dedup/stats.
func (h *Handler) GetDedupStats(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	stats, err := es.GetCandidateStats()
	if err != nil {
		httputil.InternalError(c, "failed to get dedup stats", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{"stats": stats})
}

// BulkMergeDedupCandidates handles POST /api/v1/dedup/candidates/bulk-merge.
//
// Accepts the same filter params as listDedupCandidates in the JSON body
// (entity_type, status, layer, min_similarity, max_similarity) and merges
// every matching candidate by calling MergeService.MergeBooks. Returns a
// summary with counts of attempted, merged, and failed candidates.
//
// The endpoint is intended for the "Merge Filtered" bulk action in the
// Embedding Dedup UI. It only operates on book candidates; author
// candidates are skipped (and counted as failed with a reason) since
// they're merged through a different service.
//
// Safety: caller should confirm with the user before invoking, because
// this is destructive and irreversible.
func (h *Handler) BulkMergeDedupCandidates(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}
	if h.mergeService == nil {
		httputil.RespondWithServiceUnavailable(c, "merge service not available")
		return
	}

	var body struct {
		EntityType    string   `json:"entity_type"`
		Status        string   `json:"status"`
		Layer         string   `json:"layer"`
		MinSimilarity *float64 `json:"min_similarity"`
		MaxSimilarity *float64 `json:"max_similarity"`
	}
	_ = c.ShouldBindJSON(&body)

	// Default to pending status if caller did not set one. Merging already-
	// merged or already-dismissed rows makes no sense.
	if body.Status == "" {
		body.Status = "pending"
	}
	// Only book candidates are mergeable through this endpoint.
	if body.EntityType == "" {
		body.EntityType = "book"
	}
	if body.EntityType != "book" {
		httputil.RespondWithBadRequest(c, "bulk merge only supports entity_type=book")
		return
	}

	filter := database.CandidateFilter{
		EntityType:    body.EntityType,
		Status:        body.Status,
		Layer:         body.Layer,
		MinSimilarity: body.MinSimilarity,
		MaxSimilarity: body.MaxSimilarity,
		Limit:         100000,
	}

	candidates, total, err := es.ListCandidates(filter)
	if err != nil {
		httputil.InternalError(c, "failed to list candidates for bulk merge", err)
		return
	}

	type failure struct {
		CandidateID int64  `json:"candidate_id"`
		Reason      string `json:"reason"`
	}
	var failures []failure
	merged := 0

	for _, cand := range candidates {
		// Snapshot features before the merge absorbs one side (best-effort).
		labelExample := h.snapshotCandidateExample(&cand)
		_, mergeErr := h.mergeService.MergeBooks([]string{cand.EntityAID, cand.EntityBID}, "")
		if mergeErr != nil {
			failures = append(failures, failure{CandidateID: cand.ID, Reason: mergeErr.Error()})
			slog.Info("dedup bulk merge candidate failed", "cand", cand.ID, "mergeErr", mergeErr)
			continue
		}
		if err := es.UpdateCandidateStatus(cand.ID, "merged"); err != nil {
			// The books were merged on the server side, but we couldn't
			// update the candidate row — log it and count as merged
			// since the destructive action already happened.
			slog.Info("dedup bulk merge candidate merged but status update failed", "cand", cand.ID, "err", err)
		}
		merged++
		// Capture the user's bulk merge as a gold true_dup label (best-effort).
		h.recordHumanLabel(labelExample, labelTrueDup, labelReasonUserMerge)
	}

	slog.Info("dedup bulk merge complete merged, failed out of matched", "merged", merged, "failures_count", len(failures), "total", total)

	httputil.RespondWithOK(c, gin.H{
		"attempted": total,
		"merged":    merged,
		"failed":    len(failures),
		"failures":  failures,
	})
}

// MergeDedupCluster handles POST /api/v1/dedup/candidates/merge-cluster.
//
// Body: {"book_ids": ["id1", "id2", "id3", ...]}
//
// Merges the supplied book IDs into a single version group with one call to
// MergeService.MergeBooks, then marks every dedup_candidate row whose pair
// is fully contained in the set as status=merged. This is the backend for
// the Embedding tab's multi-book cluster card, where 3+ candidate books form
// a connected component in the pairwise candidate graph and should be
// merged together as one logical group rather than one pairwise merge at a
// time (which would fight the version-group state mid-way).
func (h *Handler) MergeDedupCluster(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}
	if h.mergeService == nil {
		httputil.RespondWithServiceUnavailable(c, "merge service not available")
		return
	}

	var body struct {
		BookIDs       []string `json:"book_ids"`
		PrimaryBookID string   `json:"primary_book_id,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if len(body.BookIDs) < 2 {
		httputil.RespondWithBadRequest(c, "book_ids must contain at least 2 entries")
		return
	}
	// If primary_book_id is set, it must be one of the books in the
	// cluster. Empty means "let bookIsBetter auto-pick".
	if body.PrimaryBookID != "" {
		found := false
		for _, id := range body.BookIDs {
			if id == body.PrimaryBookID {
				found = true
				break
			}
		}
		if !found {
			httputil.RespondWithBadRequest(c, "primary_book_id must be one of book_ids")
			return
		}
	}

	mergeResult, err := h.mergeService.MergeBooks(body.BookIDs, body.PrimaryBookID)
	if err != nil {
		httputil.InternalError(c, "failed to merge books in cluster", err)
		return
	}

	// Mark every candidate whose pair is fully contained in the cluster
	// as merged. Using a set for O(1) membership so a cluster of N books
	// checks each row's pair in constant time.
	inCluster := make(map[string]struct{}, len(body.BookIDs))
	for _, id := range body.BookIDs {
		inCluster[id] = struct{}{}
	}
	candidates, _, listErr := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Limit:      100000,
	})
	updated := 0
	if listErr != nil {
		slog.Info("dedup cluster merge list candidates failed", "listErr", listErr)
	} else {
		for _, cand := range candidates {
			_, aIn := inCluster[cand.EntityAID]
			_, bIn := inCluster[cand.EntityBID]
			if !aIn || !bIn {
				continue
			}
			if err := es.UpdateCandidateStatus(cand.ID, "merged"); err != nil {
				slog.Info("dedup cluster merge status update", "cand", cand.ID, "err", err)
				continue
			}
			updated++
		}
	}
	slog.Info("dedup cluster merge merged books, marked candidate row(s) as merged", "count", len(body.BookIDs), "updated", updated)

	httputil.RespondWithOK(c, gin.H{
		"status":             "merged",
		"merged_books":       len(body.BookIDs),
		"candidates_updated": updated,
		"result":             mergeResult,
	})
}

// DismissDedupCluster handles POST /api/v1/dedup/candidates/dismiss-cluster.
//
// Body: {"book_ids": ["id1", "id2", ...]}
//
// Marks every dedup_candidate row whose pair is fully contained in the set
// as status=dismissed. No books are modified — this just removes the pair
// from the pending queue.
func (h *Handler) DismissDedupCluster(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	var body struct {
		BookIDs []string `json:"book_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if len(body.BookIDs) < 2 {
		httputil.RespondWithBadRequest(c, "book_ids must contain at least 2 entries")
		return
	}

	inCluster := make(map[string]struct{}, len(body.BookIDs))
	for _, id := range body.BookIDs {
		inCluster[id] = struct{}{}
	}
	candidates, _, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Limit:      100000,
	})
	if err != nil {
		httputil.InternalError(c, "failed to list candidates for cluster dismiss", err)
		return
	}
	dismissed := 0
	for _, cand := range candidates {
		_, aIn := inCluster[cand.EntityAID]
		_, bIn := inCluster[cand.EntityBID]
		if !aIn || !bIn {
			continue
		}
		if err := es.UpdateCandidateStatus(cand.ID, "dismissed"); err != nil {
			slog.Info("dedup cluster dismiss status update", "cand", cand.ID, "err", err)
			continue
		}
		dismissed++
		// Capture each dismissed pair as a gold not_dup label (best-effort).
		h.recordHumanLabel(h.snapshotCandidateExample(&cand), labelNotDup, labelReasonUserDismiss)
	}
	slog.Info("dedup cluster dismiss dismissed candidate row(s) across books", "dismissed", dismissed, "count", len(body.BookIDs))
	h.markDuplicatesFlaggedDirty("dismiss_cluster")

	httputil.RespondWithOK(c, gin.H{
		"status":    "dismissed",
		"dismissed": dismissed,
	})
}

// RemoveFromDedupCluster handles POST /api/v1/dedup/candidates/remove-from-cluster.
//
//	Body: {
//	  "cluster_book_ids": [...],
//	  "remove_book_id": "X"      // singular, backwards-compat
//	  "remove_book_ids": [...]   // plural, preferred
//	}
//
// Dismisses every pending candidate whose pair is one-side-in-remove-set
// and other-side in (cluster \ remove-set). In other words: "these books
// are NOT duplicates of the remaining books in this cluster". Pairs where
// both sides are in the remove set are ALSO dismissed — removing two
// wrong-books from a cluster means neither should stay paired with
// anything that was in the original cluster.
//
// Pairs involving a removed book with books OUTSIDE the cluster are
// left alone — this is a scoped split, not a global ban.
//
// Accepts both singular and plural forms for backwards compatibility.
// If both are provided, they're merged into a single set.
func (h *Handler) RemoveFromDedupCluster(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	var body struct {
		ClusterBookIDs []string `json:"cluster_book_ids"`
		RemoveBookID   string   `json:"remove_book_id,omitempty"`
		RemoveBookIDs  []string `json:"remove_book_ids,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}

	// Normalize into one remove set. Singular and plural both supported
	// so the existing × button (singular) keeps working while the new
	// multi-select UI sends the plural.
	removeSet := make(map[string]struct{})
	if body.RemoveBookID != "" {
		removeSet[body.RemoveBookID] = struct{}{}
	}
	for _, id := range body.RemoveBookIDs {
		if id != "" {
			removeSet[id] = struct{}{}
		}
	}
	if len(removeSet) == 0 {
		httputil.RespondWithBadRequest(c, "remove_book_id or remove_book_ids is required")
		return
	}
	if len(body.ClusterBookIDs) < 2 {
		httputil.RespondWithBadRequest(c, "cluster_book_ids must contain at least 2 entries")
		return
	}

	// Build the set of "remaining books in this cluster" — cluster
	// minus the remove set.
	remaining := make(map[string]struct{}, len(body.ClusterBookIDs))
	for _, id := range body.ClusterBookIDs {
		if _, removed := removeSet[id]; removed {
			continue
		}
		remaining[id] = struct{}{}
	}
	if len(remaining) == 0 {
		httputil.RespondWithBadRequest(c, "cluster must contain at least one book that is not in remove set")
		return
	}

	candidates, _, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Limit:      100000,
	})
	if err != nil {
		httputil.InternalError(c, "failed to list candidates for cluster remove", err)
		return
	}

	dismissed := 0
	for _, cand := range candidates {
		// A pair is dismissible if at least one side is in the remove
		// set AND the other side is either also in the remove set or in
		// the remaining cluster. Pairs where a removed book touches a
		// book OUTSIDE the cluster are not touched — those are
		// different clusters.
		_, aRemoved := removeSet[cand.EntityAID]
		_, bRemoved := removeSet[cand.EntityBID]
		if !aRemoved && !bRemoved {
			continue
		}
		_, aRemaining := remaining[cand.EntityAID]
		_, bRemaining := remaining[cand.EntityBID]

		touchesCluster := false
		switch {
		case aRemoved && (bRemaining || bRemoved):
			touchesCluster = true
		case bRemoved && (aRemaining || aRemoved):
			touchesCluster = true
		}
		if !touchesCluster {
			continue
		}

		if err := es.UpdateCandidateStatus(cand.ID, "dismissed"); err != nil {
			slog.Info("dedup remove-from-cluster status update", "cand", cand.ID, "err", err)
			continue
		}
		dismissed++
	}
	slog.Info("dedup remove-from-cluster dismissed edge(s), removed book(s) from cluster of", "dismissed", dismissed, "removeSet_count", len(removeSet), "count", len(body.ClusterBookIDs))

	httputil.RespondWithOK(c, gin.H{
		"status":    "removed",
		"dismissed": dismissed,
		"removed":   len(removeSet),
	})
}

// MergeDedupCandidate handles POST /api/v1/dedup/candidates/:id/merge.
func (h *Handler) MergeDedupCandidate(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid candidate id")
		return
	}

	candidate, err := es.GetCandidateByID(id)
	if err != nil {
		httputil.InternalError(c, "failed to get candidate", err)
		return
	}
	if candidate == nil {
		httputil.RespondWithNotFound(c, "candidate", idStr)
		return
	}

	// Optional JSON body {"keep_id": "<bookID>"} lets the caller pick
	// which side of the candidate pair becomes the merge primary. When
	// absent or empty, fall back to MergeService's auto-select (by
	// format/bitrate/size). When present, it must match one of the two
	// candidate entities — otherwise the caller is confused and we
	// refuse rather than silently auto-picking.
	var body struct {
		KeepID string `json:"keep_id"`
	}
	// Ignore parse errors: an empty/missing body is valid (back-compat).
	_ = c.ShouldBindJSON(&body)
	keepID := body.KeepID
	if keepID != "" && keepID != candidate.EntityAID && keepID != candidate.EntityBID {
		httputil.RespondWithBadRequest(c, "keep_id must match the candidate's entity_a_id or entity_b_id")
		return
	}

	// Snapshot the feature example BEFORE the merge: MergeBooks absorbs/deletes one
	// side, after which the snapshot can no longer load that book. Best-effort.
	labelExample := h.snapshotCandidateExample(candidate)

	var result interface{}
	if candidate.EntityType == "book" && h.mergeService != nil {
		mergeResult, mergeErr := h.mergeService.MergeBooks([]string{candidate.EntityAID, candidate.EntityBID}, keepID)
		if mergeErr != nil {
			// MAYDEPLOY-B2: when one of the source books no longer exists (a previous
			// merge already absorbed it), the candidate is stale rather than the
			// request being a server error. Treat that as 409 Conflict and mark the
			// candidate merged so the UI's next refresh drops it.
			//
			// We use a substring match on "not found" because the underlying merge
			// service returns a plain fmt.Errorf("book %s not found", ...) without an
			// exported sentinel error. Switch to errors.Is if/when that error type
			// becomes exported.
			if strings.Contains(mergeErr.Error(), "not found") {
				if statusErr := es.UpdateCandidateStatus(id, "merged"); statusErr != nil {
					slog.Warn("dedup merge already-merged: failed to update candidate status", "candidate_id", id, "err", statusErr)
				}
				h.publishEvent(c.Request.Context(), plugin.NewEvent(plugin.EventDedupMerged, candidate.EntityAID, map[string]any{
					"entity_b_id":    candidate.EntityBID,
					"entity_type":    candidate.EntityType,
					"candidate_id":   id,
					"already_merged": true,
				}))
				slog.Info("dedup merge skipped: source book already merged away",
					"candidate_id", id,
					"entity_a", candidate.EntityAID,
					"entity_b", candidate.EntityBID,
					"err", mergeErr,
				)
				c.JSON(http.StatusConflict, gin.H{
					"status":       "already_merged",
					"candidate_id": id,
				})
				return
			}
			httputil.InternalError(c, "failed to merge books", mergeErr)
			return
		}
		result = mergeResult

		// MAYDEPLOY-B3: sweep orphan candidates. Any *other* pending candidate
		// row that still references the merged-away book(s) is now stale —
		// clicking Merge on it would 500 ("book not found") absent the
		// PR #1160 409-hotfix path. Mark them merged proactively so the UI
		// drops them on the next refresh rather than the user having to
		// dismiss each by hand.
		if h.dedupEngine != nil && mergeResult != nil {
			var mergedAway []string
			for _, bid := range []string{candidate.EntityAID, candidate.EntityBID} {
				if bid != "" && bid != mergeResult.PrimaryID {
					mergedAway = append(mergedAway, bid)
				}
			}
			h.dedupEngine.CleanupCandidatesAfterMerge(mergedAway)
		}
	}

	if err := es.UpdateCandidateStatus(id, "merged"); err != nil {
		httputil.InternalError(c, "failed to update candidate status", err)
		return
	}

	h.publishEvent(c.Request.Context(), plugin.NewEvent(plugin.EventDedupMerged, candidate.EntityAID, map[string]any{
		"entity_b_id":  candidate.EntityBID,
		"entity_type":  candidate.EntityType,
		"candidate_id": id,
	}))

	// Capture the user's merge as a gold true_dup label (best-effort).
	h.recordHumanLabel(labelExample, labelTrueDup, labelReasonUserMerge)

	httputil.RespondWithOK(c, gin.H{"status": "merged", "result": result, "keep_id": keepID})
}

// DismissDedupCandidate handles POST /api/v1/dedup/candidates/:id/dismiss.
func (h *Handler) DismissDedupCandidate(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}

	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid candidate id")
		return
	}

	if err := es.UpdateCandidateStatus(id, "dismissed"); err != nil {
		httputil.InternalError(c, "failed to dismiss candidate", err)
		return
	}
	h.markDuplicatesFlaggedDirty("dismiss_candidate")

	// Capture the user's dismiss as a gold not_dup label (best-effort).
	h.captureHumanLabelByID(id, labelNotDup, labelReasonUserDismiss)

	httputil.RespondWithOK(c, gin.H{"status": "dismissed"})
}

// TriggerDedupScan handles POST /api/v1/dedup/scan.
// Delegates to the UOS registry (dedup.full-scan op) since UOS-09.
func (h *Handler) TriggerDedupScan(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.full-scan", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue dedup scan", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// TriggerDedupLLM handles POST /api/v1/dedup/scan-llm.
// Delegates to the UOS registry (dedup.llm-review op) since UOS-09.
func (h *Handler) TriggerDedupLLM(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.llm-review", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue LLM review", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// TriggerDedupRefresh handles POST /api/v1/dedup/refresh.
// Re-runs the full scan as a tracked Operation. Identical behavior to
// TriggerDedupScan — kept as a separate endpoint for backwards compatibility.
func (h *Handler) TriggerDedupRefresh(c *gin.Context) {
	h.TriggerDedupScan(c)
}

// TriggerDedupAcoustID handles POST /api/v1/dedup/scan-acoustid.
// Delegates to the UOS registry (acoustid.scan op) since UOS-09.
func (h *Handler) TriggerDedupAcoustID(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "acoustid.scan", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue acoustid scan", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// ResetAcoustIDFingerprints handles POST /api/v1/dedup/reset-acoustid.
// Enqueues acoustid.reset-all (clears every stored fingerprint + drops
// acoustid-layer dedup candidates) immediately followed by a forced
// fingerprint rescan over the whole library. Both ops share the
// "acoustid.fingerprint" concurrency key so the rescan queues behind the
// reset and runs sequentially.
func (h *Handler) ResetAcoustIDFingerprints(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	resetID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "acoustid.reset-all", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue acoustid reset", err)
		return
	}
	rescanParams := map[string]any{"scope": "all", "force": true}
	rescanID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "acoustid.fingerprint-rescan", rescanParams)
	if err != nil {
		// Reset already enqueued; rescan failure is non-fatal — surface it
		// in the response so the operator can re-run rescan manually.
		httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]any{
			"op_id":          resetID,
			"reset_op_id":    resetID,
			"rescan_op_id":   "",
			"rescan_warning": err.Error(),
		})
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]any{
		"op_id":        resetID,
		"reset_op_id":  resetID,
		"rescan_op_id": rescanID,
	})
}

// PurgeStaleCandidates handles POST /api/v1/dedup/purge-stale.
// Enqueues the dedup.purge-stale UOS op so the cleanup shows up in the
// bell with proper start/end log lines, instead of silently running and
// returning a count. Engine.PurgeStaleCandidates still does the actual
// work — see internal/plugins/dedup/purge_stale.go.
func (h *Handler) PurgeStaleCandidates(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.purge-stale", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue dedup purge-stale", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// PurgeLegacyFPCandidates handles POST /api/v1/dedup/purge-legacy-fp.
//
// Enqueues the dedup.purge-legacy-fp-candidates UOS op (T015) which marks
// pre-whole-file-fingerprint exact/embedding sim=1.0 candidates as stale-fp.
//
// Optional JSON body: {"apply": true} to execute (default is dry-run, which
// returns counts only without mutating any rows). The "apply" flag is forwarded
// verbatim to the op as its params payload.
//
// Why an op and not a synchronous handler: the purge touches up to ~12K rows
// and performs a per-candidate file-hash re-check; it belongs in the op queue
// so the caller can track progress in the bell and the operation log.
func (h *Handler) PurgeLegacyFPCandidates(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	// Forward the request body as the op's params JSON so the op can see
	// {"apply":true/false} without the handler needing to parse it.
	var paramsJSON json.RawMessage
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&paramsJSON); err != nil {
			httputil.RespondWithBadRequest(c, "invalid JSON body")
			return
		}
	}

	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.purge-legacy-fp-candidates", paramsJSON)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue dedup purge-legacy-fp-candidates", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// TriggerEmbedScan handles POST /api/v1/dedup/embed.
// Delegates to the UOS registry (dedup.embed-scan op) since UOS-07.
func (h *Handler) TriggerEmbedScan(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.embed-scan", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue embed scan", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// TriggerEmbedAsync handles POST /api/v1/dedup/embed-async.
// Enqueues the nightly embed-async UOS op on demand.
func (h *Handler) TriggerEmbedAsync(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.embed-async", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue embed-async", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// TriggerLSHIndexBuild handles POST /api/v1/dedup/lsh-index.
// Enqueues the dedup.lsh-index-build op (fable5 T012), which builds
// the fpidx: PebbleDB secondary index over whole-file AcoustID
// fingerprints. The op is idempotent and resumable — re-triggering
// while the index is already up-to-date is safe (skips already-indexed
// files via the fpidx_meta: member-list).
func (h *Handler) TriggerLSHIndexBuild(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.lsh-index-build", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue lsh-index-build", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// TriggerBookSignatureScan handles POST /api/v1/dedup/scan-book-signature.
// Delegates to the UOS registry (dedup.book-signature-scan op) since UOS-09.
func (h *Handler) TriggerBookSignatureScan(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.book-signature-scan", nil)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue book signature scan", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}

// AcoustIDSegmentComparison holds comparison data for one fingerprint segment.
type AcoustIDSegmentComparison struct {
	Segment string `json:"segment"` // "seg0" … "seg6"
	HashA   string `json:"hash_a"`  // fingerprint hex or ""
	HashB   string `json:"hash_b"`
	Match   bool   `json:"match"`
}

// AcoustIDCompareResponse is the response body for the compare-acoustid endpoint.
type AcoustIDCompareResponse struct {
	BookA         database.Book               `json:"book_a"`
	BookB         database.Book               `json:"book_b"`
	OverallScore  float64                     `json:"overall_score"`  // 0.0–1.0
	SegmentScores []AcoustIDSegmentComparison `json:"segment_scores"` // 7 entries
}

// HandleCompareAcoustID handles POST /api/v1/audiobooks/:id/compare-acoustid?other=<bookID2>.
// Computes per-segment fingerprint comparison between two books' primary files.
func (h *Handler) HandleCompareAcoustID(c *gin.Context) {
	store := h.store
	idA := c.Param("id")
	idB := c.Query("other")
	if idB == "" {
		httputil.RespondWithBadRequest(c, "missing ?other= query parameter")
		return
	}

	bookA, err := store.GetBookByID(idA)
	if err != nil || bookA == nil {
		httputil.RespondWithNotFound(c, "book", idA)
		return
	}
	bookB, err := store.GetBookByID(idB)
	if err != nil || bookB == nil {
		httputil.RespondWithNotFound(c, "book", idB)
		return
	}

	filesA, _ := store.GetBookFiles(idA)
	filesB, _ := store.GetBookFiles(idB)

	primary := func(files []database.BookFile) *database.BookFile {
		for i := range files {
			if files[i].AcoustIDSeg0 != "" {
				return &files[i]
			}
		}
		if len(files) > 0 {
			return &files[0]
		}
		return nil
	}

	fa := primary(filesA)
	fb := primary(filesB)

	segNames := []string{"seg0", "seg1", "seg2", "seg3", "seg4", "seg5", "seg6"}

	segsA := []string{"", "", "", "", "", "", ""}
	segsB := []string{"", "", "", "", "", "", ""}
	if fa != nil {
		segsA = []string{fa.AcoustIDSeg0, fa.AcoustIDSeg1, fa.AcoustIDSeg2, fa.AcoustIDSeg3, fa.AcoustIDSeg4, fa.AcoustIDSeg5, fa.AcoustIDSeg6}
	}
	if fb != nil {
		segsB = []string{fb.AcoustIDSeg0, fb.AcoustIDSeg1, fb.AcoustIDSeg2, fb.AcoustIDSeg3, fb.AcoustIDSeg4, fb.AcoustIDSeg5, fb.AcoustIDSeg6}
	}

	var comparisons []AcoustIDSegmentComparison
	var matching, total int
	for i, name := range segNames {
		a, b := segsA[i], segsB[i]
		comp := AcoustIDSegmentComparison{Segment: name, HashA: a, HashB: b}
		if a != "" && b != "" {
			total++
			comp.Match = a == b
			if comp.Match {
				matching++
			}
		}
		comparisons = append(comparisons, comp)
	}

	overall := 0.0
	if total > 0 {
		overall = float64(matching) / float64(total)
	}

	httputil.RespondWithOK(c, AcoustIDCompareResponse{
		BookA:         *bookA,
		BookB:         *bookB,
		OverallScore:  overall,
		SegmentScores: comparisons,
	})
}

// EmbReeencode handles POST /api/v1/dedup/emb-reencode.
//
// Enqueues the dedup.emb-reencode UOS op (T021) which rewrites all emb:v:
// blobs from legacy float32 (v0) to float16+zstd (v1), saving ~3.5–4× disk
// space.
//
// Optional JSON body: {"apply": true} to execute (default is dry-run, which
// reports counts and compression ratio without writing any data).
//
// Why an op and not a synchronous handler: re-encoding can touch 50K+ rows and
// is a maintenance background task — tracking progress in the bell is more
// user-friendly than a long-polling HTTP response.
func (h *Handler) EmbReeencode(c *gin.Context) {
	if h.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	// Forward the request body as the op's params JSON so the op can see
	// {"apply":true/false} without the handler needing to parse it.
	var paramsJSON json.RawMessage
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&paramsJSON); err != nil {
			httputil.RespondWithBadRequest(c, "invalid JSON body")
			return
		}
	}

	opID, err := h.opRegistry.EnqueueOp(c.Request.Context(), "dedup.emb-reencode", paramsJSON)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue dedup emb-reencode", err)
		return
	}
	httputil.RespondWithSuccess(c, http.StatusAccepted, map[string]string{"op_id": opID})
}
