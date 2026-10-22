// file: internal/server/handlers/duplicates/interfaces.go
// version: 1.8.0
// guid: a04e0263-a6b1-42b9-9791-1b8b649004b5
// last-edited: 2026-10-22

// Narrow dependency interfaces for the duplicates-domain HTTP handlers
// (SQL-backed author/series duplicate detection, async link / refresh
// triggers, combine and its journal lists, series prune preview + apply, and dedup-entry
// metadata validation). Each interface lists only what the handlers actually
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

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// DuplicatesStore is the narrow database.Store subset the duplicates handlers
// require. The concrete database.Store implementations satisfy it. mergeBooks
// resolves the keep book (GetBookByID); mergeSeriesGroup resolves the keep
// series (GetSeriesByID). Resolved lazily through a provider closure (getStore)
// so a router-integration test that swaps server.store post-wire is still
// observed (mirrors the dedup / system handler getStore seam).
//
// CreateOperation was dropped on 2026-08-22. The scan / merge / refresh / dedup
// / prune triggers used to mint a v1 operation row here purely to
// have something to return; they are v2-native now and the registry owns the
// row, so the method is not merely unused by these handlers — it is unreachable
// from them.
type DuplicatesStore interface {
	GetBookByID(id string) (*database.Book, error)  // BookStore
	GetSeriesByID(id int) (*database.Series, error) // SeriesStore
}

// MergeService is the narrow *merge.Service subset used by combineBooks and
// the combine / sibling-move journal list handlers. The concrete *merge.Service
// satisfies it. The service is reached through a provider closure
// (getMergeService) so the original nil-fallback (merge.NewService(s.Ops())
// when s.mergeService is nil) is preserved by the controller without this
// package importing internal/server.
//
// MergeBooks left this interface with the retired synchronous
// POST /audiobooks/duplicates/link handler (03-PR12): version linking now runs
// only as the dedup.book-merge op, which package server drives directly.
type MergeService interface {
	// PreflightLink is the read-only pre-check POST /audiobooks/link runs
	// before it queues dedup.book-merge: the refusals the merge would give
	// after running (soft-deleted keep book, iTunes-protected participant or
	// re-homed sibling), answered as a 409 up front.
	PreflightLink(keepID string, mergeIDs []string) error
	// CombineBooks combines several single-file books into ONE multi-file book
	// on the survivor (primaryID) and soft-deletes the absorbed shells, writing
	// an undo journal. Distinct from MergeBooks, which links them as alternate
	// versions in a version group.
	CombineBooks(bookIDs []string, primaryID string, override *merge.CombineOverride) (*merge.CombineResult, error)
	// ListCombineJournals lists the combine journals newest-first
	// (GET /merge/combine-journal, a read-only diagnostic kept by D73). The
	// POST /merge/undo/:journal_id route that reversed one is retired; the
	// operation revert (POST /operations/:id/revert) reverses a combine
	// through merge.Service.UndoCombine.
	ListCombineJournals(limit int) ([]merge.CombineJournal, error)
	// ListSiblingMoveJournals lists the sibling-move journals newest-first
	// (GET /merge/sibling-journal, a read-only diagnostic kept by D73). Every
	// MergeBooks that moves a sibling writes one. The sibling-undo route is
	// retired; the operation revert reverses the move.
	ListSiblingMoveJournals(limit int) ([]merge.SiblingMoveJournal, error)
}

// MetadataFetchService is the narrow *metafetch.Service subset used by
// validateDedupEntry. The concrete *metafetch.Service satisfies it.
// BuildSourceChain returns the configured metadata sources, each of which the
// handler probes via metadata.MetadataSource.SearchByTitle / Name.
type MetadataFetchService interface {
	BuildSourceChain() []metadata.MetadataSource
}

// DedupEngine is the narrow *dedup.Engine subset used to sweep orphaned
// dedup candidates after a merge/combine hard- or soft-deletes a book.
// Mirrors the identically-named interface in the dedup handler package
// (which has its own sweep after the Dedup page's per-candidate Merge
// button). The concrete *dedup.Engine satisfies it.
type DedupEngine interface {
	CleanupCandidatesAfterMerge(mergedAwayBookIDs []string) int
}

// OperationsRegistry is the narrow operations-registry subset the duplicates
// scan/merge/refresh/dedup/prune triggers require: only EnqueueOp.
// The variadic opts param is preserved so the concrete *opsregistry.Registry
// satisfies the interface. The duplicates domain never calls Cancel.
type OperationsRegistry interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// Op-param re-exports. The handlers enqueue with the dedup-engine param structs.
// In package server these are referenced through type aliases defined in
// duplicates_ops.go (bookMergeOpParams = dedup.BookMergeParams, etc.);
// here the handlers reference the underlying dedup types directly (the aliases
// live in package server and cannot be imported without a cycle). Kept as local
// aliases so handler.go reads identically to the original.
type (
	bookMergeOpParams       = dedup.BookMergeParams
	authorDedupScanOpParams = dedup.AuthorDedupScanParams
	seriesDedupScanOpParams = dedup.SeriesDedupScanParams
	seriesDedupOpParams     = dedup.SeriesDedupParams
	seriesPruneOpParams     = dedup.SeriesPruneParams
	seriesMergeOpParams     = dedup.SeriesMergeParams
)
