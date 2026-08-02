// file: internal/server/handlers/filesystem.go
// version: 1.2.0
// guid: c4d5e6f7-a8b9-0123-cdef-012345678901
// last-edited: 2026-07-01

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
	ulid "github.com/oklog/ulid/v2"
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
type FilesystemStore interface {
	GetAllImportPaths() ([]database.ImportPath, error)
	GetDashboardStats() (*database.DashboardStats, error)
	CountBooksByPathPrefix(prefix string) (int, error)
	CreateOperation(id, opType string, folderPath *string) (*database.Operation, error)
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
		opID := ulid.Make().String()
		folderPath := folder.Path
		_, createErr := h.store.CreateOperation(opID, "scan", &folderPath)
		if createErr == nil {
			params := folderAutoScanParams{
				LegacyOpID: opID,
				FolderPath: folderPath,
				FolderID:   folder.ID,
			}
			if _, enqErr := h.opEnqueuer.EnqueueOp(c.Request.Context(), "library.folder-auto-scan", params); enqErr == nil {
				httputil.RespondWithCreated(c, gin.H{"importPath": folder, "scan_operation_id": opID})
				return
			}
		}
	}

	// Fallback: synchronous scan when op registry is unavailable.
	if folder.Enabled && h.opEnqueuer == nil {
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

	httputil.RespondWithCreated(c, result)
}

// -----------------------------------------------------------------------
// Internal types
// -----------------------------------------------------------------------

// folderAutoScanParams are the parameters for a library.folder-auto-scan op.
// This mirrors server.folderAutoScanOpParams (defined in server/folder_autoscan_op.go)
// but is redeclared here to avoid importing internal/server.
type folderAutoScanParams struct {
	LegacyOpID string `json:"legacy_op_id"`
	FolderPath string `json:"folder_path"`
	FolderID   int    `json:"folder_id"`
}
