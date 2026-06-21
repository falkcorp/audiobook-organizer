// file: internal/server/handlers/duplicates/interfaces.go
// version: 1.1.0
// guid: a04e0263-a6b1-42b9-9791-1b8b649004b5
// last-edited: 2026-06-21

// Narrow dependency interfaces for the duplicates-domain HTTP handlers
// (SQL-backed book/author/series duplicate detection, async merge / dismiss /
// scan triggers, series prune / normalize preview + apply, and dedup-entry
// metadata validation). Each interface lists only what the 17 handlers actually
// call so package duplicates stays decoupled from the concrete store / service /
// registry implementations and never imports package server (which would create
// an import cycle).
//
// Helpers that the handlers share with files that STAY in package server
// (duplicates_ops.go, server_maintenance_deps.go) are NOT abstracted here — they
// are relocated to internal/server/duplicates_helpers.go (package server) and
// the controller injects thin func closures that wrap them (so the handler can
// call them without a server import). See handler.go's injected func fields.

package duplicates

import (
	"context"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// DuplicatesStore is the narrow database.Store subset the duplicates handlers
// require. The concrete database.Store implementations satisfy it. scan / merge
// / refresh / dedup / prune / normalize triggers create the legacy operation row
// (CreateOperation); mergeBooks resolves the keep book (GetBookByID);
// mergeSeriesGroup resolves the keep series (GetSeriesByID). Resolved lazily
// through a provider closure (getStore) so a router-integration test that swaps
// server.store post-wire is still observed (mirrors the dedup / system handler
// getStore seam).
type DuplicatesStore interface {
	CreateOperation(id, opType string, folderPath *string) (*database.Operation, error) // OperationStore
	GetBookByID(id string) (*database.Book, error)                                      // BookStore
	GetSeriesByID(id int) (*database.Series, error)                                     // SeriesStore
}

// MergeService is the narrow *merge.Service subset used by
// mergeBookDuplicatesAsVersions. The concrete *merge.Service satisfies it.
// MergeBooks returns the concrete *merge.Result; the handler reads .MergedCount,
// .VersionGroupID and .PrimaryID off it. The service is reached through a
// provider closure (getMergeService) so the original nil-fallback
// (merge.NewService(s.Store()) when s.mergeService is nil) is preserved by the
// controller without this package importing internal/server.
type MergeService interface {
	MergeBooks(bookIDs []string, primaryID string) (*merge.Result, error)
	// CombineBooks combines several single-file books into ONE multi-file book
	// on the survivor (primaryID) and hard-deletes the absorbed shells. Distinct
	// from MergeBooks, which links them as alternate versions in a version group.
	CombineBooks(bookIDs []string, primaryID string) (*merge.CombineResult, error)
}

// MetadataFetchService is the narrow *metafetch.Service subset used by
// validateDedupEntry. The concrete *metafetch.Service satisfies it.
// BuildSourceChain returns the configured metadata sources, each of which the
// handler probes via metadata.MetadataSource.SearchByTitle / Name.
type MetadataFetchService interface {
	BuildSourceChain() []metadata.MetadataSource
}

// AudiobookService is the narrow *audiobookspkg.AudiobookService subset used by
// listDuplicateAudiobooks. The concrete service satisfies it. GetDuplicateBooks
// returns the SQL-grouped duplicate result the handler caches + serializes.
type AudiobookService interface {
	GetDuplicateBooks(ctx context.Context) (*audiobookspkg.DuplicatesResult, error)
}

// OperationsRegistry is the narrow operations-registry subset the duplicates
// scan/merge/refresh/dedup/prune/normalize triggers require: only EnqueueOp.
// The variadic opts param is preserved so the concrete *opsregistry.Registry
// satisfies the interface. The duplicates domain never calls Cancel.
type OperationsRegistry interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// Op-param re-exports. The handlers enqueue with the dedup-engine param structs.
// In package server these are referenced through type aliases defined in
// duplicates_ops.go (bookDedupScanOpParams = dedup.BookDedupScanParams, etc.);
// here the handlers reference the underlying dedup types directly (the aliases
// live in package server and cannot be imported without a cycle). Kept as local
// aliases so handler.go reads identically to the original.
type (
	bookDedupScanOpParams   = dedup.BookDedupScanParams
	bookMergeOpParams       = dedup.BookMergeParams
	authorDedupScanOpParams = dedup.AuthorDedupScanParams
	seriesDedupScanOpParams = dedup.SeriesDedupScanParams
	seriesDedupOpParams     = dedup.SeriesDedupParams
	seriesPruneOpParams     = dedup.SeriesPruneParams
	seriesMergeOpParams     = dedup.SeriesMergeParams
	seriesNormalizeOpParams = dedup.SeriesNormalizeParams
)
