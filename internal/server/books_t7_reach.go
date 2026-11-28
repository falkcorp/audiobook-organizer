// file: internal/server/books_t7_reach.go
// version: 1.0.0
// guid: c2f262f4-9999-4a01-949b-e9cecee7dc53
// last-edited: 2026-11-28

package server

import "github.com/falkcorp/mediacore/tasks"

// booksT7Reaches is the call map of the t7 host adapter: for every kind
// t7KindList builds, the t7Store and t7Writer methods its core reaches. The
// door builds a kind only when every method of its row is built, so a stub's
// body is never entered through a kind it was left out of.
//
// It is derived from the kind cores by a static walk of the server package
// (the TestBooksT7Reaches_MatchTheCores cell redoes the walk and fails when a
// core reaches a method its row does not list). "Ready" names both the store
// probe and the writer's; a kind that reaches it needs both built.
var booksT7Reaches = map[tasks.Kind][]string{
	"books.ai.author-merge-apply":                {"CanMergeDeleteAuthors", "CreateAuthorAlias", "GetAuthorByID", "InvalidateDedupCache", "MergeDeleteAuthor", "ReassignBooksFromAuthor", "UpdateAuthorName"},
	"books.ai.author-review":                     {"GetAllAuthors", "GetBooksByAuthorIDWithRoleCore"},
	"books.ai.author-scan":                       {"RunAIAuthorScan"},
	"books.ai.author-scan-apply":                 {"ApplyScanResult", "ClaimScanForApply", "EnsureKeepIDs", "GetScan", "GetScanResults", "InvalidateAuthorCaches", "JournalAppliedResult", "LoadRawRoleFlags", "MarkResultApplied", "ReleaseScanApply", "ScanApplyAvailable"},
	"books.catalog.coverage-report":              {"CatalogCoverageReady", "CountAuthorNameBlocks", "GetAuthorByID", "GetBookAuthors", "GetBookByID", "HasAuthorKey", "IdentBookIDs"},
	"books.catalog.harvest-authors":              {"CatalogReady", "CatalogScope", "FinishHarvest", "NewHarvester", "RunHarvest"},
	"books.collections.refresh-relative-dynamic": {"PreviewRefreshRelativeDynamic", "RefreshRelativeDynamic"},
	"books.dedup.author-scan":                    {"EmitActivityInfo", "FilterReviewedAuthorGroups", "FlushActivityOp", "GetAllAuthorBookCounts", "GetAllAuthors"},
	"books.dedup.book-combine":                   {"CleanupMergeCandidates", "CombineBooks", "EmitActivityInfo", "FlushActivityOp", "InvalidateDedupCache"},
	"books.dedup.book-merge":                     {"CleanupMergeCandidates", "EmitActivityInfo", "FlushActivityOp", "InvalidateDedupCache", "MergeBooks"},
	"books.dedup.book-scan":                      {"EmitActivityInfo", "FlushActivityOp", "GetUserPreference", "ScanBookDuplicates"},
	"books.dedup.series-dedup":                   {"ApplySeriesDedup", "EmitActivityInfo", "FlushActivityOp", "InvalidateDedupCache", "PreviewSeriesDedup"},
	"books.dedup.series-merge":                   {"EmitActivityInfo", "FlushActivityOp", "InvalidateDedupCache", "MergeSeries"},
	"books.dedup.series-normalize":               {"EmitActivityInfo", "FlushActivityOp", "GetBookByID", "InvalidateSeriesCache", "NormalizeSeries", "OrganizeBook", "WriteBackBooks"},
	"books.dedup.series-prune":                   {"EmitActivityInfo", "FlushActivityOp", "PruneSeries"},
	"books.dedup.series-scan":                    {"EmitActivityInfo", "FlushActivityOp", "ScanSeriesDuplicates"},
	"books.diagnostics.ai-analyze":               {"CollectDiagnosticsBooks"},
	"books.entities.author-merge":                {"AuthorMergeDone", "MergeAuthors"},
	"books.entities.author-reclassify":           {"CreateAuthor", "CreateNarrator", "DeleteAuthor", "GetAuthorByID", "GetAuthorByName", "GetBookAuthors", "GetBookNarrators", "GetBooksByAuthorIDForRelinkCore", "GetNarratorByName", "InvalidateAuthorCaches", "ModifyBook", "SetBookAuthors", "SetBookNarrators"},
	"books.entities.resolve-production-author":   {"AssignPublisher", "AssignResolvedAuthor", "AuthorsResolved", "CreateAuthor", "FetchBookByTitle", "GetAuthorByID", "GetAuthorByName", "GetBookByID", "GetBooksByAuthorIDWithRoleCore"},
	"books.entities.series-rename":               {"CreateOperationChange", "GetSeriesByID", "InvalidateSeriesCaches", "MarkOperationChangesReverted", "RenameSeriesIfCurrent"},
	"books.identification.advance":               {"IdentDirtySource", "OpenAdvance"},
	"books.itunes.import":                        {"EmitActivityInfo", "FlushActivityOp", "ImportITunesLibrary"},
	"books.library.ai-parse":                     {"ParseBooks"},
	"books.library.bulk-metadata-fetch":          {"EnqueueContinuation", "FetchAll", "FetchForBookIDs", "OpActor", "Ready", "ResolveFilter"},
	"books.library.bulk-write-back":              {"EmitActivityInfo", "FlushActivityOp", "WriteBackBooks"},
	"books.library.folder-auto-scan":             {"AutoOrganizeScanned", "CheckBookDuplicates", "DedupAvailable", "EmitActivityInfo", "FlushActivityOp", "GetBookByFilePath", "GetImportPathByID", "ProcessScanned", "ScanFolder", "UpdateImportPath"},
	"books.library.import":                       {"PerformScan", "ValidateImportPath"},
	"books.library.organize":                     {"PerformOrganize"},
	"books.library.organize-when-scanned":        {"RunQueuedApply", "RunQueuedOpResultCandidate", "RunQueuedOrganize"},
	"books.library.purge-soft-deleted":           {"EmitActivityInfo", "FlushActivityOp", "LogActivityBatch", "RunPurge", "RunPurgePreview"},
	"books.library.scan":                         {"PerformScan"},
	"books.library.scan-full":                    {"PerformScan"},
	"books.library.size-refresh":                 {"GetAllImportPaths"},
	"books.library.transcode":                    {"GetBookByID", "RecordVersion", "Transcode"},
	"books.metadata.apply-when-scanned":          {"RunQueuedApply", "RunQueuedOpResultCandidate", "RunQueuedOrganize"},
	"books.metadata.batch-apply-cached":          {"OpenBatchApply", "Ready"},
	"books.metadata.batch-apply-refusals-reset":  {"DeleteRawBatch", "GetRaw", "ScanPrefixPage"},
	"books.metadata.batch-save":                  {"GetBookByID", "OrganizeAfterWriteBack", "SetLastWrittenAt", "WriteBackBook"},
	"books.metadata.bulk-apply-preview":          {"CachedQueryMatchesIdentity", "CreateOperationResult", "GetAllImportPaths", "GetAuthorByID", "GetBookAuthors", "GetBookByID", "GetBookFiles", "GetCachedCandidates", "GetOperationResults", "GetRaw", "ListCachedSummaries", "LiveBookPathsUnderDir", "PreviewMetadataCandidateWithOptions", "ScanPrefix", "ValidateCachedIdentityForBook"},
	"books.metadata.candidate-fetch":             {"ClaimBook", "CreateOperationResult", "FetchCandidate", "GetOperationResults", "MarkIdentDirty", "NewFolderMemo", "SelectUnfetched"},
	"books.openlibrary.download":                 {"DownloadOLDump"},
	"books.openlibrary.import":                   {"EnsureOLStore", "ImportOLDumps"},
	"books.reconcile.apply":                      {"EmitActivityInfo", "ExecuteReconcile", "FlushActivityOp"},
	"books.reconcile.scan":                       {"EmitActivityInfo", "FlushActivityOp", "RunReconcileScan"},
}

// booksT7Needs is the *Server fields a method's HEAD implementation reads
// (depth two from the head method, from the same walk), keyed by method. The
// door refuses a kind whose methods are all built and one of whose fields is
// nil, and names the field. Fields HEAD treats a nil as valid (the activity
// writer) are not listed. A group that builds a method adds the fields its body
// reads here, and the cell that walks the head methods fails when one is
// missing.
var booksT7Needs = map[string][]string{
	"ApplyScanResult":                {"store"},
	"ApplySeriesDedup":               {"store"},
	"AssignPublisher":                {"store"},
	"AssignResolvedAuthor":           {"store"},
	"AuthorMergeDone":                {"authorsCache", "dedupCache"},
	"AuthorsResolved":                {"authorsCache", "dedupCache"},
	"AutoOrganizeScanned":            {"organizeService", "store"},
	"CheckBookDuplicates":            {"dedupEngine"},
	"ClaimScanForApply":              {"aiScanStore"},
	"CleanupMergeCandidates":         {"dedupEngine"},
	"CollectDiagnosticsBooks":        {"diagnosticsService", "store"},
	"CombineBooks":                   {"mergeService", "store"},
	"CreateAuthor":                   {"store"},
	"CreateOperationResult":          {"store"},
	"DedupAvailable":                 {"dedupEngine"},
	"EnsureKeepIDs":                  {"pipelineManager"},
	"ExecuteReconcile":               {"store"},
	"FetchAll":                       {"metadataFetchService", "store"},
	"FetchBookByTitle":               {"metadataFetchService"},
	"FetchCandidate":                 {"metadataFetchService", "store"},
	"FetchForBookIDs":                {"metadataFetchService", "store"},
	"FilterReviewedAuthorGroups":     {"aiScanStore"},
	"GetAllAuthorBookCounts":         {"store"},
	"GetAllAuthors":                  {"store"},
	"GetAuthorByID":                  {"store"},
	"GetAuthorByName":                {"store"},
	"GetBookByFilePath":              {"store"},
	"GetBookByID":                    {"store"},
	"GetBooksByAuthorIDWithRoleCore": {"store"},
	"GetImportPathByID":              {"store"},
	"GetOperationResults":            {"store"},
	"GetRaw":                         {"store"},
	"GetScan":                        {"aiScanStore"},
	"GetScanResults":                 {"aiScanStore"},
	"GetUserPreference":              {"store"},
	"IdentDirtySource":               {"store"},
	"InvalidateAuthorCaches":         {"authorsCache", "dedupCache"},
	"InvalidateDedupCache":           {"dedupCache"},
	"InvalidateSeriesCache":          {"seriesCache"},
	"InvalidateSeriesCaches":         {"dedupCache", "seriesCache"},
	"JournalAppliedResult":           {"store"},
	"LiveBookPathsUnderDir":          {"store"},
	"LoadRawRoleFlags":               {"aiScanStore"},
	"MarkIdentDirty":                 {"store"},
	"MarkResultApplied":              {"aiScanStore"},
	"MergeAuthors":                   {"store"},
	"MergeBooks":                     {"mergeService", "store"},
	"MergeSeries":                    {"store"},
	"NewFolderMemo":                  {"store"},
	"NormalizeSeries":                {"store"},
	"OpActor":                        {"store"},
	"OpenAdvance":                    {"metadataFetchService", "store"},
	"OpenBatchApply":                 {"metadataFetchService", "store"},
	"OrganizeAfterWriteBack":         {"metadataFetchService", "organizeService", "store"},
	"OrganizeBook":                   {"organizeService"},
	"PerformOrganize":                {"organizeService"},
	"PerformScan":                    {"scanService"},
	"PreviewSeriesDedup":             {"store"},
	"PruneSeries":                    {"dedupCache", "seriesCache", "store"},
	"Ready":                          {"metadataFetchService", "store"},
	"RecordVersion":                  {"store"},
	"ReleaseScanApply":               {"aiScanStore"},
	"ResolveFilter":                  {"audiobookService", "store"},
	"RunQueuedApply":                 {"applyWhenScannedHandler"},
	"RunQueuedOpResultCandidate":     {"metadataFetchService", "store"},
	"RunQueuedOrganize":              {"queuedOrganizeRunner"},
	"RunReconcileScan":               {"store"},
	"ScanApplyAvailable":             {"aiScanStore", "pipelineManager"},
	"ScanBookDuplicates":             {"store"},
	"ScanPrefix":                     {"store"},
	"ScanPrefixPage":                 {"store"},
	"ScanSeriesDuplicates":           {"store"},
	"SelectUnfetched":                {"metadataFetchService", "opRegistry", "store"},
	"SetLastWrittenAt":               {"store"},
	"Transcode":                      {"store"},
	"UpdateImportPath":               {"store"},
	"ValidateImportPath":             {"store"},
	"WriteBackBook":                  {"metadataFetchService"},
	"WriteBackBooks":                 {"metadataFetchService", "store"},
}
