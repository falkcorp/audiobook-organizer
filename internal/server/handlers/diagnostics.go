// file: internal/server/handlers/diagnostics.go
// version: 1.30.0
// guid: 14e70c44-73ca-456a-bc67-8dc6ba6e5736
// last-edited: 2026-10-26

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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
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

// DiagnosticsExportDefID is the registry id of the diagnostics export
// OperationDef: the only operation whose result DownloadExport serves.
const DiagnosticsExportDefID = "diagnostics.export"

// ExportDownloadCheck decides whether the caller may be served the archive of
// the export row, whose parsed result is res. DownloadExport calls it on the
// very row (and result) it is about to serve, after reading both once, so the
// verdict and the file cannot come from two different reads of the row. It
// returns a non-empty refusal to answer 403 with that text, an error for a
// failure to decide (answered 500: the archive is not served), or ("", nil) to
// allow it.
type ExportDownloadCheck func(c *gin.Context, row *database.OperationV2Row, res diagnostics.ExportResult) (refusal string, err error)

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
	SQLite        *dbHealthSQLite       `json:"sqlite,omitempty"`
	Pebble        *dbHealthPebble       `json:"pebble,omitempty"`
	Embeddings    dbHealthEmbeddings    `json:"embeddings"`
	AiScans       dbHealthAiScans       `json:"ai_scans"`
	MetadataCache dbHealthMetadataCache `json:"metadata_cache"`
	// ProviderAnswerCache is the shared provider-answer cache (pcache:).
	ProviderAnswerCache dbHealthProviderAnswerCache `json:"provider_answer_cache"`
	BookPathPrefixes    []database.BookPathPrefix   `json:"book_path_prefixes,omitempty"`
}

type dbHealthSQLite struct {
	//lint:ignore SA1019 kept for db-health JSON compat (2026-07-12); replaced by a PebbleDB key-count table in a future cleanup
	Tables    []database.SQLiteTableStat `json:"tables"`
	SizeBytes int64                      `json:"size_bytes"`
}

type dbHealthPebble struct {
	KeyCount  int64  `json:"key_count"`
	SizeBytes uint64 `json:"size_bytes"`
	// Estimated: KeyCount is the census TotalKeys (sstable entries minus
	// deletions), not an exact count of live keys.
	Estimated bool `json:"estimated"`
	// ErrorBoundKeys bounds |KeyCount - true on-disk keys| (sum over families).
	ErrorBoundKeys int64 `json:"error_bound_keys"`
}

type dbHealthEmbeddings struct {
	VectorCount int64 `json:"vector_count"`
	SizeBytes   int64 `json:"size_bytes"`
	// Estimated: VectorCount and SizeBytes are the census emb:v: family
	// figures (all entity types), not an exact walk.
	Estimated      bool  `json:"estimated"`
	ErrorBoundKeys int64 `json:"error_bound_keys"`
}

type dbHealthAiScans struct {
	JobCount     int    `json:"job_count"`
	PendingCount int    `json:"pending_count"`
	SizeBytes    uint64 `json:"size_bytes"`
	SizeSource   string `json:"size_source"`
}

type dbHealthMetadataCache struct {
	TotalEntries int64 `json:"total_entries"`
	// Estimated: TotalEntries is the census family estimate.
	Estimated      bool  `json:"estimated"`
	ErrorBoundKeys int64 `json:"error_bound_keys"`
	TTLDays        int   `json:"ttl_days"`
	// ExpiredEntries is -1 (ExpiredComputed false) unless ?deep=true.
	ExpiredEntries  int64 `json:"expired_entries"`
	ExpiredComputed bool  `json:"expired_entries_computed"`
	// ExpiredError says why a ?deep=true count did not complete (timeout,
	// cancelled, read error). Empty otherwise.
	ExpiredError string `json:"expired_entries_error,omitempty"`
	// ExpiredElapsedMS and ExpiredPagesWalked measure the deep walk that
	// produced the count (or failed): wall time and 1000-row pages read. A
	// cached count reports the figures of the walk that filled the cache.
	// Zero (omitted) when no walk ran for this request.
	ExpiredElapsedMS   int64 `json:"expired_entries_elapsed_ms,omitempty"`
	ExpiredPagesWalked int   `json:"expired_entries_pages_walked,omitempty"`
}

// dbHealthProviderAnswerCache is the shared provider-answer cache (pcache:):
// its row count always, and on ?deep=true the walk that decodes every row --
// rows by kind and the expired rows (past TTL, empty while negative caching
// is off, or undecodable: the rows metafetch.pcache-sweep deletes).
type dbHealthProviderAnswerCache struct {
	TotalEntries int64 `json:"total_entries"`
	// Estimated: TotalEntries is the census estimate (sum of the pcache:
	// families).
	Estimated      bool  `json:"estimated"`
	ErrorBoundKeys int64 `json:"error_bound_keys"`
	// The deep walk's figures; zero (omitted) unless ExpiredComputed.
	Questions int64 `json:"questions,omitempty"`
	Products  int64 `json:"products,omitempty"`
	Pointers  int64 `json:"pointers,omitempty"`
	// ExpiredEntries is -1 (ExpiredComputed false) unless ?deep=true.
	ExpiredEntries     int64  `json:"expired_entries"`
	ExpiredComputed    bool   `json:"expired_entries_computed"`
	ExpiredError       string `json:"expired_entries_error,omitempty"`
	ExpiredElapsedMS   int64  `json:"expired_entries_elapsed_ms,omitempty"`
	ExpiredPagesWalked int    `json:"expired_entries_pages_walked,omitempty"`
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

	// ApplySuggestions writes Title / SeriesID from an AI suggestion; the
	// user's field locks on the target book win over the suggestion.
	database.MetadataFieldStateReader

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
	expired        expiredCounter           // ?deep=true expired-row count: single-flight + short cache
	pcache         pcacheCounter            // ?deep=true shared provider-answer cache count

	// downloadCheck decides, per export row, who may be served the archive
	// (WithDownloadCheck). With auth on, DownloadExport refuses every download
	// while it is nil.
	downloadCheck ExportDownloadCheck
	// access reads an operation's free text the way the operation routes serve
	// it (WithOpAccess); nil withholds text that could name a user.
	access *opaccess.Repairs
}

// WithDownloadCheck wires the per-row scope check of DownloadExport. It is
// required when auth is enabled: without it every download is refused (403), so
// an unwired handler cannot serve an archive to a caller whose scope it never
// compared with the export's.
func (h *DiagnosticsHandler) WithDownloadCheck(check ExportDownloadCheck) *DiagnosticsHandler {
	h.downloadCheck = check
	return h
}

// WithOpAccess wires the reader of an operation's free text (its progress
// message, which DownloadExport serves while the export runs). Unwired, such
// text is withheld when it could name a user.
func (h *DiagnosticsHandler) WithOpAccess(a *opaccess.Repairs) *DiagnosticsHandler {
	h.access = a
	return h
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
	//
	// The caller is stamped on the op as its actor: the export runs after this
	// request is gone and narrows its operation rows and log lines to what that
	// account may read (server.diagnosticsExportOptionsFor), re-read from the op
	// row rather than taken from params, which a generic start can set.
	opts := servermiddleware.ActorOptions(c)
	opID, enqErr := h.registry.EnqueueOp(c.Request.Context(), "diagnostics.export", params, opts...)
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
	// Only a diagnostics export answers here. The id in the URL is any
	// operation's, and a status, progress message or result path of another one
	// (a Repairs run the caller may not read, say) is not this route's to serve.
	if op == nil || op.DefID != DiagnosticsExportDefID {
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
			"message":      h.access.For(c).UserText(op.ProgressMessage),
		})
		return
	}

	if op.ResultData == nil || *op.ResultData == "" {
		httputil.RespondWithInternalError(c, "no result data available")
		return
	}

	result, err := diagnostics.ParseExportResult(*op.ResultData)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to parse result data")
		return
	}

	// The scope decision is made here, on the row just read and parsed and no
	// other: a check run earlier (a middleware that read the row itself) can see
	// a different state than the one served, and one that cannot decide must not
	// pass. With auth on, no check wired is a refusal.
	if h.downloadCheck != nil {
		refusal, err := h.downloadCheck(c, op, result)
		if err != nil {
			httputil.InternalError(c, "failed to check who this export was built for", err)
			return
		}
		if refusal != "" {
			httputil.RespondWithForbidden(c, refusal)
			return
		}
	} else if config.AppConfig.EnableAuth {
		httputil.RespondWithForbidden(c, "diagnostics export downloads are not available: no scope check is wired")
		return
	}

	zipPath := result.ZipPath
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
	// Only a diagnostics AI run answers here. The id in the URL is any
	// operation's, and a status, progress message and result of another one
	// (a Repairs run the caller may not read, say) is not this route's to serve.
	if err != nil || op == nil || op.DefID != DiagnosticsAIDefID {
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
	// Only a diagnostics AI run answers here, as GetAIResults does: the id in
	// the body is any operation's, and another one's result (a Repairs run's
	// rows, say) is not a list of suggestions to read or apply.
	if err != nil || op == nil || op.DefID != DiagnosticsAIDefID {
		httputil.RespondWithNotFound(c, "operation", req.OperationID)
		return
	}
	// Suggestions exist only once the run has finished; a queued, running,
	// failed or canceled run has none, and a half-written result must not be
	// applied.
	if op.Status != "completed" {
		httputil.RespondWithConflict(c, "the analysis has not completed")
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

	// Fail-safe cap (internal/applycap). The unit that gets WRITTEN is a book,
	// not a suggestion: merge_versions merges every ID in BookIDs, delete_orphan
	// and the fix actions UpdateBook each one. An earlier draft counted
	// len(ApprovedSuggestionIDs), which let 4,000 three-book merges through a
	// 5,000 cap as 12,000 book writes, and counted IDs that matched no
	// suggestion at all. Count the books behind the approved intersection, and
	// do it here — after decode, before the first MergeBooks/UpdateBook.
	bookWrites := 0
	for _, suggestion := range suggestions {
		if !approvedSet[suggestion.ID] {
			continue
		}
		if n := len(suggestion.BookIDs); n > 0 {
			bookWrites += n
		} else {
			bookWrites++
		}
	}
	if ex := applycap.Refuse("diagnostics/apply-suggestions", bookWrites, config.AppConfig.BulkApplyMaxItems); ex != nil {
		httputil.RespondWithApplyCapExceeded(c, ex)
		return
	}

	ms := h.mergeService
	if ms == nil {
		ms = merge.NewService(store)
	}

	applied := 0
	failed := 0
	skipped := 0
	var errors []string
	var skippedReasons []string
	// merges reports each merge_versions suggestion's outcome as the link
	// endpoints do: the survivor, the flag and user-state holder, and whether
	// Audiobookshelf lists it.
	type mergeOutcome struct {
		SuggestionID string `json:"suggestion_id"`
		merge.FlagOutcome
		// SiblingOperationID: when the merge carried version-group siblings
		// into its group, POST /operations/:id/revert with it puts them back.
		SiblingOperationID string `json:"sibling_operation_id,omitempty"`
	}
	merges := []mergeOutcome{}

	for _, suggestion := range suggestions {
		if !approvedSet[suggestion.ID] {
			continue
		}

		var applyErr error
		var lockedBooks []string
		switch suggestion.Action {
		case "merge_versions":
			if len(suggestion.BookIDs) >= 2 {
				var res *merge.Result
				res, applyErr = ms.MergeBooks(suggestion.BookIDs, suggestion.PrimaryID)
				if applyErr == nil {
					out := mergeOutcome{SuggestionID: suggestion.ID, FlagOutcome: res.FlagOutcome()}
					if res != nil {
						out.SiblingOperationID = res.SiblingOperationID
					}
					merges = append(merges, out)
				}
			}

		case "delete_orphan":
			for _, bookID := range suggestion.BookIDs {
				// ModifyBook sets only the deletion flag on the stored row; the
				// old GetBookByID -> UpdateBook(book) wrote the whole stale row
				// back and reverted any column written meanwhile.
				marked, modErr := store.ModifyBook(bookID, func(b *database.Book) error {
					if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
						return database.ErrSkipBookWrite
					}
					yes := true
					b.MarkedForDeletion = &yes
					return nil
				})
				if modErr != nil {
					applyErr = modErr
					break
				}
				if marked == nil {
					applyErr = fmt.Errorf("book %s not found", bookID)
					break
				}
				// A suggested "orphan" can still be its version group's
				// primary; marking it deleted hands the flag on.
				handOffDeletedPrimary(store, marked)
			}

		case "fix_metadata":
			// Fix field is a JSON string with field updates
			if suggestion.Fix != "" && len(suggestion.BookIDs) > 0 {
				var fixes map[string]any
				if parseErr := json.Unmarshal([]byte(suggestion.Fix), &fixes); parseErr != nil {
					applyErr = fmt.Errorf("invalid fix data: %w", parseErr)
				} else if title, ok := fixes["title"].(string); ok {
					lockedBooks, applyErr = applySuggestionRespectingLocks(store, suggestion.BookIDs, func(b *database.Book) {
						b.Title = title
					})
				}
			}

		case "reassign_series":
			if suggestion.Fix != "" && len(suggestion.BookIDs) > 0 {
				var fixes map[string]any
				if parseErr := json.Unmarshal([]byte(suggestion.Fix), &fixes); parseErr != nil {
					applyErr = fmt.Errorf("invalid fix data: %w", parseErr)
				} else if seriesIDFloat, ok := fixes["series_id"].(float64); ok {
					seriesID := int(seriesIDFloat)
					lockedBooks, applyErr = applySuggestionRespectingLocks(store, suggestion.BookIDs, func(b *database.Book) {
						b.SeriesID = &seriesID
					})
				}
			}

		default:
			applyErr = fmt.Errorf("unknown action: %s", suggestion.Action)
		}

		switch {
		case applyErr != nil:
			failed++
			errors = append(errors, fmt.Sprintf("suggestion %s: %v", suggestion.ID, applyErr))
			slog.Warn("Failed to apply diagnostics suggestion", "suggestion", suggestion.ID, "applyErr", applyErr)
		case len(lockedBooks) > 0:
			// Every book the suggestion named is behind a user lock on the very
			// field it wanted to change: not a failure, not an apply.
			skipped++
			skippedReasons = append(skippedReasons, fmt.Sprintf("suggestion %s: %s", suggestion.ID, strings.Join(lockedBooks, "; ")))
			slog.Info("Diagnostics suggestion skipped: user-locked field", "suggestion", suggestion.ID, "books", lockedBooks)
		default:
			applied++
		}
	}

	httputil.RespondWithOK(c, gin.H{
		"applied":         applied,
		"failed":          failed,
		"errors":          errors,
		"skipped":         skipped,
		"skipped_reasons": skippedReasons,
		"merges":          merges,
	})
}

// applySuggestionRespectingLocks runs mutate on each named book and saves it,
// under the book's field locks (database.FieldLocks.Apply) so a field the user
// locked is never overwritten by an AI suggestion. A book whose locked field
// was the only thing the suggestion changed is not written at all and is
// reported in the returned list as "<bookID> (<locked keys>)". Lock reads fail
// closed: an unreadable lock set is an error for the whole suggestion, and
// nothing is written.
//
// The locks are read INSIDE the ModifyBook callback, under the row's write
// lock, so a lock set while the suggestion is being applied is honoured
// (reading them first left a window in which a just-locked field went
// through). The lock read is a plain Pebble read that takes no book lock.
// mutate runs on the stored row, so only the suggestion's own column is
// written and a column another writer committed meanwhile is kept. Until
// 2026-09-14 this was GetBookByID -> mutate -> UpdateBook, which wrote the
// whole stale row back.
func applySuggestionRespectingLocks(store diagnosticsStore, bookIDs []string, mutate func(*database.Book)) ([]string, error) {
	var locked []string
	for _, bookID := range bookIDs {
		var restored []string
		written, err := store.ModifyBook(bookID, func(b *database.Book) error {
			locks, lerr := database.LoadFieldLocks(store, bookID)
			if lerr != nil {
				return lerr
			}
			restored = locks.Apply(b, mutate)
			if len(restored) > 0 {
				return database.ErrSkipBookWrite
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if written == nil {
			return nil, fmt.Errorf("book %s not found", bookID)
		}
		if len(restored) > 0 {
			locked = append(locked, fmt.Sprintf("%s (%s)", bookID, strings.Join(restored, ", ")))
		}
	}
	return locked, nil
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

	// Main store stats: the estimated census, not a key iteration. The census
	// reads sstable properties only (no key iteration, no value reads), so it
	// is cheap where the old full KeyCount walk took minutes on a
	// production-size store. It is cached for 5 minutes; a cache miss does
	// real work (a bounded seek per family range, and a flush when enough
	// writes are unflushed), so this path is not free, just bounded.
	//
	// The capability is resolved through the decorator chain with
	// database.AsCapability, never a bare assertion: the server's store was wrapped
	// (indexedStore, until 07-S3) and could be again, so a bare assertion fails
	// behind a decorator, and the failure is invisible -- a nil census just drops resp.Pebble
	// from the payload.
	census := h.healthCensus(c.Request.Context())
	if census != nil {
		resp.Pebble = &dbHealthPebble{
			KeyCount:  census.TotalKeys,
			SizeBytes: census.DiskSpaceUsage,
			Estimated: true,
			// The bound is the sum over families; a table shared by two
			// families is bounded in each, so the total is conservative.
			ErrorBoundKeys: censusErrorBound(census),
		}
	}

	// Embedding vectors live in the main DB under emb:v:. Their count comes
	// from the census family (one walk of ~230k keys per request otherwise);
	// a backend without a census keeps the exact count.
	if h.embeddingStore != nil {
		if fam, ok := censusFamily(census, embeddingVectorPrefix); ok {
			resp.Embeddings = dbHealthEmbeddings{
				VectorCount:    fam.Keys,
				SizeBytes:      int64(fam.DiskBytes),
				Estimated:      true,
				ErrorBoundKeys: fam.ErrorBoundKeys,
			}
		} else if ctx := c.Request.Context(); ctx.Err() == nil {
			estats, err := h.embeddingStore.HealthStats()
			if err != nil {
				diagnosticsLog.Warn("db-health embedding stats: %v", err)
			}
			resp.Embeddings = dbHealthEmbeddings{
				VectorCount: estats.VectorCount,
				SizeBytes:   estats.SizeBytes,
			}
		}
	}

	// AI scan store (always PebbleDB, may be nil).
	if h.aiScanStore != nil {
		astats, err := h.aiScanStore.HealthStats()
		if err != nil {
			diagnosticsLog.Warn("db-health ai scan stats: %v", err)
		}
		resp.AiScans = dbHealthAiScans{
			JobCount:     astats.JobCount,
			PendingCount: astats.PendingCount,
			SizeBytes:    astats.SizeBytes,
			SizeSource:   astats.SizeSource,
		}
	}

	// Metadata fetch cache. The entry count comes from the census family when
	// the backend has one, and from a counting scan otherwise. Expired entries
	// need every row decoded, so they are counted only on ?deep=true.
	ttlDays := config.AppConfig.MetadataFetchCacheTTLDays
	mc := dbHealthMetadataCache{TTLDays: ttlDays, ExpiredEntries: -1}
	if fam, ok := censusFamily(census, metadataFetchCachePrefix); ok {
		mc.TotalEntries = fam.Keys
		mc.Estimated = true
		mc.ErrorBoundKeys = fam.ErrorBoundKeys
	} else if c.Request.Context().Err() == nil {
		// Skipped once the client is gone: this fallback walks every row.
		total, err := database.CountCachedMetadataFetches(store)
		if err != nil {
			diagnosticsLog.Warn("db-health metadata cache count: %v", err)
		}
		mc.TotalEntries = total
	}
	if c.Query("deep") == "true" && ttlDays > 0 {
		res, err := h.expired.count(c.Request.Context(), store, ttlDays)
		mc.ExpiredElapsedMS = res.Elapsed.Milliseconds()
		mc.ExpiredPagesWalked = res.Pages
		if err != nil {
			diagnosticsLog.Warn("db-health metadata cache expired count: %v", err)
			mc.ExpiredError = err.Error()
		} else {
			mc.ExpiredEntries = res.Value
			mc.ExpiredComputed = true
		}
	}
	resp.MetadataCache = mc
	resp.ProviderAnswerCache = h.providerAnswerHealth(c, census)

	httputil.RespondWithOK(c, resp)
}

// providerAnswerHealth is db-health's shared provider-answer cache section.
func (h *DiagnosticsHandler) providerAnswerHealth(c *gin.Context, census *database.DBCensus) dbHealthProviderAnswerCache {
	ctx := c.Request.Context()
	pc := dbHealthProviderAnswerCache{ExpiredEntries: -1}
	total, estimated, bound, err := pcacheSize(ctx, census, h.store)
	if err != nil && ctx.Err() == nil {
		diagnosticsLog.Warn("db-health shared cache count: %v", err)
	}
	pc.TotalEntries, pc.Estimated, pc.ErrorBoundKeys = total, estimated, bound
	if c.Query("deep") != "true" {
		return pc
	}
	res, err := h.pcache.count(ctx, h.store)
	pc.ExpiredElapsedMS = res.Elapsed.Milliseconds()
	pc.ExpiredPagesWalked = res.Pages
	if err != nil {
		diagnosticsLog.Warn("db-health shared cache expired count: %v", err)
		pc.ExpiredError = err.Error()
		return pc
	}
	pc.Questions, pc.Products, pc.Pointers = res.Value.Questions, res.Value.Products, res.Value.Pointers
	pc.ExpiredEntries, pc.ExpiredComputed = res.Value.Expired, true
	return pc
}

const (
	metadataFetchCachePrefix = "metadata_fetch_cache:"
	embeddingVectorPrefix    = "emb:v:"
)

// healthCensus returns the cached estimated census for db-health and
// /cache/stats, or nil when the backend keeps none (a non-Pebble store) or the
// census fails; a failure is logged and the caller falls back or omits the
// section.
func (h *DiagnosticsHandler) healthCensus(ctx context.Context) *database.DBCensus {
	return resolveHealthCensus(ctx, h.store)
}

func resolveHealthCensus(ctx context.Context, store any) *database.DBCensus {
	provider, ok := database.AsCapability[database.DBCensusProvider](store)
	if !ok {
		return nil
	}
	census, err := provider.DBCensus(ctx, database.CensusOptions{})
	if err != nil {
		diagnosticsLog.Warn("health census: %v", err)
		return nil
	}
	return census
}

// censusFamily returns the census family with the given prefix.
func censusFamily(census *database.DBCensus, prefix string) (database.FamilyCensus, bool) {
	if census == nil {
		return database.FamilyCensus{}, false
	}
	for _, f := range census.Families {
		if f.Prefix == prefix {
			return f, true
		}
	}
	return database.FamilyCensus{}, false
}

// censusErrorBound sums the per-family error bounds of an estimated census.
func censusErrorBound(census *database.DBCensus) int64 {
	var n int64
	for _, f := range census.Families {
		n += f.ErrorBoundKeys
	}
	return n
}

const (
	// expiredCountTimeout bounds one deep walk, which runs detached from the
	// caller that started it (a disconnect must not waste a half-done walk
	// the other waiters share). The walk's elapsed time and page count are
	// logged and returned, so whether this ceiling is right for the
	// production cache can be read off real runs.
	expiredCountTimeout = 2 * time.Minute
	// expiredCountCacheTTL is how long a finished deep count is served again.
	expiredCountCacheTTL = 60 * time.Second
	// expiredCountPageSize is the number of cache rows read per page.
	expiredCountPageSize = 1000
)

// deepWalkResult is one deep walk's value and the measurements of the walk
// behind it. Pages and Elapsed are filled on failure too, so a timeout
// reports how far the walk got.
type deepWalkResult[R any] struct {
	Value   R
	Pages   int
	Elapsed time.Duration
}

// expiredCountResult is one deep expired-row count of the fetch cache.
type expiredCountResult = deepWalkResult[int64]

// expiredCountTimeoutError is a deep walk hitting its ceiling. It reads
// "timed out after 2m, N pages walked" rather than the bare "context deadline
// exceeded", and still matches context.DeadlineExceeded.
type expiredCountTimeoutError struct {
	limit time.Duration
	pages int
}

func (e *expiredCountTimeoutError) Error() string {
	return fmt.Sprintf("timed out after %s, %d pages walked", shortDuration(e.limit), e.pages)
}

func (e *expiredCountTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// shortDuration formats d without trailing zero units ("2m", not "2m0s").
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// deepWalker runs a whole-family walk that decodes every row (the ?deep=true
// counts). Concurrent callers asking the same key share one walk (N clicks
// must not cost N walks), the walk is bounded by expiredCountTimeout
// independent of any caller, and a finished result is served for
// expiredCountCacheTTL while the key is unchanged.
type deepWalker[R any] struct {
	flight singleflight.Group
	// timeout overrides expiredCountTimeout when non-zero (tests).
	timeout time.Duration
	mu      sync.Mutex
	last    struct {
		key    string
		result deepWalkResult[R]
		at     time.Time
		valid  bool
	}
}

// run returns the cached result for key, or runs walk (shared by every
// concurrent caller of key) and caches it. name labels the log lines.
func (e *deepWalker[R]) run(ctx context.Context, name, key string, walk func(context.Context) (R, int, error)) (deepWalkResult[R], error) {
	e.mu.Lock()
	if e.last.valid && e.last.key == key && time.Since(e.last.at) < expiredCountCacheTTL {
		res := e.last.result
		e.mu.Unlock()
		return res, nil
	}
	e.mu.Unlock()

	limit := e.timeout
	if limit <= 0 {
		limit = expiredCountTimeout
	}
	ch := e.flight.DoChan(key, func() (_ any, ferr error) {
		// This recover catches every panic, unlike AIScanStore.HealthStats,
		// which recovers only pebble.ErrClosed and re-raises the rest: a panic
		// here would be re-raised by DoChan on a fresh goroutine, where no
		// recover can reach it, and kill the process.
		defer func() {
			if rec := recover(); rec != nil {
				diagnosticsLog.Error("%s panicked: %v\n%s", name, rec, debug.Stack())
				ferr = fmt.Errorf("%s panicked: %v", name, rec)
			}
		}()
		// start precedes the deadline, so Elapsed covers the whole bounded
		// window and a timed-out walk always reports at least limit.
		start := time.Now()
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
		defer cancel()
		v, pages, err := walk(fctx)
		res := deepWalkResult[R]{Value: v, Pages: pages, Elapsed: time.Since(start)}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				err = &expiredCountTimeoutError{limit: limit, pages: pages}
			}
			diagnosticsLog.Warn("%s failed after %s, %d pages walked: %v", name, res.Elapsed, pages, err)
			return res, err
		}
		diagnosticsLog.Info("%s: %v, %d pages walked in %s", name, v, pages, res.Elapsed)
		e.mu.Lock()
		e.last.key, e.last.result, e.last.at, e.last.valid = key, res, time.Now(), true
		e.mu.Unlock()
		return res, nil
	})
	select {
	case r := <-ch:
		res, _ := r.Val.(deepWalkResult[R])
		return res, r.Err
	case <-ctx.Done():
		return deepWalkResult[R]{}, ctx.Err()
	}
}

// expiredCounter runs the fetch cache's deep expired-row count of ?deep=true.
type expiredCounter struct {
	deepWalker[int64]
}

func (e *expiredCounter) count(ctx context.Context, store database.RawKVStore, ttlDays int) (expiredCountResult, error) {
	return e.run(ctx, "expired count", strconv.Itoa(ttlDays), func(fctx context.Context) (int64, int, error) {
		start := time.Now()
		return countExpiredMetadataFetches(fctx, store, start.Add(-time.Duration(ttlDays)*24*time.Hour))
	})
}

// countExpiredMetadataFetches counts fetch-cache rows cached before cutoff and
// returns the number of pages read alongside, on failure too. It pages through
// the family expiredCountPageSize rows at a time and decodes only cached_at,
// so memory stays bounded however many rows there are. ctx is checked between
// pages.
func countExpiredMetadataFetches(ctx context.Context, store database.RawKVStore, cutoff time.Time) (expired int64, pages int, err error) {
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return 0, pages, err
		}
		pairs, next, err := store.ScanPrefixPage(metadataFetchCachePrefix, after, expiredCountPageSize)
		if err != nil {
			return 0, pages, err
		}
		pages++
		for _, kv := range pairs {
			var entry struct {
				CachedAt time.Time `json:"cached_at"`
			}
			if json.Unmarshal(kv.Value, &entry) == nil && entry.CachedAt.Before(cutoff) {
				expired++
			}
		}
		if next == "" {
			return expired, pages, nil
		}
		after = next
	}
}

// GetDBCensus reports the main Pebble store's per-key-family census.
// GET /api/v1/diagnostics/db-census[?fresh=true]
//
// The response is the cheap estimated census — sstable properties apportioned
// by span bytes plus one bounded seek per family range; it reads no values and
// iterates no family — together with last_exact, the last census written by
// the maintenance.db-census-exact op (exact per-family counts and the
// book_ver: history distribution, with its generated_at), and
// exact_in_progress while a run is unfinished. The endpoint never runs the
// exact pass itself. fresh=true bypasses the 5-minute cache of the estimated
// part. The computation is shared by concurrent callers and does not stop
// when one of them disconnects.
func (h *DiagnosticsHandler) GetDBCensus(c *gin.Context) {
	fresh := c.Query("fresh") == "true"
	provider, ok := database.AsCapability[database.DBCensusProvider](h.store)
	if !ok {
		httputil.RespondWithInternalError(c, "db census requires the Pebble store")
		return
	}
	census, err := provider.DBCensus(c.Request.Context(), database.CensusOptions{Fresh: fresh})
	if err != nil {
		diagnosticsLog.Warn("db-census failed (fresh=%t): %v", fresh, err)
		httputil.RespondWithInternalError(c, "db census failed: "+err.Error())
		return
	}
	httputil.RespondWithOK(c, census)
}

var diagnosticsLog = logger.New("handlers.diagnostics")

// handOffDeletedPrimary runs versionprimary.EnsureSinglePrimary on the group
// of a book diagnostics just marked deleted, so a group it was primary of
// ends with one live eligible primary (or is held for
// version-group-primary-repair). Best-effort: the mark has committed.
func handOffDeletedPrimary(store versionprimary.EnsureStore, b *database.Book) {
	if b == nil || b.VersionGroupID == nil || *b.VersionGroupID == "" {
		return
	}
	gid := *b.VersionGroupID
	if _, err := versionprimary.EnsureSinglePrimary(context.Background(), store, gid,
		versionprimary.Env{RootDir: config.AppConfig.RootDir}); err != nil {
		diagnosticsLog.Warn("diagnostics: primary hand-off in version group %s after deleting %s failed: %v",
			logger.SanitizeLogValue(gid), logger.SanitizeLogValue(b.ID), err)
	}
}
