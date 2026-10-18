// file: internal/merge/store.go
// version: 1.15.0
// guid: 3f9a7c21-6d84-4e05-b13f-8a2c5e097d64
// last-edited: 2026-10-18

package merge

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// The store surface this package needs, measured with an empty-interface
// compiler probe under -gcflags=-e: 19 methods, no forwarding constraints. It
// was database.Store (398 methods) until 2026-08-19. On 2026-09-13
// playCountMarkStore added ModifyBook to the progress carry, making 20
// (GetBookByID was already counted, through bookReader). On 2026-10-18 the
// combine undo's removal of an author the combine created replaced CreateAuthor
// with MintAuthor and added GetAuthorByID, GetAuthorAliases,
// GetBooksByAuthorIDForRelinkCore and DeleteAuthor (net +4, all on
// database.Store).
//
// Grouped rather than declared flat so each consumer can name the slice it uses:
// the free functions in sync_follow.go need only user-progress methods.

// bookReader is the single book lookup the merge writers build on. Unexported:
// nothing outside this package names it (BookWriter embeds it).
type bookReader interface {
	GetBookByID(id string) (*database.Book, error)
}

// BookWriter covers the book mutations a merge performs. Exported because
// SoftDeleteBook is exported, so a caller that forwards its own store into it
// has to be able to name this requirement -- see internal/dedup.
type BookWriter interface {
	bookReader

	UpdateBook(id string, book *database.Book) (*database.Book, error)
	// ModifyBook is the read-modify-write every merge column write goes
	// through (database.BookMutator.ModifyBook): the callback sets only the
	// columns the site owns, so a concurrent writer's columns are not
	// reverted by a whole-row UpdateBook of a stale read.
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	DeleteBook(id string) error
	RecomputeBookAggregates(bookID string) error
}

type mergeBookFileStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	CreateBookFile(file *database.BookFile) error
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	MoveBookFilesToBookBulk(moves []database.BookFileMove, targetBookID string) error
}

type mergeAuthorStore interface {
	GetAuthorByName(name string) (*database.Author, error)
	// MintAuthor, not CreateAuthor: a combine's override journals that it
	// CREATED an author (CombineOverrideUndo.AuthorCreated) so its undo can
	// remove the row, and CreateAuthor cannot say whether this call wrote it.
	MintAuthor(name string) (*database.Author, bool, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	// The remainder removes an author the combine created and nothing credits
	// any more (removeUncreditedAuthor). DeleteAuthor sweeps the author out of
	// every book_authors row and drops its aliases, so it is only ever called
	// after the other three show there is nothing left to lose:
	// GetBooksByAuthorIDForRelinkCore is database.AuthorRelinkLister (every
	// book crediting the author in any role and any state, the legacy
	// Book.AuthorID included), GetAuthorByID tells a live row from one merged
	// away, GetAuthorAliases is what the delete would cascade.
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorAliases(authorID int) ([]database.AuthorAlias, error)
	GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error)
	DeleteAuthor(id int) error
}

// playCountMarkStore is the book leg of a progress carry. A carried finish
// brings the loser's ITunesPlayCountBumpedAt with it (carryPlayCountMark).
type playCountMarkStore interface {
	GetBookByID(id string) (*database.Book, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// userPositionStore is what carrying one user's listening progress across a
// merge requires. mergeUserProgressFor takes exactly this.
type userPositionStore interface {
	playCountMarkStore

	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	SetUserBookState(state *database.UserBookState) error
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
	ClearUserPositions(userID, bookID string) error
}

// UserProgressMerger adds the fan-out over every user. The Follow* entry points
// take this because that is the whole of what they forward. Exported for the
// same reason as BookWriter: used across packages (dedup, undo, audiobooks, scanner, ...).
type UserProgressMerger interface {
	userPositionStore

	ListUsers() ([]database.User, error)
	// SetRaw/DeleteRaw keep the durable pending-repair record a follow writes
	// before it moves anything and deletes only after every user's state and
	// bookmarks moved (see pending_repair.go). On the interface rather than
	// reached by a capability assertion: a store that silently lacked them
	// would turn a failed move back into a silent loss.
	SetRaw(key string, value []byte) error
	DeleteRaw(key string) error
	// ScanPrefix lists the pending records, so a merge can first complete
	// any left by an earlier merge of the same books.
	ScanPrefix(prefix string) ([]database.KVPair, error)
}

type mergeExternalIDReader interface {
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
}

// mergeVersionGroupStore loads every CURRENT live member of a version group.
// MergeBooks needs it to demote pre-existing members when it reuses an
// existing group's ID (VG-DOUBLE-PRIMARY): without it, a member that joined
// the group in a PRIOR merge and is absent from this call keeps its
// is_primary_version=true and the group ends up with two primaries.
//
// Deliberately its own interface rather than an addition to
// bookReader/BookWriter: BookWriter is exported precisely so internal/dedup
// can forward its own store into SoftDeleteBook, and widening either would
// force a method on callers that do not need it. Only Store composes this.
//
// It also carries versionprimary.EnsureStore: CombineBooks hands the primary
// flag on in each group a participant left (handOffLeftGroups), and MergeBooks
// does the same when its membership writes fail part-way; the hand-off reads
// the group, its members' files and chapter rows and writes the flags.
// MergeBooks also reads each left group's live members here, to move them
// with the loser (MergeBooks item 6).
type mergeVersionGroupStore interface {
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	versionprimary.EnsureStore
}

// syncCapabilityStore is deliberately `any`: FollowFileMove and
// followSyncFilesForBookChange never call a method on the store themselves.
// They hand it to database.AsSyncFileStore / AsSyncIdentityStore, which take
// `any` precisely because a decorator that embeds the Store interface
// and so hides every capability method from a static type. Naming a wider
// interface here would state a requirement that does not exist.
type syncCapabilityStore = any

// combineUndoStore is what the combine undo journal and UndoCombine need on
// top of the merge surface: raw keys for the journal itself (see
// combine_journal.go for why it is not the dedup auto-merge journal), point
// reads/writes of single file rows (the refuse-if-changed checks and the
// disc/track restore), and the reverse of each mapping the combine rewrote.
// Every method here is on database.Store, so a Store-embedding decorator
// forwards it without a capability assertion. Composed from three focused
// pieces so no single declaration is wide.
type combineUndoStore interface {
	combineJournalKV
	combineFileRowStore
	combineReverseMappingStore
}

// combineJournalKV stores the journal itself as raw keys.
type combineJournalKV interface {
	SetRaw(key string, value []byte) error
	journalKVReader
}

// journalKVReader reads the combine and sibling-move journals: a point read
// and a prefix scan. The undo-pointer backfill needs only this half.
type journalKVReader interface {
	GetRaw(key string) ([]byte, error)
	ScanPrefix(prefix string) ([]database.KVPair, error)
}

// combineFileRowStore reads and rewrites single file rows: the refuse-if-
// changed checks and the disc/track restore. It has no delete on purpose:
// book_file rows are never deleted, a combine's undo moves each row it
// created to the book whose audio it names, and leaving the method off the
// interface keeps any merge path from deleting one.
type combineFileRowStore interface {
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// combineReverseMappingStore reverses the mappings a combine rewrites:
// external IDs, the survivor's author links, and override lock rows.
type combineReverseMappingStore interface {
	GetBookByExternalID(source, externalID string) (string, error)
	ReassignExternalID(source, externalID, newBookID string) error
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	DeleteMetadataFieldState(bookID, field string) error
}

// Store is the whole merge surface, for Service and its constructor. Exported
// so a caller that constructs a Service can name it instead of database.Store.
type Store interface {
	BookWriter
	// UserOverrideRecorder: Combine's metadata override is a HUMAN's choice
	// of title/author/narrator for the survivor, so it must record a lock
	// row or the next fetch overwrites it (database.RecordUserOverrides).
	database.UserOverrideRecorder
	mergeBookFileStore
	mergeAuthorStore
	UserProgressMerger
	mergeExternalIDReader
	mergeVersionGroupStore
	combineUndoStore
}
