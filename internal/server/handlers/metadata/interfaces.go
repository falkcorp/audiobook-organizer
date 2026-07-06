// file: internal/server/handlers/metadata/interfaces.go
// version: 1.1.0
// guid: b1ab2e4a-1f73-42f2-955d-c4a30f0fbaac
// last-edited: 2026-07-05

// Narrow dependency interfaces for the metadata-domain HTTP handlers (the 19
// per-book + library metadata endpoints extracted from the server package's
// metadata_handlers.go: batch-update / validate / export / import, external
// search, per-book fetch / search / apply / mark-no-match / revert,
// metadata-rejections, copy-on-write version list / prune, write-back, bulk
// fetch + bulk write-back enqueue, batch write-back enqueue, the field
// enumeration endpoint, and the rating PATCH).
//
// Each interface lists only what the 19 handlers actually call so package
// metadatahandler stays decoupled from the concrete store / service / registry
// implementations and never imports package server (which would create an
// import cycle).
//
// The package is named metadatahandler (dir handlers/metadata) to avoid
// clashing with the existing internal/metadata package (imported here as
// metadatapkg) and internal/metafetch.
//
// The async-operation machinery that lived in the SAME source file
// (registryProgressAdapter, runBulkMetadataFetchAll / ForBookIDs,
// runBulkWriteBack, runIsbnEnrichment, runMetadataRefreshScan,
// resolveFilterToBookIDs, RegisterBulkMetadataFetchOp, init) is NOT abstracted
// here — it is referenced by 15+ server-resident files (every *_ops.go, plus
// server_maintenance_deps.go / metadata_batch_candidates.go /
// library_writeback_op.go / duplicates_ops.go), so it was relocated verbatim to
// internal/server/metadata_ops.go (package server) and stays on the *Server
// receiver. None of it is reachable from the 19 handlers.

package metadatahandler

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// MetadataStore is the narrow database.Store subset the metadata handlers
// require. It embeds database.BookStore (BookReader + BookWriter) so the
// handler can pass the live store directly to metadata.BatchUpdateMetadata /
// metadata.ImportMetadata (both take database.BookStore) without a widening
// cast, and adds the handful of non-book methods the handlers touch.
//
// Resolved lazily through a provider closure (getStore) so a router-integration
// test that swaps server.store post-wire is still observed (mirrors the dedup /
// duplicates / system / audiobooks handler getStore seam). The concrete
// database.Store implementations satisfy it.
type MetadataStore interface {
	database.BookStore

	// Author resolution (bulkFetchMetadata).
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)

	// Metadata rejections (markAudiobookNoMatch / handleGetMetadataRejections).
	AddMetadataRejection(r database.MetadataRejection) error
	GetMetadataRejections(bookID string) ([]database.MetadataRejection, error)

	// Copy-on-write snapshots (revert / list / prune CoW versions).
	RevertBookToVersion(id string, ts time.Time) (*database.Book, error)
	GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error)
	PruneBookSnapshots(id string, keepCount int) (int, error)

	// Filtered book queries (handleBulkWriteBack).
	// GetBooksByAuthorIDCore is Core-typed (STOREFID P3-W2) — already
	// required via the embedded database.BookStore above; redeclared here
	// (matching the GetBooksBySeriesID convention below) purely for the
	// "what this handler touches" documentation grouping.
	GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error)
	GetBooksBySeriesID(seriesID int) ([]database.Book, error)

	// Legacy supervisor op row (batchWriteBackAudiobooks).
	CreateOperation(id, opType string, folderPath *string) (*database.Operation, error)

	// Rating PATCH (handleUpdateBookRating).
	UpdateBookRating(id string, req database.UpdateBookRatingRequest) error
}

// MetadataFetchService is the narrow *metafetch.Service subset the metadata
// handlers call. The concrete *metafetch.Service satisfies it. Only the methods
// reached from the 19 HTTP handlers are listed — BuildSourceChain /
// ISBNEnrichment are used exclusively by the relocated async-op machinery
// (metadata_ops.go), so they are intentionally absent here.
//
// WriteBackMetadataForBook keeps the variadic segment filter so the single call
// (writeBackAudiobookMetadata) can invoke it both with and without a segment
// list, matching the original.
type MetadataFetchService interface {
	FetchMetadataForBook(id string) (*metafetch.FetchMetadataResponse, error)
	InvalidateCachedCandidates(bookID string) error
	GetCachedCandidates(bookID string) (*metafetch.MetadataCandidateCache, bool, error)
	FetchAndCache(ctx context.Context, bookID, query, author, narrator, series string, opts metafetch.SearchOptions) (*metafetch.MetadataCandidateCache, error)
	SearchMetadataForBookWithOptions(id, query, author, narrator, series string, opts metafetch.SearchOptions) (*metafetch.SearchMetadataResponse, error)
	ApplyMetadataCandidate(id string, candidate metafetch.MetadataCandidate, fields []string) (*metafetch.FetchMetadataResponse, error)
	ApplyMetadataFileIO(id string)
	WriteBackMetadataForBook(id string, segmentFilter ...[]string) (int, error)
	MarkNoMatch(id string) error
	RunApplyPipelineRenameOnly(id string, book *database.Book) error
	RecordChangeHistory(book *database.Book, meta metadata.BookMetadata, sourceName string)
	ApplyMetadataSystemTags(bookID, sourceName, language string)
}

// WriteBackEnqueuer is the narrow *itunesservice.WriteBackBatcher subset used by
// fetchAudiobookMetadata / applyAudiobookMetadata to queue books for iTunes
// auto write-back. Only Enqueue is used. Resolved through a provider closure
// (getWriteBack) because server.writeBackBatcher is swapped post-wire by
// integration tests and the original handlers read it at request time.
type WriteBackEnqueuer interface {
	Enqueue(bookID string)
}

// OperationsRegistry is the narrow operations-registry subset the
// handleBulkWriteBack / batchWriteBackAudiobooks triggers require: only
// EnqueueOp. The variadic opts param is preserved so the concrete
// *opsregistry.Registry satisfies the interface.
type OperationsRegistry interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// FileIOPool is the narrow *server.FileIOPool subset used by
// applyAudiobookMetadata to schedule the slow cover-embed / tag / rename file
// I/O off the request path. Only Submit is used; the in-method `pool != nil`
// guard is preserved by the controller passing a typed-nil-guarded value.
type FileIOPool interface {
	Submit(bookID string, fn func())
}
