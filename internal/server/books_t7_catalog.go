// file: internal/server/books_t7_catalog.go
// version: 1.0.0
// guid: 19f84de0-7c47-478d-a7da-459911477a6f
// last-edited: 2026-11-28

package server

// Group B of the t7 host adapter (see books_t7_adapter.go): the methods the
// dedup, metadata, identification, collection and catalog kinds' cores call,
// and only those.
//
//	dedup.author-scan                   GetAllAuthors, GetAllAuthorBookCounts,
//	                                    FilterReviewedAuthorGroups
//	dedup.series-prune                  PruneSeries and the activity trio
//	dedup.series-normalize              GetBookByID, NormalizeSeries,
//	                                    InvalidateSeriesCache, OrganizeBook,
//	                                    WriteBackBooks and the activity trio
//	metadata.candidate-fetch            GetOperationResults, SelectUnfetched,
//	                                    NewFolderMemo, ClaimBook, FetchCandidate,
//	                                    CreateOperationResult, MarkIdentDirty
//	identification.advance              IdentDirtySource, OpenAdvance
//	collections.refresh-relative-dynamic PreviewRefreshRelativeDynamic,
//	                                    RefreshRelativeDynamic
//	catalog.harvest-authors             CatalogReady, CatalogScope, NewHarvester,
//	                                    RunHarvest, FinishHarvest
//
// The activity trio (FlushActivityOp, EmitActivityInfo, LogActivityBatch) is in
// books_t7_adapter.go: reconcile.scan (group A) and the dedup kinds (group B)
// both write through it. Every method refuses by name until it is built.

import (
	"context"

	"golang.org/x/time/rate"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/identification"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

func (a *booksT7Store) CatalogReady() error {
	return booksT7NotBuilt("CatalogReady")
}

func (a *booksT7Store) CatalogScope(_ context.Context) ([]catalog.ScopeAuthor, catalog.ScopeCensus, error) {
	return nil, catalog.ScopeCensus{}, booksT7NotBuilt("CatalogScope")
}

func (a *booksT7Store) FilterReviewedAuthorGroups(_ []dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup {
	panic(booksT7NotBuilt("FilterReviewedAuthorGroups"))
}

func (a *booksT7Store) GetAllAuthorBookCounts() (map[int]int, error) {
	return nil, booksT7NotBuilt("GetAllAuthorBookCounts")
}

func (a *booksT7Store) GetAllAuthors() ([]database.Author, error) {
	return nil, booksT7NotBuilt("GetAllAuthors")
}

func (a *booksT7Store) GetBookByID(_ string) (*database.Book, error) {
	return nil, booksT7NotBuilt("GetBookByID")
}

func (a *booksT7Store) GetOperationResults(_ string) ([]database.OperationResult, error) {
	return nil, booksT7NotBuilt("GetOperationResults")
}

func (a *booksT7Store) IdentDirtySource() (identification.Source, error) {
	return nil, booksT7NotBuilt("IdentDirtySource")
}

func (a *booksT7Store) NewFolderMemo() *metabatch.FolderMemo {
	panic(booksT7NotBuilt("NewFolderMemo"))
}

func (a *booksT7Store) NewHarvester(_ catalogHarvestOpen) *catalog.Harvester {
	panic(booksT7NotBuilt("NewHarvester"))
}

func (a *booksT7Store) PreviewRefreshRelativeDynamic(_ context.Context, _ func(done int, total int)) (handlers.RelativeRefreshResult, error) {
	return handlers.RelativeRefreshResult{}, booksT7NotBuilt("PreviewRefreshRelativeDynamic")
}

func (a *booksT7Store) SelectUnfetched(_ context.Context) (unfetchedSelection, error) {
	return unfetchedSelection{}, booksT7NotBuilt("SelectUnfetched")
}

func (a *booksT7Writer) ClaimBook(_ context.Context, _ string) (release func(), err error) {
	return nil, booksT7NotBuilt("ClaimBook")
}

func (a *booksT7Writer) CreateOperationResult(_ *database.OperationResult) error {
	return booksT7NotBuilt("CreateOperationResult")
}

func (a *booksT7Writer) FetchCandidate(_ context.Context, _ *rate.Limiter, _ string, _ string, _ bool, _ bool, _ *metabatch.FolderMemo, _ bool) CandidateResult {
	panic(booksT7NotBuilt("FetchCandidate"))
}

func (a *booksT7Writer) FinishHarvest(_ context.Context) {
	panic(booksT7NotBuilt("FinishHarvest"))
}

func (a *booksT7Writer) InvalidateSeriesCache() {
	panic(booksT7NotBuilt("InvalidateSeriesCache"))
}

func (a *booksT7Writer) MarkIdentDirty(_ string, _ string) bool {
	panic(booksT7NotBuilt("MarkIdentDirty"))
}

func (a *booksT7Writer) NormalizeSeries(_ context.Context, _ string) ([]string, error) {
	return nil, booksT7NotBuilt("NormalizeSeries")
}

func (a *booksT7Writer) OpenAdvance(_ identification.Source, _ string, _ func(identification.Summary)) (identification.Advance, error) {
	return identification.Advance{}, booksT7NotBuilt("OpenAdvance")
}

func (a *booksT7Writer) OrganizeBook(_ *database.Book, _ string) error {
	return booksT7NotBuilt("OrganizeBook")
}

func (a *booksT7Writer) PruneSeries(_ context.Context, _ string, _ operations.ProgressReporter) error {
	return booksT7NotBuilt("PruneSeries")
}

func (a *booksT7Writer) RefreshRelativeDynamic(_ context.Context, _ func(done int, total int)) (handlers.RelativeRefreshResult, error) {
	return handlers.RelativeRefreshResult{}, booksT7NotBuilt("RefreshRelativeDynamic")
}

func (a *booksT7Writer) RunHarvest(_ context.Context, _ *catalog.Harvester, _ []catalog.ScopeAuthor, _ func() error) (*catalog.Tally, error) {
	return nil, booksT7NotBuilt("RunHarvest")
}

func (a *booksT7Writer) WriteBackBooks(_ context.Context, _ string, _ []string, _ bool, _ int, _ operations.ProgressReporter, _ func(bookID string)) error {
	return booksT7NotBuilt("WriteBackBooks")
}
