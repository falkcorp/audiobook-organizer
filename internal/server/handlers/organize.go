// file: internal/server/handlers/organize.go
// version: 1.13.3
// guid: b3c4d5e6-f7a8-9012-bcde-f01234567890
// last-edited: 2026-11-18

// Package handlers — OrganizeHandler covers the organize-preview and
// single-book organize HTTP endpoints.
//
// The concrete service types (organizer.Service,
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

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
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
	// CreateOperation is deliberately absent. OrganizeBook is
	// synchronous and use a bare ULID as the opchange correlation key; leaving
	// the method off makes minting a v1 row here a compile error rather than
	// something a reviewer has to catch.
	CreateOperationChange(change *database.OperationChange) error
}

// -----------------------------------------------------------------------
// Handler
// -----------------------------------------------------------------------

// OrganizeHandler handles the organize-preview and the single-book
// organize HTTP endpoints.
//
// previewSvc, organizeSvc, and publisher may be
// constructed via their concrete types in wireHandlers; tests may inject
// fakes through the interface.
type OrganizeHandler struct {
	store        OrganizeStore
	previewSvc   OrganizePreviewServicer
	organizeSvc  OrganizeServicer
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
func NewOrganizeHandler(
	store OrganizeStore,
	previewSvc OrganizePreviewServicer,
	organizeSvc OrganizeServicer,
	publisher EventPublisher,
	autoOrganize bool,
) *OrganizeHandler {
	return &OrganizeHandler{
		store:        store,
		previewSvc:   previewSvc,
		organizeSvc:  organizeSvc,
		publisher:    publisher,
		autoOrganize: autoOrganize,
	}
}

// -----------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------

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
		if retiredErr, ok := errors.AsType[*organizer.LibraryCopyRetiredError](err); ok {
			c.JSON(http.StatusConflict, libraryCopyRetiredBody(retiredErr))
			return
		}
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
	//
	// The core's re-check can find a library copy that appeared after the lock
	// set settled. That is the same condition the lock step retries, so it is
	// retried here too (the next lock step resolves and holds the copy), at most
	// organizeLockTries times, then queued or answered 409 like a set that never
	// settles. Nothing is written on an unsettled attempt.
	for attempt := 1; ; attempt++ {
		hold, subject, ok := h.lockBookForOrganize(c, id)
		if !ok {
			return
		}
		unsettled := func() bool {
			defer hold.Release()
			return h.organizeBookCore(c.Request.Context(), ginOrganizeResponder{c}, id, subject)
		}()
		if !unsettled {
			return
		}
		if attempt >= organizeLockTries {
			h.organizeStillUnsettled(c, id)
			return
		}
	}
}

// libraryCopyRetiredBody is the 409 body of an organize or preview refused
// because the book's only library copy is retired: merged into another book,
// or merged into this same book (the copy of a duplicate pair). A copy in the
// trash cannot be named here: the library-copy lookup leaves trashed rows
// out, and `reason` says "was merged away" for the same reason. copy_book_id
// names the copy.
func libraryCopyRetiredBody(e *organizer.LibraryCopyRetiredError) gin.H {
	return gin.H{
		"error":        "organize declined: this book's only library copy is retired (merged into another book, or into this one); revive or un-merge it first",
		"category":     organizer.CollisionLibraryCopyRetired,
		"reason":       e.Error(),
		"book_id":      e.BookID,
		"copy_book_id": e.CopyID,
		"copy_path":    e.CopyPath,
	}
}

// organizeBookCore is POST /audiobooks/:id/organize after its scan lock is
// held, shared by the handler and the queued organize. r receives the
// outcome: the handler writes it as the HTTP response, the queued op turns an
// error status into its failure.
//
// locked is what lockOrganizeSet resolved under the scan lock: the row it
// locked (the library copy, or the book itself) and, for a book whose only
// library copy was merged away, the refusal. The core acts on that row and
// answers that refusal; it never swaps in a row resolved later, because a row
// outside the hold has no scan lock. nil means the lock step could not resolve
// (no store, or no such book), and the core resolves here.
//
// unsettled is true, with nothing written and nothing sent to r, when the core's
// re-check found a library copy the hold does not cover (see below). The
// caller releases its hold and retries from the lock step, which resolves and
// holds the copy. Every other outcome goes to r and returns false.
func (h *OrganizeHandler) organizeBookCore(ctx context.Context, r organizeResponder, id string, locked *organizeSubject) (unsettled bool) {

	// Correlation key only — no operations row is created: undo replays the
	// OperationChange records with a GetOperationChanges prefix scan keyed on
	// this id, which never loads an operations row. The v1 row this used to mint as type "organize" was never read.
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
	//
	// A book whose only library copy was merged away is refused, not organized:
	// the original would be copied beside the merged row's file. The lock step
	// computed that under its hold and hands it over in locked.retired.
	//
	// When the lock step resolved to the book itself, the row is asked for once
	// more here only to check that answer still holds. A copy that appeared
	// after the lock set settled is a row the hold does not cover: organizing
	// it would run without its scan lock, so the answer is "unsettled" (returned
	// to the caller, which retries from the lock step like a set that never
	// settled) and nothing is written. When the lock step resolved to a library
	// copy, that copy is in the hold and is the subject as it stands; it is not
	// resolved again. The hold is the SCAN lock only: a merge or trash of that
	// copy takes no scan lock, so a copy retired after the lock step is still
	// organized in place, as on main (OrganizeOneBook has no retired check).
	// That window is the same size as the one the book-itself case keeps between
	// this re-check and OrganizeOneBook; the store-guard todo lists it.
	book := locked.bookOrNil()
	if locked != nil && locked.retired != nil {
		r.json(http.StatusConflict, libraryCopyRetiredBody(locked.retired))
		return
	}
	if book == nil || book.ID == requested.ID {
		resolved, rerr := organizer.ResolveOrganizeSubject(h.resolveLibraryCopy, requested)
		if retiredErr, ok := errors.AsType[*organizer.LibraryCopyRetiredError](rerr); ok {
			r.json(http.StatusConflict, libraryCopyRetiredBody(retiredErr))
			return
		}
		if book == nil {
			book = resolved
		} else if resolved == nil || resolved.ID != book.ID {
			return true
		}
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
		if hasCopy, ok := errors.AsType[*organizer.LibraryCopyExistsError](err); ok {
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
		if conflict, ok := errors.AsType[*organizer.DestinationConflictError](err); ok {
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

	// Deluge follows the move through the durable relocation follow-up: the
	// store stages books.book.relocated.v1 in the batch that wrote the new path,
	// and books' outbox consumer starts the path-update run (F6-03).

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
	return false
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
