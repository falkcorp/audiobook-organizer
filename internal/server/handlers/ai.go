// file: internal/server/handlers/ai.go
// version: 1.2.0
// guid: 6ccf0c64-9654-46c5-aed0-584943acb1c5
// last-edited: 2026-07-05

// AIHandler hosts the AI HTTP endpoints extracted from the server package:
// filename parsing, OpenAI / metadata-source connection tests, per-book AI
// parsing, the AI author-dedup scan lifecycle (start/list/get/results/apply/
// delete/cancel/compare), the duplicate-author review + apply flows, and the
// ai-jobs listing. Business logic that does not depend on the *Server receiver
// is reproduced here behind narrow interfaces so package handlers stays free of
// any import on package server.

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

// --- narrow dependency interfaces ---

// aiParser mirrors the server-package aiParser interface structurally. It is
// redefined here (rather than imported) to keep package handlers independent of
// package server. The concrete *ai.OpenAIParser satisfies it.
type aiParser interface {
	IsEnabled() bool
	ParseFilename(ctx context.Context, filename string) (*ai.ParsedMetadata, error)
	ParseAudiobook(ctx context.Context, abCtx ai.AudiobookContext) (*ai.ParsedMetadata, error)
	ParseCoverArt(ctx context.Context, imageBytes []byte, mimeType string) (*ai.ParsedMetadata, error)
	ReviewAuthorDuplicates(ctx context.Context, groups []ai.AuthorDedupInput) ([]ai.AuthorDedupSuggestion, error)
	DiscoverAuthorDuplicates(ctx context.Context, inputs []ai.AuthorDiscoveryInput) ([]ai.AuthorDiscoverySuggestion, error)
	TestConnection(ctx context.Context) error
}

// newAIParser constructs an aiParser from config. Copied (unexported) from the
// server package, which keeps its own copy because ai_ops.go and
// entities_ops.go also build parsers this way. Pure construction — safe to
// duplicate.
func newAIParser(apiKey string, enabled bool) aiParser {
	return ai.NewOpenAIParser(&config.AppConfig, apiKey, enabled)
}

// AIScanStore is the narrow database interface AIHandler requires from the AI
// scan store. It lists only the *database.AIScanStore methods the scan handlers
// call.
type AIScanStore interface {
	GetScan(id int) (*database.Scan, error)
	ListScans() ([]database.Scan, error)
	GetScanResults(scanID int) ([]database.ScanResult, error)
	GetPhases(scanID int) ([]database.ScanPhase, error)
	MarkResultApplied(scanID, resultID int) error
	DeleteScan(id int) error
}

// AIPipeline is the narrow interface AIHandler requires from the AI scan
// pipeline manager. Only StartScan and CancelScan are used by the handlers.
type AIPipeline interface {
	StartScan(ctx context.Context, mode string) (*database.Scan, error)
	CancelScan(scanID int) error
}

// AudiobookUpdater is the narrow interface AIHandler requires from the
// audiobook update service. Defining it here is MANDATORY: the concrete type is
// *audiobooks.AudiobookUpdateService (aliased in package server), and importing
// it would create an import cycle. Only UpdateAudiobook is called.
type AudiobookUpdater interface {
	UpdateAudiobook(ctx context.Context, id string, payload map[string]any) (*database.Book, error)
}

// --- op param wrappers ---
//
// These mirror the unexported server-package types of the same shape
// (server.aiReviewOpParams / server.aiMergeApplyOpParams /
// server.aiMergeApplySuggestion). EnqueueOp json.Marshals params immediately,
// and the op executors in package server json.Unmarshal them back into their
// own copies — so the wire shape (JSON tags) must stay byte-identical to the
// server-side definitions, even though the Go types live in two packages.

type aiReviewOpParams struct {
	LegacyOpID  string                   `json:"legacy_op_id"`
	Mode        string                   `json:"mode"`
	DedupGroups []dedup.AuthorDedupGroup `json:"dedup_groups,omitempty"`
}

// AIMergeApplySuggestion is the per-item suggestion for the merge-apply op. It
// is exported because it doubles as the HTTP request body shape for
// ApplyAuthorReview. JSON tags mirror server.aiMergeApplySuggestion exactly.
type AIMergeApplySuggestion struct {
	GroupIndex    int    `json:"group_index"`
	Action        string `json:"action"`
	CanonicalName string `json:"canonical_name"`
	KeepID        int    `json:"keep_id"`
	MergeIDs      []int  `json:"merge_ids"`
	Rename        bool   `json:"rename"`
}

type aiMergeApplyOpParams struct {
	LegacyOpID  string                   `json:"legacy_op_id"`
	Suggestions []AIMergeApplySuggestion `json:"suggestions"`
}

// AIHandler hosts the AI HTTP endpoints. Fields are narrow dependency
// interfaces (plus the concrete dedup cache and an injected enrich function) so
// the handler is fully mockable and package handlers never imports package
// server.
type AIHandler struct {
	store      database.Store           // full store for the author-review paths
	scanStore  AIScanStore              // AI scan persistence
	pipeline   AIPipeline               // scan pipeline manager (start/cancel)
	updater    AudiobookUpdater         // audiobook update service (parse-with-ai)
	dedupCache *cache.Cache[gin.H]      // dedup-group cache (spec cache exception)
	registry   OperationsRegistry       // shared ops registry (EnqueueOp only)
	enrichBook func(*database.Book) any // wraps server.enrichBookForResponseSingle
}

// NewAIHandler constructs an AIHandler. enrichBook wraps the server-private
// enrichBookForResponseSingle; its result is only used as a JSON response body,
// so any is sufficient.
func NewAIHandler(
	store database.Store,
	scanStore AIScanStore,
	pipeline AIPipeline,
	updater AudiobookUpdater,
	dedupCache *cache.Cache[gin.H],
	registry OperationsRegistry,
	enrichBook func(*database.Book) any,
) *AIHandler {
	return &AIHandler{
		store:      store,
		scanStore:  scanStore,
		pipeline:   pipeline,
		updater:    updater,
		dedupCache: dedupCache,
		registry:   registry,
		enrichBook: enrichBook,
	}
}

// ParseFilename uses OpenAI to parse a filename into structured metadata.
func (h *AIHandler) ParseFilename(c *gin.Context) {
	var req struct {
		Filename string `json:"filename" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "filename is required")
		return
	}

	// Create AI parser
	parser := ai.NewOpenAIParser(&config.AppConfig, config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
	if !parser.IsEnabled() {
		httputil.RespondWithBadRequest(c, "AI parsing is not enabled or API key not configured")
		return
	}

	// Parse filename
	metadata, err := parser.ParseFilename(c.Request.Context(), req.Filename)
	if err != nil {
		httputil.InternalError(c, "failed to parse filename", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{"metadata": metadata})
}

// TestConnection tests the OpenAI API connection.
func (h *AIHandler) TestConnection(c *gin.Context) {
	// Parse request body for API key (allows testing without saving)
	var req struct {
		APIKey string `json:"api_key"`
	}

	// Try to get API key from request body first, fall back to config
	apiKey := config.AppConfig.OpenAIAPIKey
	if err := c.ShouldBindJSON(&req); err == nil && req.APIKey != "" {
		apiKey = req.APIKey
	}

	if apiKey == "" {
		httputil.RespondWithBadRequest(c, "API key not provided")
		return
	}

	// Create parser with the provided/configured API key
	parser := ai.NewOpenAIParser(&config.AppConfig, apiKey, true)
	if err := parser.TestConnection(c.Request.Context()); err != nil {
		slog.Error("connection test failed", "err", err)
		httputil.RespondWithInternalError(c, "connection test failed")
		return
	}

	httputil.RespondWithOK(c, gin.H{"success": true, "message": "OpenAI connection successful"})
}

// TestMetadataSource tests a metadata source API key by performing a simple search.
func (h *AIHandler) TestMetadataSource(c *gin.Context) {
	var req struct {
		SourceID string `json:"source_id"`
		APIKey   string `json:"api_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SourceID == "" {
		httputil.RespondWithBadRequest(c, "source_id is required")
		return
	}
	if req.APIKey == "" {
		httputil.RespondWithBadRequest(c, "api_key is required")
		return
	}

	testQuery := "The Hobbit" // well-known book for test queries
	ctx := c.Request.Context()

	switch req.SourceID {
	case "google-books":
		client := metadata.NewGoogleBooksClient(req.APIKey)
		results, err := client.SearchByTitle(ctx, testQuery)
		if err != nil {
			httputil.RespondWithOK(c, gin.H{"success": false, "error": fmt.Sprintf("Google Books API error: %v", err)})
			return
		}
		httputil.RespondWithOK(c, gin.H{"success": true, "message": fmt.Sprintf("Google Books connection successful (%d results)", len(results))})

	case "hardcover":
		client := metadata.NewHardcoverClient(req.APIKey)
		results, err := client.SearchByTitle(ctx, testQuery)
		if err != nil {
			httputil.RespondWithOK(c, gin.H{"success": false, "error": fmt.Sprintf("Hardcover API error: %v", err)})
			return
		}
		httputil.RespondWithOK(c, gin.H{"success": true, "message": fmt.Sprintf("Hardcover connection successful (%d results)", len(results))})

	default:
		httputil.RespondWithBadRequest(c, fmt.Sprintf("unknown source: %s", req.SourceID))
	}
}

// ParseAudiobook parses an audiobook's filename with AI and updates its metadata.
func (h *AIHandler) ParseAudiobook(c *gin.Context) {
	id := c.Param("id")

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// Get the book
	book, err := h.store.GetBookByID(id)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	// Create AI parser
	parser := newAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
	if !parser.IsEnabled() {
		httputil.RespondWithBadRequest(c, "AI parsing is not enabled or API key not configured")
		return
	}

	// Build rich context for the AI parser
	abCtx := ai.AudiobookContext{
		FilePath: book.FilePath,
		Title:    book.Title,
	}
	if book.Narrator != nil {
		abCtx.Narrator = *book.Narrator
	}
	if book.Duration != nil {
		abCtx.TotalDuration = *book.Duration
	}
	// Resolve author name from author_id
	if book.AuthorID != nil {
		if author, err := h.store.GetAuthorByID(*book.AuthorID); err == nil {
			abCtx.AuthorName = author.Name
		}
	}

	// Parse with AI using full context
	metadata, err := parser.ParseAudiobook(c.Request.Context(), abCtx)
	if err != nil {
		httputil.InternalError(c, "failed to parse audiobook", err)
		return
	}

	// Build payload for the update service (routes through AudiobookService
	// which handles "&" splitting for authors/narrators, junction tables, etc.)
	payload := map[string]any{}
	if metadata.Title != "" {
		payload["title"] = metadata.Title
	}
	if metadata.Author != "" {
		payload["author_name"] = metadata.Author
	}
	if metadata.Narrator != "" {
		payload["narrator"] = metadata.Narrator
	}
	if metadata.Publisher != "" {
		payload["publisher"] = metadata.Publisher
	}
	if metadata.Year > 0 {
		payload["audiobook_release_year"] = metadata.Year
	}
	if metadata.Series != "" {
		payload["series_name"] = metadata.Series
	}
	if metadata.SeriesNum > 0 {
		payload["series_sequence"] = metadata.SeriesNum
	}

	// Route through the service layer for proper multi-author/narrator handling
	updatedBook, err := h.updater.UpdateAudiobook(c.Request.Context(), id, payload)
	if err != nil {
		httputil.InternalError(c, "failed to update audiobook", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"message":    "audiobook updated with AI-parsed metadata",
		"book":       h.enrichBook(updatedBook),
		"confidence": metadata.Confidence,
	})
}

// StartScan kicks off a new multi-pass AI author dedup scan.
func (h *AIHandler) StartScan(c *gin.Context) {
	if h.pipeline == nil {
		httputil.RespondWithInternalError(c, "AI scan pipeline not configured")
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		req.Mode = "realtime"
	}
	if req.Mode != "batch" && req.Mode != "realtime" {
		req.Mode = "realtime"
	}
	scan, err := h.pipeline.StartScan(c.Request.Context(), req.Mode)
	if err != nil {
		httputil.InternalError(c, "failed to start AI scan", err)
		return
	}
	httputil.RespondWithSuccess(c, 202, scan)
}

// ListScans returns all AI scan pipeline runs.
func (h *AIHandler) ListScans(c *gin.Context) {
	if h.scanStore == nil {
		httputil.RespondWithOK(c, gin.H{"scans": []interface{}{}})
		return
	}
	scans, err := h.scanStore.ListScans()
	if err != nil {
		httputil.InternalError(c, "failed to list AI scans", err)
		return
	}
	if scans == nil {
		scans = []database.Scan{}
	}
	httputil.RespondWithOK(c, gin.H{"scans": scans})
}

// GetScan returns a single scan with its phases.
func (h *AIHandler) GetScan(c *gin.Context) {
	if h.scanStore == nil {
		httputil.RespondWithNotFound(c, "scan store", "")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid scan ID")
		return
	}
	scan, err := h.scanStore.GetScan(id)
	if err != nil {
		httputil.InternalError(c, "failed to get AI scan", err)
		return
	}
	if scan == nil {
		httputil.RespondWithNotFound(c, "scan", "")
		return
	}
	phases, _ := h.scanStore.GetPhases(id)
	httputil.RespondWithOK(c, gin.H{"scan": scan, "phases": phases})
}

// GetScanResults returns results for a scan, with optional agreement filter.
func (h *AIHandler) GetScanResults(c *gin.Context) {
	if h.scanStore == nil {
		httputil.RespondWithNotFound(c, "scan store", "")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid scan ID")
		return
	}
	results, err := h.scanStore.GetScanResults(id)
	if err != nil {
		httputil.InternalError(c, "failed to get AI scan results", err)
		return
	}

	// Optional agreement filter
	agreement := c.Query("agreement")
	if agreement != "" {
		var filtered []database.ScanResult
		for _, r := range results {
			if r.Agreement == agreement {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	if results == nil {
		results = []database.ScanResult{}
	}
	httputil.RespondWithOK(c, gin.H{"results": results})
}

// ApplyScanResults marks selected scan results as applied.
func (h *AIHandler) ApplyScanResults(c *gin.Context) {
	if h.scanStore == nil {
		httputil.RespondWithNotFound(c, "scan store", "")
		return
	}
	scanID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid scan ID")
		return
	}
	var req struct {
		ResultIDs []int `json:"result_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}

	applied := 0
	var errors []string
	for _, resultID := range req.ResultIDs {
		if err := h.scanStore.MarkResultApplied(scanID, resultID); err != nil {
			errors = append(errors, fmt.Sprintf("result %d: %v", resultID, err))
		} else {
			applied++
		}
	}

	httputil.RespondWithOK(c, gin.H{"applied": applied, "errors": errors})
}

// DeleteScan removes a scan and all its associated data.
func (h *AIHandler) DeleteScan(c *gin.Context) {
	if h.scanStore == nil {
		httputil.RespondWithNotFound(c, "scan store", "")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid scan ID")
		return
	}
	if err := h.scanStore.DeleteScan(id); err != nil {
		httputil.InternalError(c, "failed to delete AI scan", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"status": "deleted"})
}

// CancelScan cancels a running AI scan, including any in-flight batch jobs.
func (h *AIHandler) CancelScan(c *gin.Context) {
	if h.pipeline == nil {
		httputil.RespondWithInternalError(c, "AI scan pipeline not configured")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid scan ID")
		return
	}
	if err := h.pipeline.CancelScan(id); err != nil {
		httputil.RespondWithNotFound(c, "scan", "")
		return
	}
	httputil.RespondWithOK(c, gin.H{"status": "canceled"})
}

// CompareScans compares results between two scans.
func (h *AIHandler) CompareScans(c *gin.Context) {
	if h.scanStore == nil {
		httputil.RespondWithNotFound(c, "scan store", "")
		return
	}
	aID, err := strconv.Atoi(c.Query("a"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid scan ID 'a'")
		return
	}
	bID, err := strconv.Atoi(c.Query("b"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid scan ID 'b'")
		return
	}

	resultsA, _ := h.scanStore.GetScanResults(aID)
	resultsB, _ := h.scanStore.GetScanResults(bID)

	// Build comparison: new in B, resolved from A, unchanged
	aMap := make(map[string]database.ScanResult)
	for _, r := range resultsA {
		key := fmt.Sprintf("%s:%s", r.Suggestion.Action, r.Suggestion.CanonicalName)
		aMap[key] = r
	}

	var newInB, unchanged []database.ScanResult
	bSeen := make(map[string]bool)
	for _, r := range resultsB {
		key := fmt.Sprintf("%s:%s", r.Suggestion.Action, r.Suggestion.CanonicalName)
		bSeen[key] = true
		if _, found := aMap[key]; found {
			unchanged = append(unchanged, r)
		} else {
			newInB = append(newInB, r)
		}
	}

	var resolvedFromA []database.ScanResult
	for key, r := range aMap {
		if !bSeen[key] {
			resolvedFromA = append(resolvedFromA, r)
		}
	}

	httputil.RespondWithOK(c, gin.H{
		"new_in_b":        newInB,
		"resolved_from_a": resolvedFromA,
		"unchanged":       unchanged,
	})
}

// ReviewDuplicateAuthors enqueues an AI duplicate-author review operation.
func (h *AIHandler) ReviewDuplicateAuthors(c *gin.Context) {
	parser := newAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
	if !parser.IsEnabled() {
		httputil.RespondWithBadRequest(c, "AI parsing is not enabled")
		return
	}

	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	// Parse optional mode from request body
	var reqBody struct {
		Mode string `json:"mode"`
	}
	_ = c.ShouldBindJSON(&reqBody)
	mode := reqBody.Mode
	if mode == "" {
		mode = "groups"
	}
	if mode != "full" && mode != "groups" {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("invalid mode %q; must be full or groups", mode))
		return
	}

	store := h.store

	// Check for an already-running ai-author-review of the same mode — block concurrent same-mode runs
	opType := "ai-author-review-" + mode
	recentOps, _, _ := store.ListOperations(50, 0)
	for _, existing := range recentOps {
		if existing.Type == opType && (existing.Status == "pending" || existing.Status == "running") {
			httputil.RespondWithSuccess(c, 202, existing)
			return
		}
	}

	// For groups mode, we need dedup groups — use cache if available, otherwise compute inline
	var dedupGroups []dedup.AuthorDedupGroup
	if mode == "groups" {
		cached, ok := h.dedupCache.Get("author-duplicates")
		if ok {
			groupsRaw, ok2 := cached["groups"]
			if ok2 {
				groupsJSON, err := json.Marshal(groupsRaw)
				if err == nil {
					_ = json.Unmarshal(groupsJSON, &dedupGroups)
				}
			}
		}
		if len(dedupGroups) == 0 {
			// Cache is cold — compute dedup groups inline instead of requiring a separate refresh
			authors, err := store.GetAllAuthors()
			if err != nil {
				httputil.InternalError(c, "failed to fetch authors", err)
				return
			}
			bookCounts, err := store.GetAllAuthorBookCounts()
			if err != nil {
				httputil.InternalError(c, "failed to fetch book counts", err)
				return
			}
			bookCountFn := func(authorID int) int { return bookCounts[authorID] }
			dedupGroups = dedup.FindDuplicateAuthors(authors, 0.9, bookCountFn, nil)
			// Warm the cache for subsequent requests
			result := gin.H{"groups": dedupGroups, "count": len(dedupGroups)}
			h.dedupCache.SetWithTTL("author-duplicates", result, 30*time.Minute)
		}
		if len(dedupGroups) == 0 {
			httputil.RespondWithOK(c, gin.H{"message": "no duplicate groups to review"})
			return
		}
	}

	opID := ulid.Make().String()
	op, err := store.CreateOperation(opID, opType, nil)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	reviewParams := aiReviewOpParams{LegacyOpID: op.ID, Mode: mode, DedupGroups: dedupGroups}
	if _, enqErr := h.registry.EnqueueOp(c.Request.Context(), "ai.author-review", reviewParams); enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, 202, op)
}

// ApplyAuthorReview enqueues an AI author merge-apply operation.
func (h *AIHandler) ApplyAuthorReview(c *gin.Context) {
	var req struct {
		Suggestions []AIMergeApplySuggestion `json:"suggestions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.Suggestions) == 0 {
		httputil.RespondWithBadRequest(c, "no suggestions provided")
		return
	}

	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	store := h.store
	opID := ulid.Make().String()
	op, err := store.CreateOperation(opID, "ai-author-merge-apply", nil)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	applyParams := aiMergeApplyOpParams{LegacyOpID: op.ID, Suggestions: req.Suggestions}
	if _, enqErr := h.registry.EnqueueOp(c.Request.Context(), "ai.author-merge-apply", applyParams); enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, 202, op)
}

// ListAIJobs serves GET /api/v1/ai-jobs with optional type/status filters.
// Query params: type, status, limit (default 100, max 500), offset (default 0).
func (h *AIHandler) ListAIJobs(c *gin.Context) {
	typeF := c.Query("type")
	statusF := c.Query("status")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}

	store := database.GetAIJobs(h.store)
	if store == nil {
		httputil.RespondWithInternalError(c, "store does not implement AIJobsStore")
		return
	}
	jobs, err := store.ListAIJobs(typeF, statusF, limit, offset)
	if err != nil {
		httputil.InternalError(c, "list ai_jobs", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Jobs any `json:"jobs"`
	}{Jobs: jobs})
}

// UnwrapAIJobsStore peels Store decorator layers until it finds one satisfying
// database.AIJobsStore. Delegates to database.GetAIJobs; kept for API compat.
func UnwrapAIJobsStore(s database.Store) (database.AIJobsStore, bool) {
	aij := database.GetAIJobs(s)
	return aij, aij != nil
}

// AIReviewGroupsMode is the Groups mode of the AI author review: local
// heuristics build groups, AI validates them. Relocated from the server package
// (was *Server.aiReviewGroupsMode); the op executor in package server
// (ai_ops.go) calls it as a package-level function. Receiver-free — every
// dependency arrives as a parameter.
func AIReviewGroupsMode(ctx context.Context, progress operations.ProgressReporter, parser aiParser, store database.Store, opID string, dedupGroups []dedup.AuthorDedupGroup) error {
	_ = progress.Log("info", fmt.Sprintf("Starting AI review (groups mode) of %d duplicate author groups", len(dedupGroups)), nil)
	// Schedule: 1 setup + N input rows + 1 send + 1 done = len+3 steps.
	totalSteps := len(dedupGroups) + 3
	_ = progress.UpdateProgress(0, totalSteps, fmt.Sprintf("Building AI review input for %d groups... (0/%d 0.00%%)", len(dedupGroups), totalSteps))

	var inputs []ai.AuthorDedupInput
	for i, group := range dedupGroups {
		var variantNames []string
		for _, v := range group.Variants {
			variantNames = append(variantNames, v.Name)
		}
		var sampleTitles []string
		if group.Canonical.ID > 0 {
			books, err := store.GetBooksByAuthorIDWithRoleCore(group.Canonical.ID)
			if err == nil {
				for j, b := range books {
					if j >= 3 {
						break
					}
					sampleTitles = append(sampleTitles, b.Title)
				}
			}
		}
		inputs = append(inputs, ai.AuthorDedupInput{
			Index:         i,
			CanonicalName: dedup.NormalizeAuthorName(group.Canonical.Name),
			VariantNames:  variantNames,
			BookCount:     group.BookCount,
			SampleTitles:  sampleTitles,
		})
	}

	sent := len(inputs) + 1 // setup + N inputs built
	_ = progress.UpdateProgress(sent, totalSteps, fmt.Sprintf("Sending %d groups to AI for review... (%d/%d %.2f%%)", len(inputs), sent, totalSteps, float64(sent)/float64(totalSteps)*100))

	suggestions, err := parser.ReviewAuthorDuplicates(ctx, inputs)
	if err != nil {
		return fmt.Errorf("AI review failed: %w", err)
	}

	// Normalize initials formatting in AI-returned canonical names
	for i := range suggestions {
		suggestions[i].CanonicalName = dedup.NormalizeAuthorName(suggestions[i].CanonicalName)
	}

	_ = progress.Log("info", fmt.Sprintf("Received %d suggestions from AI", len(suggestions)), nil)

	resultPayload := map[string]interface{}{
		"mode":        "groups",
		"suggestions": suggestions,
		"groups":      dedupGroups,
	}
	resultJSON, err := json.Marshal(resultPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal suggestions: %w", err)
	}
	if err := store.UpdateOperationResultData(opID, string(resultJSON)); err != nil {
		return fmt.Errorf("failed to store results: %w", err)
	}

	_ = progress.UpdateProgress(totalSteps, totalSteps, fmt.Sprintf("AI review complete: %d suggestions (%d/%d 100.00%%)", len(suggestions), totalSteps, totalSteps))
	return nil
}

// AIReviewFullMode sends all authors to AI for duplicate discovery. Relocated
// from the server package (was *Server.aiReviewFullMode); called by the op
// executor in package server (ai_ops.go) as a package-level function.
func AIReviewFullMode(ctx context.Context, progress operations.ProgressReporter, parser aiParser, store interface {
	database.AuthorStore
	database.OperationStore
}, opID string) error {
	_ = progress.Log("info", "Starting AI review (full mode) — discovering duplicates from all authors", nil)
	// Pre-load total is unknown; use a placeholder (0/1) Start so we never emit 0/0.
	_ = progress.UpdateProgress(0, 1, "Loading all authors... (0/1 0.00%)")

	allAuthors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("failed to get authors: %w", err)
	}

	_ = progress.Log("info", fmt.Sprintf("Building discovery input for %d authors", len(allAuthors)), nil)
	// Schedule: N input rows + 1 send + 1 done = len+2 steps.
	totalSteps := len(allAuthors) + 2
	_ = progress.UpdateProgress(0, totalSteps, fmt.Sprintf("Building input for %d authors... (0/%d 0.00%%)", len(allAuthors), totalSteps))

	var inputs []ai.AuthorDiscoveryInput
	for _, author := range allAuthors {
		var sampleTitles []string
		books, err := store.GetBooksByAuthorIDWithRoleCore(author.ID)
		if err == nil {
			for j, b := range books {
				if j >= 3 {
					break
				}
				sampleTitles = append(sampleTitles, b.Title)
			}
		}
		inputs = append(inputs, ai.AuthorDiscoveryInput{
			ID:           author.ID,
			Name:         author.Name,
			BookCount:    len(books),
			SampleTitles: sampleTitles,
		})
	}

	sent := len(inputs)
	_ = progress.UpdateProgress(sent, totalSteps, fmt.Sprintf("Sending %d authors to AI for discovery... (%d/%d %.2f%%)", len(inputs), sent, totalSteps, float64(sent)/float64(totalSteps)*100))

	discoveries, err := parser.DiscoverAuthorDuplicates(ctx, inputs)
	if err != nil {
		return fmt.Errorf("AI discovery failed: %w", err)
	}

	_ = progress.Log("info", fmt.Sprintf("AI discovered %d duplicate groups", len(discoveries)), nil)

	// Build author ID→Author map for lookup
	authorMap := make(map[int]database.Author)
	for _, a := range allAuthors {
		authorMap[a.ID] = a
	}

	// Convert discovery suggestions to standard AuthorDedupSuggestion + AuthorDedupGroup format
	var suggestions []ai.AuthorDedupSuggestion
	var groups []dedup.AuthorDedupGroup
	for _, disc := range discoveries {
		if len(disc.AuthorIDs) < 2 && disc.Action != "rename" {
			continue
		}
		// First ID = canonical, rest = variants
		canonicalID := disc.AuthorIDs[0]
		canonical, ok := authorMap[canonicalID]
		if !ok {
			continue
		}
		var variants []database.Author
		for _, aid := range disc.AuthorIDs[1:] {
			if a, ok := authorMap[aid]; ok {
				variants = append(variants, a)
			}
		}
		groups = append(groups, dedup.AuthorDedupGroup{
			Canonical: canonical,
			Variants:  variants,
			BookCount: disc.AuthorIDs[0], // placeholder; we just need a count
		})
		// Fix book count — count books for all authors in the group
		totalBooks := 0
		for _, aid := range disc.AuthorIDs {
			bks, err := store.GetBooksByAuthorIDWithRoleCore(aid)
			if err == nil {
				totalBooks += len(bks)
			}
		}
		groups[len(groups)-1].BookCount = totalBooks

		suggestions = append(suggestions, ai.AuthorDedupSuggestion{
			GroupIndex:    len(groups) - 1, // index into groups slice, not discoveries
			Action:        disc.Action,
			CanonicalName: dedup.NormalizeAuthorName(disc.CanonicalName),
			Reason:        disc.Reason,
			Confidence:    disc.Confidence,
			IsNarrator:    disc.IsNarrator,
			IsPublisher:   disc.IsPublisher,
		})
	}

	resultPayload := map[string]interface{}{
		"mode":        "full",
		"suggestions": suggestions,
		"groups":      groups,
	}
	resultJSON, err := json.Marshal(resultPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal results: %w", err)
	}
	if err := store.UpdateOperationResultData(opID, string(resultJSON)); err != nil {
		return fmt.Errorf("failed to store results: %w", err)
	}

	_ = progress.UpdateProgress(totalSteps, totalSteps, fmt.Sprintf("AI discovery complete: %d groups found (%d/%d 100.00%%)", len(groups), totalSteps, totalSteps))
	return nil
}
