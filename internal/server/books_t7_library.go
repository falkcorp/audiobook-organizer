// file: internal/server/books_t7_library.go
// version: 1.1.0
// guid: a3ced322-1920-49c4-bdb7-bbd69ed85636
// last-edited: 2026-11-28

package server

// Group A of the t7 host adapter (see books_t7_adapter.go): the methods that
// only the kinds below reach, by kind file. Group A owns these files and the
// kinds in them (their Effects declarations, their head* shells and their
// cells); group B owns the rest, and no HEAD file belongs to both.
//
//	ai_ops.go                          ai.author-merge-apply, ai.author-review
//	ai_scan_apply_op.go                ai.author-scan-apply
//	aiscan_op.go                       ai.author-scan
//	apply_when_scanned_op.go           library.organize-when-scanned, metadata.apply-when-scanned
//	author_reclassify_op.go            entities.author-reclassify
//	batch_save_op.go                   metadata.batch-save
//	diagnostics_ai_ops.go              diagnostics.ai-analyze
//	folder_autoscan_op.go              library.folder-auto-scan
//	itunes_ops.go                      itunes.import
//	library_ai_parse_op.go             library.ai-parse
//	library_core_ops.go                library.import, library.organize, library.scan, library.scan-full, library.transcode
//	library_size_refresh_op.go         library.size-refresh
//	library_writeback_op.go            library.bulk-write-back
//	openlibrary_ops.go                 openlibrary.download, openlibrary.import
//	purge_soft_deleted_op.go           library.purge-soft-deleted
//	reconcile_ops.go                   reconcile.apply, reconcile.scan
//	series_rename_ops.go               entities.series-rename
//
// A method that a kind of each group reaches is in books_t7_adapter.go. Every
// method here returns a refusal that names it (ErrKindNotBuilt) until its group
// builds it, and the door leaves out every kind that reaches one.

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/transcode"
)

func (a *booksT7Store) CanMergeDeleteAuthors() bool {
	booksT7Entered("CanMergeDeleteAuthors")
	return false
}

func (a *booksT7Store) CollectDiagnosticsBooks() ([]database.BookCore, error) {
	return nil, booksT7NotBuilt("CollectDiagnosticsBooks")
}

func (a *booksT7Store) DedupAvailable() bool {
	booksT7Entered("DedupAvailable")
	return false
}

func (a *booksT7Store) GetBookByFilePath(_ string) (*database.Book, error) {
	return nil, booksT7NotBuilt("GetBookByFilePath")
}

func (a *booksT7Store) GetBookNarrators(_ string) ([]database.BookNarrator, error) {
	return nil, booksT7NotBuilt("GetBookNarrators")
}

func (a *booksT7Store) GetBooksByAuthorIDForRelinkCore(_ int) ([]database.BookCore, error) {
	return nil, booksT7NotBuilt("GetBooksByAuthorIDForRelinkCore")
}

func (a *booksT7Store) GetImportPathByID(_ int) (*database.ImportPath, error) {
	return nil, booksT7NotBuilt("GetImportPathByID")
}

func (a *booksT7Store) GetNarratorByName(_ string) (*database.Narrator, error) {
	return nil, booksT7NotBuilt("GetNarratorByName")
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

func (a *booksT7Store) LoadRawRoleFlags(_ int) (aiscan.RawRoleFlags, error) {
	return aiscan.RawRoleFlags{}, booksT7NotBuilt("LoadRawRoleFlags")
}

func (a *booksT7Store) RunPurgePreview(_ context.Context, _ audiobooks.PurgeOptions) (*audiobooks.PurgeResult, error) {
	return nil, booksT7NotBuilt("RunPurgePreview")
}

func (a *booksT7Store) RunReconcileScan(_ context.Context, _ reconcile.SaveResultFunc, _ operations.ProgressReporter, _ reconcile.ScanHooks) error {
	return booksT7NotBuilt("RunReconcileScan")
}

func (a *booksT7Store) ScanApplyAvailable() bool {
	booksT7Entered("ScanApplyAvailable")
	return false
}

func (a *booksT7Store) ScanFolder(_ context.Context, _ string, _ int, _ logger.Logger) ([]scanner.Book, error) {
	return nil, booksT7NotBuilt("ScanFolder")
}

func (a *booksT7Store) ValidateImportPath(_ string) (string, error) {
	return "", booksT7NotBuilt("ValidateImportPath")
}

func (a *booksT7Writer) ApplyScanResult(_ context.Context, _ string, _ database.ScanResult, _ aiscan.RawRoleFlags, _ bool, _ scanLeaseRenewer, _ func() bool) (scanApplyResult, bool) {
	booksT7Entered("ApplyScanResult")
	return scanApplyResult{}, false
}

func (a *booksT7Writer) AutoOrganizeScanned(_ context.Context, _ []scanner.Book, _ logger.Logger) {
	booksT7Entered("AutoOrganizeScanned")
}

func (a *booksT7Writer) CheckBookDuplicates(_ context.Context, _ string) (bool, error) {
	return false, booksT7NotBuilt("CheckBookDuplicates")
}

func (a *booksT7Writer) ClaimScanForApply(_ int, _ string) error {
	return booksT7NotBuilt("ClaimScanForApply")
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

func (a *booksT7Writer) DownloadOLDump(_ string, _ string) error {
	return booksT7NotBuilt("DownloadOLDump")
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

func (a *booksT7Writer) ImportITunesLibrary(_ context.Context, _ string, _ itunesservice.ImportRequest, _ logger.Logger) error {
	return booksT7NotBuilt("ImportITunesLibrary")
}

func (a *booksT7Writer) ImportOLDumps(_ context.Context, _ operations.ProgressReporter, _ string, _ []string) error {
	return booksT7NotBuilt("ImportOLDumps")
}

func (a *booksT7Writer) InvalidateAuthorCaches() {
	booksT7Entered("InvalidateAuthorCaches")
}

func (a *booksT7Writer) InvalidateSeriesCaches() {
	booksT7Entered("InvalidateSeriesCaches")
}

func (a *booksT7Writer) JournalAppliedResult(_ string, _ int, _ int) (void func() error, err error) {
	return nil, booksT7NotBuilt("JournalAppliedResult")
}

func (a *booksT7Writer) LogActivityBatch(_ string, _ string, _ string, _ activity.BatchItem) {
	booksT7Entered("LogActivityBatch")
}

func (a *booksT7Writer) MarkOperationChangesReverted(_ string, _ []string) error {
	return booksT7NotBuilt("MarkOperationChangesReverted")
}

func (a *booksT7Writer) MarkResultApplied(_ int, _ int) error {
	return booksT7NotBuilt("MarkResultApplied")
}

func (a *booksT7Writer) MergeDeleteAuthor(_ string, _ int, _ int, _ string) []string {
	booksT7Entered("MergeDeleteAuthor")
	return nil
}

func (a *booksT7Writer) ModifyBook(_ string, _ func(*database.Book) error) (*database.Book, error) {
	return nil, booksT7NotBuilt("ModifyBook")
}

func (a *booksT7Writer) OrganizeAfterWriteBack(_ string, _ string) (bool, error) {
	return false, booksT7NotBuilt("OrganizeAfterWriteBack")
}

func (a *booksT7Writer) ParseBooks(_ context.Context, _ []scanner.AIParseCandidate, _ logger.Logger) (scanner.AIPhaseSummary, error) {
	return scanner.AIPhaseSummary{}, booksT7NotBuilt("ParseBooks")
}

func (a *booksT7Writer) PerformOrganize(_ context.Context, _ *OrganizeRequest, _ logger.Logger) error {
	return booksT7NotBuilt("PerformOrganize")
}

func (a *booksT7Writer) PerformScan(_ context.Context, _ *scanner.ScanRequest, _ logger.Logger) error {
	return booksT7NotBuilt("PerformScan")
}

func (a *booksT7Writer) ProcessScanned(_ context.Context, _ []scanner.Book, _ int, _ logger.Logger, _ func(msg string)) error {
	return booksT7NotBuilt("ProcessScanned")
}

func (a *booksT7Writer) ReassignBooksFromAuthor(_ string, _ int, _ int) (errs []string, problems []string, booksFailed int) {
	booksT7Entered("ReassignBooksFromAuthor")
	return
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
