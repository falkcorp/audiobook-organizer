// file: internal/server/handlers/organize.go
// version: 1.0.0
// guid: b3c4d5e6-f7a8-9012-bcde-f01234567890
// last-edited: 2026-06-02

// Package handlers — OrganizeHandler covers the rename-preview, rename-apply,
// organize-preview, and single-book organize HTTP endpoints.
//
// The concrete service types (organizer.RenameService, organizer.Service,
// organizer.PreviewService) live in internal/organizer, which is not
// internal/server, so there is no circular-import risk. We define narrow
// interfaces here so tests can inject fakes without touching the organizer
// package at all.

package handlers

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

// -----------------------------------------------------------------------
// Narrow interfaces
// -----------------------------------------------------------------------

// RenameServicer is the narrow interface for the rename service.
type RenameServicer interface {
	PreviewRename(bookID string) (*organizer.RenamePreview, error)
	ApplyRename(bookID, operationID string) (*organizer.RenameApplyResult, error)
}

// OrganizePreviewServicer is the narrow interface for the organize-preview service.
type OrganizePreviewServicer interface {
	PreviewOrganize(bookID string) (*organizer.PreviewResponse, error)
}

// OrganizeServicer is the narrow interface for the organize service.
type OrganizeServicer interface {
	ReOrganizeInPlace(book *database.Book, log logger.Logger) (string, error)
	OrganizeDirectoryBook(org *organizer.Organizer, book *database.Book, log logger.Logger) (string, error)
	CreateOrganizedVersion(org *organizer.Organizer, book *database.Book, newPath string, isDir bool, operationID string, log logger.Logger) (*database.Book, error)
}

// OrganizeStore is the database interface required by OrganizeHandler.
// We use database.Store directly because organizer.Organizer.SetStore and
// deluge.NotifyDelugeAfterOrganize both require the full database.Store
// interface, so a narrower subset would not satisfy those call sites.
// database is not internal/server, so there is no circular-import risk.
type OrganizeStore = database.Store

// OrganizeWriteBackEnqueuer is an alias for the shared WriteBackEnqueuer; kept
// here so existing call sites that reference OrganizeWriteBackEnqueuer continue
// to compile without change.
type OrganizeWriteBackEnqueuer = WriteBackEnqueuer

// -----------------------------------------------------------------------
// Handler
// -----------------------------------------------------------------------

// OrganizeHandler handles rename-preview, rename-apply, organize-preview, and
// the single-book organize HTTP endpoints.
//
// renameSvc, previewSvc, organizeSvc, writeBack, and publisher may be
// constructed via their concrete types in wireHandlers; tests may inject
// fakes through the interface.
type OrganizeHandler struct {
	store        OrganizeStore
	renameSvc    RenameServicer
	previewSvc   OrganizePreviewServicer
	organizeSvc  OrganizeServicer
	writeBack    WriteBackEnqueuer // may be nil
	publisher    EventPublisher
	rootDir      string
	autoOrganize bool
}

// NewOrganizeHandler constructs an OrganizeHandler.
// writeBack may be nil (the handler is nil-safe).
func NewOrganizeHandler(
	store OrganizeStore,
	renameSvc RenameServicer,
	previewSvc OrganizePreviewServicer,
	organizeSvc OrganizeServicer,
	writeBack WriteBackEnqueuer,
	publisher EventPublisher,
	rootDir string,
	autoOrganize bool,
) *OrganizeHandler {
	return &OrganizeHandler{
		store:        store,
		renameSvc:    renameSvc,
		previewSvc:   previewSvc,
		organizeSvc:  organizeSvc,
		writeBack:    writeBack,
		publisher:    publisher,
		rootDir:      rootDir,
		autoOrganize: autoOrganize,
	}
}

// -----------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------

// PreviewRename handles GET /api/v1/audiobooks/:id/rename/preview.
// Returns the current path, proposed path, and tag diff for a book.
func (h *OrganizeHandler) PreviewRename(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "book id is required")
		return
	}

	preview, err := h.renameSvc.PreviewRename(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "book", id)
			return
		}
		httputil.InternalError(c, "failed to preview rename", err)
		return
	}

	httputil.RespondWithOK(c, preview)
}

// ApplyRename handles POST /api/v1/audiobooks/:id/rename/apply.
// Executes the rename, tag write, and DB update for a book.
func (h *OrganizeHandler) ApplyRename(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "book id is required")
		return
	}

	opID := ulid.Make().String()
	op, err := h.store.CreateOperation(opID, "rename", strPtr(id))
	if err != nil {
		slog.Error("rename failed to create operation", "err", err)
		httputil.RespondWithInternalError(c, "failed to create operation record")
		return
	}

	result, err := h.renameSvc.ApplyRename(id, op.ID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "book", id)
			return
		}
		httputil.InternalError(c, "failed to apply rename", err)
		return
	}

	// Rename moved the file on disk → push a location update to iTunes.
	if h.writeBack != nil {
		h.writeBack.Enqueue(id)
	}

	httputil.RespondWithOK(c, result)
}

// PreviewOrganize handles GET /api/v1/audiobooks/:id/organize/preview.
// Returns a step-by-step preview of what organizing a single book would do.
func (h *OrganizeHandler) PreviewOrganize(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "book id is required")
		return
	}

	preview, err := h.previewSvc.PreviewOrganize(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "book", id)
			return
		}
		httputil.InternalError(c, "failed to preview organize", err)
		return
	}

	httputil.RespondWithOK(c, preview)
}

// OrganizeBook handles POST /api/v1/audiobooks/:id/organize.
// Executes the full organize pipeline for a single book, mirroring the batch
// organize logic: re-organize-in-place for books already under rootDir,
// OrganizeDirectoryBook for multi-file books, and OrganizeBook for single-file.
func (h *OrganizeHandler) OrganizeBook(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "book id is required")
		return
	}

	opID := ulid.Make().String()
	op, err := h.store.CreateOperation(opID, "organize", strPtr(id))
	if err != nil {
		slog.Error("organize failed to create operation", "err", err)
		httputil.RespondWithInternalError(c, "failed to create operation record")
		return
	}

	book, err := h.store.GetBookByID(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "book", id)
			return
		}
		httputil.InternalError(c, "failed to fetch book", err)
		return
	}

	oldPath := book.FilePath
	org := organizer.NewOrganizer(&config.AppConfig)
	org.SetStore(h.store)
	log2 := logger.NewWithActivityLog("organize", h.store)

	bookFiles, _ := h.store.GetBookFiles(id)
	isDir := false
	if len(bookFiles) > 1 {
		isDir = true
	} else if len(bookFiles) == 0 {
		if info, statErr := os.Stat(oldPath); statErr == nil && info.IsDir() {
			isDir = true
		}
	} else if len(bookFiles) == 1 {
		if info, statErr := os.Stat(oldPath); statErr == nil && info.IsDir() {
			isDir = true
		}
	}

	alreadyInRoot := h.rootDir != "" && strings.HasPrefix(oldPath, h.rootDir)

	var newPath string
	if alreadyInRoot {
		newPath, err = h.organizeSvc.ReOrganizeInPlace(book, log2)
	} else if isDir {
		newPath, err = h.organizeSvc.OrganizeDirectoryBook(org, book, log2)
	} else {
		newPath, _, err = org.OrganizeBook(book)
	}

	if err != nil {
		httputil.InternalError(c, "failed to organize book", err)
		return
	}

	if oldPath == newPath {
		httputil.RespondWithOK(c, gin.H{
			"message":      "already organized",
			"book_id":      book.ID,
			"old_path":     oldPath,
			"new_path":     newPath,
			"operation_id": op.ID,
		})
		return
	}

	if alreadyInRoot {
		now := time.Now()
		book.LastOrganizeOperationID = &opID
		book.LastOrganizedAt = &now
		if _, updateErr := h.store.UpdateBook(book.ID, book); updateErr != nil {
			slog.Warn("organize failed to stamp book", "book", book.ID, "updateErr", updateErr)
		}
		_ = h.store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: op.ID,
			BookID:      book.ID,
			ChangeType:  "organize_rename",
			FieldName:   "file_path",
			OldValue:    oldPath,
			NewValue:    newPath,
		})
		if h.publisher != nil {
			h.publisher.Publish(c.Request.Context(), plugin.NewEvent(plugin.EventFileOrganized, book.ID, map[string]any{
				"old_path":     oldPath,
				"new_path":     newPath,
				"operation_id": op.ID,
			}))
		}
		httputil.RespondWithOK(c, gin.H{
			"message":      fmt.Sprintf("re-organized: %s → %s", oldPath, newPath),
			"book_id":      book.ID,
			"old_path":     oldPath,
			"new_path":     newPath,
			"operation_id": op.ID,
		})
		return
	}

	createdBook, createErr := h.organizeSvc.CreateOrganizedVersion(org, book, newPath, isDir, op.ID, log2)
	if createErr != nil {
		httputil.InternalError(c, "failed to create organized version", createErr)
		return
	}

	now := time.Now()
	createdBook.LastOrganizeOperationID = &opID
	createdBook.LastOrganizedAt = &now
	if _, updateErr := h.store.UpdateBook(createdBook.ID, createdBook); updateErr != nil {
		slog.Warn("organize failed to stamp organized book", "createdBook", createdBook.ID, "updateErr", updateErr)
	}

	// Notify Deluge that the file moved so the torrent client keeps seeding
	// from the new library path. Best-effort — errors are logged inside
	// NotifyDelugeAfterOrganize; the organize operation already succeeded.
	deluge.NotifyDelugeAfterOrganize(h.store, book.ID, newPath)

	if h.publisher != nil {
		h.publisher.Publish(c.Request.Context(), plugin.NewEvent(plugin.EventFileOrganized, createdBook.ID, map[string]any{
			"old_path":         oldPath,
			"new_path":         newPath,
			"original_book_id": book.ID,
			"operation_id":     op.ID,
		}))
	}

	httputil.RespondWithOK(c, gin.H{
		"message":          fmt.Sprintf("organized: %s → %s", oldPath, newPath),
		"book_id":          createdBook.ID,
		"original_book_id": book.ID,
		"old_path":         oldPath,
		"new_path":         newPath,
		"operation_id":     op.ID,
	})
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// strPtr returns a pointer to s. Used to pass string IDs to CreateOperation
// which takes *string for its optional bookID / folderPath parameter.
func strPtr(s string) *string { return &s }
