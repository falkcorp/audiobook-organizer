// file: internal/server/wire_handlers.go
// version: 2.19.0
// guid: f7a8b9c0-d1e2-3456-7890-abcdef012345
// last-edited: 2026-07-14

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	aibackendshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/aibackends"
	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	deduphandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup"
	duplicates "github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates"
	entities "github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
	operations "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
	system "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	toolshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/tools"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/gin-gonic/gin"
)

// wireHandlers instantiates handler structs and registers their routes.
// Called from Start() after the protected group is created.
// Route registration is delegated to per-domain wire*Routes methods.
func (s *Server) wireHandlers(api *gin.RouterGroup, authMiddleware gin.HandlerFunc, protected *gin.RouterGroup) {
	authH := handlers.NewAuthHandler(s.Store(), config.AppConfig.EnableAuth)
	apiKeyH := handlers.NewAPIKeyHandler(s.Store())

	s.wireAuthRoutes(api, authMiddleware, authH, apiKeyH)

	// ── Build split-book candidate store ─────────────────────────────────────
	var splitBookCands handlers.SplitBookCandidateStore
	if s.embeddingStore != nil {
		if db := s.embeddingStore.PebbleDB(); db != nil {
			splitBookCands = dedupengine.NewSplitBookStore(db)
		}
	}

	// ── Instantiate Phase 2 handlers ─────────────────────────────────────────
	cacheH := handlers.NewCacheHandler(s.metricsStore, s.Store())
	activityH := handlers.NewActivityHandler(s.activityService, s.Store())
	readingH := handlers.NewReadingHandler(s.Store())
	userH := handlers.NewUserHandler(s.Store())
	splitBookH := handlers.NewSplitBookHandler(s.opRegistry, splitBookCands, s.Store())
	metaCacheH := handlers.NewMetadataCacheHandler(s.Store(), s.metadataFetchService, s.writeBackBatcher)
	organizeH := handlers.NewOrganizeHandler(
		s.Store(),
		NewRenameService(s.Store()),
		NewOrganizePreviewService(s.Store()),
		s.organizeService,
		s.writeBackBatcher,
		s.eventBus,
		config.AppConfig.RootDir,
		config.AppConfig.AutoOrganize,
	)
	filesystemH := handlers.NewFilesystemHandler(
		s.Store(),
		s.filesystemService,
		s.importPathService,
		s.importService,
		s.opRegistry,
		s.eventBus,
		config.AppConfig.RootDir,
		config.AppConfig.AutoOrganize,
	)
	playlistH := handlers.NewPlaylistHandlerWithGetter(s.Store(), s.SearchIndex)
	pluginsH := handlers.NewPluginsHandler(s.pluginRegistry, config.AppConfig.Plugins)
	versionsH := handlers.NewVersionsHandler(s.Store())

	// Entities domain handler (authors/series/narrators/works). Guard typed-nil
	// boxing for each interface-typed dep so the handler's nil checks (and the
	// concrete pointers' own nil semantics) are preserved. workService and
	// authorSeriesService are concrete *struct pointers on Server that are
	// always constructed in NewServer, but the guards keep parity with the
	// established wiring pattern and are harmless.
	var entWorkSvc entities.WorkService
	if s.workService != nil {
		entWorkSvc = s.workService
	}
	var entAuthorSeriesSvc entities.AuthorSeriesService
	if s.authorSeriesService != nil {
		entAuthorSeriesSvc = s.authorSeriesService
	}
	var entOpReg entities.OperationsRegistry
	if s.opRegistry != nil {
		entOpReg = s.opRegistry
	}
	// enrichBooks mirrors the original getAuthorBooks/getSeriesBooks loop: one
	// batch fetch, then per-book enrichment in order. Returns a non-nil slice so
	// the JSON "items" field is [] (never null) for an empty book list.
	enrichBooks := func(books []database.Book) []any {
		bookIDs := make([]string, len(books))
		for i, b := range books {
			bookIDs[i] = b.ID
		}
		bookAuthorsMap, authorsByID, bookNarratorsMap, narratorsByID := s.batchFetchBookAuthorsAndNarrators(bookIDs)
		out := make([]any, len(books))
		for i := range books {
			out[i] = s.enrichBookForResponse(&books[i], bookAuthorsMap, authorsByID, bookNarratorsMap, narratorsByID)
		}
		return out
	}
	entitiesH := entities.New(
		s.Store(),
		entWorkSvc,
		entAuthorSeriesSvc,
		entOpReg,
		s.authorsCache,
		s.seriesCache,
		s.dedupCache,
		enrichBooks,
	)

	// Guard typed-nil boxing of the operations registry and event hub. Both are
	// concrete pointers on Server (*opsregistry.Registry / *opsregistry.EventHub)
	// that can legitimately be nil (e.g. container without an opregistry entry;
	// see server_queue_test.go). Boxing a nil pointer into an interface yields a
	// non-nil interface, which would defeat the handlers' `h.registry == nil` /
	// `h.hub == nil` guards (they mirror the old `s.opRegistry == nil` checks on
	// the concrete pointers) and panic instead of returning a clean 500/503.
	var opReg handlers.OperationsRegistry
	if s.opRegistry != nil {
		opReg = s.opRegistry
	}
	var opEventHub handlers.OperationsEventHub
	if s.opHub != nil {
		opEventHub = s.opHub
	}

	// Resolve the opsV2 store from the composite store (nil if unsupported).
	opsV2 := database.GetOpsV2(s.Store())
	opsV2H := handlers.NewOperationsV2Handler(opsV2, opReg, opEventHub)

	// Operations domain handler (scan/organize/optimize/transcode triggers,
	// operation status/logs/result/changes/revert, stale-op management, DB
	// optimize, tasks, maintenance window). Guard typed-nil boxing for each
	// interface-typed concrete-pointer dep: s.opRegistry (*opsregistry.Registry),
	// s.pipelineManager (*aiscan.PipelineManager) and s.aiScanStore
	// (*database.AIScanStore) can all legitimately be nil; boxing a nil concrete
	// pointer into the interface would yield a non-nil interface and defeat the
	// handler's in-method nil guards (which mirror the old `s.opRegistry == nil` /
	// `s.pipelineManager != nil && s.aiScanStore != nil` checks). opRegistry,
	// pipelineManager and aiScanStore are all wired before setupRoutes
	// (wireServerFromContainer) and aiScanStore is only re-nilled during
	// shutdown, so snapshotting them here is safe. s.scheduler is the exception:
	// it is assigned in Start() AFTER this runs, so it is passed as a lazy
	// provider closure (below) instead of a snapshot.
	var opsOpReg operations.OperationsRegistry
	if s.opRegistry != nil {
		opsOpReg = s.opRegistry
	}
	var opsPipeline operations.ScanCanceler
	if s.pipelineManager != nil {
		opsPipeline = s.pipelineManager
	}
	var opsScanStore operations.AIScanLister
	if s.aiScanStore != nil {
		opsScanStore = s.aiScanStore
	}
	// collectStale stays in package server (also called from server_lifecycle.go).
	// preflightUndo / revert wrap server-private re-export helpers that consume a
	// full database.Store opaquely; the controller closes over s.Store().
	operationsH := operations.New(
		s.Store(),
		opsOpReg,
		// Lazy scheduler provider: s.scheduler is assigned in Start() (after this
		// wire-time runs), so resolve it at request time. Guard inside the
		// closure so a nil *scheduler.TaskScheduler is not boxed into a non-nil
		// interface (which would defeat the handler's nil check).
		func() operations.Scheduler {
			if s.scheduler == nil {
				return nil
			}
			return s.scheduler
		},
		opsPipeline,
		opsScanStore,
		s.collectStaleOperations,
		func(id string) (*undo.UndoConflictReport, error) {
			return undo.PreflightUndoConflicts(s.Store(), id)
		},
		func(id string) error {
			return NewRevertService(s.Store()).RevertOperation(id)
		},
	)
	// getSystemLogs (system handler) delegates its operation_id branch to
	// operationsH.GetOperationLogs; stash it on the Server for that call.
	s.operationsHandler = operationsH

	// System domain handler (health/status/announcements/storage/logs/
	// activity-log/reset/factory-reset/config/SSE events/backups/dashboard/
	// blocked-hashes/user-preferences/policy-tags/quick-queries). Guard typed-nil
	// boxing for each interface-typed concrete-pointer dep so the handler's
	// in-method nil guards (which mirror the old `s.X == nil` / `s.X != nil`
	// checks on the concrete pointers) are preserved: boxing a nil concrete
	// pointer into an interface yields a non-nil interface and would defeat them.
	// s.systemService / s.configUpdateService are concrete *struct pointers always
	// constructed in NewServer, but the guards keep parity with the established
	// pattern and are harmless. s.pluginRegistry can legitimately be nil
	// (HealthCheckAll skipped). s.hub is passed as a LAZY provider closure (not a
	// snapshot): handleEvents read s.hub at request time, and a test nils s.hub
	// AFTER wiring to drive the SSE 503 guard — snapshotting it here would capture
	// a live hub and invoke HandleSSE instead of 503 (mirrors the operations
	// getScheduler seam). s.olService is passed as a CONCRETE
	// pointer (factoryReset reaches its .Mu / .OLStore fields, which an interface
	// cannot abstract); the handler nil-checks it directly. s.operationsHandler
	// (set just above) backs OperationLogsProvider. The store is passed as a LAZY
	// provider closure (not a snapshot): the original handlers read s.Store() at
	// request time, and a router-integration test swaps server.store post-wire to
	// inject a mock — snapshotting would miss it. s.Store() returns the
	// database.Store interface, so a nil store stays a nil interface.
	var sysSvc system.SystemService
	if s.systemService != nil {
		sysSvc = s.systemService
	}
	var sysConfigUpdate system.ConfigUpdateService
	if s.configUpdateService != nil {
		sysConfigUpdate = s.configUpdateService
	}
	var sysPlugins system.PluginHealthChecker
	if s.pluginRegistry != nil {
		sysPlugins = s.pluginRegistry
	}
	var sysOpLogs system.OperationLogsProvider
	if s.operationsHandler != nil {
		sysOpLogs = s.operationsHandler
	}
	systemH := system.New(
		// Lazy store provider: resolve s.Store() at request time (late binding, as
		// the original handlers did). A test swaps server.store post-wire, so a
		// snapshot would miss it. s.Store() returns the database.Store interface,
		// so a nil store stays a nil interface and the handler's store==nil guards
		// hold.
		func() system.SystemStore { return s.Store() },
		sysSvc,
		sysConfigUpdate,
		sysPlugins,
		// Lazy hub provider: resolve s.hub at request time with a typed-nil guard
		// so a nil *realtime.EventHub is never boxed into a non-nil interface.
		func() system.EventStreamer {
			if s.hub == nil {
				return nil
			}
			return s.hub
		},
		sysOpLogs,
		s.olService, // concrete pointer; handler nil-checks it for field access
		getDiskStats,
		resetLibrarySizeCache,
		func() string { return appVersion },
		s.filterReviewedAuthorGroups,
	)
	s.systemHandler = systemH

	// Dedup domain handler (candidate / cluster / series listing, merge /
	// dismiss / remove, bulk merge, stats, CSV/JSON export, the dedup / embed /
	// acoustid / book-signature scan triggers, and per-segment acoustid compare).
	// Guard typed-nil boxing for each interface-typed concrete-pointer dep so the
	// handler's in-method nil guards (mirroring the old `s.opRegistry == nil` /
	// `s.mergeService == nil` checks) hold: boxing a nil concrete pointer into an
	// interface yields a non-nil interface and would defeat them. s.opRegistry,
	// s.mergeService and s.dedupEngine are all wired before setupRoutes
	// (wireServerFromContainer) and never swapped post-wire, so snapshotting them
	// here is safe. The store and the embedding store are passed as LAZY provider
	// closures (not snapshots): the original handlers read s.Store() / s.embeddingStore
	// at request time, and a router-integration test swaps server.store post-wire to
	// inject a mock — snapshotting would miss it. s.Store() returns the
	// database.Store interface (a nil store stays a nil interface); s.embeddingStore
	// is a concrete *database.EmbeddingStore (a nil pointer stays nil, no boxing).
	// publishEvent / markDuplicatesFlaggedDirty are injected as funcs because the
	// *Server methods stay in package server (shared with other domains).
	var dedupOpReg deduphandler.OperationsRegistry
	if s.opRegistry != nil {
		dedupOpReg = s.opRegistry
	}
	var dedupMergeSvc deduphandler.MergeService
	if s.mergeService != nil {
		dedupMergeSvc = s.mergeService
	}
	var dedupEng deduphandler.DedupEngine
	if s.dedupEngine != nil {
		dedupEng = s.dedupEngine
	}
	dedupH := deduphandler.New(
		s.Store(),
		s.embeddingStore,
		dedupOpReg,
		dedupMergeSvc,
		dedupEng,
		s.publishEvent,
		s.markDuplicatesFlaggedDirty,
	)

	// Duplicates domain handler (SQL-backed book/author/series duplicate listing,
	// async scan/merge/dismiss/refresh/dedup/prune/normalize triggers, series
	// prune + normalize preview, and dedup-entry metadata validation; 17 handlers).
	// Guard typed-nil boxing for each interface-typed concrete-pointer dep so the
	// handler's in-method nil guards (mirroring the old `s.opRegistry == nil`,
	// `s.audiobookService`/`s.metadataFetchService` checks) hold. s.opRegistry,
	// s.audiobookService and s.metadataFetchService are all wired before
	// setupRoutes and never swapped post-wire, so snapshotting them here is safe.
	// The store is a LAZY provider closure (not a snapshot): the original handlers
	// read s.Store() at request time and a router-integration test swaps
	// server.store post-wire; s.Store() returns the database.Store interface so a
	// nil store stays a nil interface. s.dedupCache is the concrete
	// *cache.Cache[gin.H] (the cache exception), passed as-is.
	//
	// The merge service is reached through getMergeService, which reproduces the
	// original nil-fallback (s.mergeService when set, else merge.NewService(s.Store())).
	// dismissDedupGroup / computeSeriesPrunePreview / seriesNormalizePreview wrap
	// helpers that STAY in package server (server_middleware.go, server_title_helpers.go,
	// duplicates_helpers.go) because they are shared with files that did not move;
	// the closures let the sub-package call them without importing package server.
	var dupOpReg duplicates.OperationsRegistry
	if s.opRegistry != nil {
		dupOpReg = s.opRegistry
	}
	var dupAudiobookSvc duplicates.AudiobookService
	if s.audiobookService != nil {
		dupAudiobookSvc = s.audiobookService
	}
	var dupMetadataSvc duplicates.MetadataFetchService
	if s.metadataFetchService != nil {
		dupMetadataSvc = s.metadataFetchService
	}
	var dupDedupEng duplicates.DedupEngine
	if s.dedupEngine != nil {
		dupDedupEng = s.dedupEngine
	}
	duplicatesH := duplicates.New(
		s.Store(),
		s.dedupCache,
		dupOpReg,
		dupAudiobookSvc,
		dupMetadataSvc,
		func() duplicates.MergeService {
			if s.mergeService != nil {
				return s.mergeService
			}
			return merge.NewService(s.Store())
		},
		dupDedupEng,
		func(groupKey string) {
			store := s.Store()
			if store == nil {
				return
			}
			dismissed := loadDismissedDedupGroups(store)
			dismissed[groupKey] = true
			saveDismissedDedupGroups(store, dismissed)
		},
		func() (any, error) {
			store := s.Store()
			if store == nil {
				return nil, nil
			}
			return computeSeriesPrunePreview(store)
		},
		func() any {
			return buildSeriesNormalizePreview(s.Store())
		},
	)

	// iTunes handlers. Guard the service/importer wiring: s.itunesSvc is set
	// from the service registry and may be nil (iTunes disabled / not
	// configured). Boxing a typed-nil *Service into the interface would make
	// the handler's `h.svc == nil` guard read false, so only assign when the
	// concrete service is non-nil.
	var itSvc handlers.ITunesService
	var itImporter handlers.ITunesImporter
	if s.itunesSvc != nil {
		itSvc = s.itunesSvc
		itImporter = s.itunesSvc.Importer
	}
	itunesH := handlers.NewITunesHandler(itSvc, itImporter, opReg, s.Store())

	// AI handlers. Guard each concrete dependency so a typed-nil pointer is not
	// boxed into the handler's interface fields — that would defeat the
	// `h.scanStore == nil` / `h.pipeline == nil` guards (which mirror the old
	// `s.aiScanStore == nil` checks on the concrete pointers).
	var aiScanStore handlers.AIScanStore
	if s.aiScanStore != nil {
		aiScanStore = s.aiScanStore
	}
	var aiPipeline handlers.AIPipeline
	if s.pipelineManager != nil {
		aiPipeline = s.pipelineManager
	}
	var aiUpdater handlers.AudiobookUpdater
	if s.audiobookUpdateService != nil {
		aiUpdater = s.audiobookUpdateService
	}
	aiH := handlers.NewAIHandler(
		s.Store(),
		aiScanStore,
		aiPipeline,
		aiUpdater,
		s.dedupCache,
		opReg,
		func(b *database.Book) any { return s.enrichBookForResponseSingle(b) },
	)

	// Diagnostics handlers. Resolve the AI batch parser from the (unexported)
	// batchPoller field — the handler cannot import package server, so the
	// controller reads parser here and passes it in. Guard typed-nil boxing of
	// the diagnostics/merge services so the handler's nil-fallback (lazy
	// construction) fires correctly.
	var diagParser *ai.OpenAIParser
	if s.batchPoller != nil {
		diagParser = s.batchPoller.parser
	}
	var diagSvc handlers.DiagnosticsService
	if s.diagnosticsService != nil {
		diagSvc = s.diagnosticsService
	}
	var diagMergeSvc handlers.MergeService
	if s.mergeService != nil {
		diagMergeSvc = s.mergeService
	}
	diagH := handlers.NewDiagnosticsHandler(
		s.Store(),
		diagSvc,
		diagMergeSvc,
		s.embeddingStore,
		s.aiScanStore,
		opReg,
		diagParser,
	)

	// Audiobooks domain handler (main library list / CRUD: list, count, facets,
	// soft-delete listing / restore / purge, rescan, cover, get, segments,
	// book-file listing + patch, track-info extract, relocate, segment tags,
	// metadata + path history, field states, undo, external IDs, user tags +
	// detailed tags, alternative titles CRUD, batch tag update, update / delete /
	// batch update / batch operations, changelog, changes; 36 handlers).
	//
	// Guard typed-nil boxing for each interface-typed concrete-pointer dep so the
	// handler's in-method nil guards (mirroring the old `s.audiobookService`/
	// `s.writeBackBatcher`/`s.metadataFetchService` checks) hold. All of these are
	// wired before setupRoutes and never swapped post-wire, so snapshotting them
	// here is safe. The store is a LAZY provider closure (not a snapshot): the
	// original handlers read s.Store() at request time and a router-integration
	// test swaps server.store post-wire; s.Store() returns the database.Store
	// interface (un-stripped) so the handlers' inline type assertions
	// (Unwrap / ListBooksWithFileErrors / GetAllBookIDsForQuickQuery /
	// GetBookFilesForIDsCore / InvalidateLibraryStats) still resolve against the
	// dynamic type. The caches are concrete (*cache.Cache[T], the cache exception).
	//
	// buildListResponse wraps the relocated *Server.buildAudiobookListResponse
	// (audiobooks_helpers.go), shared with the library list cache warmer.
	// isProtectedPath / enrichBook / getFieldStates / getExternalIDStore /
	// publishEvent wrap helpers / behavior that STAY in package server (and in two
	// cases reference server- or metafetch-private types); the closures let the
	// sub-package call them without importing package server.
	var abSvc audiobookshandler.AudiobookService
	if s.audiobookService != nil {
		abSvc = s.audiobookService
	}
	var abUpdater audiobookshandler.AudiobookUpdater
	if s.audiobookUpdateService != nil {
		abUpdater = s.audiobookUpdateService
	}
	var abMetaState audiobookshandler.MetadataStateService
	if s.metadataStateService != nil {
		abMetaState = s.metadataStateService
	}
	var abMetaFetch audiobookshandler.MetadataFetchService
	if s.metadataFetchService != nil {
		abMetaFetch = s.metadataFetchService
	}
	var abBatch audiobookshandler.BatchService
	if s.batchService != nil {
		abBatch = s.batchService
	}
	var abChangelog audiobookshandler.ChangelogService
	if s.changelogService != nil {
		abChangelog = s.changelogService
	}
	audiobooksH := audiobookshandler.New(
		s.Store(),
		abSvc,
		abUpdater,
		// Lazy provider: server.writeBackBatcher is swapped post-wire by
		// integration tests and the original handlers read it at request time, so
		// snapshotting would capture the pre-swap value. Nil stays a nil interface.
		func() audiobookshandler.WriteBackEnqueuer {
			if s.writeBackBatcher == nil {
				return nil
			}
			return s.writeBackBatcher
		},
		abMetaState,
		abMetaFetch,
		abBatch,
		abChangelog,
		s.listCache,
		s.facetsCache,
		s.authorsCache,
		s.seriesCache,
		s.buildAudiobookListResponse,
		s.buildFacetsResponse,
		s.isProtectedPath,
		func(b *database.Book) any { return s.enrichBookForResponseSingle(b) },
		func(id string) (any, error) { return s.metadataStateService.LoadMetadataState(id) },
		func() audiobookshandler.ExternalIDStore {
			eid := asExternalIDStore(s.Store())
			if eid == nil {
				return nil
			}
			return eid
		},
		s.publishEvent,
	)

	// ── Metadata domain (handlers/metadata) ──────────────────────────────────
	// The 19 metadata HTTP handlers (batch-update / validate / export / import,
	// external search, per-book fetch / search / apply / mark-no-match / revert,
	// metadata-rejections, cow-versions(+prune), write-back, bulk fetch + bulk
	// write-back enqueue, batch write-back enqueue, fields, rating PATCH).
	//
	// store and writeBackBatcher are resolved through lazy provider closures
	// (swapped post-wire by integration tests / read at request time by the
	// originals). metadataFetchService / opRegistry / fileIOPool are wire-time
	// interface snapshots, each typed-nil guarded so the in-method `!= nil` /
	// `== nil` checks hold. enrichBook wraps the server-private
	// enrichBookForResponseSingle (return type private → any). loadMetadataState /
	// updateFetchedMetadataState / isProtectedPath / publishEvent wrap helpers
	// that STAY in package server (server_metadata.go / server_middleware.go).
	var mdMetaFetch metadatahandler.MetadataFetchService
	if s.metadataFetchService != nil {
		mdMetaFetch = s.metadataFetchService
	}
	var mdOpRegistry metadatahandler.OperationsRegistry
	if s.opRegistry != nil {
		mdOpRegistry = s.opRegistry
	}
	var mdFileIOPool metadatahandler.FileIOPool
	if s.fileIOPool != nil {
		mdFileIOPool = s.fileIOPool
	}
	metadataH := metadatahandler.New(
		s.Store(),
		mdMetaFetch,
		// Lazy provider: server.writeBackBatcher is swapped post-wire by
		// integration tests and the original handlers read it at request time, so
		// snapshotting would capture the pre-swap value. Nil stays a nil interface.
		func() metadatahandler.WriteBackEnqueuer {
			if s.writeBackBatcher == nil {
				return nil
			}
			return s.writeBackBatcher
		},
		mdOpRegistry,
		mdFileIOPool,
		s.listCache,
		func(b *database.Book) any { return s.enrichBookForResponseSingle(b) },
		s.isProtectedPath,
		s.loadMetadataState,
		s.updateFetchedMetadataState,
		s.publishEvent,
	)

	// Tools lifecycle handler (instantiated here so wireMediaRoutes receives it).
	toolsH := toolshandler.New(s.toolRegistry, &config.AppConfig.Tools, nil)

	// AI backend-mode status/pull-model handler (TASK-11, FE half of TASK-10's
	// AIBackendConfig toggle). Reuses the same ToolRegistry/OllamaDaemon
	// lifecycle as the tools handler above.
	aiBackendsH := aibackendshandler.New(s.toolRegistry, s.ollamaDaemon)

	// Review-queue handler (PR-A1). The store is the wide database.Store, which
	// embeds database.ReviewStore. s.Store() returns the interface so a nil store
	// stays a nil interface (the handler guards on store == nil). Apply handlers
	// are registered later by producers (Track B2); none exist at A1.
	reviewH := reviewhandler.New(s.Store())

	// Register the regroup APPLY handlers (PR-B2) so approving a confident hold in
	// the review UI performs the real merge. Only the two confident kinds get a
	// handler; anthology/ambiguous stay handler-less (approve → "approved", never
	// auto-applied). Guarded on a real store — with a nil store the review handler
	// short-circuits before dispatch, so the closures would never run anyway.
	if s.Store() != nil {
		mergeSvc := s.mergeService
		if mergeSvc == nil {
			mergeSvc = merge.NewService(s.Store())
		}
		reviewH.RegisterApplyHandler(itunesservice.KindMultidisc,
			maintenanceplugin.ApplyMultidisc(s.Store(), mergeSvc))
		reviewH.RegisterApplyHandler(itunesservice.KindVersionGroup,
			maintenanceplugin.ApplyVersionGroup(s.Store()))
	}

	// ── Register protected routes via per-domain methods ─────────────────────
	s.wireLibraryRoutes(protected, cacheH, activityH, splitBookH, filesystemH, organizeH, metaCacheH, readingH, playlistH, userH, versionsH)
	s.wireMediaRoutes(protected, itunesH, aiH, diagH, toolsH, aiBackendsH, pluginsH)
	s.wireEntitiesRoutes(protected, entitiesH)
	s.wireOperationsRoutes(protected, opsV2H, operationsH)
	s.wireSystemRoutes(protected, systemH)
	s.wireDedupRoutes(protected, dedupH, duplicatesH)
	s.wireReviewRoutes(protected, reviewH)
	s.wireAudiobooksRoutes(protected, audiobooksH)
	s.wireMetadataRoutes(protected, metadataH)
}
