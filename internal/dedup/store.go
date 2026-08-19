// file: internal/dedup/store.go
// version: 1.0.0
// guid: 6c17e2b9-3f48-4d95-8a20-7b5e1c904f36
// last-edited: 2026-08-19

package dedup

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/dataset"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// The store surface this package needs, measured with an empty-interface
// compiler probe under -gcflags=-e: 27 direct calls plus twelve forwarding
// constraints, exhaustive (no "too many errors" marker). It was database.Store
// -- 398 methods -- until 2026-08-19.
//
// Every forwarding constraint is EMBEDDED BY NAME rather than inlined. That
// costs one entry for N methods, keeps each group inside interfacebloat's limit
// of 8, and means this re-narrows on its own the next time any of those twelve
// narrows further -- no second edit here.

// dedupCollectorStores are the five candidate collectors this package runs.
// Each already declared its own interface; naming them keeps that intact.
type dedupCollectorStores interface {
	ExactFileHashStore
	ISBNASINStore
	MetaSrcHashStore
	DurationCollectorStore
	MetaFuzzyStore
}

// dedupForwardedStores is everything outside this package that the engine hands
// its store to.
type dedupForwardedStores interface {
	dedupCollectorStores

	database.BookTagSingletonStore
	dataset.AdapterSource
	merge.BookWriter
	merge.UserProgressMerger
}

// dedupCheckpointStores is the op checkpoint/resume surface.
type dedupCheckpointStores interface {
	operations.OperationStateReader
	operations.OperationStateWriter
	operations.OperationStateDeleter
}

type dedupBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	GetBookByFileHash(hash string) (*database.Book, error)
	GetBooksByMetadataSourceHash(hash string) ([]database.Book, error)
	GetBookAlternativeTitles(bookID string) ([]database.BookAlternativeTitle, error)
	GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error)
	GetDuplicateBooks() ([][]database.Book, error)
}

type dedupBookWriter interface {
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	DeleteBook(id string) error
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	RevertBookToVersion(id string, ts time.Time) (*database.Book, error)
}

type dedupAuthorStore interface {
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
}

type dedupSeriesStore interface {
	GetAllSeries() ([]database.Series, error)
	GetSeriesByID(id int) (*database.Series, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	DeleteSeries(id int) error
	UpdateSeriesName(id int, name string) error
}

type dedupDuplicateStore interface {
	GetDuplicateBooksByMetadataCore(threshold float64) ([][]database.BookCore, error)
	GetFolderDuplicatesCore() ([][]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByAcoustID(fingerprint string) (*database.BookFile, error)
	CreateOperationChange(change *database.OperationChange) error
}

// dedupStore is the whole surface, for Engine and the exported entry points.
type dedupStore interface {
	dedupForwardedStores
	dedupCheckpointStores
	dedupBookReader
	dedupBookWriter
	dedupAuthorStore
	dedupSeriesStore
	dedupDuplicateStore
}
