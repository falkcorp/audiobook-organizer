// file: internal/maintenance/jobs/store_slices.go
// version: 1.13.0
// guid: 3a142df0-9e5d-4ead-9db6-bb75dbed428f
// last-edited: 2026-11-18

package jobs

import database "github.com/falkcorp/audiobook-organizer/internal/books/store"

// Narrow store slices for this package's free helper functions.
//
// `MaintenanceJob.Run` (internal/maintenance/job.go:70) takes the full
// `database.Store` and cannot be narrowed — an interface method's parameter
// type is fixed for every implementer, and 37 job types implement it. That one
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
//
// It reads BOTH operation keyspaces. `opstate:` is keyed by whichever id the
// writing op had, and since the v1 id minter was retired on 2026-08-23 that is
// always a v2 id — so a slice holding only GetOperationByID cannot answer
// "is this operation still live" for any checkpoint written since.
type retentionOpStateStore interface {
	ScanPrefix(prefix string) ([]database.KVPair, error)
	GetOperationByID(id string) (*database.Operation, error)
	GetOperationV2(id string) (*database.OperationV2Row, error)
	DeleteOperationState(opID string) error
}

// retentionFlagStore is the slice needed to read and write the one-shot
// "this sweep already ran" marker.
type retentionFlagStore interface {
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
}

// bookMutator is the read-modify-write slice: change a field on the stored
// row under its write lock. It deliberately has no GetBookByID/UpdateBook
// pair, so a whole-row write-back of a pre-read copy (the lost-update shape,
// audit A1#15) cannot be written against it. It carries the field-lock read
// beside the write, so a caller changing a user-lockable column goes through
// database.ModifyBookRespectingLocks and cannot be handed a store that skips it.
type bookMutator interface {
	database.BookModifier
	database.MetadataFieldStateReader
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

// bookSoftDeleter is what retiring a duplicate needs: read the row and write it
// back flagged, under the per-book lock (ModifyBook), so a concurrent write to
// the same row is neither reverted nor lost. It deliberately cannot DeleteBook — the hard-delete fallback
// that once justified that method was removed (a failed UpdateBook must not
// become a destroyed row), and taking the method away makes the fallback
// impossible to reintroduce by accident.
type bookSoftDeleter interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// seriesBookModifier is the one book write the series cleanup makes: a
// read-modify-write of a single row under that book's write lock. It carries the
// field-lock read beside it because the write changes SeriesID, which the user
// can lock (series_name): see database.ModifyBookRespectingLocks.
type seriesBookModifier interface {
	database.BookModifier
	database.MetadataFieldStateReader
}

// seriesUnlinker moves books off a series (under each book's write lock) and
// then removes the series row, but only if no book names it at that moment.
//
// It carries database.SeriesUnreferencedDeleter and deliberately NOT a plain
// DeleteSeries: the job decides to delete from membership and reference counts
// it read at the start of the run, and a book linked into the series after that
// read would be left naming a deleted series. The conditional delete counts and
// deletes under every book write lock (CLEANUP-SERIES-DELETE-NOT-CONDITIONAL).
type seriesUnlinker interface {
	seriesBookModifier
	database.SeriesUnreferencedDeleter
}

// seriesMerger is what csMergeSeriesGroup writes through. It deliberately has
// NO membership read: the caller hoists the complete (AllVersions) membership
// once per job via database.SeriesMembershipAllVersions and passes it in, so
// a per-series GetBooksBySeriesIDAllVersions inside the merge loop -- a full
// Pebble scan per series once memdb is tainted -- is a compile error here
// (SERIES-MERGE-PERSERIES-SCAN-COST). It has no plain DeleteSeries either, for
// the reason seriesUnlinker does not.
type seriesMerger interface {
	seriesBookModifier
	database.SeriesUnreferencedDeleter
}

// cleanupSeriesStore is what the cleanup-series job hands its two helpers: the
// job's store for the book writes and the conditional delete the store resolves
// as a capability (database.SeriesUnreferencedDeleter is not on database.Store,
// so maintenance.JobStore cannot carry it). Run builds it once, before any
// write, and refuses to run for real without the capability.
type cleanupSeriesStore struct {
	seriesBookModifier
	database.SeriesUnreferencedDeleter
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
	_ bookFileCreator         = (*database.PebbleStore)(nil)
	_ bookPager               = (*database.PebbleStore)(nil)
	_ userLister              = (*database.PebbleStore)(nil)
	_ userBookStateWriter     = (*database.PebbleStore)(nil)
	_ bookAggregateRecomputer = (*database.PebbleStore)(nil)
	_ bookSoftDeleter         = (*database.PebbleStore)(nil)
	_ seriesUnlinker          = (*database.PebbleStore)(nil)
	_ seriesMerger            = (*database.PebbleStore)(nil)
	_ seriesUnlinker          = cleanupSeriesStore{}
	_ seriesMerger            = cleanupSeriesStore{}
)
