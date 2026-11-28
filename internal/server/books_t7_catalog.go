// file: internal/server/books_t7_catalog.go
// version: 1.1.0
// guid: 19f84de0-7c47-478d-a7da-459911477a6f
// last-edited: 2026-11-28

package server

// Group B of the t7 host adapter (see books_t7_adapter.go): the methods that
// only the kinds below reach, by kind file. Group B owns these files and the
// kinds in them (their Effects declarations, their head* shells and their
// cells); group A owns the rest, and no HEAD file belongs to both.
//
//	batch_apply_op.go                  metadata.batch-apply-cached
//	batch_apply_refusal_reset_op.go    metadata.batch-apply-refusals-reset
//	bulk_apply_preview.go              metadata.bulk-apply-preview
//	catalog_coverage_op.go             catalog.coverage-report
//	catalog_harvest_op.go              catalog.harvest-authors
//	collection_refresh_op.go           collections.refresh-relative-dynamic
//	duplicates_ops.go                  dedup.author-scan, dedup.book-combine, dedup.book-merge, dedup.book-scan, dedup.series-dedup, dedup.series-merge, dedup.series-normalize, dedup.series-prune, dedup.series-scan
//	entities_ops.go                    entities.author-merge, entities.resolve-production-author
//	identification_advance_op.go       identification.advance
//	metadata_candidate_op.go           metadata.candidate-fetch
//	metadata_ops.go                    library.bulk-metadata-fetch
//
// A method that a kind of each group reaches is in books_t7_adapter.go. Every
// method here returns a refusal that names it (ErrKindNotBuilt) until its group
// builds it, and the door leaves out every kind that reaches one.

import (
	"context"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/identification"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"golang.org/x/time/rate"
)

func (a *booksT7Store) CachedQueryMatchesIdentity(_ *metafetch.MetadataCandidateCache, _ *database.Book, _ []string, _ string) bool {
	booksT7Entered("CachedQueryMatchesIdentity")
	return false
}

func (a *booksT7Store) CatalogCoverageReady() error {
	return booksT7NotBuilt("CatalogCoverageReady")
}

func (a *booksT7Store) CatalogReady() error {
	return booksT7NotBuilt("CatalogReady")
}

func (a *booksT7Store) CatalogScope(_ context.Context) ([]catalog.ScopeAuthor, catalog.ScopeCensus, error) {
	return nil, catalog.ScopeCensus{}, booksT7NotBuilt("CatalogScope")
}

func (a *booksT7Store) CountAuthorNameBlocks() (int, error) {
	return 0, booksT7NotBuilt("CountAuthorNameBlocks")
}

func (a *booksT7Store) FilterReviewedAuthorGroups(_ []dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup {
	booksT7Entered("FilterReviewedAuthorGroups")
	return nil
}

func (a *booksT7Store) GetAllAuthorBookCounts() (map[int]int, error) {
	return nil, booksT7NotBuilt("GetAllAuthorBookCounts")
}

func (a *booksT7Store) GetBookFiles(_ string) ([]database.BookFile, error) {
	return nil, booksT7NotBuilt("GetBookFiles")
}

func (a *booksT7Store) GetCachedCandidates(_ string) (*metafetch.MetadataCandidateCache, bool, error) {
	return nil, false, booksT7NotBuilt("GetCachedCandidates")
}

func (a *booksT7Store) GetOperationResults(_ string) ([]database.OperationResult, error) {
	return nil, booksT7NotBuilt("GetOperationResults")
}

func (a *booksT7Store) GetRaw(_ string) ([]byte, error) {
	return nil, booksT7NotBuilt("GetRaw")
}

func (a *booksT7Store) GetUserPreference(_ string) (*database.UserPreference, error) {
	return nil, booksT7NotBuilt("GetUserPreference")
}

func (a *booksT7Store) HasAuthorKey(_ string) (bool, error) {
	return false, booksT7NotBuilt("HasAuthorKey")
}

func (a *booksT7Store) IdentBookIDs(_ []database.IdentState) (map[string]struct{}, error) {
	return nil, booksT7NotBuilt("IdentBookIDs")
}

func (a *booksT7Store) IdentDirtySource() (identification.Source, error) {
	return nil, booksT7NotBuilt("IdentDirtySource")
}

func (a *booksT7Store) ListCachedSummaries(_ context.Context) ([]metafetch.MetadataCacheSummary, error) {
	return nil, booksT7NotBuilt("ListCachedSummaries")
}

func (a *booksT7Store) LiveBookPathsUnderDir(_ string) (map[string]string, error) {
	return nil, booksT7NotBuilt("LiveBookPathsUnderDir")
}

func (a *booksT7Store) NewFolderMemo() *metabatch.FolderMemo {
	booksT7Entered("NewFolderMemo")
	return nil
}

func (a *booksT7Store) NewHarvester(_ catalogHarvestOpen) *catalog.Harvester {
	booksT7Entered("NewHarvester")
	return nil
}

func (a *booksT7Store) OpActor(_ string) (opActorIdentity, error) {
	return opActorIdentity{}, booksT7NotBuilt("OpActor")
}

func (a *booksT7Store) PreviewMetadataCandidateWithOptions(_ string, _ metafetch.MetadataCandidate, _ bool, _ metafetch.ApplyOptions) (*metafetch.ApplyPreview, error) {
	return nil, booksT7NotBuilt("PreviewMetadataCandidateWithOptions")
}

func (a *booksT7Store) PreviewRefreshRelativeDynamic(_ context.Context, _ func(done int, total int)) (handlers.RelativeRefreshResult, error) {
	return handlers.RelativeRefreshResult{}, booksT7NotBuilt("PreviewRefreshRelativeDynamic")
}

func (a *booksT7Store) PreviewSeriesDedup(_ context.Context, _ string, _ dedup.ProgressReporter) (dedup.SeriesDedupResult, error) {
	return dedup.SeriesDedupResult{}, booksT7NotBuilt("PreviewSeriesDedup")
}

func (a *booksT7Store) Ready() bool {
	booksT7Entered("Ready")
	return false
}

func (a *booksT7Store) ResolveFilter(_ context.Context, _ operations.FilterSpec, _ string) ([]string, error) {
	return nil, booksT7NotBuilt("ResolveFilter")
}

func (a *booksT7Store) ScanBookDuplicates(_ context.Context, _ map[string]bool, _ dedup.ProgressReporter) (dedup.BookScanResult, error) {
	return dedup.BookScanResult{}, booksT7NotBuilt("ScanBookDuplicates")
}

func (a *booksT7Store) ScanPrefix(_ string) ([]database.KVPair, error) {
	return nil, booksT7NotBuilt("ScanPrefix")
}

func (a *booksT7Store) ScanPrefixPage(_ string, _ string, _ int) (pairs []database.KVPair, next string, err error) {
	return nil, "", booksT7NotBuilt("ScanPrefixPage")
}

func (a *booksT7Store) ScanSeriesDuplicates(_ context.Context, _ dedup.ProgressReporter) (dedup.SeriesScanResult, error) {
	return dedup.SeriesScanResult{}, booksT7NotBuilt("ScanSeriesDuplicates")
}

func (a *booksT7Store) SelectUnfetched(_ context.Context) (unfetchedSelection, error) {
	return unfetchedSelection{}, booksT7NotBuilt("SelectUnfetched")
}

func (a *booksT7Store) ValidateCachedIdentityForBook(_ *metafetch.MetadataCandidateCache, _ *database.Book, _ []string) error {
	return booksT7NotBuilt("ValidateCachedIdentityForBook")
}

func (a *booksT7Writer) ApplySeriesDedup(_ context.Context, _ string, _ dedup.ScanStandDownController, _ dedup.ProgressReporter) (dedup.SeriesDedupResult, error) {
	return dedup.SeriesDedupResult{}, booksT7NotBuilt("ApplySeriesDedup")
}

func (a *booksT7Writer) AssignPublisher(_ string, _ string) error {
	return booksT7NotBuilt("AssignPublisher")
}

func (a *booksT7Writer) AssignResolvedAuthor(_ string, _ int, _ int) error {
	return booksT7NotBuilt("AssignResolvedAuthor")
}

func (a *booksT7Writer) AuthorMergeDone() {
	booksT7Entered("AuthorMergeDone")
}

func (a *booksT7Writer) AuthorsResolved() {
	booksT7Entered("AuthorsResolved")
}

func (a *booksT7Writer) ClaimBook(_ context.Context, _ string) (release func(), err error) {
	return nil, booksT7NotBuilt("ClaimBook")
}

func (a *booksT7Writer) CleanupMergeCandidates(_ []string) {
	booksT7Entered("CleanupMergeCandidates")
}

func (a *booksT7Writer) CombineBooks(_ context.Context, _ string, _ []string, _ string, _ *merge.CombineOverride) (*merge.CombineResult, error) {
	return nil, booksT7NotBuilt("CombineBooks")
}

func (a *booksT7Writer) CreateOperationResult(_ *database.OperationResult) error {
	return booksT7NotBuilt("CreateOperationResult")
}

func (a *booksT7Writer) DeleteRawBatch(_ []string) error {
	return booksT7NotBuilt("DeleteRawBatch")
}

func (a *booksT7Writer) EnqueueContinuation(_ context.Context, _ bulkMetadataFetchV2Params, _ opActorIdentity) (string, error) {
	return "", booksT7NotBuilt("EnqueueContinuation")
}

func (a *booksT7Writer) FetchAll(_ context.Context, _ string, _ operations.BulkMetadataFetchParams, _ operations.ProgressReporter) (incomplete bool, err error) {
	return false, booksT7NotBuilt("FetchAll")
}

func (a *booksT7Writer) FetchBookByTitle(_ context.Context, _ string) (*metafetch.FetchMetadataResponse, error) {
	return nil, booksT7NotBuilt("FetchBookByTitle")
}

func (a *booksT7Writer) FetchCandidate(_ context.Context, _ *rate.Limiter, _ string, _ string, _ bool, _ bool, _ *metabatch.FolderMemo, _ bool) CandidateResult {
	booksT7Entered("FetchCandidate")
	return CandidateResult{}
}

func (a *booksT7Writer) FetchForBookIDs(_ context.Context, _ string, _ []string, _ operations.BulkMetadataFetchParams, _ operations.ProgressReporter) (incomplete bool, err error) {
	return false, booksT7NotBuilt("FetchForBookIDs")
}

func (a *booksT7Writer) FinishHarvest(_ context.Context) {
	booksT7Entered("FinishHarvest")
}

func (a *booksT7Writer) InvalidateSeriesCache() {
	booksT7Entered("InvalidateSeriesCache")
}

func (a *booksT7Writer) MarkIdentDirty(_ string, _ string) bool {
	booksT7Entered("MarkIdentDirty")
	return false
}

func (a *booksT7Writer) MergeAuthors(_ context.Context, _ string, _ authorMergeSpec, _ func() bool, _ func(done int, total int)) (authorMergeOutcome, bool) {
	booksT7Entered("MergeAuthors")
	return authorMergeOutcome{}, false
}

func (a *booksT7Writer) MergeBooks(_ string, _ string, _ []string) (*merge.Result, []SkippedMergeLoser, error) {
	return nil, nil, booksT7NotBuilt("MergeBooks")
}

func (a *booksT7Writer) MergeSeries(_ context.Context, _ string, _ int, _ []int, _ string, _ dedup.ProgressReporter, _ dedup.MergeSeriesOptions) (dedup.SeriesMergeResult, error) {
	return dedup.SeriesMergeResult{}, booksT7NotBuilt("MergeSeries")
}

func (a *booksT7Writer) NormalizeSeries(_ context.Context, _ string) ([]string, error) {
	return nil, booksT7NotBuilt("NormalizeSeries")
}

func (a *booksT7Writer) OpenAdvance(_ identification.Source, _ string, _ func(identification.Summary)) (identification.Advance, error) {
	return identification.Advance{}, booksT7NotBuilt("OpenAdvance")
}

func (a *booksT7Writer) OpenBatchApply(_ context.Context) (batchApplyRun, error) {
	return nil, booksT7NotBuilt("OpenBatchApply")
}

func (a *booksT7Writer) OrganizeBook(_ *database.Book, _ string) error {
	return booksT7NotBuilt("OrganizeBook")
}

func (a *booksT7Writer) PruneSeries(_ context.Context, _ string, _ operations.ProgressReporter) error {
	return booksT7NotBuilt("PruneSeries")
}

func (a *booksT7Writer) Ready() error {
	return booksT7NotBuilt("Ready")
}

func (a *booksT7Writer) RefreshRelativeDynamic(_ context.Context, _ func(done int, total int)) (handlers.RelativeRefreshResult, error) {
	return handlers.RelativeRefreshResult{}, booksT7NotBuilt("RefreshRelativeDynamic")
}

func (a *booksT7Writer) RunHarvest(_ context.Context, _ *catalog.Harvester, _ []catalog.ScopeAuthor, _ func() error) (*catalog.Tally, error) {
	return nil, booksT7NotBuilt("RunHarvest")
}
