// file: internal/server/handlers/diagnostics.go
// version: 1.8.1
// guid: 14e70c44-73ca-456a-bc67-8dc6ba6e5736
// last-edited: 2026-09-02

// DiagnosticsHandler hosts the diagnostics HTTP endpoints extracted from the
// server package: ZIP export start/download, AI batch submit + results, applying
// approved AI suggestions, and the db-health stats endpoint. Dependencies that
// would otherwise require importing package server (the merge service) arrive as
// constructor params behind narrow interfaces, so package handlers stays free of
// any import on package server.
//
// The export and AI flows hold no such dependency at all any more: both are
// registered OperationDefs, so the handler enqueues and the op — which already
// lives in package server — reaches the services it needs directly.

package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
)

// --- narrow dependency interfaces ---

// DiagnosticsService stood here: a one-method interface (CollectAllBooks) whose
// only consumer was SubmitAI's inline goroutine. Collecting books is now the
// diagnostics.ai-analyze op's job, and the op reaches the service directly, so
// the interface, its field and its mock were deleted rather than narrowed —
// with the last caller gone there was nothing left for it to decouple.
//
// MergeService is the narrow interface DiagnosticsHandler requires from the
// merge service. Only MergeBooks is called by the handlers; the concrete
// *merge.Service satisfies it.
type MergeService interface {
	MergeBooks(bookIDs []string, primaryID string) (*merge.Result, error)
}

// --- op param wrapper ---
//
// diagnosticsExportOpParams mirrors the unexported server-package type of the
// same shape (server.diagnosticsExportOpParams in diagnostics_ops.go).
// EnqueueOp json.Marshals params immediately and the op executor in package
// server json.Unmarshals them back into its own copy, so the wire shape (JSON
// tags) must stay byte-identical to the server-side definition even though the
// Go types live in two packages.
//
// Because the two definitions are in different packages, the compiler cannot
// check them against each other: dropping a field on one side and not the other
// is a silent wire-shape drift, not a build error. legacy_op_id was dropped from
// both on 2026-08-22.
type diagnosticsExportOpParams struct {
	Category    string `json:"category"`
	Description string `json:"description"`
}

// DiagnosticsAIDefID is the registry id of the AI-diagnostics OperationDef,
// shared with its registration in package server so the two cannot drift.
const DiagnosticsAIDefID = "diagnostics.ai-analyze"

// DiagnosticsAIOpParams is the param shape for DiagnosticsAIDefID.
//
// Deliberately exported and used verbatim by the server-side registration,
// rather than mirrored the way diagnosticsExportOpParams above is. A mirrored
// pair is coupled only by its JSON tags, so dropping a field on one side is
// silent wire-shape drift instead of a build error — the same trap that made
// the AI author-review params worth deleting in #2771.
type DiagnosticsAIOpParams struct {
	Category    string `json:"category"`
	Description string `json:"description"`
}

// diagnosticsSuggestion represents a single AI suggestion from diagnostics
// analysis. Relocated from the server package (was an unexported type in
// diagnostics_handlers.go); only used internally to decode ResultData.
type diagnosticsSuggestion struct {
	ID        string   `json:"id"`
	Action    string   `json:"action"`
	BookIDs   []string `json:"book_ids"`
	PrimaryID string   `json:"primary_id,omitempty"`
	Reason    string   `json:"reason"`
	Fix       string   `json:"fix,omitempty"`
}

// --- db-health response shapes ---
//
// Relocated verbatim from the server package (diagnostics_handlers.go) to
// preserve the JSON contract of GET /api/v1/diagnostics/db-health.

type dbHealthResponse struct {
	SQLite           *dbHealthSQLite           `json:"sqlite,omitempty"`
	Pebble           *dbHealthPebble           `json:"pebble,omitempty"`
	Embeddings       dbHealthEmbeddings        `json:"embeddings"`
	AiScans          dbHealthAiScans           `json:"ai_scans"`
	MetadataCache    dbHealthMetadataCache     `json:"metadata_cache"`
	BookPathPrefixes []database.BookPathPrefix `json:"book_path_prefixes,omitempty"`
}

type dbHealthSQLite struct {
	//lint:ignore SA1019 kept for db-health JSON compat (2026-07-12); replaced by a PebbleDB key-count table in a future cleanup
	Tables    []database.SQLiteTableStat `json:"tables"`
	SizeBytes int64                      `json:"size_bytes"`
}

type dbHealthPebble struct {
	KeyCount  int64  `json:"key_count"`
	SizeBytes uint64 `json:"size_bytes"`
}

type dbHealthEmbeddings struct {
	VectorCount int64 `json:"vector_count"`
	SizeBytes   int64 `json:"size_bytes"`
}

type dbHealthAiScans struct {
	JobCount     int    `json:"job_count"`
	PendingCount int    `json:"pending_count"`
	SizeBytes    uint64 `json:"size_bytes"`
}

type dbHealthMetadataCache struct {
	TotalEntries   int64 `json:"total_entries"`
	TTLDays        int   `json:"ttl_days"`
	ExpiredEntries int64 `json:"expired_entries"`
}

// diagnosticsStore is everything the diagnostics endpoints need from the store.
// Was database.Store (398 methods) until 2026-08-19, on a comment claiming a
// "db-health type switch" required the full surface. Verified before believing:
// there is no type switch or type assertion on this field anywhere in the file.
//
// Most of the width is forwarding, so those requirements are embedded BY NAME
// rather than transcribed -- one declared entry each, and each re-narrows on its
// own when its own package does. Note that an empty-interface compiler probe
// reports only the FIRST missing method of a constraint, so a constraint is
// indistinguishable from a single direct call in the raw output; the direct
// calls below were confirmed by re-probing with the constraints satisfied.
type diagnosticsStore interface {
	diagnostics.Store   // NewService, lazy-construction fallback
	merge.Store         // NewService, lazy-construction fallback
	database.RawKVStore // CountCachedMetadataFetches

	diagnosticsOperationStore
}

// diagnosticsOperationStore is the operation bookkeeping the diagnostics
// endpoints do while a long run is in flight.
//
// One method, because every diagnostics flow is now a registered OperationDef
// whose op is created by EnqueueOp and whose result is written by its own
// reporter: the export op, and — since diagnostics.ai-analyze replaced the bare
// goroutine that used to drive a legacy row by hand — the AI flow as well.
// GetOperationV2 reads that row back for DownloadExport, GetAIResults and
// ApplySuggestions alike.
//
// The five legacy methods that stood here (CreateOperation, GetOperationByID and
// the three status/result/error writers) went with that goroutine. A previous
// note warned that they did not share a single owner — the writers were
// SubmitAI's, but GetOperationByID was read by GetAIResults and ApplySuggestions
// — and that they would nonetheless retire together, since all three handlers
// read rows SubmitAI minted. They did.
type diagnosticsOperationStore interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
}

type DiagnosticsHandler struct {
	store          diagnosticsStore         // see diagnosticsStore
	mergeService   MergeService             // merge service (MergeBooks); may be nil
	embeddingStore *database.EmbeddingStore // embeddings health stats; may be nil
	aiScanStore    *database.AIScanStore    // ai-scan health stats; may be nil
	registry       OperationsRegistry       // shared ops registry (EnqueueOp only)
}

// NewDiagnosticsHandler constructs a DiagnosticsHandler. Field/param order:
// store, mergeService, embeddingStore, aiScanStore, registry. mergeService may
// be nil — ApplySuggestions replicates the server-side lazy construction
// fallback.
//
// The diagService and batchParser params are gone: both existed only for
// SubmitAI's inline goroutine, which is now the diagnostics.ai-analyze op. The
// op constructs its own parser and reaches the diagnostics service directly, so
// neither had to be threaded through the handler to get there.
func NewDiagnosticsHandler(
	store diagnosticsStore,
	mergeService MergeService,
	embeddingStore *database.EmbeddingStore,
	aiScanStore *database.AIScanStore,
	registry OperationsRegistry,
) *DiagnosticsHandler {
	return &DiagnosticsHandler{
		store:          store,
		mergeService:   mergeService,
		embeddingStore: embeddingStore,
		aiScanStore:    aiScanStore,
		registry:       registry,
	}
}

// StartExport creates a diagnostic ZIP export asynchronously.
func (h *DiagnosticsHandler) StartExport(c *gin.Context) {
	var req struct {
		Category    string `json:"category"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if req.Category == "" {
		req.Category = "general"
	}

	validCategories := map[string]bool{
		"deduplication":    true,
		"error_analysis":   true,
		"metadata_quality": true,
		"general":          true,
	}
	if !validCategories[req.Category] {
		httputil.RespondWithBadRequest(c, "invalid category; must be one of: deduplication, error_analysis, metadata_quality, general")
		return
	}

	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	params := diagnosticsExportOpParams{
		Category:    req.Category,
		Description: req.Description,
	}
	// Return the id EnqueueOp minted. The client polls this against
	// GET /operations/v2/:id and passes it back to DownloadExport, so it has to
	// be the v2 run's own id — a separately-minted legacy id resolves at neither.
	opID, enqErr := h.registry.EnqueueOp(c.Request.Context(), "diagnostics.export", params)
	if enqErr != nil {
		httputil.InternalError(c, "failed to enqueue diagnostics export", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, 202, gin.H{
		"operation_id": opID,
		"status":       "queued",
	})
}

// DownloadExport serves the ZIP file for a completed diagnostics export.
func (h *DiagnosticsHandler) DownloadExport(c *gin.Context) {
	opID := c.Param("operationId")

	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// Reads the v2 row, because that is where ExportDiagnostics' op now lives and
	// where its Run stores the zip path (via ReporterSetResult). The id in the URL
	// came from EnqueueOp, so looking it up as a legacy row would always 404.
	op, err := store.GetOperationV2(opID)
	if err != nil {
		// Split from the not-found case deliberately. GetOperationV2 returns
		// (nil, nil) for an absent row, so a non-nil error here is a store
		// failure — reporting that as "no such export" tells the user their
		// export never existed and logs nothing.
		httputil.InternalError(c, "failed to load export operation", err)
		return
	}
	if op == nil {
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	}

	// Terminal-but-not-completed has to be distinguishable from still-running.
	// Both used to take the 202 branch below, which reports a SUCCESS envelope
	// carrying ProgressMessage — so a failed export was byte-identical to a
	// running one and a polling client never stopped. ErrorMessage is the only
	// field the worker writes the reason into, and nothing read it.
	if opsregistry.IsTerminalStatus(op.Status) && op.Status != "completed" {
		reason := op.Status
		if op.ErrorMessage != nil && *op.ErrorMessage != "" {
			reason = *op.ErrorMessage
		}
		httputil.InternalError(c, "diagnostics export did not complete",
			fmt.Errorf("operation %s ended as %s: %s", opID, op.Status, reason))
		return
	}

	if op.Status != "completed" {
		httputil.RespondWithSuccess(c, 202, gin.H{
			"operation_id": opID,
			"status":       op.Status,
			"message":      op.ProgressMessage,
		})
		return
	}

	if op.ResultData == nil || *op.ResultData == "" {
		httputil.RespondWithInternalError(c, "no result data available")
		return
	}

	var result map[string]string
	if err := json.Unmarshal([]byte(*op.ResultData), &result); err != nil {
		httputil.RespondWithInternalError(c, "failed to parse result data")
		return
	}

	zipPath := result["zip_path"]
	if zipPath == "" {
		httputil.RespondWithInternalError(c, "zip path not found in result")
		return
	}

	if _, err := os.Stat(zipPath); os.IsNotExist(err) {
		httputil.RespondWithNotFound(c, "export file", "no longer available")
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=diagnostics-%s.zip", opID))
	c.File(zipPath)
}

// SubmitAI generates a diagnostics export and submits it to OpenAI batch API.
func (h *DiagnosticsHandler) SubmitAI(c *gin.Context) {
	if config.AppConfig.OpenAIAPIKey == "" {
		httputil.RespondWithBadRequest(c, "OpenAI API key not configured")
		return
	}

	var req struct {
		Category    string `json:"category"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if req.Category == "" {
		req.Category = "general"
	}

	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}

	// Everything this used to do inline — collect books, build the JSONL, upload
	// it, create the batch, then wait out a run that OpenAI may take 24h to
	// finish — now belongs to the diagnostics.ai-analyze OperationDef, which
	// reports progress and stores its result on its own v2 row. The handler's
	// only job is to enqueue it.
	opID, err := h.registry.EnqueueOp(c.Request.Context(), DiagnosticsAIDefID, DiagnosticsAIOpParams{
		Category:    req.Category,
		Description: req.Description,
	})
	if err != nil {
		httputil.InternalError(c, "failed to enqueue diagnostics AI analysis", err)
		return
	}

	httputil.RespondWithSuccess(c, 202, gin.H{
		"operation_id": opID,
		"status":       "queued",
	})
}

// GetAIResults returns the AI analysis results for a diagnostics operation.
func (h *DiagnosticsHandler) GetAIResults(c *gin.Context) {
	opID := c.Param("operationId")

	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	op, err := store.GetOperationV2(opID)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	}

	// "completed" is the terminal-success status in both keyspaces, so this
	// comparison did not have to change when the run moved to a v2 row. Any
	// other status (queued / running / failed / canceled) is reported straight
	// back for the client to keep polling on.
	if op.Status != "completed" {
		httputil.RespondWithOK(c, gin.H{
			"operation_id": opID,
			"status":       op.Status,
			"message":      op.ProgressMessage,
		})
		return
	}

	if op.ResultData == nil || *op.ResultData == "" {
		httputil.RespondWithOK(c, gin.H{
			"status":      "completed",
			"suggestions": []any{},
		})
		return
	}

	var resultData map[string]any
	if err := json.Unmarshal([]byte(*op.ResultData), &resultData); err != nil {
		httputil.RespondWithInternalError(c, "failed to parse result data")
		return
	}

	suggestions := resultData["suggestions"]
	if suggestions == nil {
		suggestions = []any{}
	}
	rawResponses := resultData["raw_responses"]
	if rawResponses == nil {
		rawResponses = []any{}
	}

	httputil.RespondWithOK(c, gin.H{
		"status":        op.Status,
		"suggestions":   suggestions,
		"raw_responses": rawResponses,
	})
}

// ApplySuggestions applies approved AI suggestions from diagnostics analysis.
func (h *DiagnosticsHandler) ApplySuggestions(c *gin.Context) {
	var req struct {
		OperationID           string   `json:"operation_id" binding:"required"`
		ApprovedSuggestionIDs []string `json:"approved_suggestion_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	op, err := store.GetOperationV2(req.OperationID)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", req.OperationID)
		return
	}

	if op.ResultData == nil || *op.ResultData == "" {
		httputil.RespondWithBadRequest(c, "no suggestions available")
		return
	}

	var resultData map[string]any
	if err := json.Unmarshal([]byte(*op.ResultData), &resultData); err != nil {
		httputil.RespondWithInternalError(c, "failed to parse result data")
		return
	}

	suggestionsRaw, ok := resultData["suggestions"]
	if !ok {
		httputil.RespondWithBadRequest(c, "no suggestions in result data")
		return
	}

	suggestionsJSON, _ := json.Marshal(suggestionsRaw)
	var suggestions []diagnosticsSuggestion
	if err := json.Unmarshal(suggestionsJSON, &suggestions); err != nil {
		httputil.RespondWithInternalError(c, "failed to parse suggestions")
		return
	}

	// Build lookup of approved IDs
	approvedSet := make(map[string]bool)
	for _, id := range req.ApprovedSuggestionIDs {
		approvedSet[id] = true
	}

	ms := h.mergeService
	if ms == nil {
		ms = merge.NewService(store)
	}

	applied := 0
	failed := 0
	var errors []string

	for _, suggestion := range suggestions {
		if !approvedSet[suggestion.ID] {
			continue
		}

		var applyErr error
		switch suggestion.Action {
		case "merge_versions":
			if len(suggestion.BookIDs) >= 2 {
				_, applyErr = ms.MergeBooks(suggestion.BookIDs, suggestion.PrimaryID)
			}

		case "delete_orphan":
			for _, bookID := range suggestion.BookIDs {
				book, getErr := store.GetBookByID(bookID)
				if getErr != nil || book == nil {
					applyErr = fmt.Errorf("book %s not found", bookID)
					break
				}
				marked := true
				book.MarkedForDeletion = &marked
				if _, updateErr := store.UpdateBook(book.ID, book); updateErr != nil {
					applyErr = updateErr
					break
				}
			}

		case "fix_metadata":
			// Fix field is a JSON string with field updates
			if suggestion.Fix != "" && len(suggestion.BookIDs) > 0 {
				var fixes map[string]any
				if parseErr := json.Unmarshal([]byte(suggestion.Fix), &fixes); parseErr != nil {
					applyErr = fmt.Errorf("invalid fix data: %w", parseErr)
				} else {
					for _, bookID := range suggestion.BookIDs {
						book, getErr := store.GetBookByID(bookID)
						if getErr != nil || book == nil {
							applyErr = fmt.Errorf("book %s not found", bookID)
							break
						}
						if title, ok := fixes["title"].(string); ok {
							book.Title = title
						}
						if _, updateErr := store.UpdateBook(book.ID, book); updateErr != nil {
							applyErr = updateErr
							break
						}
					}
				}
			}

		case "reassign_series":
			if suggestion.Fix != "" && len(suggestion.BookIDs) > 0 {
				var fixes map[string]any
				if parseErr := json.Unmarshal([]byte(suggestion.Fix), &fixes); parseErr != nil {
					applyErr = fmt.Errorf("invalid fix data: %w", parseErr)
				} else {
					if seriesIDFloat, ok := fixes["series_id"].(float64); ok {
						seriesID := int(seriesIDFloat)
						for _, bookID := range suggestion.BookIDs {
							book, getErr := store.GetBookByID(bookID)
							if getErr != nil || book == nil {
								applyErr = fmt.Errorf("book %s not found", bookID)
								break
							}
							book.SeriesID = &seriesID
							if _, updateErr := store.UpdateBook(book.ID, book); updateErr != nil {
								applyErr = updateErr
								break
							}
						}
					}
				}
			}

		default:
			applyErr = fmt.Errorf("unknown action: %s", suggestion.Action)
		}

		if applyErr != nil {
			failed++
			errors = append(errors, fmt.Sprintf("suggestion %s: %v", suggestion.ID, applyErr))
			slog.Warn("Failed to apply diagnostics suggestion", "suggestion", suggestion.ID, "applyErr", applyErr)
		} else {
			applied++
		}
	}

	httputil.RespondWithOK(c, gin.H{
		"applied": applied,
		"failed":  failed,
		"errors":  errors,
	})
}

// GetDBHealth returns health stats for all backing stores.
// GET /api/v1/diagnostics/db-health
func (h *DiagnosticsHandler) GetDBHealth(c *gin.Context) {
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	resp := dbHealthResponse{}

	// Main store stats — PebbleDB only since fable5 T022.
	//
	// resolveKeyCounter, not a bare assertion and no longer the concrete type.
	// Traced 2026-08-19: this handler is built in wireHandlers, which runs
	// setupRoutes -> NewServer, so the s.Ops() it captured is the BARE store
	// and the bare form was NOT failing here. Construction time is what decides
	// this, not the fact that GetDBHealth itself runs at request time. The
	// failure mode is invisible either way: a nil here just drops resp.Pebble
	// from the payload, so db-health reports a healthy store with no Pebble
	// section rather than an error.
	if st := resolveKeyCounter(store); st != nil {
		keyCount, sizeBytes, err := st.KeyCount()
		if err != nil {
			slog.Warn("db-health pebble key count", "err", err)
		}
		resp.Pebble = &dbHealthPebble{
			KeyCount:  keyCount,
			SizeBytes: sizeBytes,
		}
	}

	// Embeddings store (always SQLite, may be nil if DB path not set yet).
	if h.embeddingStore != nil {
		estats, err := h.embeddingStore.HealthStats()
		if err != nil {
			slog.Warn("db-health embedding stats", "err", err)
		}
		resp.Embeddings = dbHealthEmbeddings{
			VectorCount: estats.VectorCount,
			SizeBytes:   estats.SizeBytes,
		}
	}

	// AI scan store (always PebbleDB, may be nil).
	if h.aiScanStore != nil {
		astats, err := h.aiScanStore.HealthStats()
		if err != nil {
			slog.Warn("db-health ai scan stats", "err", err)
		}
		resp.AiScans = dbHealthAiScans{
			JobCount:     astats.JobCount,
			PendingCount: astats.PendingCount,
			SizeBytes:    astats.SizeBytes,
		}
	}

	// Metadata fetch cache — works against whatever backend is active.
	totalEntries, err := database.CountCachedMetadataFetches(store)
	if err != nil {
		slog.Warn("db-health metadata cache count", "err", err)
	}
	ttlDays := config.AppConfig.MetadataFetchCacheTTLDays

	var expiredEntries int64
	if ttlDays > 0 {
		cutoff := time.Now().Add(-time.Duration(ttlDays) * 24 * time.Hour)
		pairs, scanErr := store.ScanPrefix("metadata_fetch_cache:")
		if scanErr == nil {
			for _, kv := range pairs {
				var entry database.CachedMetadataEntry
				if jsonErr := json.Unmarshal(kv.Value, &entry); jsonErr == nil {
					if entry.CachedAt.Before(cutoff) {
						expiredEntries++
					}
				}
			}
		}
	}

	resp.MetadataCache = dbHealthMetadataCache{
		TotalEntries:   totalEntries,
		TTLDays:        ttlDays,
		ExpiredEntries: expiredEntries,
	}

	httputil.RespondWithOK(c, resp)
}

// keyCounter is the single Pebble-only statistic the db-health endpoint reports.
//
// Not on database.Store (compile-probed 2026-08-19), so a bare assertion fails
// through the Bleve indexedStore decorator. Named rather than resolved with
// database.AsPebbleStore so this package does not depend on the concrete type
// by name -- see docs/plans/2026-08-19-split-the-pebblestore-surface.md.
type keyCounter interface {
	KeyCount() (count int64, sizeBytes uint64, err error)
}

// resolveKeyCounter walks the decorator chain, returning nil on a backend that
// does not keep Pebble key statistics.
func resolveKeyCounter(s any) keyCounter {
	if c, ok := database.AsCapability[keyCounter](s); ok {
		return c
	}
	return nil
}
