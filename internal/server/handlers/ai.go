// file: internal/server/handlers/ai.go
// version: 1.7.0
// guid: 6ccf0c64-9654-46c5-aed0-584943acb1c5
// last-edited: 2026-08-23

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
)

// aiHandlerStore is what this handler calls, measured by emptying it and reading
// the compiler's enumeration. It was an inline anonymous interface embedding
// database.AuthorStore + database.OperationStore — 51 methods.
type aiHandlerStore interface {
	GetAllAuthors() ([]database.Author, error)
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

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
// pipeline manager.
//
// StartScan was split into CreateScan + LinkOperation on 2026-08-22 when the
// scan became a v2 operation. The handler must still answer with the scan id
// synchronously — DedupAIReviewTab.tsx:47-48 calls startAIScan() and then
// immediately getAIScan(newScan.id) — so creating the scan cannot be deferred
// into the op. RunScan (the work itself) is reached by the registry, not here.
type AIPipeline interface {
	CreateScan(mode string) (*database.Scan, error)
	LinkOperation(scanID int, opID string) error
	CancelScan(scanID int) error
}

// AudiobookUpdater is the narrow interface AIHandler requires from the
// audiobook update service. Defining it here is MANDATORY: the concrete type is
// *audiobooks.AudiobookUpdateService (aliased in package server), and importing
// it would create an import cycle. Only UpdateAudiobook is called.
type AudiobookUpdater interface {
	UpdateAudiobook(ctx context.Context, id string, payload map[string]any) (*database.Book, error)
}

// --- op param types ---
//
// These are the params for the two AI author ops, and they are EXPORTED for a
// reason. Package server used to declare its own structurally-identical copies
// (server.aiReviewOpParams / aiMergeApplyOpParams / aiMergeApplySuggestion),
// because this package enqueues the op and that package decodes it in Run.
// Nothing coupled the two but matching JSON tags: edit one side's fields and
// the build stays green while the op silently decodes a zero value at runtime.
// That is not hypothetical — dropping the legacy_op_id field on 2026-08-23
// required editing both halves in lockstep, and only the second edit was load-
// bearing. The copies are gone; ai_ops.go decodes into these types directly, so
// the drift is a compile error instead of a wire-shape bug.

// AIReviewOpParams holds the serializable parameters for the ai.author-review op.
type AIReviewOpParams struct {
	Mode        string                   `json:"mode"`
	DedupGroups []dedup.AuthorDedupGroup `json:"dedup_groups,omitempty"`
}

// aiAuthorScanOpParams mirrors server.aiAuthorScanParams. Unlike its siblings
// above it carries no legacy_op_id: ai.author-scan never had a v1 row to bridge
// back to once the scan became a v2 operation.
type aiAuthorScanOpParams struct {
	ScanID int `json:"scan_id"`
}

// AIMergeApplySuggestion is the per-item suggestion for the merge-apply op. It
// is exported because it doubles as the HTTP request body shape for
// ApplyAuthorReview, and because AIMergeApplyOpParams carries it across the
// package boundary into ai_ops.go.
type AIMergeApplySuggestion struct {
	GroupIndex    int    `json:"group_index"`
	Action        string `json:"action"`
	CanonicalName string `json:"canonical_name"`
	KeepID        int    `json:"keep_id"`
	MergeIDs      []int  `json:"merge_ids"`
	Rename        bool   `json:"rename"`
}

// AIMergeApplyOpParams holds the serializable parameters for the
// ai.author-merge-apply op.
type AIMergeApplyOpParams struct {
	Suggestions []AIMergeApplySuggestion `json:"suggestions"`
}

// AIHandler hosts the AI HTTP endpoints. Fields are narrow dependency
// interfaces (plus the concrete dedup cache and an injected enrich function) so
// the handler is fully mockable and package handlers never imports package
// server.
// aiStore is everything the AI handler needs from the store. Was
// database.Store (398 methods) until 2026-08-19, on a comment reading "full
// store for the author-review paths". An empty-interface compiler probe under
// -gcflags=-e enumerated exactly the eight methods below, with no forwarding
// constraints and no `too many errors` truncation.
type aiStore interface {
	aiReviewGroupsStore
	aiAuthorReviewStore
	aiOperationStore
}

// aiReviewGroupsStore is the two methods AIReviewGroupsMode itself touches. It
// is a separate declaration rather than a slice of aiStore because the function
// is exported and callable without an AIHandler, so its own requirement is worth
// stating exactly -- probed independently, not assumed to be a subset.
type aiReviewGroupsStore interface {
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

// aiAuthorReviewStore is the author/book reads behind the author-review paths.
type aiAuthorReviewStore interface {
	GetAllAuthorBookCounts() (map[int]int, error)
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetBookByID(id string) (*database.Book, error)
}

// AIAuthorReviewDefID and AIAuthorMergeApplyDefID are the registered
// OperationDef ids for the two AI author operations.
//
// They are exported so internal/server/ai_ops.go registers each op under the
// same string this package enqueues and compares against. The review id in
// particular must not drift: activeAuthorReview matches rows by DefID, so a
// mismatch would not fail a build, and a unit test written with the same
// literal would not see it either — the guard would simply match nothing,
// forever, and every request would start a second concurrent review.
const (
	AIAuthorReviewDefID     = "ai.author-review"
	AIAuthorMergeApplyDefID = "ai.author-merge-apply"
)

// SaveResultFunc persists an operation's result payload.
//
// The two review-mode functions used to call store.UpdateOperationResultData
// with the id of a v1 row the handler had minted. They now take this instead, so
// the result lands wherever the CALLER's operations system keeps it — for the v2
// ops in ai_ops.go, that is the run's own v2 row via ReporterSetResult. The
// error is returned rather than logged: the payload IS the review's output, and
// a run whose suggestions were never stored has not succeeded.
type SaveResultFunc func(payload any) error

// activeAuthorReview returns the in-flight ai.author-review of the given mode,
// or nil if none is running.
//
// The mode lives in the run's params rather than in its def id — there is one
// def for both modes — so this decodes Params to compare. ListActiveOperationsV2
// is already scoped to non-terminal statuses, which is why no status list
// appears here: the v1 version hard-coded {"pending","running"} and would have
// silently stopped matching if the registry ever added a third active status.
func (h *AIHandler) activeAuthorReview(mode string) *database.OperationV2Row {
	if h.store == nil {
		return nil
	}
	rows, err := h.store.ListActiveOperationsV2()
	if err != nil {
		// A failed lookup must not block the user from starting a review; the
		// worst case is two concurrent runs of the same mode, which is what the
		// v1 scan also did when ListOperations errored (it discarded the error).
		slog.Warn("ai author review: active-op lookup failed, not deduping", "err", err)
		return nil
	}
	for i := range rows {
		if rows[i].DefID != AIAuthorReviewDefID {
			continue
		}
		var p AIReviewOpParams
		if err := json.Unmarshal([]byte(rows[i].Params), &p); err != nil {
			continue
		}
		if p.Mode == mode {
			return &rows[i]
		}
	}
	return nil
}

// aiOperationStore is the operation bookkeeping the AI review paths do.
//
// CreateOperation and ListOperations are deliberately ABSENT. The handlers used
// to mint a v1 row and scan v1 rows for an in-flight run of the same mode; both
// now go through the v2 keyspace, and leaving the methods off makes a
// reintroduction a compile error rather than something a reviewer has to catch.
type aiOperationStore interface {
	ListActiveOperationsV2() ([]database.OperationV2Row, error)
}

type AIHandler struct {
	store      aiStore                  // 8 methods; see aiStore
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
	store aiStore,
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
//
// Three steps, in this order: create the scan (so its id can be returned now),
// enqueue the operation that will run it, then record the link between them.
// The link is what lets CancelOperationV2 route a cancel from the operations
// timeline back into the pipeline.
func (h *AIHandler) StartScan(c *gin.Context) {
	if h.pipeline == nil {
		httputil.RespondWithInternalError(c, "AI scan pipeline not configured")
		return
	}
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not configured")
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

	scan, err := h.pipeline.CreateScan(req.Mode)
	if err != nil {
		httputil.InternalError(c, "failed to create AI scan", err)
		return
	}

	opID, err := h.registry.EnqueueOp(c.Request.Context(), "ai.author-scan",
		aiAuthorScanOpParams{ScanID: scan.ID})
	if err != nil {
		httputil.InternalError(c, "failed to enqueue AI scan operation", err)
		return
	}

	// A failed link is logged, not fatal: the op is already queued and will run
	// the scan regardless. What is lost is the cancel route and the timeline's
	// ability to associate the two, which is worth a warning but not worth
	// failing a request whose work is under way.
	if err := h.pipeline.LinkOperation(scan.ID, opID); err != nil {
		slog.Warn("AI scan started but could not be linked to its operation",
			"scan_id", scan.ID, "op_id", opID, "error", err)
	}
	scan.OperationID = opID

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

	// Block concurrent same-mode runs by answering with the run already in
	// flight. This used to scan ListOperations(50, 0) for a v1 row of type
	// "ai-author-review-<mode>"; it now asks the v2 keyspace, which is where the
	// run actually lives.
	//
	// Not ConcurrencyKey: that gate is per-DEF and makes the second op WAIT in
	// the queue, where this contract is per-MODE and hands the caller back the
	// running op immediately. Those are different behaviours, and the caller's
	// is the one clients depend on.
	//
	// ListActiveOperationsV2 is already status-scoped, so unlike the v1 scan
	// there is no status list here to drift out of sync with the registry's own
	// idea of "active".
	if existing := h.activeAuthorReview(mode); existing != nil {
		httputil.RespondWithSuccess(c, 202, gin.H{
			"operation_id": existing.ID,
			"status":       existing.Status,
			"mode":         mode,
		})
		return
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

	// No v1 row is minted. The id handed back is the one EnqueueOp keyed the run
	// under, so it resolves at GET /operations/v2/:id — the only place a client
	// can poll it. Returning a separately-minted id here is the defect that made
	// the diagnostics export undownloadable (#2747) and the folder scan's
	// progress unpollable (#2762).
	reviewParams := AIReviewOpParams{Mode: mode, DedupGroups: dedupGroups}
	opID, enqErr := h.registry.EnqueueOp(c.Request.Context(), AIAuthorReviewDefID, reviewParams)
	if enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, 202, gin.H{
		"operation_id": opID,
		"status":       "queued",
		"mode":         mode,
	})
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

	// See ReviewDuplicateAuthors: the returned id is EnqueueOp's, and no v1 row
	// is minted alongside it.
	applyParams := AIMergeApplyOpParams{Suggestions: req.Suggestions}
	opID, enqErr := h.registry.EnqueueOp(c.Request.Context(), AIAuthorMergeApplyDefID, applyParams)
	if enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, 202, gin.H{
		"operation_id": opID,
		"status":       "queued",
	})
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
func UnwrapAIJobsStore(s any) (database.AIJobsStore, bool) {
	aij := database.GetAIJobs(s)
	return aij, aij != nil
}

// AIReviewGroupsMode is the Groups mode of the AI author review: local
// heuristics build groups, AI validates them. Relocated from the server package
// (was *Server.aiReviewGroupsMode); the op executor in package server
// (ai_ops.go) calls it as a package-level function. Receiver-free — every
// dependency arrives as a parameter.
func AIReviewGroupsMode(ctx context.Context, progress operations.ProgressReporter, parser aiParser, store aiReviewGroupsStore, saveResult SaveResultFunc, dedupGroups []dedup.AuthorDedupGroup) error {
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
	if err := saveResult(json.RawMessage(resultJSON)); err != nil {
		return fmt.Errorf("failed to store results: %w", err)
	}

	_ = progress.UpdateProgress(totalSteps, totalSteps, fmt.Sprintf("AI review complete: %d suggestions (%d/%d 100.00%%)", len(suggestions), totalSteps, totalSteps))
	return nil
}

// AIReviewFullMode sends all authors to AI for duplicate discovery. Relocated
// from the server package (was *Server.aiReviewFullMode); called by the op
// executor in package server (ai_ops.go) as a package-level function.
func AIReviewFullMode(ctx context.Context, progress operations.ProgressReporter, parser aiParser, store aiHandlerStore, saveResult SaveResultFunc) error {
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
	if err := saveResult(json.RawMessage(resultJSON)); err != nil {
		return fmt.Errorf("failed to store results: %w", err)
	}

	_ = progress.UpdateProgress(totalSteps, totalSteps, fmt.Sprintf("AI discovery complete: %d groups found (%d/%d 100.00%%)", len(groups), totalSteps, totalSteps))
	return nil
}
