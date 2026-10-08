// file: internal/server/handlers/itunes.go
// version: 1.6.1
// guid: d4e5f6a7-b8c9-0123-defa-123456789012
// last-edited: 2026-09-13

package handlers

import (
	"errors"
	"fmt"
	stdlog "log/slog"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
)

// ITunesValidateRequest represents a validation request for an iTunes library.
type ITunesValidateRequest struct {
	LibraryPath  string               `json:"library_path" binding:"required"`
	PathMappings []itunes.PathMapping `json:"path_mappings,omitempty"`
}

// ITunesValidateResponse summarizes validation results for an iTunes library.
type ITunesValidateResponse struct {
	TotalTracks     int      `json:"total_tracks"`
	AudiobookTracks int      `json:"audiobook_tracks"`
	AudiobookCount  int      `json:"audiobook_count"`
	FilesFound      int      `json:"files_found"`
	FilesMissing    int      `json:"files_missing"`
	MissingPaths    []string `json:"missing_paths,omitempty"`
	PathPrefixes    []string `json:"path_prefixes,omitempty"`
	DuplicateCount  int      `json:"duplicate_count"`
	EstimatedTime   string   `json:"estimated_import_time"`
}

// ITunesImportRequest represents a request to import an iTunes library.
type ITunesImportRequest struct {
	LibraryPath      string               `json:"library_path" binding:"required"`
	ImportMode       string               `json:"import_mode" binding:"required,oneof=organized import organize"`
	PreserveLocation bool                 `json:"preserve_location"`
	ImportPlaylists  bool                 `json:"import_playlists"`
	SkipDuplicates   bool                 `json:"skip_duplicates"`
	FetchMetadata    bool                 `json:"fetch_metadata"`
	PathMappings     []itunes.PathMapping `json:"path_mappings,omitempty"`
}

// ITunesImportResponse acknowledges an iTunes import operation.
type ITunesImportResponse struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

// ITunesBookMapping is one book with an iTunes persistent ID, as listed by
// GET /itunes/books.
type ITunesBookMapping struct {
	BookID             string `json:"book_id"`
	Title              string `json:"title"`
	Author             string `json:"author"`
	ITunesPersistentID string `json:"itunes_persistent_id"`
	LocalPath          string `json:"local_path"`
}

// ITunesImportStatusResponse is returned by GET /itunes/import/:id.
type ITunesImportStatusResponse struct {
	OperationID string   `json:"operation_id"`
	Status      string   `json:"status"`
	Progress    int      `json:"progress"`
	Message     string   `json:"message"`
	TotalBooks  int      `json:"total_books"`
	Processed   int      `json:"processed"`
	Imported    int      `json:"imported"`
	Skipped     int      `json:"skipped"`
	Failed      int      `json:"failed"`
	Errors      []string `json:"errors,omitempty"`
}

// ITunesTestMappingRequest tests a single path mapping against the library.
type ITunesTestMappingRequest struct {
	LibraryPath string `json:"library_path" binding:"required"`
	From        string `json:"from" binding:"required"`
	To          string `json:"to" binding:"required"`
}

// ITunesTestMappingResponse returns sample results from testing a mapping.
type ITunesTestMappingResponse struct {
	Tested   int                 `json:"tested"`
	Found    int                 `json:"found"`
	Examples []ITunesTestExample `json:"examples"`
}

// ITunesTestExample is a single found file example.
type ITunesTestExample struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

// ITunesSyncRequest is the wire type for POST /itunes/sync.
type ITunesSyncRequest struct {
	LibraryPath  string               `json:"library_path,omitempty"`
	PathMappings []itunes.PathMapping `json:"path_mappings,omitempty"`
	Force        bool                 `json:"force,omitempty"`
}

// ITunesSyncResponse acknowledges a sync operation.
type ITunesSyncResponse struct {
	OperationID string `json:"operation_id"`
	Message     string `json:"message"`
}

// --- enqueue param wrappers ---
//
// These mirror the unexported server-package types of the same shape
// (server.itunesImportOpParams / server.itunesSyncOpParams). EnqueueOp
// json.Marshals params immediately, and the op executors in package server
// json.Unmarshal them back into their own copies — so the wire shape (JSON
// tags) must stay byte-identical to the server-side definitions, even though
// the Go types live in two packages.

type itunesImportOpParams struct {
	LegacyOpID string                      `json:"legacy_op_id"`
	Request    itunesservice.ImportRequest `json:"request"`
}

type itunesSyncOpParams struct {
	LegacyOpID   string               `json:"legacy_op_id"`
	LibraryPath  string               `json:"library_path"`
	PathMappings []itunes.PathMapping `json:"path_mappings"`
}

// --- narrow dependency interfaces ---

// ITunesService is the narrow interface ITunesHandler requires from the iTunes
// service for enable/disable gating. Only Enabled() is used directly on the
// service value; the import-pipeline methods are split into ITunesImporter
// because they live on the service's *Importer field (which an interface
// cannot express as field access).
type ITunesService interface {
	Enabled() bool
}

// ITunesImporter is the narrow interface ITunesHandler requires from the iTunes
// service's import pipeline (the *itunesservice.Importer reachable via
// Service.Importer). It is a separate constructor argument because Service
// exposes Importer as an exported field, not a method, so it cannot be reached
// through the ITunesService interface.
type ITunesImporter interface {
	GetStatus(opID string) *itunesservice.ImportStatusSnapshot
	GetStatusBulk(ids []string) map[string]*itunesservice.ImportStatusSnapshot
	DiscoverLibraryPath() string
}

// ITunesStore is the narrow database interface ITunesHandler requires. It lists
// only the database.Store methods the 12 iTunes handlers actually call.
type ITunesStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetAuthorByID(id int) (*database.Author, error)
	SearchBooks(query string, limit, offset int) ([]database.Book, error)
	ListBooksByITunesPID(limit, offset int) ([]database.Book, error)
	// GetOperationV2 backs the import-status endpoints. iTunes ops are
	// v2-native: the handler no longer creates a v1 operations row, so there
	// is no CreateOperation/GetOperationByID here to reach one.
	GetOperationV2(id string) (*database.OperationV2Row, error)
	GetLibraryFingerprint(path string) (*database.LibraryFingerprintRecord, error)
}

// ITunesHandler handles the iTunes HTTP endpoints: validate, test-mapping,
// import (+ status), write-back (+ all/preview), library-status, sync,
// library-stats, and listing iTunes-linked books. All business logic lives in
// internal/itunes/service; this layer is request/response translation plus the
// enabled/disabled and database-initialized guards.
//
// The registry parameter reuses the package-level OperationsRegistry interface
// (declared in operations_v2.go); the 12 handlers only call EnqueueOp, which is
// a subset of that interface, and *opsregistry.Registry already satisfies it.
type ITunesHandler struct {
	svc      ITunesService
	importer ITunesImporter
	registry OperationsRegistry
	store    ITunesStore
}

// NewITunesHandler constructs an ITunesHandler.
//
// NOTE: this constructor takes a 4th argument (importer) beyond the
// 3-argument shape suggested in the task spec. The iTunes service exposes its
// import pipeline as an exported *Importer FIELD (Service.Importer), not a
// method, so an interface cannot reach it via ITunesService. Splitting it into
// a dedicated ITunesImporter parameter keeps the handler fully mockable without
// touching the service package or polluting *Service with proxy methods. The
// caller (wire_handlers.go) must guard against a nil *itunesservice.Service so
// it does not box a typed-nil into the interfaces (which would defeat the
// itunesEnabledOrError nil check).
func NewITunesHandler(svc ITunesService, importer ITunesImporter, registry OperationsRegistry, store ITunesStore) *ITunesHandler {
	return &ITunesHandler{svc: svc, importer: importer, registry: registry, store: store}
}

// itunesEnabledOrError returns false and sends a 503 error when the iTunes
// service is nil or disabled. Callers should return immediately on false.
func (h *ITunesHandler) itunesEnabledOrError(c *gin.Context) bool {
	if h.svc == nil || !h.svc.Enabled() {
		httputil.RespondWithServiceUnavailable(c, itunesservice.ErrITunesDisabled.Error())
		return false
	}
	return true
}

// --- handlers ---

// Validate validates an iTunes library without importing.
func (h *ITunesHandler) Validate(c *gin.Context) {
	var req ITunesValidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	svcMappings := make([]itunesservice.PathMapping, len(req.PathMappings))
	for i, m := range req.PathMappings {
		svcMappings[i] = itunesservice.PathMapping{From: m.From, To: m.To}
	}

	resp, err := itunesservice.Validate(itunesservice.ValidateRequest{
		LibraryPath:  req.LibraryPath,
		PathMappings: svcMappings,
	})
	if err != nil {
		if errors.Is(err, itunesservice.ErrLibraryNotFound) {
			httputil.RespondWithBadRequest(c, err.Error())
		} else {
			httputil.InternalError(c, "validation failed", err)
		}
		return
	}

	httputil.RespondWithOK(c, ITunesValidateResponse{
		TotalTracks:     resp.TotalTracks,
		AudiobookTracks: resp.AudiobookTracks,
		AudiobookCount:  resp.AudiobookCount,
		FilesFound:      resp.FilesFound,
		FilesMissing:    resp.FilesMissing,
		MissingPaths:    resp.MissingPaths,
		PathPrefixes:    resp.PathPrefixes,
		DuplicateCount:  resp.DuplicateCount,
		EstimatedTime:   resp.EstimatedTime,
	})
}

// TestMapping tests a single path mapping against a few tracks.
func (h *ITunesHandler) TestMapping(c *gin.Context) {
	var req ITunesTestMappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	resp, err := itunesservice.TestMapping(itunesservice.TestMappingRequest{
		LibraryPath: req.LibraryPath,
		From:        req.From,
		To:          req.To,
	})
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	examples := make([]ITunesTestExample, len(resp.Examples))
	for i, e := range resp.Examples {
		examples[i] = ITunesTestExample{Title: e.Title, Path: e.Path}
	}
	httputil.RespondWithOK(c, ITunesTestMappingResponse{
		Tested:   resp.Tested,
		Found:    resp.Found,
		Examples: examples,
	})
}

// Import starts an asynchronous iTunes library import operation.
func (h *ITunesHandler) Import(c *gin.Context) {
	if !h.itunesEnabledOrError(c) {
		return
	}
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	var req ITunesImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	cleanLibPath, err := pathvalidation.CleanAbsolutePath(req.LibraryPath)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid library_path: "+err.Error())
		return
	}
	if _, err := os.Stat(cleanLibPath); os.IsNotExist(err) {
		httputil.RespondWithBadRequest(c, "iTunes library file not found")
		return
	}

	svcMappings := make([]itunesservice.PathMapping, len(req.PathMappings))
	for i, m := range req.PathMappings {
		svcMappings[i] = itunesservice.PathMapping{From: m.From, To: m.To}
	}
	svcReq := itunesservice.ImportRequest{
		LibraryPath:      cleanLibPath,
		ImportMode:       req.ImportMode,
		PreserveLocation: req.PreserveLocation,
		ImportPlaylists:  req.ImportPlaylists,
		SkipDuplicates:   req.SkipDuplicates,
		FetchMetadata:    req.FetchMetadata,
		PathMappings:     svcMappings,
	}

	params := itunesImportOpParams{Request: svcReq}
	opID, enqErr := h.registry.EnqueueOp(c.Request.Context(), "itunes.import", params)
	if enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, ITunesImportResponse{
		OperationID: opID,
		Status:      "queued",
		Message:     "iTunes import operation queued",
	})
}

// itunesSearchOverfetchWindow bounds the SearchBooks over-fetch used by
// ListBooks' search path below. SearchBooks has no iTunes-PID filter, so
// the PID narrowing has to happen in Go after the substring search runs —
// a small limit could return zero PID-tagged results even when matches
// exist further down the scan. Mirrors the searchPostFilterWindow
// precedent (internal/audiobooks/service_query.go) for this exact
// over-fetch-then-post-filter shape: bound the fetch instead of leaving it
// unlimited. When a search fills the window, rows past it were never
// scanned, so ListBooks logs a warning AND returns "truncated": true with
// "count" reporting only the iTunes-tagged matches inside the window (a
// lower bound on the real total). The UI reads the flag to tell the user
// to refine the search instead of presenting that count as exact.
const itunesSearchOverfetchWindow = 10000

// ListBooks returns paginated books that have iTunes persistent IDs.
//
// Response data: {"items": [...], "count": N, "truncated": true?}. "count"
// is the number of iTunes-tagged books the handler saw; it is exact unless
// "truncated" is present, in which case the search filled
// itunesSearchOverfetchWindow and "count" is a lower bound. "truncated" is
// omitted when false and is only ever set on the search path — the
// no-search path reads the full PID index and is never truncated.
func (h *ITunesHandler) ListBooks(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	p := httputil.ParsePaginationParams(c)
	search := p.Search
	limit, offset := p.Limit, p.Offset

	var filtered []database.Book
	truncated := false
	if search != "" {
		// Search path still needs to scan the search results then filter,
		// since SearchBooks doesn't have an iTunes-PID filter.
		allBooks, err := h.store.SearchBooks(search, itunesSearchOverfetchWindow, 0)
		if err != nil {
			httputil.InternalError(c, "failed to list books", err)
			return
		}
		if len(allBooks) >= itunesSearchOverfetchWindow {
			// Truncated: rows past the window were never scanned, so the
			// PID-tagged results below are a lower bound, not a complete
			// set. Flag it in the response (and the log) rather than
			// reporting the count as exact.
			truncated = true
			stdlog.Warn("itunes ListBooks: search over-fetch window exhausted; iTunes-tagged results may be a lower bound",
				"query", logger.SanitizeLogValue(search), "window", itunesSearchOverfetchWindow)
		}
		for _, book := range allBooks {
			if book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
				filtered = append(filtered, book)
			}
		}
	} else {
		// Pushdown: memdb itunes_persistent_id index returns only books
		// with a non-empty PID, O(matches) instead of O(50K).
		var err error
		filtered, err = h.store.ListBooksByITunesPID(0, 0)
		if err != nil {
			httputil.InternalError(c, "failed to list books", err)
			return
		}
	}

	total := len(filtered)

	if offset >= len(filtered) {
		filtered = nil
	} else {
		end := min(offset+limit, len(filtered))
		filtered = filtered[offset:end]
	}

	items := make([]ITunesBookMapping, 0, len(filtered))
	for _, book := range filtered {
		author := ""
		if book.AuthorID != nil {
			if a, aErr := h.store.GetAuthorByID(*book.AuthorID); aErr == nil && a != nil {
				author = a.Name
			}
		}
		items = append(items, ITunesBookMapping{
			BookID:             book.ID,
			Title:              book.Title,
			Author:             author,
			ITunesPersistentID: *book.ITunesPersistentID,
			LocalPath:          book.FilePath,
		})
	}

	resp := gin.H{
		"items": items,
		"count": total,
	}
	if truncated {
		resp["truncated"] = true
	}
	httputil.RespondWithOK(c, resp)
}

// ImportStatus returns the status of an iTunes import operation.
func (h *ITunesHandler) ImportStatus(c *gin.Context) {
	if !h.itunesEnabledOrError(c) {
		return
	}
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	opID := c.Param("id")
	op, err := h.store.GetOperationV2(opID)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	}

	progress := calculatePercent(op.ProgressCurrent, op.ProgressTotal)
	snapshot := h.importer.GetStatus(op.ID)

	httputil.RespondWithOK(c, ITunesImportStatusResponse{
		OperationID: op.ID,
		Status:      op.Status,
		Progress:    progress,
		Message:     op.ProgressMessage,
		TotalBooks:  snapshot.Total,
		Processed:   snapshot.Processed,
		Imported:    snapshot.Imported,
		Skipped:     snapshot.Skipped,
		Failed:      snapshot.Failed,
		Errors:      snapshot.Errors,
	})
}

// ImportStatusBulk returns the status of multiple iTunes import operations.
func (h *ITunesHandler) ImportStatusBulk(c *gin.Context) {
	if !h.itunesEnabledOrError(c) {
		return
	}
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var req struct {
		IDs []string `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	snapshots := h.importer.GetStatusBulk(req.IDs)

	results := make(map[string]ITunesImportStatusResponse, len(req.IDs))
	for _, opID := range req.IDs {
		op, err := h.store.GetOperationV2(opID)
		if err != nil || op == nil {
			continue
		}
		progress := calculatePercent(op.ProgressCurrent, op.ProgressTotal)
		snapshot := snapshots[opID]
		if snapshot == nil {
			snapshot = &itunesservice.ImportStatusSnapshot{}
		}
		results[opID] = ITunesImportStatusResponse{
			OperationID: op.ID,
			Status:      op.Status,
			Progress:    progress,
			Message:     op.ProgressMessage,
			TotalBooks:  snapshot.Total,
			Processed:   snapshot.Processed,
			Imported:    snapshot.Imported,
			Skipped:     snapshot.Skipped,
			Failed:      snapshot.Failed,
			Errors:      snapshot.Errors,
		}
	}

	httputil.RespondWithOK(c, gin.H{"statuses": results})
}

// LibraryStatus returns the current status of an iTunes library file.
func (h *ITunesHandler) LibraryStatus(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		httputil.RespondWithBadRequest(c, "path query parameter required")
		return
	}
	cleanPath, err := pathvalidation.CleanAbsolutePath(path)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid path: "+err.Error())
		return
	}

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	rec, err := h.store.GetLibraryFingerprint(cleanPath)
	if err != nil {
		httputil.InternalError(c, "failed to get library fingerprint", err)
		return
	}

	stat, statErr := os.Stat(cleanPath)
	fileExists := statErr == nil

	if rec == nil {
		httputil.RespondWithOK(c, gin.H{
			"path":        cleanPath,
			"exists":      fileExists,
			"last_synced": nil,
			"changed":     fileExists,
		})
		return
	}

	changed := false
	if fileExists {
		changed = stat.Size() != rec.Size || !stat.ModTime().Equal(rec.ModTime)
	}

	httputil.RespondWithOK(c, gin.H{
		"path":        cleanPath,
		"exists":      fileExists,
		"last_synced": rec.ModTime,
		"size":        rec.Size,
		"changed":     changed,
	})
}

// Sync triggers an incremental sync from iTunes Library.xml.
func (h *ITunesHandler) Sync(c *gin.Context) {
	if !h.itunesEnabledOrError(c) {
		return
	}
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	var req ITunesSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = ITunesSyncRequest{}
	}

	libraryPath := req.LibraryPath
	if libraryPath != "" {
		cleanLibPath, err := pathvalidation.CleanAbsolutePath(libraryPath)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid library_path: "+err.Error())
			return
		}
		libraryPath = cleanLibPath
	} else {
		libraryPath = config.AppConfig.ITunes.LibraryReadPath
		if libraryPath == "" {
			libraryPath = h.importer.DiscoverLibraryPath()
		}
	}
	if libraryPath == "" {
		httputil.RespondWithBadRequest(c, "no iTunes library path configured or provided")
		return
	}

	if _, err := os.Stat(libraryPath); os.IsNotExist(err) {
		httputil.RespondWithBadRequest(c, "iTunes library file not found")
		return
	}

	if !req.Force {
		if rec, err := h.store.GetLibraryFingerprint(libraryPath); err == nil && rec != nil {
			if info, statErr := os.Stat(libraryPath); statErr == nil {
				if info.Size() == rec.Size && info.ModTime().Equal(rec.ModTime) {
					httputil.RespondWithOK(c, gin.H{"message": "no changes detected — use force:true to sync anyway", "operation_id": ""})
					return
				}
			}
		}
	}

	pathMappings := req.PathMappings
	if len(pathMappings) == 0 {
		for _, m := range config.AppConfig.ITunes.PathMappings {
			pathMappings = append(pathMappings, itunes.PathMapping{From: m.From, To: m.To})
		}
	}

	syncParams := itunesSyncOpParams{LibraryPath: libraryPath, PathMappings: pathMappings}
	opID, enqErr := h.registry.EnqueueOp(c.Request.Context(), "itunes.sync", syncParams)
	if enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, ITunesSyncResponse{
		OperationID: opID,
		Message:     "iTunes sync operation queued",
	})
}

// LibraryStats reads the configured ITL file and reports low-level structural
// counts useful for verifying orphan-cleanup progress: master-track count and
// dangling playlist→track refs (mtph items pointing at TrackIDs not present in
// the master list).
//
// Cheap to call: parses the binary directly with ITL helpers, no full
// library-object materialization.
func (h *ITunesHandler) LibraryStats(c *gin.Context) {
	if !h.itunesEnabledOrError(c) {
		return
	}
	itlPath := config.AppConfig.ITunes.LibraryITLPath
	if itlPath == "" {
		httputil.RespondWithBadRequest(c, "no ITL library path configured")
		return
	}
	if _, err := os.Stat(itlPath); err != nil {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("ITL not accessible: %v", err))
		return
	}

	data, err := os.ReadFile(itlPath)
	if err != nil {
		httputil.InternalError(c, "read ITL", err)
		return
	}
	dec, decErr := itunes.DecryptAndInflateITL(data)
	if decErr != nil {
		httputil.InternalError(c, "decrypt/inflate ITL", decErr)
		return
	}

	masterTIDs := itunes.CollectMasterTrackIDsLE(dec)
	dangling := itunes.FindDanglingMtphRefsLE(dec, masterTIDs)

	httputil.RespondWithOK(c, gin.H{
		"success":        true,
		"itl_path":       itlPath,
		"itl_size_bytes": len(data),
		"master_tracks":  len(masterTIDs),
		"dangling_mtph":  len(dangling),
		"itl_size_mb":    fmt.Sprintf("%.2f", float64(len(data))/(1024*1024)),
	})
}

// calculatePercent returns current/total as a 0–100 percentage, clamped.
func calculatePercent(current, total int) int {
	if total <= 0 {
		return 0
	}
	pct := (current * 100) / total
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}
