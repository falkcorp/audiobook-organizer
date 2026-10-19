// file: internal/server/handlers/ai.go
// version: 1.15.1
// guid: 6ccf0c64-9654-46c5-aed0-584943acb1c5
// last-edited: 2026-10-18

// AIHandler hosts the AI HTTP endpoints extracted from the server package:
// filename parsing, OpenAI / metadata-source connection tests, per-book AI
// parsing, the AI author-dedup scan lifecycle (start/list/get/results/apply/
// delete/cancel/compare), and the ai-jobs listing. The ai.author-review and
// ai.author-merge-apply ops have no HTTP routes here; they are started through
// the generic operations endpoints and their review modes live below as
// package-level functions. Business logic that does not depend on the *Server receiver
// is reproduced here behind narrow interfaces so package handlers stays free of
// any import on package server.

package handlers

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/util"
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
	// CreateScan refuses with an error wrapping *database.ScanAlreadyRunningError
	// while another scan has not finished.
	CreateScan(mode string) (*database.Scan, error)
	LinkOperation(scanID int, opID string) error
	CancelScan(scanID int) error
	// EnsureKeepIDs fills the keep author on a scan's stored merge results that
	// lack one (a scan stored before the field existed) and persists it.
	EnsureKeepIDs(scanID int) error
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
// because this package once enqueued the ops (through HTTP handlers since
// removed) and that package decodes them in Run.
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
// is exported because AIMergeApplyOpParams carries it across the package
// boundary into ai_ops.go. (It also used to be the request body of the removed
// ApplyAuthorReview handler.)
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

// AIScanApplyOpParams holds the serializable parameters for the
// ai.author-scan-apply op: merge the authors of these results of one scan.
//
// DryRun follows the owner's preview-by-default rule (opmode.ResolveDryRun):
// omitted means a preview that writes nothing. ApplyScanResults sets it to false
// explicitly, because the person clicking Apply wants the merge.
type AIScanApplyOpParams struct {
	ScanID      int   `json:"scan_id"`
	ResultIDs   []int `json:"result_ids"`
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

// aiStore is everything the AI handler needs from the store. Was
// database.Store (398 methods) until 2026-08-19, on a comment reading "full
// store for the author-review paths". Narrowed to the eight methods the
// author-review paths needed, then to the four below once the review/apply
// HTTP handlers were removed (01-P72): only ParseAudiobook / shownSeriesName
// call the store through AIHandler now.
type aiStore interface {
	GetAuthorByID(id int) (*database.Author, error)
	GetBookByID(id string) (*database.Book, error)
	// BookFilesGetter feeds the parse context's runtime
	// (database.LoadBookRuntime), which sums the book's files.
	database.BookFilesGetter
	// GetSeriesByID resolves the series a parsed series is compared with
	// (shownSeriesName).
	GetSeriesByID(id int) (*database.Series, error)
}

// aiReviewGroupsStore is the one method AIReviewGroupsMode itself touches. It
// is a separate declaration, not part of aiStore, because the function is
// exported and called by the op executor in package server without an
// AIHandler, so its own requirement is stated exactly.
type aiReviewGroupsStore interface {
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

// AIAuthorReviewDefID and AIAuthorMergeApplyDefID are the registered
// OperationDef ids for the two AI author operations.
//
// They are exported so internal/server/ai_ops.go registers each op under the
// same string the rest of the codebase refers to (the former in-package
// active-review lookup matched rows by DefID and is gone with the removed
// handlers, but the ids are still the wire identity of the ops).
const (
	AIAuthorReviewDefID     = "ai.author-review"
	AIAuthorMergeApplyDefID = "ai.author-merge-apply"
	// AIScanApplyDefID merges the authors of an AI scan's selected results.
	AIScanApplyDefID = "ai.author-scan-apply"
)

// SaveResultFunc persists an operation's result payload.
//
// The two review-mode functions used to call store.UpdateOperationResultData
// with the id of a v1 row a handler had minted. They now take this instead, so
// the result lands wherever the CALLER's operations system keeps it — for the v2
// ops in ai_ops.go, that is the run's own v2 row via ReporterSetResult. The
// error is returned rather than logged: the payload IS the review's output, and
// a run whose suggestions were never stored has not succeeded.
type SaveResultFunc func(payload any) error

// AIHandler hosts the AI HTTP endpoints. Fields are narrow dependency
// interfaces (plus an injected enrich function) so the handler is fully
// mockable and package handlers never imports package server.
type AIHandler struct {
	store      aiStore                  // 4 methods; see aiStore
	scanStore  AIScanStore              // AI scan persistence
	pipeline   AIPipeline               // scan pipeline manager (start/cancel)
	updater    AudiobookUpdater         // audiobook update service (parse-with-ai)
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
	registry OperationsRegistry,
	enrichBook func(*database.Book) any,
) *AIHandler {
	return &AIHandler{
		store:      store,
		scanStore:  scanStore,
		pipeline:   pipeline,
		updater:    updater,
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
	// A person clicked Test and is waiting: the lookup is interactive for the
	// daily quota budgets (Google Books' shared 1,000/day).
	ctx := metadata.WithInteractiveQuota(c.Request.Context())

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

// AIParsedUpdatePayload is the PUT-shaped payload ParseAudiobook sends to
// the update service: only the fields the parser filled, under the update
// service's own keys. The series number goes as series_position; it was sent
// as series_sequence, a key the update service never reads, so every
// AI-parsed series number was dropped.
//
// The series number depends on whether the parse keeps the book's series.
// shownSeries is the series name the book shows now.
//   - Same series (the names normalize alike, the parse names none, or the
//     book shows no series): the
//     number only fills a book with no stored position. A parse yields a
//     whole number, and sending it would overwrite a stored "1.5" with 1 and
//     lock it like a user edit.
//   - A different series: the parsed number is sent, and with no parsed
//     number the position is cleared (null). A number from the old series is
//     wrong in the new one, and the new series' lock would stop a fetch from
//     correcting it.
func AIParsedUpdatePayload(metadata *ai.ParsedMetadata, book *database.Book, shownSeries string) map[string]any {
	payload := map[string]any{}
	if metadata == nil {
		return payload
	}
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
	// A book that shows no series (none, or a dangling link with no embedded
	// object) has no position from another series to replace: the parse is
	// treated like its own series, fill-only, and never clears the number.
	seriesChanges := metadata.Series != "" && shownSeries != "" &&
		util.NormalizeAuthor(metadata.Series) != util.NormalizeAuthor(shownSeries)
	switch {
	case metadata.SeriesNum > 0 && (seriesChanges || !hasSeriesPosition(book)):
		payload["series_position"] = metadata.SeriesNum
	case metadata.SeriesNum <= 0 && seriesChanges:
		payload["series_position"] = nil
	}
	return payload
}

// hasSeriesPosition reports whether book already has a series position (the
// int or the raw value). 0 is no position, as everywhere else that reads it
// (organizer, scanner, applygate).
func hasSeriesPosition(book *database.Book) bool {
	if book == nil {
		return false
	}
	if book.SeriesSequence != nil && *book.SeriesSequence != 0 {
		return true
	}
	if book.SeriesPositionRaw == nil {
		return false
	}
	raw := strings.TrimSpace(*book.SeriesPositionRaw)
	if raw == "" {
		return false
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil && f == 0 {
		return false
	}
	return true
}

// shownSeriesName is the series name a GET shows for book: the embedded
// object, else the series row by SeriesID.
func (h *AIHandler) shownSeriesName(book *database.Book) string {
	if book.Series != nil {
		return book.Series.Name
	}
	if book.SeriesID != nil {
		if s, err := h.store.GetSeriesByID(*book.SeriesID); err == nil && s != nil {
			return s.Name
		}
	}
	return ""
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
	// Total duration is the canonical runtime (sum over the book's files), and
	// only when complete: Book.Duration is a partial sum for a multi-file book
	// whose chapters were not all probed, and telling the model a 10 h book is
	// 40 minutes long is worse than telling it nothing.
	rt, rtErr := database.LoadBookRuntime(h.store, book)
	if rtErr != nil {
		logging.Warn(c.Request.Context(), "ai parse: book files unreadable; runtime omitted", "book_id", book.ID, "err", rtErr)
	}
	if sec, ok := rt.KnownSeconds(); ok {
		abCtx.TotalDuration = sec
	}
	if rt.FilesCounted > 0 {
		abCtx.FileCount = rt.FilesCounted
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
	payload := AIParsedUpdatePayload(metadata, book, h.shownSeriesName(book))

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
		// A scan that has not finished refuses a second one. This is the
		// authoritative check (the store makes it and the create one step): the
		// page's own "a scan is running" guard only knows what it last read, and
		// the nightly ai-dedup-batch creates scans too.
		var running *database.ScanAlreadyRunningError
		if stderrors.As(err, &running) {
			httputil.RespondWithConflict(c, running.Error())
			return
		}
		httputil.InternalError(c, "failed to create AI scan", err)
		return
	}

	opID, err := h.registry.EnqueueOp(c.Request.Context(), "ai.author-scan",
		aiAuthorScanOpParams{ScanID: scan.ID})
	if err != nil {
		// The scan row already exists and is not finished: left alone it would
		// read as a running scan and refuse every later start. Cancel it.
		if cerr := h.pipeline.CancelScan(scan.ID); cerr != nil {
			logging.Warn(c.Request.Context(), "AI scan could not be canceled after its operation failed to enqueue",
				"scan_id", scan.ID, "err", cerr)
		}
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
		httputil.RespondWithOK(c, gin.H{"scans": []any{}})
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

// scanResultView is a stored result plus what the page needs to show it.
type scanResultView struct {
	database.ScanResult
	// PairedWith is the other half of a disagreement: the two phases' answers are
	// stored as two rows (groups first, then full), and this names the one that
	// is not this. Zero for every other row.
	PairedWith int `json:"paired_with,omitempty"`
}

// pairDisagreements links each disagreed groups_scan row to the full_scan row
// stored right after it (CrossValidate appends them as a pair, and ids only grow).
// The adjacency AND both sources are checked, so a row that merely sits next to
// another disagreed row is not paired by accident.
func pairDisagreements(results []database.ScanResult) []scanResultView {
	views := make([]scanResultView, len(results))
	for i := range results {
		views[i].ScanResult = results[i]
	}
	for i := 0; i+1 < len(results); i++ {
		a, b := results[i], results[i+1]
		if a.Agreement == "disagreed" && b.Agreement == "disagreed" &&
			a.Suggestion.Source == "groups_scan" && b.Suggestion.Source == "full_scan" &&
			views[i].PairedWith == 0 && views[i+1].PairedWith == 0 {
			views[i].PairedWith = b.ID
			views[i+1].PairedWith = a.ID
		}
	}
	return views
}

// GetScanResults returns results for a scan, with optional agreement filter.
// Alongside them it returns author_names: the name of every author a merge row
// names, by id (a merged-away author is absent), so the page can show
// "keep X, merge Y, Z" without a request per author.
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
	// Scans stored before the keep author existed get it now, once; a failure
	// leaves those rows without one (the page shows them as having no keep, and
	// Apply refuses them) rather than failing the read.
	if h.pipeline != nil {
		if kerr := h.pipeline.EnsureKeepIDs(id); kerr != nil {
			logging.Warn(c.Request.Context(), "ai scan results: could not fill in keep authors", "scan_id", id, "err", kerr)
		}
	}
	results, err := h.scanStore.GetScanResults(id)
	if err != nil {
		httputil.InternalError(c, "failed to get AI scan results", err)
		return
	}

	views := pairDisagreements(results)

	// Optional agreement filter
	agreement := c.Query("agreement")
	if agreement != "" {
		var filtered []scanResultView
		for _, r := range views {
			if r.Agreement == agreement {
				filtered = append(filtered, r)
			}
		}
		views = filtered
	}

	if views == nil {
		views = []scanResultView{}
	}
	httputil.RespondWithOK(c, gin.H{"results": views, "author_names": h.authorNames(views)})
}

// authorNames resolves every author id the results name. GetAuthorByID follows
// the author tombstone, so an answer under a different id means the author was
// merged away and is left out.
func (h *AIHandler) authorNames(views []scanResultView) map[string]string {
	names := map[string]string{}
	if h.store == nil {
		return names
	}
	seen := map[int]bool{}
	look := func(id int) {
		if id <= 0 || seen[id] {
			return
		}
		seen[id] = true
		if a, err := h.store.GetAuthorByID(id); err == nil && a != nil && a.ID == id {
			names[strconv.Itoa(id)] = a.Name
		}
	}
	for _, v := range views {
		look(v.Suggestion.KeepID)
		for _, aid := range v.Suggestion.AuthorIDs {
			look(aid)
		}
	}
	return names
}

// ApplyScanResults merges the authors of the selected results of a scan: it
// validates the request and enqueues an ai.author-scan-apply operation, answering
// 202 with the operation's id. The operation merges and marks each result that
// succeeded; the page follows it through the bell and re-reads the results when
// it finishes.
//
// Everything that can be refused is refused here, before anything is queued: a
// superseded scan (409, naming the scan to apply from), a scan that has not
// finished (409), a result that is not in the scan or is not a merge (400), and
// a result already applied (409). The operation checks again, because the state
// can change between the request and the run.
func (h *AIHandler) ApplyScanResults(c *gin.Context) {
	if h.scanStore == nil {
		httputil.RespondWithNotFound(c, "scan store", "")
		return
	}
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not configured")
		return
	}
	scanID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid scan ID")
		return
	}
	var req struct {
		ResultIDs []int `json:"result_ids"`
		// DryRun is optional and absent means a LIVE apply: unlike the op's own
		// default, the person pressing Apply asked for the merge.
		DryRun *bool `json:"dry_run"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if len(req.ResultIDs) == 0 {
		httputil.RespondWithBadRequest(c, "result_ids must not be empty")
		return
	}

	scan, err := h.scanStore.GetScan(scanID)
	if err != nil {
		httputil.InternalError(c, "failed to get AI scan", err)
		return
	}
	if scan == nil {
		httputil.RespondWithNotFound(c, "scan", "")
		return
	}
	// A superseded scan's list was replaced by a newer run: refuse and point at it.
	if scan.Status == "superseded" {
		httputil.RespondWithConflict(c, (&database.ScanSupersededError{ScanID: scanID, By: scan.SupersededBy}).Error())
		return
	}
	if scan.Status != "complete" {
		httputil.RespondWithConflict(c, fmt.Sprintf("scan %d is %s; only a finished scan's results can be applied", scanID, scan.Status))
		return
	}

	if h.pipeline != nil {
		if kerr := h.pipeline.EnsureKeepIDs(scanID); kerr != nil {
			logging.Warn(c.Request.Context(), "ai scan apply: could not fill in keep authors", "scan_id", scanID, "err", kerr)
		}
	}
	results, err := h.scanStore.GetScanResults(scanID)
	if err != nil {
		httputil.InternalError(c, "failed to get AI scan results", err)
		return
	}
	byID := make(map[int]database.ScanResult, len(results))
	for _, r := range results {
		byID[r.ID] = r
	}

	var invalid, conflicts []string
	seen := make(map[int]bool, len(req.ResultIDs))
	ids := make([]int, 0, len(req.ResultIDs))
	for _, rid := range req.ResultIDs {
		if seen[rid] {
			continue
		}
		seen[rid] = true
		ids = append(ids, rid)
		r, ok := byID[rid]
		switch {
		case !ok:
			invalid = append(invalid, fmt.Sprintf("result %d is not in scan %d", rid, scanID))
		case r.Applied:
			conflicts = append(conflicts, fmt.Sprintf("result %d is already applied", rid))
		case r.Suggestion.Action != "merge":
			invalid = append(invalid, fmt.Sprintf(
				"result %d is a %q suggestion; only merge suggestions can be applied here, review the others in the Authors view",
				rid, r.Suggestion.Action))
		case r.Suggestion.KeepID <= 0 || len(r.Suggestion.AuthorIDs) < 2:
			invalid = append(invalid, fmt.Sprintf("result %d has no keep author or no author to merge into it", rid))
		}
	}
	if len(invalid) > 0 {
		httputil.RespondWithBadRequest(c, strings.Join(append(invalid, conflicts...), "; "))
		return
	}
	if len(conflicts) > 0 {
		httputil.RespondWithConflict(c, strings.Join(conflicts, "; "))
		return
	}

	params := AIScanApplyOpParams{ScanID: scanID, ResultIDs: ids, DryRun: req.DryRun}
	if params.DryRun == nil {
		params.DryRun = opmode.Live()
	}
	opID, err := h.registry.EnqueueOp(c.Request.Context(), AIScanApplyDefID, params)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue the AI scan apply operation", err)
		return
	}
	httputil.RespondWithSuccess(c, 202, gin.H{
		"id":      opID,
		"type":    AIScanApplyDefID,
		"status":  "queued",
		"scan_id": scanID,
		"results": len(ids),
	})
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

	resultPayload := map[string]any{
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

	resultPayload := map[string]any{
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
