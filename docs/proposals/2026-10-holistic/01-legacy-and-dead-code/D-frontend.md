<!-- file: docs/proposals/2026-10-holistic/01-legacy-and-dead-code/D-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: 20604b99-8696-4cf6-af3c-3a3e47003b07 -->
<!-- last-edited: 2026-10-08 -->

# Appendix D: Frontend dead code (knip 5)

Method: `web/` copied to a scratch directory (no install into the repo), `npm ci --ignore-scripts`, then `npx knip@5 --reporter compact` (tests count as entries) and `npx knip@5 --production --include files,exports` (tests do not count). Line spans for `api.ts` were measured with the TypeScript compiler API; a function is counted as deletable only when it has 0 references inside `api.ts`.

Known false positives: `tests/e2e/*`, `tests/smoke/*`, `tests/visual/*`, `src/test/*` in production mode (test infrastructure); `tests/e2e/global-setup.ts` in default mode (Playwright loads it). "Unused exported types" are mostly Props types; un-exporting them is cosmetic and not counted.

## knip (default mode)

```
Unused files (10)
src/components/FingerprintVisualsColumn.tsx
src/components/LoadingSpinner.tsx
src/components/audiobooks/FileSelector.tsx
src/components/audiobooks/InlineEditField.tsx
src/components/audiobooks/MetadataDiffTable.tsx
src/components/audiobooks/TagEditor.tsx
src/components/filemanager/DirectoryTree.tsx
src/hooks/useTimeout.ts
src/pages/FileManager.tsx
tests/e2e/global-setup.ts
Unused devDependencies (1)
package.json: axios
Unlisted dependencies (1)
src/theme.slot-merge.test.ts: @mui/utils
Unused exports (40)
src/components/OperationActivityPanel.tsx: default
src/components/activity/compactDays.ts: MAX_COMPACT_DAYS
src/components/audiobooks/stagedMetadataApply.ts: startStagedApply, awaitStagedApply
src/components/dedup/BandFilterBar.tsx: BAND_ORDER
src/components/dedup/DedupAcousticTab.tsx: AcousticBookMetadata, AcousticBookCard
src/components/dedup/DedupEmbeddingTab.tsx: fetchBookFilesCached
src/components/dedup/FingerprintCanvas.tsx: FingerprintCanvas
src/components/dedup/dedupHelpers.tsx: PAGE_SIZE_OPTIONS
src/components/layout/operationsFormat.ts: formatOperationType
src/components/library/LibrarySoftDeletedSection.tsx: progressText, progressCaption
src/components/review/RepairsPanel.tsx: ageOf
src/components/review/dedupPipeline.ts: MAX_RETRY_BACKOFF_MS
src/components/review/evidence/EvidencePanel.tsx: default
src/components/review/evidence/signalLabels.ts: SIGNAL_LABELS, EXACT_RULE_LABELS
src/components/review/lanes/useDupesLane.ts: DUPES_SEARCH_DEBOUNCE_MS, BULK_LIST_LOADING_REASON, isKeyboardShortcutSuppressed
src/components/review/lanes/useMetadataLane.ts: pinOfCandidate, PER_BOOK_CONCURRENCY, chunk, filtersMatchLevel, saveReviewLevel, saveLanguageFilter, saveReviewPageSize, normalizeLanguage, reviewProviderID, candidateKey, isMarkedNoMatch
src/components/review/lanes/useRegroupLane.ts: REGROUP_FETCH_LIMIT, REGROUP_FETCH_TIMEOUT_MS, searchTextFor, compareItems
src/components/review/lanes/useRepairsLane.ts: REPAIRS_DEFAULT_PAGE_SIZE, REPAIRS_POLL_INTERVAL_MS, REPAIRS_PAGE_SIZE_STORAGE_KEY
src/components/review/repairs/RepairsDetailsView.tsx: FETCH_TIMEOUT_MS
src/components/review/spine/CandidatesCard.tsx: candidateIdentity
src/components/review/spine/RegroupSpine.tsx: fetchBooksByIds, RecommendationPanel, ActionSelector, ItemActions, MemberFilesDetail
src/components/review/spine/candidateLoader.ts: fillableFields
src/components/settings/TempLoginTab.tsx: default
src/config/columnDefinitions.ts: formatRelativeDate
src/hooks/useAdvancedSettings.ts: ADVANCED_SETTINGS_STORAGE_KEY
src/hooks/useImportFolderHandlers.ts: interruptedScanState
src/hooks/useLibraryQuery.ts: SOFT_DELETED_PAGE_SIZE
src/hooks/useLibraryScrollKeeper.ts: findScrollContainer
src/lib/reviewKinds.ts: REVIEW_KIND_LABELS
src/pages/BookDetail.tsx: mapBookToAudiobook
src/pages/Library.tsx: DEFAULT_ITEMS_PER_PAGE, MAX_ITEMS_PER_PAGE, clampItemsPerPage
src/services/activityApi.ts: ACTIVITY_REQUEST_TIMEOUT_MS (activityApi)
src/services/api.ts: SEARCH_POLL_TIMEOUT_MS, buildBookListParams, SEARCH_WALK_RESTARTS, searchBooksPage, countBooks, UPDATE_WARNINGS_SHOWN, getAnnouncements, getAuthorAliases, startBulkMetadataFetch, OperationPollTimeoutError, getOperationChanges, getBookChanges, getSystemLogs, listSessions, revokeSession, getVersionGroup, getITunesImportStatusBulk, searchMetadata, parseFilenameWithAI, getFieldMetadataHistory, getOperationResults, getRecentMetadataFetches, getPendingReview, getMetadataResults, batchApplyCandidates, batchRejectCandidates, batchUnrejectCandidates, requestAIAuthorReview, applyAIAuthorReview, deleteAIScan, compareAIScans, previewRename, applyRename, getReconcilePreview, getBookUserTags, setBookUserTags, addBookUserTag, removeBookUserTag, triggerDedupRefresh, getAPIKey (api)
src/services/playlistApi.ts: reorderPlaylist
src/services/readingApi.ts: getBookPosition, setBookPosition, listByStatus
src/stores/operationGrouping.ts: groupTimestamp
src/test/factories.ts: buildBook, buildAuthor, buildSeries
src/theme.ts: lightPalette, darkPalette, default
tests/e2e/e2e-env.ts: REPO_ROOT
tests/e2e/utils/test-helpers.ts: setupCommonRoutes
Unused exported types (63)
src/components/BatchToolbar.tsx: BatchToolbarProps
src/components/CoverLightbox.tsx: CoverLightboxProps
src/components/audiobooks/stagedMetadataApply.ts: StagedBookRef, StagedApplyStart, StagedApplyEnd, StagedBookPick
src/components/bookdetail/BookDetailActions.tsx: BookDetailActionsProps
src/components/bookdetail/BookDetailDialogs.tsx: BookDetailDialogsProps
src/components/bookdetail/BookDetailFilesTab.tsx: BookDetailFilesTabProps
src/components/bookdetail/BookDetailHeader.tsx: BookDetailHeaderProps
src/components/bookdetail/BookDetailInfoTab.tsx: BookDetailInfoTabProps
src/components/bookdetail/BookDetailStatusAlerts.tsx: BookDetailStatusAlertsProps
src/components/bookdetail/bookDetailUtils.ts: FileDiskStatus, FileDiskFields
src/components/common/BulkConfirmDialog.tsx: BulkConfirmDialogProps
src/components/common/PathLinks.tsx: PathLinksProps
src/components/common/SelectAllMatchingBanner.tsx: SelectAllMatchingBannerProps
src/components/dedup/LabelToggle.tsx: DedupLabelValue
src/components/library/TagCloud.tsx: TagCloudProps
src/components/library/libraryContentState.ts: LibraryContentState
src/components/review/ActionBar.tsx: ActionBarProps
src/components/review/CommandBar.tsx: CommandScope
src/components/review/DupesPanel.tsx: DupesPanelProps
src/components/review/MetadataPanel.tsx: MetadataPanelProps
src/components/review/QueueRail.tsx: QueueRailProps
src/components/review/RegroupPanel.tsx: RegroupPanelProps
src/components/review/RepairsPanel.tsx: RepairsPanelProps
src/components/review/ReplaceConfirmDialog.tsx: ReplaceConfirmDialogProps
src/components/review/SelectionBar.tsx: SelectionBarProps
src/components/review/dedupPipeline.ts: DedupRunInfo, FollowOptions
src/components/review/evidence/EvidencePanel.tsx: EvidencePanelProps
src/components/review/evidence/signalLabels.ts: PrimarySignal
src/components/review/lanes/index.ts: LaneDescriptor
src/components/review/lanes/useMetadataLane.ts: ReviewLevelFilters
src/components/review/spine/BookInfoPanel.tsx: BookInfoPanelProps
src/components/review/spine/DupesSpine.tsx: DupesSpineHandlers, DupesSpineProps, CandidateRowProps
src/components/review/spine/candidateLoader.ts: CandidateSearchFn, CandidateEntry, Limiter
src/components/review/useDedupPipeline.tsx: UseDedupPipelineOptions
src/config/columnDefinitions.ts: ColumnCategory
src/hooks/useAsyncAction.ts: AsyncActionState, UseAsyncActionReturn
src/hooks/useBackupHandlers.ts: UseBackupHandlersParams, UseBackupHandlersReturn
src/hooks/useColumnConfig.ts: UseColumnConfigReturn
src/hooks/useImportFolderHandlers.ts: UseImportFolderHandlersParams, UseImportFolderHandlersReturn
src/hooks/useKeyboardShortcuts.ts: ShortcutDefinition
src/hooks/useLibraryFilters.ts: LibraryFiltersResult
src/hooks/useLibraryQuery.ts: UseLibraryQueryFilters
src/hooks/useMetadataSourceHandlers.ts: UseMetadataSourceHandlersParams, UseMetadataSourceHandlersReturn
src/hooks/usePendingFileOps.ts: UsePendingFileOpsOptions, UsePendingFileOpsResult
src/hooks/useScopedTagFacets.ts: ScopedTagFacetsState
src/hooks/useServerMatchingCount.ts: UseServerMatchingCount
src/hooks/useSettingsHandlers.ts: SettingsState, UseSettingsHandlersParams, UseSettingsHandlersReturn
src/lib/reviewPayload.ts: EvidenceFact, ReviewActionSpec (reviewPayload)
src/lib/storageKeys.ts: StorageKey
src/pages/organizeRollback.ts: OrganizeRollbackResult
src/services/activityApi.ts: ActivityRequestOptions, ActivityResponse, ActivityFilter, SourcesResponse, CompactStarted, OperationActivityResponse, MergedOperationActivityResponse (activityApi)
src/services/api.ts: DeleteBookResponse, Series, ITunesValidateRequest, ITunesImportResponse, ITunesLibraryStatus, AIAuthorSuggestion, ApplyAISuggestion, OperationTimelineResponse, OperationSSEHandler, SystemStorage, SystemLogs, ITunesPathMap, AuthStatus, AuthSession, BookFacets, DiscardProgressResult, RescanBookResult, BatchUpdateResult, CoverTextResponse, Announcement, MergeAuthorsResult, DuplicatesResponse, MergeBooksResult, CombineBooksResult, CombineOverride, BookDedupScanResponse, AddImportPathDetailedResponse, OptimizeDatabaseResult, RetryResult, OperationChange, ValidateOpenAIKeyResponse, SeriesBookSummary, SeriesDedupResult, MetadataResult, MetadataScoreStep, AsinConflictRefusal, SearchMetadataResponse, QueuedBehindScan, WriteBackMetadataResponse, BulkWriteBackFilter, BulkWriteBackRequest, BulkWriteBackResponse, ExtractTrackInfoResponse, RelocateResult, BulkFetchMetadataResult, BulkFetchMetadataResponse, AIParseResult, BackupListResponse, BlockedHashesResponse, BatchFetchResponse, BatchFetchStartResponse, MetadataFetchSummary, CachedMetadataEntry, ReviewBucket, CachedReviewOptions, BatchApplySkip, BatchApplyFromCacheResult, BatchApplyDispatch, MetadataResultStatus, MetadataResultItem, MetadataResultsResponse, AIReviewMode, EnqueuedAIReview, EnqueuedOperation, AIScanComparison, TagChange, RenamePreview, RenameApplyResult, OrganizeResult, LatestReconcileScan, BulkRejectDedupResult, RevertBulkRejectResult, ClusterMergeResult, SeriesMergeResult, SplitBookCandidatesResponse, SplitBookMergeResult, BulkSplitBookMergeResponse, CreateAPIKeyRequest, CreateAPIKeyResponse, DuplicateFileInfo, MetadataHashDupBook, BackfillHashesResult, MaintenanceJobsResult, ReviewCount, ReviewBulkRequest, RepairRisk (api)
src/services/eventSourceManager.ts: EventSourceListener, EventSourceStatusListener
src/services/fileOpsApi.ts: PendingFileOpsResponse
src/services/readingApi.ts: UserPosition
src/stores/useOperationsStore.ts: OperationLogEvent
src/theme.ts: SignalPalette
src/types/index.ts: Work, User, ApiError, QuickQuery, PaginationParams, PaginatedResponse
src/utils/activityTagColors.ts: TagChipProps
src/utils/apiFetch.ts: ApiFetchOptions
src/utils/operationPolling.ts: PollOptions, OperationUpdateCallback, OperationCompleteCallback, OperationErrorCallback
src/utils/queryGrammar.ts: ValueMatcher, TitleFilter
tests/e2e/utils/test-helpers.ts: MockMetadataSource, MockAuthUser
Duplicate exports (4)
src/components/OperationActivityPanel.tsx: OperationActivityPanel, default
src/components/review/evidence/EvidencePanel.tsx: EvidencePanel, default
src/components/settings/TempLoginTab.tsx: TempLoginTab, default
src/theme.ts: appTheme, default
```

## knip --production, unused files

```
Unused files (24)
src/components/FingerprintVisualsColumn.tsx
src/components/LoadingSpinner.tsx
src/components/audiobooks/FileSelector.tsx
src/components/audiobooks/InlineEditField.tsx
src/components/audiobooks/MetadataDiffTable.tsx
src/components/audiobooks/TagEditor.tsx
src/components/dedup/BulkActionBar.tsx
src/components/filemanager/DirectoryTree.tsx
src/components/filemanager/ImportPathCard.tsx
src/hooks/useTimeout.ts
src/pages/FileManager.tsx
src/pages/Library.metadata.ts
src/test/factories.ts
src/test/renderWithProviders.tsx
src/test/setup.ts
tests/e2e/check-spec-discovery.mjs
tests/e2e/e2e-env.ts
tests/e2e/global-setup.ts
tests/e2e/playwright.config.ts
tests/e2e/utils/demo-helpers.ts
tests/e2e/utils/setup-modes.ts
tests/e2e/utils/test-helpers.ts
tests/smoke/routes.mjs
tests/visual/compare-layout.mjs
```
