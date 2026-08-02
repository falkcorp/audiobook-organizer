// file: internal/server/handlers/itunes.go
// version: 1.3.0
// guid: d4e5f6a7-b8c9-0123-defa-123456789012
// last-edited: 2026-06-16

package handlers

import (
	"errors"
	"fmt"
	stdlog "log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
	"github.com/oklog/ulid/v2"
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

// ITunesWriteBackRequest represents a write-back request for iTunes ITL updates.
type ITunesWriteBackRequest struct {
	LibraryPath  string               `json:"library_path"`
	AudiobookIDs []string             `json:"audiobook_ids"`
	PathMappings []itunes.PathMapping `json:"path_mappings,omitempty"`
}

// ITunesWriteBackResponse reports the result of an ITL write-back.
type ITunesWriteBackResponse struct {
	Success      bool   `json:"success"`
	UpdatedCount int    `json:"updated_count"`
	Message      string `json:"message"`
}

// ITunesBookMapping is a single book-to-iTunes-path mapping used in preview.
//
// Four path columns surface the full picture so users can see exactly what
// is currently in iTunes vs what AO has on disk vs what AO would write back:
//
//   - ITunesPath               — what iTunes currently has, e.g. W:/foo/bar.m4b
//   - ITunesPathTranslated     — local equivalent of ITunesPath after applying
//     forward path mappings (so users can stat it)
//   - AOPath                   — where AO has the file on disk (book.FilePath)
//   - AOITunesTranslatedPath   — what AO will write into the iTunes ITL when
//     write-back runs (ReverseRemapPath of AOPath)
//
// PathDiffers is true iff AOITunesTranslatedPath != ITunesPath — i.e. the
// thing AO wants to write does not match what iTunes already has.
//
// Backwards compatibility: LocalPath is preserved as an alias of AOPath so
// older clients keep working through the migration. Remove once no caller
// reads it.
type ITunesBookMapping struct {
	BookID                 string `json:"book_id"`
	Title                  string `json:"title"`
	Author                 string `json:"author"`
	ITunesPersistentID     string `json:"itunes_persistent_id"`
	ITunesPath             string `json:"itunes_path,omitempty"`
	ITunesPathTranslated   string `json:"itunes_path_translated,omitempty"`
	AOPath                 string `json:"ao_path"`
	AOITunesTranslatedPath string `json:"ao_itunes_translated_path,omitempty"`
	PathDiffers            bool   `json:"path_differs,omitempty"`

	// LocalPath duplicates AOPath for backwards compatibility with the
	// previous response shape. Will be removed once no caller reads it.
	LocalPath string `json:"local_path"`
}

// ITunesWriteBackPreviewRequest is the wire type for POST /itunes/write-back-preview.
//
// LibraryPath is now optional — when empty, the handler uses the configured
// ITunesLibraryReadPath. The dialog used to require the user to type this
// path on every preview, which was confusing because the actual write-back
// always targets the configured ITunesLibraryWritePath (.itl) regardless.
type ITunesWriteBackPreviewRequest struct {
	LibraryPath string   `json:"library_path,omitempty"`
	BookIDs     []string `json:"book_ids,omitempty"`
}

// ITunesWriteBackPreviewResponse is returned by POST /itunes/write-back-preview.
type ITunesWriteBackPreviewResponse struct {
	Items []ITunesBookMapping `json:"items"`
	Total int                 `json:"total"`
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
	CollectITLUpdatesWithBookIDs() ([]itunes.ITLLocationUpdate, []string)
}

// ITunesStore is the narrow database interface ITunesHandler requires. It lists
// only the database.Store methods the 12 iTunes handlers actually call.
type ITunesStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetAuthorByID(id int) (*database.Author, error)
	SearchBooks(query string, limit, offset int) ([]database.Book, error)
	ListBooksByITunesPID(limit, offset int) ([]database.Book, error)
	GetOperationByID(id string) (*database.Operation, error)
	CreateOperation(id, opType string, folderPath *string) (*database.Operation, error)
	GetLibraryFingerprint(path string) (*database.LibraryFingerprintRecord, error)
	MarkITunesSynced(bookIDs []string) (int64, error)
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

	opID := ulid.Make().String()
	op, err := h.store.CreateOperation(opID, "itunes_import", &cleanLibPath)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
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

	params := itunesImportOpParams{LegacyOpID: op.ID, Request: svcReq}
	if _, enqErr := h.registry.EnqueueOp(c.Request.Context(), "itunes.import", params); enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, ITunesImportResponse{
		OperationID: op.ID,
		Status:      "queued",
		Message:     "iTunes import operation queued",
	})
}

// WriteBack updates the iTunes ITL binary with new file paths.
func (h *ITunesHandler) WriteBack(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var req ITunesWriteBackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if !config.AppConfig.ITunes.WriteBackEnabled || config.AppConfig.ITunes.LibraryWritePath == "" {
		httputil.RespondWithBadRequest(c, "ITL write-back is not enabled in config")
		return
	}

	pathMappings := req.PathMappings
	if len(pathMappings) == 0 {
		for _, m := range config.AppConfig.ITunes.PathMappings {
			pathMappings = append(pathMappings, itunes.PathMapping{From: m.From, To: m.To})
		}
	}

	var itlUpdates []itunes.ITLLocationUpdate
	for _, id := range req.AudiobookIDs {
		book, err := h.store.GetBookByID(id)
		if err != nil {
			httputil.RespondWithInternalError(c, fmt.Sprintf("failed to get audiobook %s: %v", id, err))
			return
		}
		if book == nil || book.ITunesPersistentID == nil || *book.ITunesPersistentID == "" {
			continue
		}

		// TASK-006 / SPEC §1b: normalize the remapped path into the canonical
		// WinPath (the LE writer derives the 0x0B URL). Unmappable values are
		// skipped with a WARN + metric, never written raw into 0x0D (CRIT-2).
		itunesPath := itunes.ReverseRemapPath(book.FilePath, pathMappings)
		pair, err := itunes.NewLocationPair(itunesPath)
		if err != nil {
			metrics.RecordITunesLocationUnmappable("invalid_path")
			stdlog.Warn("iTunes update-locations: skipping unmappable location (never written raw — CRIT-2)",
				"pid", *book.ITunesPersistentID, "raw", itunesPath, "error", err.Error())
			continue
		}
		itlUpdates = append(itlUpdates, itunes.ITLLocationUpdate{
			PersistentID: *book.ITunesPersistentID,
			NewLocation:  pair.WinPath,
		})
	}

	if len(itlUpdates) == 0 {
		httputil.RespondWithBadRequest(c, "no audiobooks with iTunes persistent IDs found")
		return
	}

	itlPath := config.AppConfig.ITunes.LibraryWritePath
	itlResult, itlErr := itunes.UpdateITLLocations(itlPath, itlPath+".tmp", itlUpdates)
	if itlErr != nil {
		stdlog.Warn("ITL write-back failed", "err", itlErr)
		httputil.RespondWithInternalError(c, fmt.Sprintf("ITL write-back failed: %v", itlErr))
		return
	}

	if renameErr := itunes.RenameITLFile(itlPath+".tmp", itlPath); renameErr != nil {
		stdlog.Warn("ITL rename failed", "err", renameErr)
		httputil.RespondWithInternalError(c, fmt.Sprintf("ITL rename failed: %v", renameErr))
		return
	}

	stdlog.Info("ITL write-back: updated tracks", "count", itlResult.UpdatedCount)
	httputil.RespondWithOK(c, ITunesWriteBackResponse{
		Success:      true,
		UpdatedCount: itlResult.UpdatedCount,
		Message:      fmt.Sprintf("Successfully updated %d audiobook locations in ITL", itlResult.UpdatedCount),
	})
}

// WriteBackAll writes ALL books with iTunes persistent IDs back to the ITL.
func (h *ITunesHandler) WriteBackAll(c *gin.Context) {
	if !h.itunesEnabledOrError(c) {
		return
	}
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	if !config.AppConfig.ITunes.WriteBackEnabled {
		httputil.RespondWithBadRequest(c, "ITL write-back is not enabled in config")
		return
	}

	itlPath := config.AppConfig.ITunes.LibraryWritePath
	if itlPath == "" {
		httputil.RespondWithBadRequest(c, "no ITL library path configured")
		return
	}

	if _, err := os.Stat(itlPath); os.IsNotExist(err) {
		httputil.RespondWithBadRequest(c, "ITL file not found at configured path")
		return
	}

	if err := itunesservice.CheckITLConflict(itlPath); err != nil {
		httputil.RespondWithConflict(c, err.Error())
		return
	}

	itlUpdates, writtenBookIDs := h.importer.CollectITLUpdatesWithBookIDs()

	if len(itlUpdates) == 0 {
		httputil.RespondWithOK(c, gin.H{
			"success":       true,
			"updated_count": 0,
			"message":       "no books with iTunes persistent IDs found",
		})
		return
	}

	itlResult, itlErr := itunes.UpdateITLLocations(itlPath, itlPath+".tmp", itlUpdates)
	if itlErr != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("ITL write-back failed: %v", itlErr))
		return
	}

	if renameErr := itunes.RenameITLFile(itlPath+".tmp", itlPath); renameErr != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("ITL rename failed: %v", renameErr))
		return
	}

	itunesservice.RecordITLReadTime()
	stdlog.Info("Bulk ITL write-back: updated tracks out of candidates", "updated", itlResult.UpdatedCount, "candidates", len(itlUpdates))

	if n, markErr := h.store.MarkITunesSynced(writtenBookIDs); markErr == nil && n > 0 {
		stdlog.Info("Marked books as iTunes-synced after write-back", "count", n)
	}

	httputil.RespondWithOK(c, gin.H{
		"success":            true,
		"updated_count":      itlResult.UpdatedCount,
		"file_pid_pairs":     len(itlUpdates),
		"primary_book_count": len(writtenBookIDs),
		"message":            fmt.Sprintf("ITL write-back complete: %d ITL chunks updated across %d (file,PID) pairs from %d primary books", itlResult.UpdatedCount, len(itlUpdates), len(writtenBookIDs)),
	})
}

// WriteBackPreview returns a comparison of local paths vs iTunes paths.
func (h *ITunesHandler) WriteBackPreview(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var req ITunesWriteBackPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	// Fall back to the configured read path when the request omits one.
	// The dialog no longer requires users to type the .xml path — they
	// configure it once in Settings and the preview endpoint uses it
	// directly. The actual write-back always targets the configured
	// ITunesLibraryWritePath (.itl) regardless of this read path.
	libraryPath := strings.TrimSpace(req.LibraryPath)
	if libraryPath != "" {
		cleanLibPath, err := pathvalidation.CleanAbsolutePath(libraryPath)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid library_path: "+err.Error())
			return
		}
		libraryPath = cleanLibPath
	} else {
		libraryPath = config.AppConfig.ITunes.LibraryReadPath
	}
	if libraryPath == "" {
		httputil.RespondWithBadRequest(c, "no iTunes library path configured (set ITunesLibraryReadPath in settings)")
		return
	}

	if _, err := os.Stat(libraryPath); os.IsNotExist(err) {
		httputil.RespondWithBadRequest(c, "iTunes library file not found")
		return
	}

	library, err := itunes.ParseLibrary(libraryPath)
	if err != nil {
		httputil.InternalError(c, "failed to parse iTunes library", err)
		return
	}

	itunesLocations := make(map[string]string)
	for _, track := range library.Tracks {
		if track.PersistentID != "" {
			decoded, decErr := itunes.DecodeLocation(track.Location)
			if decErr == nil {
				itunesLocations[track.PersistentID] = decoded
			} else {
				itunesLocations[track.PersistentID] = track.Location
			}
		}
	}

	var books []database.Book
	if len(req.BookIDs) > 0 {
		for _, id := range req.BookIDs {
			book, bErr := h.store.GetBookByID(id)
			if bErr != nil || book == nil {
				continue
			}
			if book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
				books = append(books, *book)
			}
		}
	} else {
		// Pushdown: use the memdb itunes_persistent_id index so we only
		// walk books that actually have a PID, instead of loading all
		// ~50K books and post-filtering.
		var bErr error
		books, bErr = h.store.ListBooksByITunesPID(0, 0)
		if bErr != nil {
			httputil.InternalError(c, "failed to list books", bErr)
			return
		}
	}

	var previewMappings []itunes.PathMapping
	for _, m := range config.AppConfig.ITunes.PathMappings {
		previewMappings = append(previewMappings, itunes.PathMapping{From: m.From, To: m.To})
	}
	// Forward-mapper for translating an iTunes location into its local
	// equivalent. Wraps the existing ImportOptions.RemapPath because that
	// is the canonical forward direction; the receiver pattern is
	// historical and not worth refactoring here.
	forwardOpts := itunes.ImportOptions{PathMappings: previewMappings}

	items := make([]ITunesBookMapping, 0, len(books))
	for _, book := range books {
		persistentID := *book.ITunesPersistentID
		itunesPath := itunesLocations[persistentID]
		author := ""
		if book.AuthorID != nil {
			if a, aErr := h.store.GetAuthorByID(*book.AuthorID); aErr == nil && a != nil {
				author = a.Name
			}
		}
		aoITunesTranslated := itunes.ReverseRemapPath(book.FilePath, previewMappings)
		itunesTranslated := ""
		if itunesPath != "" {
			itunesTranslated = forwardOpts.RemapPath(itunesPath)
		}
		items = append(items, ITunesBookMapping{
			BookID:                 book.ID,
			Title:                  book.Title,
			Author:                 author,
			ITunesPersistentID:     persistentID,
			ITunesPath:             itunesPath,
			ITunesPathTranslated:   itunesTranslated,
			AOPath:                 book.FilePath,
			AOITunesTranslatedPath: aoITunesTranslated,
			PathDiffers:            aoITunesTranslated != itunesPath,
			LocalPath:              book.FilePath,
		})
	}

	httputil.RespondWithOK(c, ITunesWriteBackPreviewResponse{
		Items: items,
		Total: len(items),
	})
}

// ListBooks returns paginated books that have iTunes persistent IDs.
func (h *ITunesHandler) ListBooks(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	p := httputil.ParsePaginationParams(c)
	search := p.Search
	limit, offset := p.Limit, p.Offset

	var filtered []database.Book
	if search != "" {
		// Search path still needs to scan the search results then filter,
		// since SearchBooks doesn't have an iTunes-PID filter.
		allBooks, err := h.store.SearchBooks(search, 0, 0)
		if err != nil {
			httputil.InternalError(c, "failed to list books", err)
			return
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
		end := offset + limit
		if end > len(filtered) {
			end = len(filtered)
		}
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

	httputil.RespondWithOK(c, gin.H{
		"items": items,
		"count": total,
	})
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
	op, err := h.store.GetOperationByID(opID)
	if err != nil || op == nil {
		httputil.RespondWithNotFound(c, "operation", opID)
		return
	}

	progress := calculatePercent(op.Progress, op.Total)
	snapshot := h.importer.GetStatus(op.ID)

	httputil.RespondWithOK(c, ITunesImportStatusResponse{
		OperationID: op.ID,
		Status:      op.Status,
		Progress:    progress,
		Message:     op.Message,
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
		op, err := h.store.GetOperationByID(opID)
		if err != nil || op == nil {
			continue
		}
		progress := calculatePercent(op.Progress, op.Total)
		snapshot := snapshots[opID]
		if snapshot == nil {
			snapshot = &itunesservice.ImportStatusSnapshot{}
		}
		results[opID] = ITunesImportStatusResponse{
			OperationID: op.ID,
			Status:      op.Status,
			Progress:    progress,
			Message:     op.Message,
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

	opID := ulid.Make().String()
	op, err := h.store.CreateOperation(opID, "itunes_sync", &libraryPath)
	if err != nil {
		httputil.InternalError(c, "failed to create operation", err)
		return
	}

	pathMappings := req.PathMappings
	if len(pathMappings) == 0 {
		for _, m := range config.AppConfig.ITunes.PathMappings {
			pathMappings = append(pathMappings, itunes.PathMapping{From: m.From, To: m.To})
		}
	}

	syncParams := itunesSyncOpParams{LegacyOpID: op.ID, LibraryPath: libraryPath, PathMappings: pathMappings}
	if _, enqErr := h.registry.EnqueueOp(c.Request.Context(), "itunes.sync", syncParams); enqErr != nil {
		httputil.InternalError(c, "failed to enqueue operation", enqErr)
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, ITunesSyncResponse{
		OperationID: op.ID,
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
	itlPath := config.AppConfig.ITunes.LibraryWritePath
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
