// file: internal/server/handlers/organize.go
// version: 1.10.0
// guid: b3c4d5e6-f7a8-9012-bcde-f01234567890
// last-edited: 2026-09-30

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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
//
// OrganizeOneBook is the three-way in-place / directory / single-file decision
// the batch organize worker uses. The handler used to make its own copy of
// that decision and call the three strategies itself; the copies had drifted
// (the worker looked only at file_path, this one at the row count), so one of
// them was wrong for multi-row books whose file_path names a file.
type OrganizeServicer interface {
	OrganizeOneBook(org *organizer.Organizer, book *database.Book, log logger.Logger) (*organizer.Landing, error)
	CreateOrganizedVersion(book *database.Book, landing *organizer.Landing, operationID string, log logger.Logger) (*database.Book, error)
}

// OrganizeStore is what OrganizeHandler actually needs, measured by emptying it
// and reading the compiler's enumeration: five direct calls plus three
// already-narrow constraints.
//
// It was `= database.Store`, an alias to all 398 methods, justified by a comment
// saying organizer.Organizer.SetStore and deluge.NotifyDelugeAfterOrganize "both
// require the full database.Store interface, so a narrower subset would not
// satisfy those call sites". Neither does: SetStore takes
// organizer.OrganizerStore (four lookups) and the deluge notifier needs one
// method. The justification was stale, not wrong when written.
type OrganizeStore interface {
	organizer.OrganizerStore
	logger.ActivityLogWriter

	GetBookByID(id string) (*database.Book, error)
	// UpdateBook is the first write of a row CreateOrganizedVersion just
	// created; every other book write here goes through ModifyBook.
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
	// CreateOperation is deliberately absent. ApplyRename and OrganizeBook are
	// synchronous and use a bare ULID as the opchange correlation key; leaving
	// the method off makes minting a v1 row here a compile error rather than
	// something a reviewer has to catch.
	CreateOperationChange(change *database.OperationChange) error
}

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
	autoOrganize bool

	// resolveLibraryCopy maps a protected original to its existing library
	// copy, so OrganizeBook organizes the row the preview showed (the
	// preview service is wired with the same resolver). Nil: every book is
	// organized as itself. Set with SetLibraryCopyResolver.
	resolveLibraryCopy organizer.LibraryCopyResolver

	// queuer hands an organize whose book the library scan holds past
	// organizeBookLockWait to the queued op (202). Nil: keep waiting. Set
	// with SetOrganizeQueuer.
	queuer OrganizeQueuer
}

// SetLibraryCopyResolver installs the protected-original -> library-copy
// lookup OrganizeBook applies before organizing. Production passes
// metafetch's Service.ExistingLibraryCopyOfFile (the metadata apply's lookup,
// narrowed to a copy of this book's own file, never another edition) and
// gives the preview service the same one.
func (h *OrganizeHandler) SetLibraryCopyResolver(resolve organizer.LibraryCopyResolver) {
	h.resolveLibraryCopy = resolve
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
	autoOrganize bool,
) *OrganizeHandler {
	return &OrganizeHandler{
		store:        store,
		renameSvc:    renameSvc,
		previewSvc:   previewSvc,
		organizeSvc:  organizeSvc,
		writeBack:    writeBack,
		publisher:    publisher,
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

	// The id is the correlation key for the OperationChange records the rename
	// writes; undo replays them with a GetOperationChanges prefix scan over
	// "opchange:<id>:", which never loads an operations row. No row is created.
	//
	// One used to be, as v1 type "rename", and nothing ever read it: the v1
	// list/status/logs routes were retired 2026-08-16, GetOperationTimeline
	// reads ListOperationsV2Since only, and CreateOperation stamps status
	// "pending" which isResumableOpStatus excludes — so the restart sweep never
	// touched it either. Every rename since has left one immortal pending row.
	opID := ulid.Make().String()

	result, err := h.renameSvc.ApplyRename(id, opID)
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
	// The book's scan lock (internal/scanlock, L0) is held for the whole
	// organize: OrganizeOneBook's file moves and CreateOrganizedVersion's rows.
	// It is keyed on the REQUESTED id even when the book resolves to its
	// library copy below: the scanner's lock set for either row includes the
	// other through their version group, so {id} covers both. L0 is taken
	// before any L1 key (the version-group key CreateOrganizedVersion takes).
	hold, subject, ok := h.lockBookForOrganize(c, id)
	if !ok {
		return
	}
	defer hold.Release()
	h.organizeBookCore(c.Request.Context(), ginOrganizeResponder{c}, id, subject)
}

// organizeBookCore is POST /audiobooks/:id/organize after its scan lock is
// held, shared by the handler and the queued organize. r receives the
// outcome: the handler writes it as the HTTP response, the queued op turns an
// error status into its failure.
//
// locked is the row lockOrganizeSet resolved under the scan lock (the library
// copy it locked, or the book itself). The core acts on it rather than
// resolving again, so it writes exactly the copy it holds; nil means the lock
// step could not resolve, and the core resolves here.
func (h *OrganizeHandler) organizeBookCore(ctx context.Context, r organizeResponder, id string, locked *database.Book) {

	// Correlation key only — see ApplyRename above for why no operations row is
	// created. The v1 row this used to mint as type "organize" was never read.
	opID := ulid.Make().String()

	requested, err := h.store.GetBookByID(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			r.notFound("book", id)
			return
		}
		r.internal("failed to fetch book", err)
		return
	}
	if requested == nil {
		r.notFound("book", id)
		return
	}

	// A protected original (import / iTunes path) whose metadata apply
	// already made a library copy is organized THROUGH that copy: the copy
	// is under RootDir, so it re-organizes in place, and when it already sits
	// at the computed target this is the "already organized" no-op below.
	// Organizing the original instead copied the same bytes a second time,
	// and the organizer's same-hash check reported the book's own copy as a
	// foreign duplicate -- a 500 and a dedup candidate pairing the book with
	// itself (2026-09-30). PreviewOrganize resolves with the same function.
	book := locked
	if book == nil {
		book = organizer.ResolveOrganizeSubject(h.resolveLibraryCopy, requested)
	}
	originalID := ""
	if book.ID != requested.ID {
		originalID = requested.ID
	}

	oldPath := book.FilePath
	org := organizer.NewOrganizer(&config.AppConfig)
	org.SetStore(h.store)
	log2 := logger.NewWithActivityLog("organize", h.store)

	// The in-place / new-version decision is OrganizeOneBook's and arrives on
	// the Landing. This handler used to recompute it from a RootDir snapshot
	// taken at startup while OrganizeOneBook read the live value; after a
	// runtime root_dir change the two disagreed and a file moved in place was
	// then also given a second book row at the same path.
	landing, err := h.organizeSvc.OrganizeOneBook(org, book, log2)
	if err != nil {
		// A declined move -- a file another book owns, the iTunes tree, an
		// occupied destination -- is the organizer's deliberate refusal, not a
		// server fault. Answer 409 with its category and reason so the client
		// can say WHY; it used to surface as a bare 500.
		// The book's content is already in the library as another version
		// of it -- a copy the resolver did not hand back (it rejects a copy
		// with a file row still in a protected tree), or one made after it
		// ran. Organizing would make a second copy; say which row to use.
		var hasCopy *organizer.LibraryCopyExistsError
		if errors.As(err, &hasCopy) {
			r.json(http.StatusConflict, gin.H{
				"error":        "organize declined: this book already has a library copy; organize that copy instead",
				"category":     organizer.CollisionLibraryCopyExists,
				"reason":       hasCopy.Error(),
				"book_id":      book.ID,
				"copy_book_id": hasCopy.CopyID,
				"copy_path":    hasCopy.CopyPath,
			})
			return
		}
		var conflict *organizer.DestinationConflictError
		if errors.As(err, &conflict) {
			r.json(http.StatusConflict, gin.H{
				"error":    "organize declined: " + conflict.Reason,
				"category": conflict.Category,
				"reason":   conflict.Reason,
				"source":   conflict.Source,
				"target":   conflict.Target,
				"book_id":  book.ID,
			})
			return
		}
		r.internal("failed to organize book", err)
		return
	}
	newPath := landing.Path

	if oldPath == newPath {
		r.ok(withOriginal(gin.H{
			"message":      "already organized",
			"book_id":      book.ID,
			"old_path":     oldPath,
			"new_path":     newPath,
			"operation_id": opID,
		}, originalID))
		return
	}

	if landing.InPlace {
		now := time.Now()
		// ModifyBook, not UpdateBook(book): `book` was read before
		// OrganizeOneBook moved the file, so writing it back whole would
		// revert any column another writer committed meanwhile. The two stamp
		// columns are the only ones this handler owns.
		stamped, updateErr := h.store.ModifyBook(book.ID, func(b *database.Book) error {
			b.LastOrganizeOperationID = &opID
			b.LastOrganizedAt = &now
			return nil
		})
		switch {
		case updateErr != nil:
			log2.Warn("organize failed to stamp book %s: %s", book.ID, updateErr.Error())
		case stamped == nil:
			log2.Warn("organize failed to stamp book %s: row no longer exists", book.ID)
		}
		// Same record CommitLanding writes: organize_rename for one rename,
		// per-file moves for a multi-file book.
		if recErr := organizer.RecordInPlaceMove(h.store, book.ID, landing, oldPath, opID); recErr != nil {
			log2.Warn("organize: undo record incomplete for book %s: %s", book.ID, logger.SanitizeLogValue(recErr.Error()))
		}
		if h.publisher != nil {
			h.publisher.Publish(ctx, plugin.NewEvent(plugin.EventFileOrganized, book.ID, map[string]any{
				"old_path":     oldPath,
				"new_path":     newPath,
				"operation_id": opID,
			}))
		}
		r.ok(withOriginal(gin.H{
			"message":      fmt.Sprintf("re-organized: %s → %s", oldPath, newPath),
			"book_id":      book.ID,
			"old_path":     oldPath,
			"new_path":     newPath,
			"operation_id": opID,
		}, originalID))
		return
	}

	createdBook, createErr := h.organizeSvc.CreateOrganizedVersion(book, landing, opID, log2)
	if createErr != nil {
		r.internal("failed to create organized version", createErr)
		return
	}

	now := time.Now()
	createdBook.LastOrganizeOperationID = &opID
	createdBook.LastOrganizedAt = &now
	// First write of the row CreateOrganizedVersion just returned: no other
	// writer can have touched it yet, so the whole-row UpdateBook is safe here.
	if _, updateErr := h.store.UpdateBook(createdBook.ID, createdBook); updateErr != nil {
		slog.Warn("organize failed to stamp organized book", "createdBook", createdBook.ID, "updateErr", updateErr)
	}

	// Notify Deluge that the file moved so the torrent client keeps seeding
	// from the new library path. Best-effort — errors are logged inside
	// NotifyDelugeAfterOrganize; the organize operation already succeeded.
	deluge.NotifyDelugeAfterOrganize(h.store, book.ID, newPath)

	if h.publisher != nil {
		h.publisher.Publish(ctx, plugin.NewEvent(plugin.EventFileOrganized, createdBook.ID, map[string]any{
			"old_path":         oldPath,
			"new_path":         newPath,
			"original_book_id": book.ID,
			"operation_id":     opID,
		}))
	}

	resp := gin.H{
		"message":          fmt.Sprintf("organized: %s → %s", oldPath, newPath),
		"book_id":          createdBook.ID,
		"original_book_id": book.ID,
		"old_path":         oldPath,
		"new_path":         newPath,
		"operation_id":     opID,
	}
	r.ok(resp)
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// withOriginal adds original_book_id to an OrganizeBook response when the
// organize acted on the requested book's library copy rather than the book
// itself, so the client can tell which row moved.
func withOriginal(resp gin.H, originalID string) gin.H {
	if originalID != "" {
		resp["original_book_id"] = originalID
	}
	return resp
}
