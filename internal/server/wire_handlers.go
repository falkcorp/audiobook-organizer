// file: internal/server/wire_handlers.go
// version: 2.9.0
// guid: f7a8b9c0-d1e2-3456-7890-abcdef012345
// last-edited: 2026-06-15

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	deduphandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup"
	duplicates "github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates"
	entities "github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
	operations "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	system "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	toolshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/tools"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/gin-gonic/gin"
)

// wireHandlers instantiates handler structs and registers their routes.
// Called from Start() after the protected group is created.
func (s *Server) wireHandlers(api *gin.RouterGroup, authMiddleware gin.HandlerFunc, protected *gin.RouterGroup) {
	authH := handlers.NewAuthHandler(s.Store(), config.AppConfig.EnableAuth)
	apiKeyH := handlers.NewAPIKeyHandler(s.Store())

	authGroup := api.Group("/auth")
	{
		authGroup.GET("/status", authH.GetStatus)
		authGroup.POST("/setup", authH.SetupInitialAdmin)
		authGroup.POST("/login", authH.Login)
		authGroup.POST("/accept-invite", s.handleAcceptInvite)
		authGroup.POST("/bootstrap", s.handleBootstrap)
	}

	authProtected := authGroup.Group("")
	authProtected.Use(authMiddleware)
	{
		authProtected.GET("/me", authH.Me)
		authProtected.PATCH("/me", authH.UpdateMe)
		authProtected.POST("/logout", authH.Logout)
		authProtected.GET("/sessions", authH.ListMySessions)
		authProtected.DELETE("/sessions/:id", authH.RevokeMySession)
		authProtected.PUT("/me/password", authH.ChangePassword)
		authProtected.POST("/temp-tokens", s.perm(permTempLoginMint()), s.createTempLoginToken)

		authProtected.POST("/api-keys", apiKeyH.Create)
		authProtected.GET("/api-keys", apiKeyH.List)
		authProtected.GET("/api-keys/:id", apiKeyH.Get)
		authProtected.PATCH("/api-keys/:id", apiKeyH.UpdateStatus)
		authProtected.DELETE("/api-keys/:id", apiKeyH.Revoke)
	}

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
	var opsV2 database.OpsV2Store
	if st := s.Store(); st != nil {
		if v2, ok := st.(database.OpsV2Store); ok {
			opsV2 = v2
		}
	}
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
		func() deduphandler.DedupStore { return s.Store() },
		func() *database.EmbeddingStore { return s.embeddingStore },
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
	duplicatesH := duplicates.New(
		func() duplicates.DuplicatesStore { return s.Store() },
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
	// GetBookFilesForIDs / InvalidateLibraryStats) still resolve against the
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
		func() audiobookshandler.AudiobooksStore { return s.Store() },
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
		func() metadatahandler.MetadataStore {
			st := s.Store()
			if st == nil {
				return nil
			}
			return st
		},
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

	// ── Public cache routes (no auth) ────────────────────────────────────────
	api.GET("/cache/stats", cacheH.HandleCacheStats)
	api.GET("/cache/stats/history", cacheH.HandleCacheStatsHistory)

	// ── Protected routes ─────────────────────────────────────────────────────

	// Activity log
	protected.GET("/activity", s.perm(auth.PermLibraryView), activityH.ListActivity)
	protected.GET("/activity/sources", s.perm(auth.PermLibraryView), activityH.ListActivitySources)
	protected.POST("/activity/compact", s.perm(auth.PermSettingsManage), activityH.CompactActivity)
	protected.GET("/operations/:id/activity", s.perm(auth.PermLibraryView), activityH.ListOperationActivity)

	// Split-book dedup
	protected.POST("/dedup/split-book-scan", s.perm(auth.PermScanTrigger), splitBookH.TriggerSplitBookScan)
	protected.GET("/dedup/split-book-candidates", s.perm(auth.PermLibraryView), splitBookH.ListSplitBookCandidates)
	protected.POST("/dedup/split-book-candidates/:id/merge", s.perm(auth.PermLibraryEditMetadata), splitBookH.MergeSplitBookCandidate)

	// Filesystem + import paths
	protected.GET("/filesystem/home", s.perm(auth.PermSettingsManage), filesystemH.GetHomeDirectory)
	protected.GET("/filesystem/browse", s.perm(auth.PermSettingsManage), filesystemH.BrowseFilesystem)
	protected.POST("/filesystem/exclude", s.perm(auth.PermSettingsManage), filesystemH.CreateExclusion)
	protected.DELETE("/filesystem/exclude", s.perm(auth.PermSettingsManage), filesystemH.RemoveExclusion)
	protected.GET("/import-paths", s.perm(auth.PermSettingsManage), filesystemH.ListImportPaths)
	protected.POST("/import-paths", s.perm(auth.PermSettingsManage), filesystemH.AddImportPath)
	protected.DELETE("/import-paths/:id", s.perm(auth.PermSettingsManage), filesystemH.RemoveImportPath)
	protected.POST("/import/file", s.perm(auth.PermScanTrigger), filesystemH.ImportFile)

	// Organize + rename
	protected.POST("/audiobooks/:id/rename/preview", s.perm(auth.PermLibraryOrganize), organizeH.PreviewRename)
	protected.POST("/audiobooks/:id/rename/apply", s.perm(auth.PermLibraryOrganize), organizeH.ApplyRename)
	protected.GET("/audiobooks/:id/preview-organize", s.perm(auth.PermLibraryOrganize), organizeH.PreviewOrganize)
	protected.POST("/audiobooks/:id/organize", s.perm(auth.PermLibraryOrganize), organizeH.OrganizeBook)

	// Metadata cache
	protected.GET("/audiobooks/metadata/cached", s.perm(auth.PermLibraryView), metaCacheH.ListCachedCandidates)
	protected.GET("/audiobooks/metadata/cache/review", s.perm(auth.PermLibraryView), metaCacheH.GetCacheReviewResults)
	protected.POST("/audiobooks/metadata/batch-apply-cached", s.perm(auth.PermLibraryEditMetadata), metaCacheH.BatchApplyFromCache)
	protected.POST("/audiobooks/:id/clear-no-match", s.perm(auth.PermLibraryEditMetadata), metaCacheH.ClearMetadataNoMatch)

	// Reading progress
	protected.POST("/books/:id/position", readingH.SetPosition)
	protected.GET("/books/:id/position", readingH.GetPosition)
	protected.GET("/books/:id/state", readingH.GetBookState)
	protected.PATCH("/books/:id/status", readingH.SetBookStatus)
	protected.DELETE("/books/:id/status", readingH.ClearBookStatus)
	protected.GET("/me/:status", readingH.ListByStatus)

	// Playlists
	protected.GET("/playlists", s.perm(auth.PermLibraryView), playlistH.ListPlaylists)
	protected.POST("/playlists", playlistH.CreatePlaylist)
	protected.GET("/playlists/:id", playlistH.GetPlaylist)
	protected.PUT("/playlists/:id", playlistH.UpdatePlaylist)
	protected.DELETE("/playlists/:id", playlistH.DeletePlaylist)
	protected.POST("/playlists/:id/books", playlistH.AddBooksToPlaylist)
	protected.DELETE("/playlists/:id/books/:bookID", playlistH.RemoveBookFromPlaylist)
	protected.POST("/playlists/:id/reorder", playlistH.ReorderPlaylist)
	protected.POST("/playlists/:id/materialize", playlistH.MaterializePlaylist)

	// User management
	users := protected.Group("/users")
	{
		users.GET("", s.perm("users.manage"), userH.ListUsers)
		users.POST("/invite", s.perm("users.manage"), userH.CreateInvite)
		users.GET("/invites", s.perm("users.manage"), userH.ListInvites)
		users.DELETE("/invites/:token", s.perm("users.manage"), userH.DeleteInvite)
		users.POST("/:id/deactivate", s.perm("users.manage"), userH.DeactivateUser)
		users.POST("/:id/reactivate", s.perm("users.manage"), userH.ReactivateUser)
		users.POST("/:id/reset-password", s.perm("users.manage"), userH.ResetPassword)
	}

	// Version groups
	protected.GET("/audiobooks/:id/versions", s.perm(auth.PermLibraryView), versionsH.ListAudiobookVersions)
	protected.POST("/audiobooks/:id/versions", s.perm(auth.PermLibraryEditMetadata), versionsH.LinkAudiobookVersion)
	protected.PUT("/audiobooks/:id/set-primary", s.perm(auth.PermLibraryEditMetadata), versionsH.SetAudiobookPrimary)
	protected.POST("/audiobooks/:id/split-version", s.perm(auth.PermLibraryEditMetadata), versionsH.SplitVersion)
	protected.POST("/audiobooks/:id/split-to-books", s.perm(auth.PermLibraryEditMetadata), versionsH.SplitSegmentsToBooks)
	protected.POST("/audiobooks/:id/move-segments", s.perm(auth.PermLibraryEditMetadata), versionsH.MoveSegments)
	protected.GET("/version-groups/:id", s.perm(auth.PermLibraryView), versionsH.GetVersionGroup)

	// iTunes (12 migrated routes; survivors stay in server_lifecycle.go).
	// Two protected.Group("/itunes") blocks (here + survivors) is fine in Gin
	// since there is no duplicate method+path.
	itunesG := protected.Group("/itunes")
	{
		itunesG.POST("/validate", s.perm(auth.PermLibraryEditMetadata), itunesH.Validate)
		itunesG.POST("/test-mapping", s.perm(auth.PermLibraryEditMetadata), itunesH.TestMapping)
		itunesG.POST("/import", s.perm(auth.PermLibraryEditMetadata), itunesH.Import)
		itunesG.POST("/write-back", s.perm(auth.PermLibraryEditMetadata), itunesH.WriteBack)
		itunesG.POST("/write-back-all", s.perm(auth.PermLibraryEditMetadata), itunesH.WriteBackAll)
		itunesG.GET("/library-stats", s.perm(auth.PermLibraryView), itunesH.LibraryStats)
		itunesG.POST("/write-back/preview", s.perm(auth.PermLibraryEditMetadata), itunesH.WriteBackPreview)
		itunesG.GET("/books", s.perm(auth.PermLibraryView), itunesH.ListBooks)
		itunesG.GET("/import-status/:id", s.perm(auth.PermLibraryView), itunesH.ImportStatus)
		itunesG.POST("/import-status/bulk", s.perm(auth.PermLibraryEditMetadata), itunesH.ImportStatusBulk)
		itunesG.GET("/library-status", s.perm(auth.PermLibraryView), itunesH.LibraryStatus)
		itunesG.POST("/sync", s.perm(auth.PermLibraryEditMetadata), itunesH.Sync)
	}

	// AI domain (migrated from server_lifecycle.go).
	protected.POST("/authors/duplicates/ai-review", s.perm(auth.PermLibraryEditMetadata), aiH.ReviewDuplicateAuthors)
	protected.POST("/authors/duplicates/ai-review/apply", s.perm(auth.PermLibraryEditMetadata), aiH.ApplyAuthorReview)
	protected.POST("/ai/parse-filename", s.perm(auth.PermLibraryEditMetadata), aiH.ParseFilename)
	protected.POST("/ai/test-connection", s.perm(auth.PermLibraryEditMetadata), aiH.TestConnection)
	aiScans := protected.Group("/ai/scans")
	{
		aiScans.POST("", s.perm(auth.PermLibraryEditMetadata), aiH.StartScan)
		aiScans.GET("", s.perm(auth.PermLibraryView), aiH.ListScans)
		aiScans.GET("/compare", aiH.CompareScans) // Must be before /:id to avoid conflict
		aiScans.GET("/:id", s.perm(auth.PermLibraryView), aiH.GetScan)
		aiScans.GET("/:id/results", s.perm(auth.PermLibraryView), aiH.GetScanResults)
		aiScans.POST("/:id/apply", s.perm(auth.PermLibraryEditMetadata), aiH.ApplyScanResults)
		aiScans.POST("/:id/cancel", s.perm(auth.PermLibraryEditMetadata), aiH.CancelScan)
		aiScans.DELETE("/:id", s.perm(auth.PermLibraryDelete), aiH.DeleteScan)
	}
	protected.POST("/metadata-sources/test", s.perm(auth.PermSettingsManage), aiH.TestMetadataSource)
	protected.POST("/audiobooks/:id/parse-with-ai", s.perm(auth.PermLibraryEditMetadata), aiH.ParseAudiobook)
	protected.GET("/ai-jobs", s.perm(auth.PermSettingsManage), aiH.ListAIJobs)

	// Entities domain (migrated from server_lifecycle.go): authors, narrators,
	// series, and works. Paths + permission guards copied verbatim. Sibling
	// /authors/duplicates*, /series/duplicates*, /authors/duplicates/ai-review*
	// (now aiH.*) and the entity-tag routes stay on *Server / their own handlers.
	protected.GET("/authors", s.perm(auth.PermLibraryView), entitiesH.ListAuthors)
	protected.GET("/authors/count", s.perm(auth.PermLibraryView), entitiesH.CountAuthors)
	protected.POST("/authors/merge", s.perm(auth.PermLibraryEditMetadata), entitiesH.MergeAuthors)
	protected.POST("/authors/:id/reclassify-as-narrator", s.perm(auth.PermLibraryEditMetadata), entitiesH.ReclassifyAuthorAsNarrator)
	protected.PUT("/authors/:id/name", s.perm(auth.PermLibraryEditMetadata), entitiesH.RenameAuthor)
	protected.POST("/authors/:id/split", s.perm(auth.PermLibraryEditMetadata), entitiesH.SplitCompositeAuthor)
	protected.POST("/authors/:id/resolve-production", s.perm(auth.PermLibraryEditMetadata), entitiesH.ResolveProductionAuthor)
	protected.GET("/authors/:id/aliases", s.perm(auth.PermLibraryView), entitiesH.GetAuthorAliases)
	protected.POST("/authors/:id/aliases", s.perm(auth.PermLibraryEditMetadata), entitiesH.CreateAuthorAlias)
	protected.DELETE("/authors/:id/aliases/:aliasId", s.perm(auth.PermLibraryDelete), entitiesH.DeleteAuthorAlias)
	protected.GET("/authors/:id/books", s.perm(auth.PermLibraryView), entitiesH.GetAuthorBooks)
	protected.DELETE("/authors/:id", s.perm(auth.PermLibraryDelete), entitiesH.DeleteAuthor)
	protected.POST("/authors/bulk-delete", s.perm(auth.PermLibraryDelete), entitiesH.BulkDeleteAuthors)

	protected.GET("/narrators", s.perm(auth.PermLibraryView), entitiesH.ListNarrators)
	protected.GET("/narrators/count", s.perm(auth.PermLibraryView), entitiesH.CountNarrators)
	protected.GET("/audiobooks/:id/narrators", s.perm(auth.PermLibraryView), entitiesH.ListAudiobookNarrators)
	protected.PUT("/audiobooks/:id/narrators", s.perm(auth.PermLibraryEditMetadata), entitiesH.SetAudiobookNarrators)

	protected.GET("/series", s.perm(auth.PermLibraryView), entitiesH.ListSeries)
	protected.GET("/series/count", s.perm(auth.PermLibraryView), entitiesH.CountSeries)
	protected.PATCH("/series/:id", s.perm(auth.PermLibraryEditMetadata), entitiesH.UpdateSeriesName)
	protected.GET("/series/:id/books", s.perm(auth.PermLibraryView), entitiesH.GetSeriesBooks)
	protected.PUT("/series/:id/name", s.perm(auth.PermLibraryEditMetadata), entitiesH.RenameSeries)
	protected.POST("/series/:id/split", s.perm(auth.PermLibraryEditMetadata), entitiesH.SplitSeries)
	protected.DELETE("/series/:id", s.perm(auth.PermLibraryDelete), entitiesH.DeleteEmptySeries)
	protected.POST("/series/bulk-delete", s.perm(auth.PermLibraryDelete), entitiesH.BulkDeleteSeries)

	protected.GET("/works", s.perm(auth.PermLibraryView), entitiesH.ListWorks)
	protected.POST("/works", s.perm(auth.PermLibraryEditMetadata), entitiesH.CreateWork)
	protected.GET("/works/:id", s.perm(auth.PermLibraryView), entitiesH.GetWork)
	protected.PUT("/works/:id", s.perm(auth.PermLibraryEditMetadata), entitiesH.UpdateWork)
	protected.DELETE("/works/:id", s.perm(auth.PermLibraryDelete), entitiesH.DeleteWork)
	protected.GET("/works/:id/books", s.perm(auth.PermLibraryView), entitiesH.ListWorkBooks)
	protected.GET("/work", s.perm(auth.PermLibraryView), entitiesH.ListWork)
	protected.GET("/work/stats", s.perm(auth.PermLibraryView), entitiesH.GetWorkStats)

	// Diagnostics (migrated from server_lifecycle.go).
	protected.GET("/diagnostics/db-health", s.perm(auth.PermSettingsManage), diagH.GetDBHealth)
	protected.POST("/diagnostics/export", s.perm(auth.PermSettingsManage), diagH.StartExport)
	protected.GET("/diagnostics/export/:operationId/download", s.perm(auth.PermSettingsManage), diagH.DownloadExport)
	protected.POST("/diagnostics/submit-ai", s.perm(auth.PermSettingsManage), diagH.SubmitAI)
	protected.GET("/diagnostics/ai-results/:operationId", s.perm(auth.PermSettingsManage), diagH.GetAIResults)
	protected.POST("/diagnostics/apply-suggestions", s.perm(auth.PermSettingsManage), diagH.ApplySuggestions)

	// Operations v2 (UOS-06)
	protected.GET("/operations/timeline", s.perm(auth.PermLibraryView), opsV2H.GetOperationTimeline)
	protected.GET("/operations/events", s.perm(auth.PermLibraryView), opsV2H.OperationsSSE)
	protected.GET("/operations/v2/:id", s.perm(auth.PermLibraryView), opsV2H.GetOperationV2)
	protected.DELETE("/operations/v2/:id", s.perm(auth.PermSettingsManage), opsV2H.CancelOperationV2)
	protected.POST("/operations/v2", s.perm(auth.PermScanTrigger), opsV2H.TriggerOperationV2)
	protected.GET("/op-defs", s.perm(auth.PermLibraryView), opsV2H.ListOpDefs)
	protected.GET("/op-defs/:id", s.perm(auth.PermLibraryView), opsV2H.GetOpDef)

	// Operations domain (migrated from server_lifecycle.go). Paths + permission
	// guards copied verbatim. These share the /operations path prefix with the
	// operations_v2 routes above (timeline/events/v2/op-defs) and the survivors
	// that stay in server_lifecycle.go (active/recent/reconcile/itunes-path-*/
	// cleanup-version-groups/results/file-ops) — all distinct method+path pairs,
	// all using the identical `:id` param name, so Gin registers them cleanly.
	protected.GET("/operations", s.perm(auth.PermLibraryView), operationsH.ListOperations)
	protected.GET("/operations/stale", s.perm(auth.PermLibraryView), operationsH.ListStaleOperations)
	protected.POST("/operations/scan", s.perm(auth.PermScanTrigger), operationsH.StartScan)
	protected.POST("/operations/organize", s.perm(auth.PermScanTrigger), operationsH.StartOrganize)
	protected.POST("/operations/transcode", s.perm(auth.PermScanTrigger), operationsH.StartTranscode)
	protected.POST("/operations/optimize", s.perm(auth.PermScanTrigger), operationsH.StartOptimize)
	protected.GET("/operations/:id/status", s.perm(auth.PermLibraryView), operationsH.GetOperationStatus)
	protected.GET("/operations/:id/logs", s.perm(auth.PermLibraryView), operationsH.GetOperationLogs)
	protected.GET("/operations/:id/result", s.perm(auth.PermLibraryView), operationsH.GetOperationResult)
	protected.DELETE("/operations/:id", s.perm(auth.PermSettingsManage), operationsH.CancelOperation)
	protected.POST("/operations/clear-stale", s.perm(auth.PermSettingsManage), operationsH.ClearStaleOperations)
	protected.DELETE("/operations/history", s.perm(auth.PermSettingsManage), operationsH.DeleteOperationHistory)
	protected.POST("/operations/optimize-database", s.perm(auth.PermSettingsManage), operationsH.OptimizeDatabase)
	protected.POST("/operations/sweep-tombstones", s.perm(auth.PermSettingsManage), operationsH.SweepTombstones)
	protected.POST("/operations/set-internal-flag", s.perm(auth.PermSettingsManage), operationsH.SetInternalFlag)
	protected.GET("/operations/audit-files", s.perm(auth.PermSettingsManage), operationsH.AuditFileConsistency)
	protected.GET("/operations/:id/changes", s.perm(auth.PermLibraryView), operationsH.GetOperationChanges)
	protected.GET("/operations/:id/undo/preflight", s.perm(auth.PermLibraryView), operationsH.UndoPreflightHandler)
	protected.POST("/operations/:id/revert", s.perm(auth.PermLibraryOrganize), operationsH.RevertOperation)
	protected.GET("/tasks", s.perm(auth.PermSettingsManage), operationsH.ListTasks)
	protected.POST("/tasks/:name/run", s.perm(auth.PermSettingsManage), operationsH.RunTask)
	protected.PUT("/tasks/:name", s.perm(auth.PermSettingsManage), operationsH.UpdateTaskConfig)
	protected.POST("/maintenance-window/run", s.perm(auth.PermSettingsManage), operationsH.RunMaintenanceWindowNow)
	protected.GET("/maintenance-window/status", s.perm(auth.PermSettingsManage), operationsH.GetMaintenanceWindowStatus)
	protected.PUT("/maintenance-window/config", s.perm(auth.PermSettingsManage), operationsH.UpdateMaintenanceWindowConfig)

	// System domain (migrated from server_lifecycle.go). Paths + permission
	// guards copied verbatim. The public /health (x3) and /api/events routes stay
	// in setupRoutes — they are registered on s.router BEFORE the /api/* redirect
	// middleware, so re-registering them here would change their middleware
	// ordering; they delegate to systemH via closures instead.
	protected.GET("/policy/tags", s.perm(auth.PermLibraryView), systemH.HandlePolicyTags)
	protected.GET("/system/status", s.perm(auth.PermSettingsManage), systemH.GetSystemStatus)
	protected.GET("/system/announcements", s.perm(auth.PermSettingsManage), systemH.GetSystemAnnouncements)
	protected.GET("/system/storage", s.perm(auth.PermSettingsManage), systemH.GetSystemStorage)
	protected.GET("/system/logs", s.perm(auth.PermSettingsManage), systemH.GetSystemLogs)
	protected.GET("/system/activity-log", s.perm(auth.PermSettingsManage), systemH.GetSystemActivityLog)
	protected.POST("/system/reset", s.perm(auth.PermSettingsManage), systemH.ResetSystem)
	protected.POST("/system/factory-reset", s.perm(auth.PermSettingsManage), systemH.FactoryReset)
	protected.GET("/config", s.perm(auth.PermSettingsManage), systemH.GetConfig)
	protected.PUT("/config", s.perm(auth.PermSettingsManage), systemH.UpdateConfig)
	protected.GET("/dashboard", s.perm(auth.PermLibraryView), systemH.GetDashboard)
	protected.POST("/backup/create", s.perm(auth.PermSettingsManage), systemH.CreateBackup)
	protected.GET("/backup/list", s.perm(auth.PermSettingsManage), systemH.ListBackups)
	protected.POST("/backup/restore", s.perm(auth.PermSettingsManage), systemH.RestoreBackup)
	protected.DELETE("/backup/:filename", s.perm(auth.PermSettingsManage), systemH.DeleteBackup)
	protected.GET("/library/quick-queries", s.perm(auth.PermLibraryView), systemH.GetQuickQueries)
	protected.GET("/blocked-hashes", s.perm(auth.PermLibraryView), systemH.ListBlockedHashes)
	protected.POST("/blocked-hashes", s.perm(auth.PermLibraryEditMetadata), systemH.AddBlockedHash)
	protected.DELETE("/blocked-hashes/:hash", s.perm(auth.PermLibraryDelete), systemH.RemoveBlockedHash)
	protected.GET("/preferences/:key", s.perm(auth.PermLibraryView), systemH.GetUserPreference)
	protected.PUT("/preferences/:key", s.perm(auth.PermLibraryEditMetadata), systemH.SetUserPreference)
	protected.DELETE("/preferences/:key", s.perm(auth.PermLibraryDelete), systemH.DeleteUserPreference)

	// Embedding-based dedup domain routes (migrated from server_lifecycle.go).
	// The split-book /dedup/* routes (registered above) and the
	// /dedup/fingerprint-rescan + /dedup/validate survivors stay where they are.
	protected.GET("/dedup/candidates", s.perm(auth.PermLibraryView), dedupH.ListDedupCandidates)
	protected.GET("/dedup/candidates/export", s.perm(auth.PermLibraryView), dedupH.ExportDedupCandidates)
	protected.GET("/dedup/stats", s.perm(auth.PermLibraryView), dedupH.GetDedupStats)
	// T016: breakdown and rescore endpoints (frozen API contract for T017).
	protected.GET("/dedup/candidates/:id/breakdown", s.perm(auth.PermLibraryView), dedupH.GetDedupCandidateBreakdown)
	protected.POST("/dedup/rescore", s.perm(auth.PermScanTrigger), dedupH.RescoreDedupCandidates)
	protected.POST("/dedup/candidates/:id/merge", s.perm(auth.PermLibraryEditMetadata), dedupH.MergeDedupCandidate)
	protected.POST("/dedup/candidates/:id/dismiss", s.perm(auth.PermLibraryEditMetadata), dedupH.DismissDedupCandidate)
	protected.POST("/dedup/candidates/bulk-merge", s.perm(auth.PermLibraryEditMetadata), dedupH.BulkMergeDedupCandidates)
	protected.POST("/dedup/candidates/merge-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.MergeDedupCluster)
	protected.POST("/dedup/candidates/dismiss-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.DismissDedupCluster)
	protected.POST("/dedup/candidates/remove-from-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.RemoveFromDedupCluster)
	protected.GET("/dedup/candidates/series-summary", s.perm(auth.PermLibraryView), dedupH.ListDedupCandidateSeries)
	// C6 — gold-dataset review (the dedup feedback-loop labels).
	protected.GET("/dedup/labels", s.perm(auth.PermLibraryView), dedupH.ListDedupLabels)
	protected.GET("/dedup/labels/stats", s.perm(auth.PermLibraryView), dedupH.GetDedupLabelStats)
	protected.POST("/dedup/labels/:id/override", s.perm(auth.PermLibraryEditMetadata), dedupH.OverrideDedupLabel)
	protected.POST("/dedup/candidates/merge-series", s.perm(auth.PermLibraryEditMetadata), dedupH.MergeDedupCandidateSeries)
	protected.POST("/dedup/scan", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupScan)
	protected.POST("/dedup/scan-llm", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupLLM)
	protected.POST("/dedup/scan-acoustid", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupAcoustID)
	protected.POST("/audiobooks/:id/compare-acoustid", s.perm(auth.PermLibraryView), dedupH.HandleCompareAcoustID)
	protected.POST("/dedup/scan-book-signature", s.perm(auth.PermScanTrigger), dedupH.TriggerBookSignatureScan)
	protected.POST("/dedup/refresh", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupRefresh)
	protected.POST("/dedup/purge-stale", s.perm(auth.PermScanTrigger), dedupH.PurgeStaleCandidates)
	protected.POST("/dedup/purge-legacy-fp", s.perm(auth.PermScanTrigger), dedupH.PurgeLegacyFPCandidates)
	protected.POST("/dedup/reset-acoustid", s.perm(auth.PermScanTrigger), dedupH.ResetAcoustIDFingerprints)
	protected.POST("/dedup/embed", s.perm(auth.PermScanTrigger), dedupH.TriggerEmbedScan)
	protected.POST("/dedup/embed-async", s.perm(auth.PermScanTrigger), dedupH.TriggerEmbedAsync)
	protected.POST("/dedup/lsh-index", s.perm(auth.PermScanTrigger), dedupH.TriggerLSHIndexBuild)
	protected.POST("/dedup/emb-reencode", s.perm(auth.PermScanTrigger), dedupH.EmbReeencode) // T021: float16+zstd re-encode op

	// Duplicates domain (SQL-backed dup detection, series prune/normalize,
	// dedup-entry validation; migrated from server_lifecycle.go). Paths + permission
	// guards copied verbatim. The /authors/duplicates(/refresh), /series/duplicates(/refresh)
	// sibling routes were intentionally left here by the entities phase and are now
	// owned by this handler; /dedup/validate is the dedup-entry validator (distinct
	// from the embedding-based /dedup/* routes above and the split-book /dedup/* routes).
	protected.GET("/audiobooks/duplicates", s.perm(auth.PermLibraryView), duplicatesH.ListDuplicateAudiobooks)
	protected.GET("/audiobooks/duplicates/scan-results", s.perm(auth.PermLibraryView), duplicatesH.ListBookDuplicateScanResults)
	protected.POST("/audiobooks/duplicates/scan", s.perm(auth.PermLibraryEditMetadata), duplicatesH.ScanBookDuplicates)
	protected.POST("/audiobooks/duplicates/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesH.MergeBookDuplicatesAsVersions)
	protected.POST("/audiobooks/duplicates/dismiss", s.perm(auth.PermLibraryEditMetadata), duplicatesH.DismissBookDuplicateGroup)
	protected.GET("/authors/duplicates", s.perm(auth.PermLibraryView), duplicatesH.ListDuplicateAuthors)
	protected.POST("/authors/duplicates/refresh", s.perm(auth.PermLibraryEditMetadata), duplicatesH.RefreshDuplicateAuthors)
	protected.POST("/audiobooks/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesH.MergeBooks)
	protected.GET("/series/duplicates", s.perm(auth.PermLibraryView), duplicatesH.ListSeriesDuplicates)
	protected.POST("/series/duplicates/refresh", s.perm(auth.PermLibraryEditMetadata), duplicatesH.RefreshSeriesDuplicates)
	protected.POST("/series/deduplicate", s.perm(auth.PermLibraryEditMetadata), duplicatesH.DeduplicateSeriesHandler)
	protected.POST("/series/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesH.MergeSeriesGroup)
	protected.GET("/series/prune/preview", s.perm(auth.PermLibraryView), duplicatesH.SeriesPrunePreview)
	protected.POST("/series/prune", s.perm(auth.PermLibraryEditMetadata), duplicatesH.SeriesPrune)
	protected.GET("/series/normalize/preview", s.perm(auth.PermLibraryView), duplicatesH.SeriesNormalizePreview)
	protected.POST("/series/normalize", s.perm(auth.PermLibraryEditMetadata), duplicatesH.SeriesNormalize)
	protected.POST("/dedup/validate", s.perm(auth.PermLibraryEditMetadata), duplicatesH.ValidateDedupEntry)

	// Audiobooks domain (main library list / CRUD; migrated from
	// server_lifecycle.go). Paths + permission guards copied verbatim. Sibling
	// /audiobooks/:id/* routes owned by OTHER domains (quarantine, rating,
	// sample, organize/rename, versions, metadata, itunes, parse-with-ai, the
	// batch-write-back/bulk-write-back endpoints) stay in server_lifecycle.go.
	protected.GET("/audiobooks", s.perm(auth.PermLibraryView), audiobooksH.ListAudiobooks)
	protected.GET("/audiobooks/count", s.perm(auth.PermLibraryView), audiobooksH.CountAudiobooks)
	protected.GET("/audiobooks/facets", s.perm(auth.PermLibraryView), audiobooksH.AudiobookFacets)
	protected.GET("/audiobooks/soft-deleted", s.perm(auth.PermLibraryView), audiobooksH.ListSoftDeletedAudiobooks)
	protected.DELETE("/audiobooks/purge-soft-deleted", s.perm(auth.PermLibraryDelete), audiobooksH.PurgeSoftDeletedAudiobooks)
	protected.POST("/audiobooks/:id/restore", s.perm(auth.PermLibraryOrganize), audiobooksH.RestoreAudiobook)
	protected.POST("/audiobooks/:id/rescan", s.perm(auth.PermLibraryEditMetadata), audiobooksH.RescanAudiobook)
	protected.GET("/audiobooks/:id", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobook)
	protected.GET("/audiobooks/:id/tags", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobookTags)
	protected.PUT("/audiobooks/:id", s.perm(auth.PermLibraryEditMetadata), audiobooksH.UpdateAudiobook)
	protected.DELETE("/audiobooks/:id", s.perm(auth.PermLibraryDelete), audiobooksH.DeleteAudiobook)
	protected.GET("/audiobooks/:id/cover", s.perm(auth.PermLibraryView), audiobooksH.ServeAudiobookCover)
	protected.GET("/audiobooks/:id/segments", s.perm(auth.PermLibraryView), audiobooksH.ListAudiobookSegments)
	protected.GET("/audiobooks/:id/segments/:segmentId/tags", s.perm(auth.PermLibraryView), audiobooksH.GetSegmentTags)
	protected.GET("/audiobooks/:id/files", s.perm(auth.PermLibraryView), audiobooksH.ListBookFiles)
	protected.PATCH("/audiobooks/:id/files/:file_id", s.perm(auth.PermLibraryEditMetadata), audiobooksH.PatchBookFile)
	protected.GET("/audiobooks/:id/changelog", s.perm(auth.PermLibraryView), audiobooksH.GetBookChangelog)
	protected.GET("/audiobooks/:id/path-history", s.perm(auth.PermLibraryView), audiobooksH.GetBookPathHistory)
	protected.GET("/audiobooks/:id/external-ids", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobookExternalIDs)
	protected.POST("/audiobooks/:id/extract-track-info", s.perm(auth.PermLibraryEditMetadata), audiobooksH.ExtractTrackInfo)
	protected.POST("/audiobooks/:id/relocate", s.perm(auth.PermLibraryOrganize), audiobooksH.RelocateBookFiles)
	protected.POST("/audiobooks/batch", s.perm(auth.PermLibraryEditMetadata), audiobooksH.BatchUpdateAudiobooks)
	protected.POST("/audiobooks/batch-operations", s.perm(auth.PermLibraryEditMetadata), audiobooksH.BatchOperations)
	protected.GET("/tags", s.perm(auth.PermLibraryView), audiobooksH.ListAllUserTags)
	protected.GET("/audiobooks/:id/user-tags", s.perm(auth.PermLibraryView), audiobooksH.GetBookUserTags)
	protected.GET("/audiobooks/:id/tags-detailed", s.perm(auth.PermLibraryView), audiobooksH.GetBookTagsDetailed)
	protected.POST("/audiobooks/batch-tags", s.perm(auth.PermLibraryEditMetadata), audiobooksH.BatchUpdateTags)
	protected.GET("/audiobooks/:id/alternative-titles", s.perm(auth.PermLibraryView), audiobooksH.GetBookAlternativeTitles)
	protected.POST("/audiobooks/:id/alternative-titles", s.perm(auth.PermLibraryEditMetadata), audiobooksH.AddBookAlternativeTitle)
	protected.DELETE("/audiobooks/:id/alternative-titles", s.perm(auth.PermLibraryDelete), audiobooksH.RemoveBookAlternativeTitle)
	protected.GET("/audiobooks/:id/metadata-history", s.perm(auth.PermLibraryView), audiobooksH.GetBookMetadataHistory)
	protected.GET("/audiobooks/:id/metadata-history/:field", s.perm(auth.PermLibraryView), audiobooksH.GetFieldMetadataHistory)
	protected.POST("/audiobooks/:id/metadata-history/:field/undo", s.perm(auth.PermLibraryEditMetadata), audiobooksH.UndoMetadataChange)
	protected.POST("/audiobooks/:id/undo-last-apply", s.perm(auth.PermLibraryEditMetadata), audiobooksH.UndoLastApply)
	protected.GET("/audiobooks/:id/field-states", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobookFieldStates)
	protected.GET("/audiobooks/:id/changes", s.perm(auth.PermLibraryView), audiobooksH.GetBookChanges)

	// Metadata domain (handlers/metadata) — 19 routes relocated from
	// server_lifecycle.go. EXACT paths + perm guards preserved.
	protected.POST("/metadata/batch-update", s.perm(auth.PermLibraryEditMetadata), metadataH.BatchUpdateMetadata)
	protected.POST("/metadata/validate", s.perm(auth.PermLibraryEditMetadata), metadataH.ValidateMetadata)
	protected.GET("/metadata/export", s.perm(auth.PermLibraryView), metadataH.ExportMetadata)
	protected.POST("/metadata/import", s.perm(auth.PermLibraryEditMetadata), metadataH.ImportMetadata)
	protected.GET("/metadata/search", s.perm(auth.PermLibraryView), metadataH.SearchMetadata)
	protected.GET("/metadata/fields", s.perm(auth.PermLibraryView), metadataH.GetMetadataFields)
	protected.POST("/metadata/bulk-fetch", s.perm(auth.PermLibraryEditMetadata), metadataH.BulkFetchMetadata)
	protected.POST("/audiobooks/:id/fetch-metadata", s.perm(auth.PermLibraryEditMetadata), metadataH.FetchAudiobookMetadata)
	protected.POST("/audiobooks/:id/search-metadata", s.perm(auth.PermLibraryEditMetadata), metadataH.SearchAudiobookMetadata)
	protected.POST("/audiobooks/:id/apply-metadata", s.perm(auth.PermLibraryEditMetadata), metadataH.ApplyAudiobookMetadata)
	protected.POST("/audiobooks/:id/mark-no-match", s.perm(auth.PermLibraryEditMetadata), metadataH.MarkAudiobookNoMatch)
	protected.POST("/audiobooks/:id/revert-metadata", s.perm(auth.PermLibraryEditMetadata), metadataH.RevertAudiobookMetadata)
	protected.GET("/audiobooks/:id/metadata-rejections", s.perm(auth.PermLibraryView), metadataH.HandleGetMetadataRejections)
	protected.GET("/audiobooks/:id/cow-versions", s.perm(auth.PermLibraryView), metadataH.ListBookCOWVersions)
	protected.POST("/audiobooks/:id/cow-versions/prune", s.perm(auth.PermLibraryEditMetadata), metadataH.PruneBookCOWVersions)
	protected.POST("/audiobooks/:id/write-back", s.perm(auth.PermLibraryEditMetadata), metadataH.WriteBackAudiobookMetadata)
	protected.PATCH("/audiobooks/:id/rating", s.perm(auth.PermLibraryEditMetadata), metadataH.HandleUpdateBookRating)
	protected.POST("/audiobooks/batch-write-back", s.perm(auth.PermLibraryEditMetadata), metadataH.BatchWriteBackAudiobooks)
	protected.POST("/audiobooks/bulk-write-back", s.perm(auth.PermLibraryEditMetadata), metadataH.HandleBulkWriteBack)

	// Tools lifecycle
	toolsH := toolshandler.New(s.toolRegistry, &config.AppConfig.Tools, nil)
	protected.GET("/tools", s.perm(auth.PermSettingsManage), toolsH.List)
	protected.GET("/tools/:name/status", s.perm(auth.PermSettingsManage), toolsH.Status)
	protected.POST("/tools/:name/install", s.perm(auth.PermSettingsManage), toolsH.Install)

	// Plugins
	plugins := protected.Group("/plugins")
	{
		plugins.GET("", s.perm(auth.PermSettingsManage), pluginsH.ListPlugins)
		plugins.GET("/:id", s.perm(auth.PermSettingsManage), pluginsH.GetPlugin)
		plugins.POST("/:id/enable", s.perm(auth.PermSettingsManage), pluginsH.EnablePlugin)
		plugins.POST("/:id/disable", s.perm(auth.PermSettingsManage), pluginsH.DisablePlugin)
		plugins.GET("/:id/health", s.perm(auth.PermSettingsManage), pluginsH.PluginHealth)
		plugins.PUT("/:id/settings", s.perm(auth.PermSettingsManage), pluginsH.UpdatePluginSettings)
	}

	// Admin-only Phase 2 routes
	adminOnly := protected.Group("")
	adminOnly.Use(servermiddleware.RequireAdmin())
	{
		adminOnly.GET("/cache/stats/keys", cacheH.HandleCacheKeysIntrospection)
		adminOnly.POST("/admin/recompact-digests", activityH.RecompactDigests)
	}
}
