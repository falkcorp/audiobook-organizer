// file: internal/server/metadata_stores.go
// version: 1.5.0
// guid: b8e04c27-5a91-4f36-9d18-2c73e5a081f4
// last-edited: 2026-09-29

package server

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
)

// Store slices for the metadata ops and the small pools around them. Each was
// database.Store -- 398 methods -- until 2026-08-19.

// operationResultStore is the op bookkeeping the bulk fetches share.
type operationResultStore interface {
	CreateOperationResult(result *database.OperationResult) error
	GetOperationResults(operationID string) ([]database.OperationResult, error)
}

// bulkMetadataFetchStore: runBulkMetadataFetchAll. The two cache helpers it
// forwards into already declare database.RawKVStore, so that is embedded by name.
type bulkMetadataFetchStore interface {
	bulkMetadataFetchCommon

	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	// For a book with no searchable title of its own: the full row (BookCore
	// carries no transcription) and its files (resolveBulkFetchQuery).
	bulkFetchTitleStore
}

// bulkMetadataFetchCommon is what both bulk-fetch entry points share: the
// operation-result bookkeeping that gives them their resume semantics, the raw
// KV cache the fetch helpers read and write, and the author list.
type bulkMetadataFetchCommon interface {
	operationResultStore
	database.RawKVStore

	GetAllAuthors() ([]database.Author, error)
}

// bulkMetadataFetchByIDStore: runBulkMetadataFetchForBookIDs, which resolves
// individual books rather than paging the whole library.
type bulkMetadataFetchByIDStore interface {
	bulkMetadataFetchCommon

	GetBookByID(id string) (*database.Book, error)
	// A book with no searchable title of its own reads its files for a
	// stand-in title, and its authors to tell an author folder from a work
	// folder (resolveBulkFetchQuery; metabatch.SearchQueryReader carries
	// GetAuthorByID).
	metabatch.SearchQueryReader
}

// candidateFetchStore: fetchCandidateForBook. Note the helpers it forwards into
// are metabatch's, not metafetch's -- both packages export a
// BuildCandidateBookInfo and a LoadRejectedCandidateKeys with different
// signatures, and only the compiler distinguishes them.
type candidateFetchStore interface {
	metabatch.BookFilesGetter
	database.RawKVStore
	// BookAuthorReader resolves the live author the fetch hints and hashes
	// with (database.LiveBookAuthorNames).
	database.BookAuthorReader

	GetBookByID(id string) (*database.Book, error)
}

// metadataResultsReader is the cache-refresh path: it only reads op history.
type metadataResultsReader interface {
	GetRecentOperations(limit int) ([]database.Operation, error)
	ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error)
	GetOperationResults(operationID string) ([]database.OperationResult, error)
}

// rawKVWriter: FileIOPool persists pending file ops under a raw key prefix and
// scans them back on recovery. database.RawKVStore is exactly that surface.
type rawKVWriter = database.RawKVStore

// delugeAdapterStore is a pure forward into the deluge package's own interface.
type delugeAdapterStore = deluge.Store
