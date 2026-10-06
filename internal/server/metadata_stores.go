// file: internal/server/metadata_stores.go
// version: 1.11.0
// guid: b8e04c27-5a91-4f36-9d18-2c73e5a081f4
// last-edited: 2026-10-06

package server

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
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
	// BookDirLister: metabatch.ResolveCandidateSearchQuery reads the other
	// rows in a book's folder (metabatch.SkipKindSiblingPart).
	database.BookDirLister
	// ImportPathReader: the resolver reads the import roots from this same
	// store, so an import root is never listed for sibling rows.
	metabatch.ImportPathReader
	// The owner-manual-only check (applygate.BulkManualOnlyGuard) reads a
	// book's series row and tags before the fetch spends a fallback
	// provider's quota on it (candidate_fallback.go).
	applygate.ManualOnlySeriesReader
	applygate.ManualOnlyTagReader

	GetBookByID(id string) (*database.Book, error)
}

// newFolderMemo returns a metabatch.FolderMemo for one pass over store (a
// candidate fetch op, a bulk fetch): each folder is listed once, and the
// import roots are read at most once per minute, both from store alone.
func (s *Server) newFolderMemo(store metabatch.FolderMemoStore) *metabatch.FolderMemo {
	return metabatch.NewFolderMemo(store)
}

// importRootsCachedBooks is a bookReader whose GetAllImportPaths is served by
// one metabatch.ImportRootsCache: every resolver call made through it during
// one apply call shares one import-path read, instead of each book reading
// the list up to three times (the gate guard, the transcribed-search check,
// the op-result check). Folder listings still go to the store per call: an
// apply moves files, so they are not cached across books.
type importRootsCachedBooks struct {
	bookReader
	roots *metabatch.ImportRootsCache
}

func (b *importRootsCachedBooks) GetAllImportPaths() ([]database.ImportPath, error) {
	return b.roots.GetAllImportPaths()
}

// withCachedImportPaths puts one import-root cache in front of books for the
// length of an apply call. Wrapping an already-wrapped reader is a no-op.
func withCachedImportPaths(books bookReader) bookReader {
	if books == nil {
		return nil
	}
	if _, ok := books.(*importRootsCachedBooks); ok {
		return books
	}
	return &importRootsCachedBooks{bookReader: books, roots: metabatch.NewImportRootsCache(books)}
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
