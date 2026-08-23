// file: internal/merge/store.go
// version: 1.4.0
// guid: 3f9a7c21-6d84-4e05-b13f-8a2c5e097d64
// last-edited: 2026-08-23

package merge

import "github.com/falkcorp/audiobook-organizer/internal/database"

// The store surface this package needs, measured with an empty-interface
// compiler probe under -gcflags=-e: 19 methods, no forwarding constraints. It
// was database.Store (398 methods) until 2026-08-19.
//
// Grouped rather than declared flat so each consumer can name the slice it uses:
// the free functions in sync_follow.go need only user-progress methods, and
// BookTitle needs exactly one.

// BookReader is the single lookup collision.go needs. Exported because
// BookTitle is exported and internal/importer forwards its store into it.
type BookReader interface {
	GetBookByID(id string) (*database.Book, error)
}

// BookWriter covers the book mutations a merge performs. Exported because
// SoftDeleteBook is exported, so a caller that forwards its own store into it
// has to be able to name this requirement -- see internal/dedup.
type BookWriter interface {
	BookReader

	UpdateBook(id string, book *database.Book) (*database.Book, error)
	DeleteBook(id string) error
	RecomputeBookAggregates(bookID string) error
}

type mergeBookFileStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	CreateBookFile(file *database.BookFile) error
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
}

type mergeAuthorStore interface {
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
}

// userPositionStore is what carrying one user's listening progress across a
// merge requires. mergeUserProgressFor takes exactly this.
type userPositionStore interface {
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	SetUserBookState(state *database.UserBookState) error
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
	ClearUserPositions(userID, bookID string) error
}

// UserProgressMerger adds the fan-out over every user. The Follow* entry points
// take this because that is the whole of what they forward. Exported for the
// same reason as BookWriter: FollowMergeWithStore is called from internal/dedup.
type UserProgressMerger interface {
	userPositionStore

	ListUsers() ([]database.User, error)
}

type mergeExternalIDReader interface {
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
}

// mergeVersionGroupReader loads every CURRENT live member of a version group.
// MergeBooks needs it to demote pre-existing members when it reuses an
// existing group's ID (VG-DOUBLE-PRIMARY): without it, a member that joined
// the group in a PRIOR merge and is absent from this call keeps its
// is_primary_version=true and the group ends up with two primaries.
//
// Deliberately its own single-method interface rather than an addition to
// BookReader/BookWriter: those two are exported precisely so internal/importer
// and internal/dedup can forward their own stores into BookTitle and
// SoftDeleteBook, and widening either would force a method on callers that do
// not need it. Only Store composes this.
type mergeVersionGroupReader interface {
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// syncCapabilityStore is deliberately `any`: FollowFileMove and
// followSyncFilesForBookChange never call a method on the store themselves.
// They hand it to database.AsSyncFileStore / AsSyncIdentityStore, which take
// `any` precisely because the indexedStore decorator embeds the Store interface
// and so hides every capability method from a static type. Naming a wider
// interface here would state a requirement that does not exist.
type syncCapabilityStore = any

// Store is the whole merge surface, for Service and its constructor. Exported
// so a caller that constructs a Service can name it instead of database.Store.
type Store interface {
	BookWriter
	mergeBookFileStore
	mergeAuthorStore
	UserProgressMerger
	mergeExternalIDReader
	mergeVersionGroupReader
}
