// file: internal/server/handlers/filesystem.go
// version: 1.5.0
// guid: c4d5e6f7-a8b9-0123-cdef-012345678901
// last-edited: 2026-08-25

// Package handlers — FilesystemHandler covers home-directory, filesystem
// browse, exclusion CRUD, import-path CRUD, and the on-demand single-file
// import HTTP endpoints.

package handlers

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
	"github.com/gin-gonic/gin"
)

// -----------------------------------------------------------------------
// Narrow interfaces
// -----------------------------------------------------------------------

// FilesystemBrowser is the narrow interface for directory browsing and
// path exclusion management.
type FilesystemBrowser interface {
	BrowseDirectory(ctx context.Context, path string) (*fileops.BrowseResult, error)
	CreateExclusion(ctx context.Context, path string) error
	RemoveExclusion(ctx context.Context, path string) error
}

// ImportPathCreator is the narrow interface for creating import paths.
type ImportPathCreator interface {
	CreateImportPath(path, name string) (*database.ImportPath, error)
}

// FileImporter is the narrow interface for importing a single file.
type FileImporter interface {
	ImportFile(req *importer.ImportFileRequest) (*importer.ImportFileResponse, error)
}

// FilesystemStore is the narrow database interface required by FilesystemHandler.
// CreateOperation is deliberately ABSENT. AddImportPath used to mint a v1
// operations row here and hand the caller its id, which the client then polled
// against /operations/v2 — where it did not exist. Leaving the method off this
// interface makes reintroducing that a compile error rather than something a
// reviewer has to notice.
type FilesystemStore interface {
	GetAllImportPaths() ([]database.ImportPath, error)
	GetDashboardStats() (*database.DashboardStats, error)
	CountBooksByPathPrefix(prefix string) (int, error)
	UpdateImportPath(id int, path *database.ImportPath) error
	DeleteImportPath(id int) error
	GetBookByFilePath(path string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// -----------------------------------------------------------------------
// Handler
// -----------------------------------------------------------------------

// FilesystemHandler handles filesystem-browse, exclusion CRUD,
// import-path CRUD, and on-demand file-import HTTP endpoints.
//
// opEnqueuer reuses SplitBookOpEnqueuer from split_book.go — both
// are in the same package, and the op-registry signature is identical.
type FilesystemHandler struct {
	store        FilesystemStore
	browser      FilesystemBrowser
	pathCreator  ImportPathCreator
	fileImporter FileImporter
	opEnqueuer   SplitBookOpEnqueuer // may be nil
	publisher    EventPublisher
	rootDir      string
	autoOrganize bool
}

// NewFilesystemHandler constructs a FilesystemHandler.
// opEnqueuer may be nil; the handler falls back to a synchronous scan.
func NewFilesystemHandler(
	store FilesystemStore,
	browser FilesystemBrowser,
	pathCreator ImportPathCreator,
	fileImporter FileImporter,
	opEnqueuer SplitBookOpEnqueuer,
	publisher EventPublisher,
	rootDir string,
	autoOrganize bool,
) *FilesystemHandler {
	return &FilesystemHandler{
		store:        store,
		browser:      browser,
		pathCreator:  pathCreator,
		fileImporter: fileImporter,
		opEnqueuer:   opEnqueuer,
		publisher:    publisher,
		rootDir:      rootDir,
		autoOrganize: autoOrganize,
	}
}

// -----------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------

// GetHomeDirectory handles GET /api/v1/filesystem/home.
// Returns the server user's home directory path.
func (h *FilesystemHandler) GetHomeDirectory(c *gin.Context) {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		httputil.RespondWithInternalError(c, "failed to determine home directory")
		return
	}
	httputil.RespondWithOK(c, gin.H{"path": homeDir})
}

// BrowseFilesystem handles GET /api/v1/filesystem/browse.
func (h *FilesystemHandler) BrowseFilesystem(c *gin.Context) {
	path := c.Query("path")
	result, err := h.browser.BrowseDirectory(c.Request.Context(), path)
	if err != nil {
		if errors.Is(err, fileops.ErrPathNotAllowed) {
			httputil.RespondWithForbidden(c, err.Error())
			return
		}
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	httputil.RespondWithOK(c, result)
}

// CreateExclusion handles POST /api/v1/filesystem/exclusions.
func (h *FilesystemHandler) CreateExclusion(c *gin.Context) {
	var req struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if err := h.browser.CreateExclusion(c.Request.Context(), req.Path); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	httputil.RespondWithCreated(c, gin.H{"message": "exclusion created"})
}

// RemoveExclusion handles DELETE /api/v1/filesystem/exclusions.
func (h *FilesystemHandler) RemoveExclusion(c *gin.Context) {
	var req struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if err := h.browser.RemoveExclusion(c.Request.Context(), req.Path); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	httputil.RespondWithNoContent(c)
}

// ListImportPaths handles GET /api/v1/import-paths.
func (h *FilesystemHandler) ListImportPaths(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	folders, err := h.store.GetAllImportPaths()
	if err != nil {
		httputil.InternalError(c, "failed to list import paths", err)
		return
	}

	if folders == nil {
		folders = []database.ImportPath{}
	}

	// Refresh BookCount with the live value from the cached LibraryStats.
	// Falls back to per-folder CountBooksByPathPrefix when the cache isn't
	// available — e.g., before first warmup.
	if len(folders) > 0 {
		if stats, serr := h.store.GetDashboardStats(); serr == nil && stats != nil && stats.BooksByImportPath != nil {
			for i := range folders {
				if n, ok := stats.BooksByImportPath[folders[i].ID]; ok {
					folders[i].BookCount = n
				}
			}
		} else {
			for i := range folders {
				if cnt, cerr := h.store.CountBooksByPathPrefix(folders[i].Path); cerr == nil {
					folders[i].BookCount = cnt
				}
			}
		}
	}

	httputil.RespondWithOK(c, gin.H{"importPaths": folders, "count": len(folders)})
}

// AddImportPath handles POST /api/v1/import-paths.
func (h *FilesystemHandler) AddImportPath(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	var req struct {
		Path    string `json:"path" binding:"required"`
		Name    string `json:"name" binding:"required"`
		Enabled *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	// Reject traversal sequences and require an absolute path before the value
	// reaches the store and the directory scanner (go/path-injection). Import
	// roots are intentionally arbitrary absolute directories, so we normalize
	// rather than confine to a fixed root; CleanAbsolutePath's return value is
	// the sanitized path used downstream.
	cleanPath, err := pathvalidation.CleanAbsolutePath(req.Path)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	createdPath, err := h.pathCreator.CreateImportPath(cleanPath, req.Name)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	folder := createdPath
	if req.Enabled != nil && !*req.Enabled {
		folder.Enabled = false
		if err := h.store.UpdateImportPath(folder.ID, folder); err != nil {
			httputil.RespondWithCreated(c, gin.H{"importPath": folder, "warning": "created but could not update enabled flag"})
			return
		}
	}

	// Auto-scan via the v2 op registry when available.
	if folder.Enabled && h.opEnqueuer != nil {
		// Return the id EnqueueOp minted. The client polls this through
		// api.getOperationStatus, which resolves against /operations/v2 only, so
		// the separately-minted v1 id this used to hand back could never complete
		// the poll — the folder list never refreshed its book count.
		folderPath := folder.Path
		params := folderAutoScanParams{
			FolderPath: folderPath,
			FolderID:   folder.ID,
		}
		opID, enqErr := h.opEnqueuer.EnqueueOp(c.Request.Context(), "library.folder-auto-scan", params)
		if enqErr == nil {
			httputil.RespondWithCreated(c, gin.H{"importPath": folder, "scan_operation_id": opID})
			return
		}
		// The folder WAS created, so this still answers 201 — but the scan the
		// caller asked for is not running and no id is advertised for it. That
		// used to be swallowed silently on the one path whose whole purpose is
		// starting a scan; the synchronous fallback below cannot cover it, being
		// gated on opEnqueuer being nil rather than on the enqueue failing.
		slog.Warn("folder auto-scan enqueue failed; folder created without a scan",
			"folder_id", folder.ID, "path", folder.Path, "err", enqErr)
	}

	// Fallback: synchronous scan when op registry is unavailable.
	//
	// folder.Path is the value CreateImportPath stored from cleanPath above,
	// i.e. the output of pathvalidation.CleanAbsolutePath, which rejects any
	// relative path and any path whose filepath.Clean form differs from the
	// input (so no traversal sequence survives). It deliberately does NOT
	// confine the path to a root: an import root is by design an arbitrary
	// absolute directory chosen by the operator, and the access control on this
	// value is authn/authz on POST /api/v1/import-paths, not containment.
	// CodeQL does not model that barrier, so suppress the false positive.
	if folder.Enabled && h.opEnqueuer == nil {
		// CodeQL go/path-injection: verified false positive, DISMISSED via the
		// code-scanning API. folder.Path is the value CleanAbsolutePath already
		// produced (it requires an absolute path and rejects any path whose
		// Clean differs), and the route requires auth.PermSettingsManage. This
		// site rests on authn/authz plus that normalization, and deliberately
		// does not claim filesystem containment.
		if _, statErr := os.Stat(folder.Path); statErr == nil {
			books, scanErr := scanner.ScanDirectory(c.Request.Context(), folder.Path, nil)
			if scanErr == nil {
				if len(books) > 0 {
					_ = scanner.ProcessBooks(books, nil)
					// h.autoOrganize and h.rootDir are snapshot values from construction time.
					// organizer.NewOrganizer still reads config.AppConfig — these two sources
					// must be kept in sync by the caller (wireHandlers passes them consistently).
					if h.autoOrganize && h.rootDir != "" {
						org := organizer.NewOrganizer(&config.AppConfig)
						for _, b := range books {
							dbBook, err := h.store.GetBookByFilePath(b.FilePath)
							if err != nil || dbBook == nil {
								continue
							}
							newPath, _, err := org.OrganizeBook(dbBook)
							if err != nil {
								continue
							}
							if newPath != dbBook.FilePath {
								dbBook.FilePath = newPath
								scanner.ApplyOrganizedFileMetadata(dbBook, newPath)
								_, _ = h.store.UpdateBook(dbBook.ID, dbBook)
							}
						}
					} else if h.autoOrganize && h.rootDir == "" {
						slog.Warn("auto-organize enabled but root_dir not set")
					}
				}
				folder.BookCount = len(books)
				now := time.Now()
				folder.LastScan = &now
				_ = h.store.UpdateImportPath(folder.ID, folder)
			}
		}
	}

	httputil.RespondWithCreated(c, gin.H{"importPath": folder})
}

// RemoveImportPath handles DELETE /api/v1/import-paths/:id.
func (h *FilesystemHandler) RemoveImportPath(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid import path id")
		return
	}
	if err := h.store.DeleteImportPath(id); err != nil {
		httputil.InternalError(c, "failed to remove import path", err)
		return
	}
	httputil.RespondWithNoContent(c)
}

// ImportFile handles POST /api/v1/import.
func (h *FilesystemHandler) ImportFile(c *gin.Context) {
	var req importer.ImportFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	result, err := h.fileImporter.ImportFile(&req)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if h.publisher != nil {
		h.publisher.Publish(c.Request.Context(), plugin.NewEvent(plugin.EventBookImported, result.ID, map[string]any{
			"file_path": result.FilePath,
			"source":    "import",
		}))
	}

	// req.Organize used to be decoded here and read NOWHERE — not by this
	// handler and not by importer.ImportFile — while the UI checkbox at
	// web/src/pages/Library.tsx defaulted to ON. Every user who left the box
	// ticked got a 201, no warning, and no organize.
	if !req.Organize {
		httputil.RespondWithCreated(c, result)
		return
	}

	// Honored by ENQUEUEING library.organize rather than organizing inline: the
	// op carries ConcurrencyKey "library.organize" (so an import-triggered
	// organize can never run alongside a library-wide one), cancellation, a 4h
	// timeout, permission and capability checks, and the operation rows the UI
	// already renders. An inline organizer call from this handler would have
	// none of those, and would push an organizer dependency into the importer.
	//
	// req.Organize is deliberately NOT ANDed with h.autoOrganize. That snapshot
	// governs AUTOMATIC organize — the post-scan hook, where no user is in the
	// loop. Ticking this box is explicit per-request intent, the same class as
	// pressing "Organize Library", which is likewise not gated on it. ANDing
	// them would make an explicitly-ticked checkbox silently do nothing: this
	// same bug wearing a different condition.
	resp := gin.H{"id": result.ID, "title": result.Title, "file_path": result.FilePath}
	if opID, skipped := h.enqueueImportOrganize(c, result.ID, result.AuthorResolved); skipped != "" {
		resp["organize_skipped"] = skipped
	} else {
		resp["organize_operation_id"] = opID
	}
	httputil.RespondWithCreated(c, resp)
}

// enqueueImportOrganize queues a library.organize run for one freshly-imported
// book. It returns (opID, "") when a run was queued, or ("", reason) when none
// was — the caller reports the reason rather than leaving a bare 201 to imply
// an organize that never happened.
//
// The book is identified by ID, never by path: OrganizeOneBook os.Renames the
// file under RootDir, so a FilePath captured before the run is stale after it.
//
// There is deliberately NO synchronous fallback for a nil enqueuer, unlike the
// folder-create path above. A nil enqueuer and a failed enqueue mean the same
// thing here — the import succeeded, the organize did not happen — and both are
// reported rather than inferred.
func (h *FilesystemHandler) enqueueImportOrganize(c *gin.Context, bookID string, authorResolved bool) (string, string) {
	if !authorResolved {
		// The SECOND gate, and the one that kept organize-on-import inert for
		// untagged files even after the book_file gate was closed.
		// FilterBooksNeedingOrganization defers any book whose author does not
		// resolve (internal/organizer/service.go:715) rather than baking the
		// placeholder into its path -- the 2026-08-11 mass-reorganize
		// mechanism. That deferral is CORRECT; queueing an op that is
		// guaranteed to hit it, and then reporting an op id for it, is not.
		//
		// Declining here is the same contract as the guards below: the import
		// succeeded, the organize did not, and the caller is told which.
		slog.Warn("import: organize requested but the book has no resolved author; organize would defer it",
			"book_id", bookID)
		return "", "book has no resolved author — fetch metadata first, then organize"
	}
	if h.rootDir == "" {
		// organizer.ensureUnderRoot already fails closed on an empty root
		// (filepath.Clean("") is ".", which no generated relative path
		// prefix-matches), so nothing would be moved to a CWD-relative path.
		// This guard exists so the caller is not handed an op id for a run
		// guaranteed to fail every book. Mirrors the precedent above.
		slog.Warn("import: organize requested but root_dir is not set", "book_id", bookID)
		return "", "root_dir is not configured"
	}
	if h.opEnqueuer == nil {
		slog.Warn("import: organize requested but the operation registry is unavailable", "book_id", bookID)
		return "", "operation registry unavailable"
	}
	opID, err := h.opEnqueuer.EnqueueOp(c.Request.Context(), "library.organize", libraryOrganizeParams{
		BookIDs: []string{bookID},
	})
	if err != nil {
		slog.Warn("import: organize enqueue failed; the book was imported but not organized",
			"book_id", bookID, "err", err)
		return "", "enqueue failed"
	}
	if opID == "" {
		// EnqueueOp returns ("", nil) for a Batchable def, whose id is assigned
		// at flush time (internal/operations/registry/registry.go:621).
		// library.organize is not Batchable today, so this cannot fire -- but
		// this handler's whole contract is "never advertise an organize that
		// did not happen", and an empty organize_operation_id would do exactly
		// that if the def ever gained the flag.
		slog.Warn("import: organize enqueued with no op id; not advertising one", "book_id", bookID)
		return "", "organize queued without a trackable id"
	}
	return opID, ""
}

// -----------------------------------------------------------------------
// Internal types
// -----------------------------------------------------------------------

// folderAutoScanParams are the parameters for a library.folder-auto-scan op.
// This mirrors server.folderAutoScanOpParams (defined in server/folder_autoscan_op.go)
// but is redeclared here to avoid importing internal/server.
//
// ⚠️ THE COMPILER CANNOT CHECK THIS PAIR. The two structs are coupled only by
// their JSON tags, so a field removed from one and left on the other does not
// fail the build — it silently marshals a key the decoder ignores, or omits one
// it needs. LegacyOpID was dropped from both on 2026-08-22; if you change either
// struct, change the other in the same commit.
type folderAutoScanParams struct {
	FolderPath string `json:"folder_path"`
	FolderID   int    `json:"folder_id"`
}

// libraryOrganizeParams are the parameters for a library.organize op. Like
// folderAutoScanParams above, this mirrors a struct in internal/server
// (server.libraryOrganizeParams, library_core_ops.go:31) and is redeclared here
// to avoid importing that package.
//
// ⚠️ THE COMPILER CANNOT CHECK THIS PAIR — see the warning on
// folderAutoScanParams. If you change either struct, change the other in the
// same commit.
//
// All four fields are mirrored even though this handler only ever sets BookIDs.
// The registry's ConcurrencyKey dedupe BYTE-COMPARES marshalled params against
// the active op's stored params (registry.go:632-670), so the shape this
// produces is not private to this call site — keeping it identical to the
// canonical struct means a field added there cannot silently change what this
// site's bytes compare against.
type libraryOrganizeParams struct {
	FolderPath         *string  `json:"folder_path,omitempty"`
	BookIDs            []string `json:"book_ids,omitempty"`
	FetchMetadataFirst bool     `json:"fetch_metadata_first"`
	SyncITunesFirst    bool     `json:"sync_itunes_first"`
}
