// file: internal/server/books_t7_adapter.go
// version: 1.0.0
// guid: 7571e5c9-96d7-4d5e-b964-9cd8c8e6b193
// last-edited: 2026-11-28

package server

// The host adapter that will build t7Deps for the twelve scheduled server-t7
// kinds (F5-18, TODO-F5-18-A6-SERVER-T7). It is one adapter over *Server, as
// registerAccountLifecycle is for the account lifecycle: it reuses the head*
// adapters' methods on the server and keeps every unexported server type
// unexported.
//
// DARK. Nothing outside tests calls BooksT7Kinds, and every method below
// refuses by name until the groups' commits fill it in: books_t7_library.go is
// group A (library.scan, library.scan-full, library.organize,
// library.size-refresh, reconcile.scan) and books_t7_catalog.go is group B
// (dedup.author-scan, dedup.series-prune, dedup.series-normalize,
// metadata.candidate-fetch, identification.advance,
// collections.refresh-relative-dynamic, catalog.harvest-authors). The kinds of
// the two groups reach disjoint methods, so a group file holds exactly the
// methods its own kinds' cores call. This file holds the types, the door and
// the rest of the two interfaces: the methods only the other thirty-three
// kinds of t7KindList reach, which stay refused for good because BooksT7Kinds
// never builds those kinds.
//
// Why every method is written out and none is promoted from an embedded
// interface: a nil embedded interface satisfies t7Store and t7Writer at
// compile time and then panics, unnamed, at the first call. A refusal that
// names its method is the only one that tells an operator what is missing.

import (
	"context"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/transcode"
	"github.com/falkcorp/mediacore/tasks"
)

// booksT7Adapter is the one host adapter: the server both views read. A method
// that needs a field of s which is unset refuses by name rather than
// dereferencing it (the fixture for that is &Server{}).
type booksT7Adapter struct{ s *Server }

// booksT7Store is the read side of the adapter, the t7Store of t7Deps.
type booksT7Store struct{ a *booksT7Adapter }

// booksT7Writer is the write side of the adapter for one run, the t7Writer a
// kind's core gets from t7NewWriter. w is the run's kernel-gated tasks.Writer,
// the one every write must go through (R10 item 4).
type booksT7Writer struct {
	a *booksT7Adapter
	w tasks.Writer
}

var (
	_ t7Store  = (*booksT7Store)(nil)
	_ t7Writer = (*booksT7Writer)(nil)
)

// booksT7NotBuilt is the refusal of a method this adapter does not build yet.
// A method with an error result returns it; a method without one panics with
// it, so the refusal still names the method (the availability probes, whose
// false is their refusal, are the exception).
func booksT7NotBuilt(method string) error {
	return fmt.Errorf("books t7: %s is not built", method)
}

// booksT7Deps is the t7Deps of the adapter over s. standDownKey is the
// concurrency key the library scan kinds run under, the same single source as
// Maintenance.StandDownKey in the registration inputs.
func (s *Server) booksT7Deps(standDownKey tasks.Key) t7Deps {
	a := &booksT7Adapter{s: s}
	return t7Deps{
		Store:        &booksT7Store{a: a},
		StandDownKey: standDownKey,
		NewWriter:    func(w tasks.Writer) t7Writer { return &booksT7Writer{a: a, w: w} },
	}
}

// BooksT7Kinds is the door the books module's registration will call: the twelve
// scheduled server-t7 kinds, built over the adapter. It returns the
// definitions, not the t7Deps, because t7Deps is unexported. sched is books'
// one schedule hook (R9). The list is spelled out and is not t7KindList's,
// which holds forty-five.
func (s *Server) BooksT7Kinds(standDownKey tasks.Key, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return booksT7KindList(s, s.booksT7Deps(standDownKey), sched)
}

// booksT7KindList is the twelve kinds of notYetBuilt's "server t7" rows.
func booksT7KindList(s *Server, deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		// group A: books_t7_library.go
		s.libraryScanNative(deps, sched),
		s.libraryScanFullNative(deps, sched),
		s.libraryOrganizeNative(deps, sched),
		s.librarySizeRefreshNative(deps, sched),
		s.reconcileScanNative(deps, sched),
		// group B: books_t7_catalog.go
		s.authorDedupScanNative(deps, sched),
		s.seriesPruneNative(deps, sched),
		s.seriesNormalizeNative(deps, sched),
		s.candidateFetchNative(deps, sched),
		s.identificationAdvanceNative(deps, sched),
		s.collectionRefreshNative(deps, sched),
		s.catalogHarvestNative(deps, sched),
	}
}

func (a *booksT7Store) CachedQueryMatchesIdentity(_ *metafetch.MetadataCandidateCache, _ *database.Book, _ []string, _ string) bool {
	panic(booksT7NotBuilt("CachedQueryMatchesIdentity"))
}

func (a *booksT7Store) CanMergeDeleteAuthors() bool {
	return false
}

func (a *booksT7Store) CatalogCoverageReady() error {
	return booksT7NotBuilt("CatalogCoverageReady")
}

func (a *booksT7Store) CollectDiagnosticsBooks() ([]database.BookCore, error) {
	return nil, booksT7NotBuilt("CollectDiagnosticsBooks")
}

func (a *booksT7Store) CountAuthorNameBlocks() (int, error) {
	return 0, booksT7NotBuilt("CountAuthorNameBlocks")
}

func (a *booksT7Store) DedupAvailable() bool {
	return false
}

func (a *booksT7Store) GetAuthorAliases(_ int) ([]database.AuthorAlias, error) {
	return nil, booksT7NotBuilt("GetAuthorAliases")
}

func (a *booksT7Store) GetAuthorByID(_ int) (*database.Author, error) {
	return nil, booksT7NotBuilt("GetAuthorByID")
}

func (a *booksT7Store) GetAuthorByName(_ string) (*database.Author, error) {
	return nil, booksT7NotBuilt("GetAuthorByName")
}

func (a *booksT7Store) GetBookAuthors(_ string) ([]database.BookAuthor, error) {
	return nil, booksT7NotBuilt("GetBookAuthors")
}

func (a *booksT7Store) GetBookByFilePath(_ string) (*database.Book, error) {
	return nil, booksT7NotBuilt("GetBookByFilePath")
}

func (a *booksT7Store) GetBookFiles(_ string) ([]database.BookFile, error) {
	return nil, booksT7NotBuilt("GetBookFiles")
}

func (a *booksT7Store) GetBookNarrators(_ string) ([]database.BookNarrator, error) {
	return nil, booksT7NotBuilt("GetBookNarrators")
}

func (a *booksT7Store) GetBookTagsDetailed(_ string) ([]database.BookTag, error) {
	return nil, booksT7NotBuilt("GetBookTagsDetailed")
}

func (a *booksT7Store) GetBooksByAuthorIDForRelinkCore(_ int) ([]database.BookCore, error) {
	return nil, booksT7NotBuilt("GetBooksByAuthorIDForRelinkCore")
}

func (a *booksT7Store) GetBooksByAuthorIDWithRoleCore(_ int) ([]database.BookCore, error) {
	return nil, booksT7NotBuilt("GetBooksByAuthorIDWithRoleCore")
}

func (a *booksT7Store) GetCachedCandidates(_ string) (*metafetch.MetadataCandidateCache, bool, error) {
	return nil, false, booksT7NotBuilt("GetCachedCandidates")
}

func (a *booksT7Store) GetImportPathByID(_ int) (*database.ImportPath, error) {
	return nil, booksT7NotBuilt("GetImportPathByID")
}

func (a *booksT7Store) GetNarratorByName(_ string) (*database.Narrator, error) {
	return nil, booksT7NotBuilt("GetNarratorByName")
}

func (a *booksT7Store) GetRaw(_ string) ([]byte, error) {
	return nil, booksT7NotBuilt("GetRaw")
}

func (a *booksT7Store) GetScan(_ int) (*database.Scan, error) {
	return nil, booksT7NotBuilt("GetScan")
}

func (a *booksT7Store) GetScanResults(_ int) ([]database.ScanResult, error) {
	return nil, booksT7NotBuilt("GetScanResults")
}

func (a *booksT7Store) GetSeriesByID(_ int) (*database.Series, error) {
	return nil, booksT7NotBuilt("GetSeriesByID")
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

func (a *booksT7Store) ListCachedSummaries(_ context.Context) ([]metafetch.MetadataCacheSummary, error) {
	return nil, booksT7NotBuilt("ListCachedSummaries")
}

func (a *booksT7Store) LiveBookPathsUnderDir(_ string) (map[string]string, error) {
	return nil, booksT7NotBuilt("LiveBookPathsUnderDir")
}

func (a *booksT7Store) LoadRawRoleFlags(_ int) (aiscan.RawRoleFlags, error) {
	return aiscan.RawRoleFlags{}, booksT7NotBuilt("LoadRawRoleFlags")
}

func (a *booksT7Store) OpActor(_ string) (opActorIdentity, error) {
	return opActorIdentity{}, booksT7NotBuilt("OpActor")
}

func (a *booksT7Store) PreviewMetadataCandidateWithOptions(_ string, _ metafetch.MetadataCandidate, _ bool, _ metafetch.ApplyOptions) (*metafetch.ApplyPreview, error) {
	return nil, booksT7NotBuilt("PreviewMetadataCandidateWithOptions")
}

func (a *booksT7Store) PreviewSeriesDedup(_ context.Context, _ string, _ dedup.ProgressReporter) (dedup.SeriesDedupResult, error) {
	return dedup.SeriesDedupResult{}, booksT7NotBuilt("PreviewSeriesDedup")
}

func (a *booksT7Store) Ready() bool {
	return false
}

func (a *booksT7Store) ResolveFilter(_ context.Context, _ operations.FilterSpec, _ string) ([]string, error) {
	return nil, booksT7NotBuilt("ResolveFilter")
}

func (a *booksT7Store) RunPurgePreview(_ context.Context, _ audiobooks.PurgeOptions) (*audiobooks.PurgeResult, error) {
	return nil, booksT7NotBuilt("RunPurgePreview")
}

func (a *booksT7Store) ScanApplyAvailable() bool {
	return false
}

func (a *booksT7Store) ScanBookDuplicates(_ context.Context, _ map[string]bool, _ dedup.ProgressReporter) (dedup.BookScanResult, error) {
	return dedup.BookScanResult{}, booksT7NotBuilt("ScanBookDuplicates")
}

func (a *booksT7Store) ScanFolder(_ context.Context, _ string, _ int, _ logger.Logger) ([]scanner.Book, error) {
	return nil, booksT7NotBuilt("ScanFolder")
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

func (a *booksT7Store) ValidateCachedIdentityForBook(_ *metafetch.MetadataCandidateCache, _ *database.Book, _ []string) error {
	return booksT7NotBuilt("ValidateCachedIdentityForBook")
}

func (a *booksT7Store) ValidateImportPath(_ string) (string, error) {
	return "", booksT7NotBuilt("ValidateImportPath")
}

func (a *booksT7Writer) ApplyScanResult(_ context.Context, _ string, _ database.ScanResult, _ aiscan.RawRoleFlags, _ bool, _ scanLeaseRenewer, _ func() bool) (scanApplyResult, bool) {
	panic(booksT7NotBuilt("ApplyScanResult"))
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
	panic(booksT7NotBuilt("AuthorMergeDone"))
}

func (a *booksT7Writer) AuthorsResolved() {
	panic(booksT7NotBuilt("AuthorsResolved"))
}

func (a *booksT7Writer) AutoOrganizeScanned(_ context.Context, _ []scanner.Book, _ logger.Logger) {
	panic(booksT7NotBuilt("AutoOrganizeScanned"))
}

func (a *booksT7Writer) CheckBookDuplicates(_ context.Context, _ string) (bool, error) {
	return false, booksT7NotBuilt("CheckBookDuplicates")
}

func (a *booksT7Writer) ClaimScanForApply(_ int, _ string) error {
	return booksT7NotBuilt("ClaimScanForApply")
}

func (a *booksT7Writer) CleanupMergeCandidates(_ []string) {
	panic(booksT7NotBuilt("CleanupMergeCandidates"))
}

func (a *booksT7Writer) CombineBooks(_ context.Context, _ string, _ []string, _ string, _ *merge.CombineOverride) (*merge.CombineResult, error) {
	return nil, booksT7NotBuilt("CombineBooks")
}

func (a *booksT7Writer) CreateAuthor(_ string) (*database.Author, error) {
	return nil, booksT7NotBuilt("CreateAuthor")
}

func (a *booksT7Writer) CreateAuthorAlias(_ int, _ string, _ string) (*database.AuthorAlias, error) {
	return nil, booksT7NotBuilt("CreateAuthorAlias")
}

func (a *booksT7Writer) CreateNarrator(_ string) (*database.Narrator, error) {
	return nil, booksT7NotBuilt("CreateNarrator")
}

func (a *booksT7Writer) CreateOperationChange(_ *database.OperationChange) error {
	return booksT7NotBuilt("CreateOperationChange")
}

func (a *booksT7Writer) DeleteAuthor(_ int) error {
	return booksT7NotBuilt("DeleteAuthor")
}

func (a *booksT7Writer) DeleteRawBatch(_ []string) error {
	return booksT7NotBuilt("DeleteRawBatch")
}

func (a *booksT7Writer) DownloadOLDump(_ string, _ string) error {
	return booksT7NotBuilt("DownloadOLDump")
}

func (a *booksT7Writer) EnqueueContinuation(_ context.Context, _ bulkMetadataFetchV2Params, _ opActorIdentity) (string, error) {
	return "", booksT7NotBuilt("EnqueueContinuation")
}

func (a *booksT7Writer) EnsureKeepIDs(_ int) error {
	return booksT7NotBuilt("EnsureKeepIDs")
}

func (a *booksT7Writer) EnsureOLStore(_ string) error {
	return booksT7NotBuilt("EnsureOLStore")
}

func (a *booksT7Writer) ExecuteReconcile(_ context.Context, _ string, _ reconcile.SaveResultFunc, _ []reconcile.ReconcileApplyItem, _ logger.Logger) error {
	return booksT7NotBuilt("ExecuteReconcile")
}

func (a *booksT7Writer) FetchAll(_ context.Context, _ string, _ operations.BulkMetadataFetchParams, _ operations.ProgressReporter) (incomplete bool, err error) {
	return false, booksT7NotBuilt("FetchAll")
}

func (a *booksT7Writer) FetchBookByTitle(_ context.Context, _ string) (*metafetch.FetchMetadataResponse, error) {
	return nil, booksT7NotBuilt("FetchBookByTitle")
}

func (a *booksT7Writer) FetchForBookIDs(_ context.Context, _ string, _ []string, _ operations.BulkMetadataFetchParams, _ operations.ProgressReporter) (incomplete bool, err error) {
	return false, booksT7NotBuilt("FetchForBookIDs")
}

func (a *booksT7Writer) ImportITunesLibrary(_ context.Context, _ string, _ itunesservice.ImportRequest, _ logger.Logger) error {
	return booksT7NotBuilt("ImportITunesLibrary")
}

func (a *booksT7Writer) ImportOLDumps(_ context.Context, _ operations.ProgressReporter, _ string, _ []string) error {
	return booksT7NotBuilt("ImportOLDumps")
}

func (a *booksT7Writer) InvalidateAuthorCaches() {
	panic(booksT7NotBuilt("InvalidateAuthorCaches"))
}

func (a *booksT7Writer) InvalidateDedupCache() {
	panic(booksT7NotBuilt("InvalidateDedupCache"))
}

func (a *booksT7Writer) InvalidateSeriesCaches() {
	panic(booksT7NotBuilt("InvalidateSeriesCaches"))
}

func (a *booksT7Writer) JournalAppliedResult(_ string, _ int, _ int) (void func() error, err error) {
	return nil, booksT7NotBuilt("JournalAppliedResult")
}

func (a *booksT7Writer) MarkOperationChangesReverted(_ string, _ []string) error {
	return booksT7NotBuilt("MarkOperationChangesReverted")
}

func (a *booksT7Writer) MarkResultApplied(_ int, _ int) error {
	return booksT7NotBuilt("MarkResultApplied")
}

func (a *booksT7Writer) MergeAuthors(_ context.Context, _ string, _ authorMergeSpec, _ func() bool, _ func(done int, total int)) (authorMergeOutcome, bool) {
	panic(booksT7NotBuilt("MergeAuthors"))
}

func (a *booksT7Writer) MergeBooks(_ string, _ string, _ []string) (*merge.Result, []SkippedMergeLoser, error) {
	return nil, nil, booksT7NotBuilt("MergeBooks")
}

func (a *booksT7Writer) MergeDeleteAuthor(_ string, _ int, _ int, _ string) []string {
	panic(booksT7NotBuilt("MergeDeleteAuthor"))
}

func (a *booksT7Writer) MergeSeries(_ context.Context, _ string, _ int, _ []int, _ string, _ dedup.ProgressReporter, _ dedup.MergeSeriesOptions) (dedup.SeriesMergeResult, error) {
	return dedup.SeriesMergeResult{}, booksT7NotBuilt("MergeSeries")
}

func (a *booksT7Writer) ModifyBook(_ string, _ func(*database.Book) error) (*database.Book, error) {
	return nil, booksT7NotBuilt("ModifyBook")
}

func (a *booksT7Writer) OpenBatchApply(_ context.Context) (batchApplyRun, error) {
	return nil, booksT7NotBuilt("OpenBatchApply")
}

func (a *booksT7Writer) OrganizeAfterWriteBack(_ string, _ string) (bool, error) {
	return false, booksT7NotBuilt("OrganizeAfterWriteBack")
}

func (a *booksT7Writer) ParseBooks(_ context.Context, _ []scanner.AIParseCandidate, _ logger.Logger) (scanner.AIPhaseSummary, error) {
	return scanner.AIPhaseSummary{}, booksT7NotBuilt("ParseBooks")
}

func (a *booksT7Writer) ProcessScanned(_ context.Context, _ []scanner.Book, _ int, _ logger.Logger, _ func(msg string)) error {
	return booksT7NotBuilt("ProcessScanned")
}

func (a *booksT7Writer) Ready() error {
	return booksT7NotBuilt("Ready")
}

func (a *booksT7Writer) ReassignBooksFromAuthor(_ string, _ int, _ int) (errs []string, problems []string, booksFailed int) {
	panic(booksT7NotBuilt("ReassignBooksFromAuthor"))
}

func (a *booksT7Writer) RecordVersion(_ context.Context, _ *database.Book, _ string, _ int, _ string, _ func(level string, msg string)) (newBook *database.Book, inPlace bool, err error) {
	return nil, false, booksT7NotBuilt("RecordVersion")
}

func (a *booksT7Writer) ReleaseScanApply(_ int, _ string) error {
	return booksT7NotBuilt("ReleaseScanApply")
}

func (a *booksT7Writer) RenameSeriesIfCurrent(_ int, _ string, _ string) error {
	return booksT7NotBuilt("RenameSeriesIfCurrent")
}

func (a *booksT7Writer) RunAIAuthorScan(_ context.Context, _ int, _ aiscan.ProgressSink) error {
	return booksT7NotBuilt("RunAIAuthorScan")
}

func (a *booksT7Writer) RunPurge(_ context.Context, _ audiobooks.PurgeOptions) (*audiobooks.PurgeResult, error) {
	return nil, booksT7NotBuilt("RunPurge")
}

func (a *booksT7Writer) RunQueuedApply(_ context.Context, _ metadatahandler.QueuedApply, _ func(msg string)) error {
	return booksT7NotBuilt("RunQueuedApply")
}

func (a *booksT7Writer) RunQueuedOpResultCandidate(_ context.Context, _ metadatahandler.QueuedApply, _ func(msg string)) error {
	return booksT7NotBuilt("RunQueuedOpResultCandidate")
}

func (a *booksT7Writer) RunQueuedOrganize(_ context.Context, _ string, _ func(msg string)) error {
	return booksT7NotBuilt("RunQueuedOrganize")
}

func (a *booksT7Writer) SetBookAuthors(_ string, _ []database.BookAuthor) error {
	return booksT7NotBuilt("SetBookAuthors")
}

func (a *booksT7Writer) SetBookNarrators(_ string, _ []database.BookNarrator) error {
	return booksT7NotBuilt("SetBookNarrators")
}

func (a *booksT7Writer) SetLastWrittenAt(_ string, _ time.Time) error {
	return booksT7NotBuilt("SetLastWrittenAt")
}

func (a *booksT7Writer) Transcode(_ context.Context, _ transcode.TranscodeOpts, _ operations.ProgressReporter) (string, error) {
	return "", booksT7NotBuilt("Transcode")
}

func (a *booksT7Writer) UpdateAuthorName(_ int, _ string) error {
	return booksT7NotBuilt("UpdateAuthorName")
}

func (a *booksT7Writer) UpdateImportPath(_ int, _ *database.ImportPath) error {
	return booksT7NotBuilt("UpdateImportPath")
}

func (a *booksT7Writer) WriteBackBook(_ context.Context, _ string) (int, error) {
	return 0, booksT7NotBuilt("WriteBackBook")
}

// The activity trio is shared: reconcile.scan (group A) and dedup.author-scan,
// series-prune and series-normalize (group B) both flush and emit through it.

func (a *booksT7Writer) EmitActivityInfo(_ string, _ string, _ string, _ string, _ ...string) {
	panic(booksT7NotBuilt("EmitActivityInfo"))
}

func (a *booksT7Writer) FlushActivityOp(_ string) {
	panic(booksT7NotBuilt("FlushActivityOp"))
}

func (a *booksT7Writer) LogActivityBatch(_ string, _ string, _ string, _ activity.BatchItem) {
	panic(booksT7NotBuilt("LogActivityBatch"))
}
