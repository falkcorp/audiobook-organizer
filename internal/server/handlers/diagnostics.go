// file: internal/server/handlers/diagnostics.go
// version: 1.3.1
// guid: 14e70c44-73ca-456a-bc67-8dc6ba6e5736
// last-edited: 2026-07-12

// DiagnosticsHandler hosts the diagnostics HTTP endpoints extracted from the
// server package: ZIP export start/download, AI batch submit + results, applying
// approved AI suggestions, and the db-health stats endpoint. Dependencies that
// would otherwise require importing package server (the AI batch parser, the
// diagnostics + merge services) arrive as constructor params behind narrow
// interfaces, so package handlers stays free of any import on package server.

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

// --- narrow dependency interfaces ---

// DiagnosticsService is the narrow interface DiagnosticsHandler requires from
// the diagnostics service. Only CollectAllBooks is called by the handlers; the
// concrete *diagnostics.Service satisfies it.
type DiagnosticsService interface {
	CollectAllBooks() ([]database.BookCore, error)
}

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
type diagnosticsExportOpParams struct {
	LegacyOpID  string `json:"legacy_op_id"`
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

// DiagnosticsHandler hosts the diagnostics HTTP endpoints. Fields are narrow
// dependency interfaces (plus clean concrete database stores and the AI batch
// parser) so the handler is mockable and package handlers never imports package
// server.
type DiagnosticsHandler struct {
	store          database.Store           // full store (db-health type switch + apply/export paths)
	diagService    DiagnosticsService       // diagnostics service (CollectAllBooks); may be nil
	mergeService   MergeService             // merge service (MergeBooks); may be nil
	embeddingStore *database.EmbeddingStore // embeddings health stats; may be nil
	aiScanStore    *database.AIScanStore    // ai-scan health stats; may be nil
	registry       OperationsRegistry       // shared ops registry (EnqueueOp only)
	batchParser    *ai.OpenAIParser         // resolved from server.batchPoller.parser; may be nil
}

// NewDiagnosticsHandler constructs a DiagnosticsHandler. Field/param order:
// store, diagService, mergeService, embeddingStore, aiScanStore, registry,
// batchParser. diagService/mergeService may be nil — the handlers replicate the
// server-side lazy construction fallback. batchParser may be nil — the submit-AI
// flow falls back to preparing batch data without submission.
func NewDiagnosticsHandler(
	store database.Store,
	diagService DiagnosticsService,
	mergeService MergeService,
	embeddingStore *database.EmbeddingStore,
	aiScanStore *database.AIScanStore,
	registry OperationsRegistry,
	batchParser *ai.OpenAIParser,
) *DiagnosticsHandler {
	return &DiagnosticsHandler{
		store:          store,
		diagService:    diagService,
		mergeService:   mergeService,
		embeddingStore: embeddingStore,
		aiScanStore:    aiScanStore,
		registry:       registry,
		batchParser:    batchParser,
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

	opID := ulid.Make().String()
	_, err := store.CreateOperation(opID, "diagnostics_export", nil)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	params := diagnosticsExportOpParams{
		LegacyOpID:  opID,
		Category:    req.Category,
		Description: req.Description,
	}
	if _, enqErr := h.registry.EnqueueOp(c.Request.Context(), "diagnostics.export", params); enqErr != nil {
		httputil.InternalError(c, "failed to enqueue diagnostics export", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, 202, gin.H{
		"operation_id": opID,
		"status":       "generating",
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

	op, err := store.GetOperationByID(opID)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	}

	if op.Status != "completed" {
		httputil.RespondWithSuccess(c, 202, gin.H{
			"operation_id": opID,
			"status":       op.Status,
			"message":      op.Message,
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

	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	opID := ulid.Make().String()
	_, err := store.CreateOperation(opID, "diagnostics_ai", nil)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	ds := h.diagService
	if ds == nil {
		ds = diagnostics.NewService(store, nil, config.AppConfig.ITunes.LibraryReadPath)
	}

	category := req.Category
	description := req.Description

	// The AI parser for file upload and batch creation. Resolved by the
	// controller from server.batchPoller.parser (may be nil).
	parser := h.batchParser

	// Run async: generate export, build JSONL, submit to OpenAI
	go func() {
		_ = store.UpdateOperationStatus(opID, "running", 10, 100, "Generating export data")

		// Collect books for JSONL
		allBooks, collectErr := ds.CollectAllBooks()
		if collectErr != nil {
			_ = store.UpdateOperationError(opID, collectErr.Error())
			return
		}

		slimBooks := make([]diagnostics.SlimBook, len(allBooks))
		for i, b := range allBooks {
			slimBooks[i] = diagnostics.ToSlimBook(b)
		}

		_ = store.UpdateOperationStatus(opID, "running", 50, 100, "Building batch JSONL")

		jsonlData, buildErr := diagnostics.BuildBatchJSONL(category, description, slimBooks, nil, nil, nil)
		if buildErr != nil {
			_ = store.UpdateOperationError(opID, buildErr.Error())
			return
		}

		// Count request lines
		requestCount := 0
		for _, b := range jsonlData {
			if b == '\n' {
				requestCount++
			}
		}
		if len(jsonlData) > 0 && jsonlData[len(jsonlData)-1] != '\n' {
			requestCount++
		}

		_ = store.UpdateOperationStatus(opID, "running", 70, 100, "Submitting to OpenAI batch API")

		// Upload JSONL and create batch with metadata tagging
		if parser != nil {
			ctx := context.Background()
			buf := bytes.NewBuffer(jsonlData)
			file, uploadErr := parser.UploadBatchFile(ctx, buf)
			if uploadErr != nil {
				_ = store.UpdateOperationError(opID, fmt.Sprintf("upload batch file: %v", uploadErr))
				return
			}

			batchID, createErr := parser.CreateBatchWithMetadata(ctx, file, "diagnostics")
			if createErr != nil {
				_ = store.UpdateOperationError(opID, fmt.Sprintf("create batch: %v", createErr))
				return
			}

			resultJSON, _ := json.Marshal(map[string]interface{}{
				"status":        "submitted",
				"request_count": requestCount,
				"batch_id":      batchID,
			})
			_ = store.UpdateOperationResultData(opID, string(resultJSON))
			_ = store.UpdateOperationStatus(opID, "running", 80, 100, fmt.Sprintf("Batch %s submitted, awaiting completion", batchID))
			slog.Info("diagnostics_ai batch submitted with requests", "batchID", batchID, "requestCount", requestCount)
		} else {
			// Fallback: store JSONL metadata without actual submission
			resultJSON, _ := json.Marshal(map[string]interface{}{
				"status":        "submitted",
				"request_count": requestCount,
				"batch_id":      "pending-" + opID,
			})
			_ = store.UpdateOperationResultData(opID, string(resultJSON))
			_ = store.UpdateOperationStatus(opID, "completed", 100, 100, "Batch data prepared (no AI parser available)")
		}
	}()

	httputil.RespondWithSuccess(c, 202, gin.H{
		"operation_id": opID,
		"status":       "submitted",
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

	op, err := store.GetOperationByID(opID)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	}

	if op.Status != "completed" {
		httputil.RespondWithOK(c, gin.H{
			"operation_id": opID,
			"status":       op.Status,
			"message":      op.Message,
		})
		return
	}

	if op.ResultData == nil || *op.ResultData == "" {
		httputil.RespondWithOK(c, gin.H{
			"status":      "completed",
			"suggestions": []interface{}{},
		})
		return
	}

	var resultData map[string]interface{}
	if err := json.Unmarshal([]byte(*op.ResultData), &resultData); err != nil {
		httputil.RespondWithInternalError(c, "failed to parse result data")
		return
	}

	suggestions := resultData["suggestions"]
	if suggestions == nil {
		suggestions = []interface{}{}
	}
	rawResponses := resultData["raw_responses"]
	if rawResponses == nil {
		rawResponses = []interface{}{}
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

	op, err := store.GetOperationByID(req.OperationID)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", req.OperationID)
		return
	}

	if op.ResultData == nil || *op.ResultData == "" {
		httputil.RespondWithBadRequest(c, "no suggestions available")
		return
	}

	var resultData map[string]interface{}
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
				var fixes map[string]interface{}
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
				var fixes map[string]interface{}
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
	if st, ok := store.(*database.PebbleStore); ok {
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
