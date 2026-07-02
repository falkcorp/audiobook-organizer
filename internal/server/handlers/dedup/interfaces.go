// file: internal/server/handlers/dedup/interfaces.go
// version: 1.2.0
// guid: e84f746d-28e9-4c8a-9520-66191e582881
// last-edited: 2026-07-02

// Narrow dependency interfaces for the dedup-domain HTTP handlers (candidate /
// cluster / series listing, merge / dismiss / remove, bulk merge, stats,
// export, and the dedup / embed / acoustid / book-signature scan triggers).
// Each interface lists only what the handlers actually call so package
// deduphandler stays decoupled from the concrete store / merge-service / engine
// / registry implementations and never imports package server (which would
// create an import cycle).
//
// NAME NOTE: this sub-package is named `deduphandler` (dir
// internal/server/handlers/dedup/) so it does not clash with the dedup ENGINE
// package github.com/falkcorp/audiobook-organizer/internal/dedup, which is
// imported under its normal `dedup` name by handler.go (the DedupEngine
// interface below is the narrow seam onto *dedup.Engine, but it does not need
// the engine import here).

package deduphandler

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// DedupStore is the narrow database.Store subset the dedup handlers require.
// The concrete database.Store implementations satisfy it. listDedupCandidates /
// exportDedupCandidates / listDedupCandidateSeries / mergeDedupCandidateSeries
// resolve book / author / series records for the candidate-existence filter and
// the export enrichment; handleCompareAcoustID resolves the two books plus their
// files. Resolved lazily through a provider closure (getStore) so a router test
// that swaps server.store post-wire is still observed (mirrors the system
// handler's getStore seam).
type DedupStore interface {
	GetBookByID(id string) (*database.Book, error)           // BookStore
	GetAuthorByID(id int) (*database.Author, error)          // AuthorStore
	GetSeriesByID(id int) (*database.Series, error)          // SeriesStore
	GetBookFiles(bookID string) ([]database.BookFile, error) // BookFileStore
}

// EmbeddingStore is the narrow *database.EmbeddingStore subset the dedup
// handlers require. NOTE: in production the concrete *database.EmbeddingStore is
// injected directly (not via this interface) because it sees heavy multi-method
// use and is a clean database type. This interface exists only so generated
// mocks can stand in for it in handler tests — the handler holds the concrete
// pointer.
type EmbeddingStore interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
	GetCandidateByID(id int64) (*database.DedupCandidate, error)
	GetCandidateStats() ([]database.CandidateStat, error)
	UpdateCandidateStatus(id int64, status string) error
}

// MergeService is the narrow *merge.Service subset used by the merge endpoints
// (mergeDedupCandidate, mergeDedupCluster, bulkMergeDedupCandidates,
// mergeDedupCandidateSeries). The concrete *merge.Service satisfies it.
// MergeBooks returns the concrete *merge.Result; the merge handler reads
// .PrimaryID off it to compute merged-away book IDs.
type MergeService interface {
	MergeBooks(bookIDs []string, primaryID string) (*merge.Result, error)
}

// DedupEngine is the narrow *dedup.Engine subset used by:
//   - mergeDedupCandidate's post-merge orphan-candidate sweep (CleanupCandidatesAfterMerge)
//   - the T016 rescore endpoint (Rescore)
//
// The concrete *dedup.Engine satisfies it.
type DedupEngine interface {
	CleanupCandidatesAfterMerge(mergedAwayBookIDs []string) int
	// Rescore re-runs unified.ComposeScore over stored signal sets for every
	// pending candidate. When apply=false this is a dry-run (no writes);
	// apply=true persists new bands and scores. Returns a delta summary
	// suitable for the HTTP response body.
	Rescore(ctx context.Context, apply bool) (dedup.RescoreResult, error)
	// ReevaluateAcoustIDConflicts finds pending non-audio candidates whose two
	// books have clearly-disagreeing book-level AcoustID signatures (false
	// positives the new acoustid veto suppresses at emission). dryRun=true
	// reports counts + a sample without mutating; dryRun=false deletes them.
	ReevaluateAcoustIDConflicts(ctx context.Context, dryRun bool) (*dedup.AcoustIDConflictResult, error)
}

// OperationsRegistry is the narrow operations-registry subset the dedup scan
// triggers require: only EnqueueOp (dedup / embed / acoustid / book-signature
// scan starters). The variadic opts param is preserved so the concrete
// *opsregistry.Registry satisfies the interface. Unlike the operations domain,
// dedup never calls Cancel.
type OperationsRegistry interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}
