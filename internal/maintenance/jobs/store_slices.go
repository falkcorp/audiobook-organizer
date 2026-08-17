// file: internal/maintenance/jobs/store_slices.go
// version: 1.1.0
// guid: 3a142df0-9e5d-4ead-9db6-bb75dbed428f
// last-edited: 2026-08-16

package jobs

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Narrow store slices for this package's free helper functions.
//
// `MaintenanceJob.Run` (internal/maintenance/job.go:70) takes the full
// `database.Store` and cannot be narrowed — an interface method's parameter
// type is fixed for every implementer, and 31 job types implement it. That one
// signature is why this package holds the largest concentration of wide-store
// declarations in the codebase.
//
// The layer *beneath* `Run` is not constrained by it. These helpers chose
// `database.Store` with nothing compelling them, and each uses 1-4 of its 398
// methods. Declaring what they actually need makes the dependency inspectable,
// lets a test supply a double it can reason about, and shrinks the blast radius
// of a helper that should never, say, delete a book.
//
// Convention (see docs/audits/2026-08-16-store-interface-decomposition.md §7):
// list the methods EXPLICITLY rather than embedding `database` sub-interfaces.
// Embedding looks tidier but is not narrowing — `database.BookStore` alone is
// 51 methods, so a helper needing two of them would still declare 51. The
// measured cost of that mistake is in `internal/reconcile`, which composes four
// sub-interfaces to get 115 declared methods and uses 11.
//
// Narrowing a parameter is monotone: `*database.PebbleStore` and the
// hand-written `*database.MockStore` both satisfy `Store`, which is composed
// purely of embedded sub-interfaces, so they satisfy every slice here too.
// Existing callers and tests need no changes.

// operationLister and operationDeleter are the two halves of what used to be
// one `retentionOperationStore` covering both. Splitting the helper into a read
// step, a pure decision, and a write step (see retention_and_hygiene.go) left
// each I/O step needing exactly one method, so the interface split followed the
// function split rather than the other way round.
//
// One method each is also the point at which a narrow interface stops earning
// its keep — a `func(limit, offset int) (...)` parameter would say the same
// thing with less ceremony (Option D in the audit's §7). They are kept as named
// interfaces here so the compile-time assertions below still cover them and so
// the retention job reads uniformly with its three siblings.
type operationLister interface {
	ListOperations(limit, offset int) ([]database.Operation, int, error)
}

type operationDeleter interface {
	DeleteOperationWithLogs(id string) error
}

// retentionKVStore is the slice needed to sweep dead key prefixes out of the
// raw KV space.
type retentionKVStore interface {
	ScanPrefix(prefix string) ([]database.KVPair, error)
	DeleteRaw(key string) error
}

// retentionOpStateStore is the slice needed to drop resume-state rows whose
// parent operation no longer exists.
type retentionOpStateStore interface {
	ScanPrefix(prefix string) ([]database.KVPair, error)
	GetOperationByID(id string) (*database.Operation, error)
	DeleteOperationState(opID string) error
}

// retentionFlagStore is the slice needed to read and write the one-shot
// "this sweep already ran" marker.
type retentionFlagStore interface {
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
}

// bookMutator is the two-method read-modify-write slice. It is by far the most
// common shape in this package: fetch a book, change a field, write it back.
type bookMutator interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// bookFileMutator is the book-file equivalent of bookMutator.
type bookFileMutator interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// bookFileReader reads a book's files without being able to write anything.
type bookFileReader interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// bookFileWriter updates an existing book file in place.
type bookFileWriter interface {
	UpdateBookFile(id string, file *database.BookFile) error
}

// bookFileCreator adds new book-file rows.
type bookFileCreator interface {
	CreateBookFile(file *database.BookFile) error
}

// bookPager walks the library in keyset-paginated order.
type bookPager interface {
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
}

// userLister enumerates users.
type userLister interface {
	ListUsers() ([]database.User, error)
}

// userBookStateWriter records per-user read state.
type userBookStateWriter interface {
	SetUserBookState(state *database.UserBookState) error
}

// bookAggregateRecomputer walks every book ID and refreshes its cached
// aggregate columns.
type bookAggregateRecomputer interface {
	ListBookIDs() ([]string, error)
	RecomputeBookAggregates(bookID string) error
}

// bookSoftDeleter is bookMutator plus the delete it needs to retire a
// duplicate. Kept distinct from bookMutator so that helpers which only edit a
// book cannot delete one by accident.
type bookSoftDeleter interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	DeleteBook(id string) error
}

// seriesUnlinker moves books off a series and then removes the series row.
type seriesUnlinker interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	DeleteSeries(id int) error
}

// seriesMerger is seriesUnlinker plus the membership read needed to fold one
// series group into another.
type seriesMerger interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	DeleteSeries(id int) error
}

// Compile-time proof that the real store satisfies every slice above. Without
// these, a drifting method signature would only surface at the call sites.
var (
	_ operationLister         = (*database.PebbleStore)(nil)
	_ operationDeleter        = (*database.PebbleStore)(nil)
	_ retentionKVStore        = (*database.PebbleStore)(nil)
	_ retentionOpStateStore   = (*database.PebbleStore)(nil)
	_ retentionFlagStore      = (*database.PebbleStore)(nil)
	_ bookMutator             = (*database.PebbleStore)(nil)
	_ bookFileMutator         = (*database.PebbleStore)(nil)
	_ bookFileReader          = (*database.PebbleStore)(nil)
	_ bookFileWriter          = (*database.PebbleStore)(nil)
	_ bookFileCreator         = (*database.PebbleStore)(nil)
	_ bookPager               = (*database.PebbleStore)(nil)
	_ userLister              = (*database.PebbleStore)(nil)
	_ userBookStateWriter     = (*database.PebbleStore)(nil)
	_ bookAggregateRecomputer = (*database.PebbleStore)(nil)
	_ bookSoftDeleter         = (*database.PebbleStore)(nil)
	_ seriesUnlinker          = (*database.PebbleStore)(nil)
	_ seriesMerger            = (*database.PebbleStore)(nil)
)
