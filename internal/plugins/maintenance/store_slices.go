// file: internal/plugins/maintenance/store_slices.go
// version: 1.0.0
// guid: 8d3b6f14-2a97-4e51-b0c8-5f7e91d24a63
// last-edited: 2026-08-17

package maintenance

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Narrow store slices for this package's helpers.
//
// Sibling of internal/maintenance/jobs/store_slices.go, and the same reasoning
// applies: the plugin entry points take the full `database.Store` (398 methods)
// because the plugin framework's own signatures require it, but the helpers
// beneath them chose `database.Store` with nothing compelling them. Each uses
// between 1 and 11 of those methods.
//
// Convention (docs/audits/2026-08-16-store-interface-decomposition.md §7): list
// the methods EXPLICITLY rather than embedding `database` sub-interfaces.
// Embedding reads tidier but is not narrowing — `database.BookStore` alone is 51
// methods, so a helper needing two of them would still declare 51. The measured
// cost of that mistake is in `internal/reconcile`, which composes four
// sub-interfaces to declare 115 methods and uses 11.
//
// Narrowing a parameter is monotone: `database.Store` is composed purely of
// embedded sub-interfaces, so `*database.PebbleStore` and the hand-written
// `*database.MockStore` satisfy every slice below exactly as they satisfy
// `Store`. No caller and no test needs to change — verified by build, not
// assumed.
//
// Single-method slices are deliberate. Option D from the audit (pass the method
// value instead of an interface) would say the same thing with no type
// declaration, but it rewrites every call site; a one-method interface keeps the
// call sites untouched, which is what makes a 19-site sweep reviewable in one
// pass. D stays available per-site later.

// bookFieldWriter reads a book and writes it back. Used by the two helpers that
// repair individual fields on an existing row (restoreRecoverableFields,
// stampVerifiedAt) — neither creates or deletes anything, and declaring only
// these two methods is what says so.
type bookFieldWriter interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// bookFileLister lists one book's files and nothing else.
type bookFileLister interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// bookFileCoreScanner reads the whole book_file table in its core projection.
// Three read-only audit helpers share it; none of them may write.
type bookFileCoreScanner interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
}

// bookUpdater writes a book row without being able to read one back.
type bookUpdater interface {
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// bookFileRelinker repoints an existing book_file row at a different path or
// book. It cannot create or delete files — only update them.
type bookFileRelinker interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// regroupSnapshotReader builds the read-only snapshot the iTunes regroup planner
// works from. Strictly reads: a planner that could write would be a defect.
type regroupSnapshotReader interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
}

// fsRegroupStore is the widest slice in this file at 11 methods — the
// filesystem regroup apply path genuinely moves files between books, creates and
// deletes rows, and reassigns external IDs. 11 of 398 is still the point: the
// declaration now states that this helper may delete a book, which is worth
// knowing at a glance.
type fsRegroupStore interface {
	CreateBookFile(file *database.BookFile) error
	DeleteBook(id string) error
	GetBookByID(id string) (*database.Book, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	ReassignExternalIDs(oldBookID, newBookID string) error
	RecomputeBookAggregates(bookID string) error
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// itunesRegroupStore is the iTunes-side twin of fsRegroupStore. It creates books
// rather than book files, and reassigns one external ID at a time.
type itunesRegroupStore interface {
	CreateBook(book *database.Book) (*database.Book, error)
	DeleteBook(id string) error
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	ReassignExternalID(source, externalID, newBookID string) error
	RecomputeBookAggregates(bookID string) error
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// bookFileBulkDeleter deletes book_file rows by ID and can do nothing else.
// This is the destructive half of the missing-file repair, and the one-method
// declaration is the point: it cannot touch a book row.
type bookFileBulkDeleter interface {
	DeleteBookFilesByIDs(ids []string) error
}

// orphanFileScanner reads every book file and every book — including
// soft-deleted ones, which is what makes an orphan detectable — and writes
// nothing.
type orphanFileScanner interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error)
}

// bookFileTrackWriter stamps disc/track numbers onto existing book files.
type bookFileTrackWriter interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// versionGroupWriter applies a version-group decision across the group's books.
type versionGroupWriter interface {
	GetBookByID(id string) (*database.Book, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// bookByIDReader reads single books by ID. Read-only by construction.
type bookByIDReader interface {
	GetBookByID(id string) (*database.Book, error)
}

// reviewHoldStore lists and clears review-queue items. Scoped to the review
// tables so a hold-reconciler cannot reach book data at all.
type reviewHoldStore interface {
	DeleteReviewItem(id string) error
	ListReviewItems(filter database.ReviewFilter) ([]database.ReviewItem, int, error)
}

// bookFileCreator creates book_file rows and nothing else.
type bookFileCreator interface {
	CreateBookFile(file *database.BookFile) error
}

// Compile-time assertions: the concrete store and the hand-written mock must
// satisfy every slice above. If a `database` method signature changes, these
// break here — at the declaration — instead of at whichever call site happens to
// be compiled first.
var (
	_ bookFieldWriter       = (*database.PebbleStore)(nil)
	_ bookFileLister        = (*database.PebbleStore)(nil)
	_ bookFileCoreScanner   = (*database.PebbleStore)(nil)
	_ bookUpdater           = (*database.PebbleStore)(nil)
	_ bookFileRelinker      = (*database.PebbleStore)(nil)
	_ regroupSnapshotReader = (*database.PebbleStore)(nil)
	_ fsRegroupStore        = (*database.PebbleStore)(nil)
	_ itunesRegroupStore    = (*database.PebbleStore)(nil)
	_ bookFileBulkDeleter   = (*database.PebbleStore)(nil)
	_ orphanFileScanner     = (*database.PebbleStore)(nil)
	_ bookFileTrackWriter   = (*database.PebbleStore)(nil)
	_ versionGroupWriter    = (*database.PebbleStore)(nil)
	_ bookByIDReader        = (*database.PebbleStore)(nil)
	_ reviewHoldStore       = (*database.PebbleStore)(nil)
	_ bookFileCreator       = (*database.PebbleStore)(nil)

	_ bookFieldWriter       = (*database.MockStore)(nil)
	_ fsRegroupStore        = (*database.MockStore)(nil)
	_ itunesRegroupStore    = (*database.MockStore)(nil)
	_ orphanFileScanner     = (*database.MockStore)(nil)
	_ reviewHoldStore       = (*database.MockStore)(nil)
)
