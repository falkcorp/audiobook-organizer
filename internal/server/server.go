// file: internal/server/server.go
// version: 2.31.0
// guid: 4c5d6e7f-8a9b-0c1d-2e3f-4a5b6c7d8e9f
// last-edited: 2026-06-18

package server

import (
	"context"
	"fmt"

	"log"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	_ "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	operationshandlers "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	systemhandlers "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	// Blank-import the plugin packages so their init() functions run and
	// the plugins register themselves with the serviceregistry. The
	// container's PostInit calls Plugin.Register(opRegistry) for each.
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/acoustid"
	dedupplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/dedup"
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/deluge"
	_ "github.com/falkcorp/audiobook-organizer/internal/plugins/itunes"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/quarantine"
	"github.com/falkcorp/audiobook-organizer/internal/realtime"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/falkcorp/audiobook-organizer/internal/sysinfo"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/internal/updater"
	"github.com/falkcorp/audiobook-organizer/internal/work"
	"github.com/quic-go/quic-go/http3"
)

// isDebugMode returns true if logging is set to debug level, indicating
// we should enable Gin debug logging for development.
func isDebugMode() bool {
	return strings.EqualFold(config.AppConfig.LogLevel, "debug")
}

// Cached library and import path sizes to avoid expensive recalculation on frequent status checks
var cachedLibrarySize int64
var cachedImportSize int64
var cachedSizeComputedAt time.Time
var cacheLock sync.RWMutex

const librarySizeCacheTTL = 24 * time.Hour

// appVersion is set at startup via SetVersion(), injected from main.version
var appVersion = "dev"

// SetVersion sets the application version string.

// resetLibrarySizeCache resets the library size cache (for testing)

// Helper functions for pointer conversions

type aiParser interface {
	IsEnabled() bool
	ParseFilename(ctx context.Context, filename string) (*ai.ParsedMetadata, error)
	ParseAudiobook(ctx context.Context, abCtx ai.AudiobookContext) (*ai.ParsedMetadata, error)
	ParseCoverArt(ctx context.Context, imageBytes []byte, mimeType string) (*ai.ParsedMetadata, error)
	ReviewAuthorDuplicates(ctx context.Context, groups []ai.AuthorDedupInput) ([]ai.AuthorDedupSuggestion, error)
	DiscoverAuthorDuplicates(ctx context.Context, inputs []ai.AuthorDiscoveryInput) ([]ai.AuthorDiscoverySuggestion, error)
	TestConnection(ctx context.Context) error
}

var newAIParser = func(apiKey string, enabled bool) aiParser {
	return ai.NewOpenAIParser(&config.AppConfig, apiKey, enabled)
}

// enrichedBookResponse wraps a Book with resolved names for JSON responses.
type enrichedBookResponse struct {
	*database.Book
	AuthorName                       *string         `json:"author_name,omitempty"`
	SeriesName                       *string         `json:"series_name,omitempty"`
	Authors                          []authorEntry   `json:"authors,omitempty"`
	Narrators                        []narratorEntry `json:"narrators,omitempty"`
	FileExists                       *bool           `json:"file_exists,omitempty"`
	MetadataSourceHashDuplicateCount *int            `json:"metadata_source_hash_duplicate_count,omitempty"`
}

type authorEntry struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Position int    `json:"position"`
}

type narratorEntry struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Position int    `json:"position"`
}

// batchFetchBookAuthorsAndNarrators pre-fetches author and narrator join table
// entries plus their full details for all given books. Returns maps keyed by
// book ID for join entries, plus maps keyed by author/narrator ID for details.
// Nil maps are returned if the global store is not available.

// enrichBookForResponseSingle enriches a single book by pre-fetching its
// author and narrator data. Convenience wrapper for single-book endpoints.

// enrichBookForResponse resolves author, series, and narrator names from join
// tables so the JSON response contains all the fields the frontend expects.
// Pre-fetched maps of authors and narrators (by book ID) are used instead of
// per-book DB calls to eliminate N+1 queries.

// buildComparisonValuesFromActivityLog reconstructs a "before" tag snapshot by
// querying the activity log for metadata_apply entries for the given book
// recorded within a ±5 second window of ts. For each field found, the
// old_value (i.e. the value BEFORE that operation) is used as the comparison
// value. This is the fallback when GetBookAtVersion is unavailable (SQLite) or
// when the exact version key is not present in PebbleDB.

// nonEmpty returns the string if non-empty, nil otherwise (for comparison map building).

// calculateLibrarySizes computes library and import path sizes with caching

// Server represents the HTTP server
type Server struct {
	store                  database.Store
	httpServer             *http.Server
	router                 *gin.Engine
	audiobookService       *audiobookspkg.AudiobookService
	audiobookUpdateService *AudiobookUpdateService
	batchService           *batch.BatchService
	workService            *work.WorkService
	authorSeriesService    *audiobookspkg.AuthorSeriesService
	filesystemService      *fileops.FilesystemService
	importPathService      *importer.ImportPathService
	importService          *importer.ImportService
	scanService            *scanner.ScanService
	organizeService        *OrganizeService
	metadataFetchService   *metafetch.Service
	configUpdateService    *config.UpdateService
	systemService          *sysinfo.SystemService
	metadataStateService   *metafetch.MetadataStateService
	dashboardService       *sysinfo.DashboardService
	olService              *metafetch.OpenLibraryService
	dedupCache             *cache.Cache[gin.H]
	listCache              *cache.Cache[gin.H]
	facetsCache            *cache.Cache[gin.H]
	authorsCache           *cache.Cache[*audiobookspkg.AuthorWithCountListResponse]
	seriesCache            *cache.Cache[*audiobookspkg.SeriesWithCountsResponse]
	itunesSvc              *itunesservice.Service
	updater                *updater.Updater
	updateScheduler        *updater.Scheduler
	scheduler              *scheduler.TaskScheduler
	aiScanStore            *database.AIScanStore
	pipelineManager        *aiscan.PipelineManager
	// operationsHandler is the migrated operations-domain handler (instantiated
	// in wireHandlers). getSystemLogs delegates its operation_id branch to
	// operationsHandler.GetOperationLogs; routes are registered in the same
	// wireHandlers call that populates this field, so it is never nil at request
	// time.
	operationsHandler *operationshandlers.Handler
	// systemHandler is the migrated system-domain handler (instantiated in
	// wireHandlers). The public /health and /api/events routes are registered in
	// setupRoutes (before the /api/* redirect middleware, so their pre-middleware
	// ordering is preserved) via closures that delegate to this handler; the
	// remaining protected system routes are registered directly in wireHandlers.
	systemHandler      *systemhandlers.Handler
	batchPoller        *BatchPoller
	mergeService       *merge.Service
	diagnosticsService *diagnostics.Service
	changelogService   *activity.ChangelogService
	activityService    *activity.Service
	embeddingStore     *database.EmbeddingStore
	embedClient        *ai.EmbeddingClient
	metricsStore       database.MetricsStorer
	dedupEngine        *dedup.Engine
	activityWriter     *activity.Writer
	itunesActivityFn   func(entry database.ActivityEntry)
	eventBus           *plugin.EventBus
	pluginRegistry     *plugin.Registry
	quarantineSvc      *quarantine.QuarantineService
	// searchIndex is the Bleve library search index (spec DES-1).
	// Opened at startup, nil if DB path isn't set yet.
	searchIndex *search.BleveIndex
	// indexQueue feeds the single index worker goroutine. Allocated
	// when searchIndex opens, closed in Shutdown. Bounded channel —
	// a full queue drops events and the startup reindex heals gaps.
	// indexQueueMu guards against concurrent close vs. send races.
	indexQueue       chan indexRequest
	indexQueueMu     sync.RWMutex
	indexQueueClosed bool
	// indexWorkerBusy is atomically incremented while the worker processes
	// an item and decremented when done. Tests use this to synchronize
	// without relying on timed sleeps.
	indexWorkerBusy int32
	http3Server     *http3.Server

	hub              *realtime.EventHub
	writeBackBatcher *itunesservice.WriteBackBatcher
	fileIOPool       *FileIOPool
	// opRegistry is the UOS-02 registry. Plugins register their OperationDefs
	// here; the registry owns dispatch and worker pool lifecycle.
	// No plugins are registered until their own bot-tasks wire them in.
	opRegistry *opsregistry.Registry

	// opHub is the UOS-06 SSE event bus for operations events.
	// Created in NewServer, wired to opRegistry via SetBus before Start().
	opHub *opsregistry.EventHub

	// protectedPathCache holds the union of Deluge save_paths and
	// config.ProtectedPaths. Consulted before any in-place tag write.
	// Nil when Deluge is not configured (extra paths only, or no Deluge URL).
	protectedPathCache *deluge.ProtectedPathCache

	// Shutdown coordination. bgCtx is canceled when Shutdown() runs, and
	// bgWG tracks every fire-and-forget background goroutine (embedding
	// backfill, async dedup scans, etc.) so Shutdown can wait for them to
	// finish BEFORE the database is closed. Without this the embedding
	// backfill goroutine would still be holding Pebble iterators when
	// database.CloseStore() ran, and Pebble would panic with "element has
	// outstanding references" during FileCache.Unref. Every goroutine that
	// touches the store must: (1) call bgWG.Add(name) before starting,
	// (2) defer bgWG.Done(name), (3) honor bgCtx.Done() for cancellation.
	// namedWaitGroup (see bg_wg.go) extends sync.WaitGroup with name
	// tracking so the 30s-grace-period timeout log names the laggards.
	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgWG     namedWaitGroup

	// container is the SERVER-PLUGIN-REG service registry built during
	// NewServer. Stashed so handlers/tests can pull services dynamically
	// if needed (rare — most access is via the typed fields above).
	container *serviceregistry.Container

	// extraOpsRegistrar holds the ExtraOpsRegistrar for 13 scheduler ops
	// that were extracted from scheduler_extra_ops.go (SERVER-THIN-RESIDUAL).
	// Constructed early (before opRegistrars loop) with empty deps; deps are
	// filled in after all services are wired so closures see correct values
	// at run time.
	extraOpsRegistrar *scheduler.ExtraOpsRegistrar

	// toolRegistry manages external binary lifecycle (Ollama, fpcalc).
	toolRegistry *tools.ToolRegistry
	// ollamaDaemon starts/stops the Ollama binary on demand.
	ollamaDaemon *tools.OllamaDaemon
	// embedQueue debounces re-embedding requests.
	embedQueue *tools.EmbedQueue
	// hnswPersistDir is the directory where HNSW snapshots are saved/loaded.
	hnswPersistDir string
}

// ServerConfig holds server configuration
type ServerConfig struct {
	Port         string
	Host         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	TLSCertFile  string // Optional TLS certificate file for HTTPS/HTTP2/HTTP3
	TLSKeyFile   string // Optional TLS key file for HTTPS/HTTP2/HTTP3
	HTTP3Port    string // Optional HTTP/3 port (UDP). If set with TLS, enables HTTP/3
}

// Store returns the database.Store dependency the server was constructed with.
func (s *Server) Store() database.Store {
	return s.store
}

// OpRegistry returns the operations registry. Used by the operation-runner
// child mode (cmd.RunOperationRunner) to dispatch a single op without
// starting workers or the HTTP server.
func (s *Server) OpRegistry() *opsregistry.Registry {
	return s.opRegistry
}

// publishEvent publishes a lifecycle event to the plugin event bus.
func (s *Server) publishEvent(ctx context.Context, event plugin.Event) {
	if s.eventBus != nil {
		s.eventBus.Publish(ctx, event)
	}
}

// NewServer constructs a Server with an explicit Store dependency.
// s.Store() is still assigned at startup for code that hasn't
// been migrated to use s.Store() yet (see DI migration plan 4.4).
func NewServer(store database.Store) *Server {
	// Set Gin to release mode (production) unless debug flag is set.
	// In release mode, Gin suppresses route-registration logging and uses optimized
	// middleware. See MED-9 in fable5-review-findings.md.
	if !isDebugMode() {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New() // don't use gin.Default() — we add our own middleware

	// Trust no proxy headers. Without this, Gin honors X-Forwarded-For from any
	// source, so c.ClientIP() can be spoofed — bypassing every per-IP rate
	// limiter (bootstrap, login throttle) by cycling the header (pen-test
	// finding HIGH-2). This deployment is direct-connect (TLS terminated by the
	// service, no reverse proxy). If a trusted reverse proxy is ever added,
	// replace nil with its CIDR allowlist, e.g.
	// router.SetTrustedProxies([]string{"10.0.0.0/8"}).
	if err := router.SetTrustedProxies(nil); err != nil {
		slog.Warn("failed to disable trusted proxies", "err", err)
	}

	// Custom logger that skips noisy polling endpoints
	// (UOS-14: /operations/active removed; SkipPaths entry removed)
	router.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		SkipPaths: []string{"/api/v1/operations/events"},
	}))
	router.Use(gin.Recovery())
	router.Use(corsMiddleware())
	router.Use(servermiddleware.BasicAuth())
	router.Use(gzip.Gzip(gzip.DefaultCompression, gzip.WithExcludedPaths([]string{"/api/events"})))
	// OpenTelemetry instrumentation: create per-handler spans and record metrics
	router.Use(otelgin.Middleware("audiobook-organizer"))

	// Register metrics (idempotent)
	metrics.Register()

	resolvedStore := store
	if resolvedStore == nil {
		slog.Error("NewServer called with nil store — this is a programming error; callers must provide a concrete database.Store")
		os.Exit(1)
	}
	// Wire the scanner package's local store so its free helpers
	// (createBookFilesForBook, saveBookToDatabase, ProcessBooksParallel
	// inline DB calls) no longer reach for database.GetGlobalStore
	// (SERVER-GLOBAL-STORE-AUDIT phase 7).
	scanner.SetStore(resolvedStore)
	bgCtx, bgCancel := context.WithCancel(context.Background())
	server := &Server{
		store:                  store,
		bgCtx:                  bgCtx,
		bgCancel:               bgCancel,
		router:                 router,
		audiobookUpdateService: NewAudiobookUpdateService(resolvedStore),
		authorSeriesService:    audiobookspkg.NewAuthorSeriesService(resolvedStore),
		importService:          importer.NewImportService(resolvedStore),
		// MAYDEPLOY-I4: caches were bounded only by 24h TTL and could grow
		// to hundreds of thousands of entries (estimated 0.5–1.5 GB) on heavy
		// use. Cap entry count via LRU; defaults are conservative starting
		// points — watch cache_evictions_total{reason="capacity"} in prod.
		dedupCache:   cache.NewWithLimit[gin.H]("dedup", 24*time.Hour, 1000),
		listCache:    cache.NewWithLimit[gin.H]("list", 24*time.Hour, 2000),
		facetsCache:  cache.NewWithLimit[gin.H]("facets", 24*time.Hour, 100),
		authorsCache: cache.NewWithLimit[*audiobookspkg.AuthorWithCountListResponse]("authors", 24*time.Hour, 1),
		seriesCache:  cache.NewWithLimit[*audiobookspkg.SeriesWithCountsResponse]("series", 24*time.Hour, 1),
		// olService, updater, updateScheduler are container-built;
		// wireServerFromContainer populates the fields.
		diagnosticsService: diagnostics.NewService(resolvedStore, nil, config.AppConfig.ITunes.LibraryReadPath),
		changelogService:   activity.NewChangelogService(resolvedStore),
	}

	// SERVER-PLUGIN-REG: build the service registry container.
	// Production wires services by named group (REGISTRY-NAMED-GROUPS,
	// PR #886). Adding a new service is just `Groups: []string{"core"}`
	// (or "ai", "plugins", etc.) on its ServiceDef — no edit to this
	// file. Audit a group with
	//   grep -rn 'Groups.*"<groupname>"' internal/ --include="*.go"
	regCtx := context.Background()
	regContainer := serviceregistry.NewContainer().
		Override("store", resolvedStore).
		Override("config", &config.AppConfig).
		Override("appversion", appVersion).
		IncludeGroup("core", "ai", "scheduler", "plugins").
		// W3.6 batchpoller is in scheduler group; opregistry/ophub are
		// in scheduler group. No additional explicit names needed.
		Include() // explicit Include() reserved for ad-hoc additions
	// `activity` (+ `activitystore`) only registers when DatabasePath is
	// set — the NutsDB sidecar can't open without a path.
	if config.AppConfig.DatabasePath != "" {
		regContainer.IncludeGroup("activity")
	}
	if err := regContainer.Resolve(); err != nil {
		slog.Error("serviceregistry resolve", "err", err)
		os.Exit(1)
	}
	if err := regContainer.Build(regCtx); err != nil {
		slog.Error("serviceregistry build", "err", err)
		os.Exit(1)
	}

	// Load the HNSW snapshot BEFORE PostInit so dedup.Engine.PostInit can detect
	// the populated store and skip HydrateChromem. Loading after PostInit caused a
	// crash loop: HydrateChromem re-inserted all ~38K existing keys into the
	// already-populated graph, triggering a nil-deref in coder/hnsw v0.6.1 when
	// Delete+Add violates the per-layer node invariant (HNSW-CRASH-2026-06-18).
	if config.AppConfig.DatabasePath != "" {
		hnswDir := filepath.Join(filepath.Dir(config.AppConfig.DatabasePath), "hnsw")
		server.hnswPersistDir = hnswDir
		if raw, ok := serviceregistry.TryGet[database.VectorANNStore](regContainer, "chromemstore"); ok && raw != nil {
			if hnswStore, ok := raw.(*database.HNSWEmbeddingStore); ok {
				if err := hnswStore.Import(hnswDir); err != nil {
					if !errors.Is(err, database.ErrNoHNSWSnapshot) {
						slog.Warn("hnsw: import failed, will hydrate from PebbleDB", "err", err)
					}
				} else {
					slog.Info("hnsw: loaded from on-disk snapshot", "dir", hnswDir)
				}
			}
		}
	}

	if err := regContainer.PostInit(regCtx); err != nil {
		slog.Error("serviceregistry postinit", "err", err)
		os.Exit(1)
	}
	wireServerFromContainer(server, regContainer)
	server.container = regContainer

	// Register batch poller handlers now that batchPoller is wired from container.
	if server.batchPoller != nil {
		server.registerBatchPollerHandlers()
	}

	// Propagate rootDir into the store so LibraryStats can split organized vs unorganized.
	resolvedStore.SetRootDir(config.AppConfig.RootDir)

	// Inject the store into the maintenance package so jobs can access it.
	maintenance.InjectStore(resolvedStore)
	if server.writeBackBatcher != nil {
		maintenance.InjectEnqueuer(server.writeBackBatcher)
	}

	// server.eventBus and server.quarantineSvc are now populated by
	// wireServerFromContainer above (W2). Only the global plugin registry
	// needs explicit construction here.
	server.pluginRegistry = plugin.Global()

	// server.opHub + server.opRegistry are populated by
	// wireServerFromContainer above (W3.5 RegistryWrapper). The dispatcher
	// is started in Server.Start (server_lifecycle.go) where the bgCtx is
	// available — same lifecycle as before.

	// SERVER-THIN-RESIDUAL: build the ExtraOpsRegistrar before the opRegistrars
	// loop so the 13 scheduler ops (scheduler_extra_ops.go shim) can delegate
	// to it. Fields available from wireServerFromContainer are set now;
	// aiScanStore, dedupEngine, and activityWriter are back-filled below after
	// their lazy initialisation. Closures see the updated values at op run time.
	server.extraOpsRegistrar = scheduler.NewExtraOpsRegistrar(resolvedStore, scheduler.ExtraOpsDeps{
		DedupCache:           server.dedupCache,
		OLService:            server.olService,
		MetadataFetchService: server.metadataFetchService,
		AudiobookService:     server.audiobookService,
	})

	// UOS-12: maintenance plugin — 26 ops migrated from scheduler_tasks.go.
	// Guard on RootDir: tests don't configure AppConfig, so RootDir is ""
	// and the mock store has no UpsertOpDefinitionV2 expectations.
	if config.AppConfig.RootDir != "" {
		if err := maintenanceplugin.New(server).Register(server.opRegistry); err != nil {
			slog.Warn("maintenance plugin register", "err", err)
		}
		// Iterate all op registrars. Each file calls addOpRegistrar in its init()
		// so new ops never require touching this block.
		for _, reg := range opRegistrars {
			if err := reg(server, server.opRegistry); err != nil {
				slog.Warn("op registrar", "err", err)
			}
		}
	}

	// iTunes service + plugin are both container-built and registered via
	// PostInit. See internal/server/registry_wire.go ("itunes") and
	// internal/plugins/itunes/register.go ("itunesplugin"). No inline
	// wiring is needed here.

	// updater + updateScheduler are container-built and started by
	// Container.Start() during Server.Start (SERVER-LIFECYCLE-FLIP).
	// No inline Start() needed.

	// OL dump store + ISBN enrichment wiring moved into metafetch.Service
	// PostInit (internal/metafetch/lifecycle.go). The container drives
	// these now; no inline server-side wiring needed.

	// aiScanStore + pipelineManager are container-built (registry_wire.go
	// "aiscanstore" + "pipelinemanager"). Back-fill extraOpsRegistrar's
	// AIScanStore dep — the registrar was constructed before
	// wireServerFromContainer populated the field.
	if server.aiScanStore != nil {
		server.extraOpsRegistrar.Deps.AIScanStore = server.aiScanStore
	}

	// server.activityService is now populated by wireServerFromContainer
	// when config.DatabasePath is set (W2). Nothing to do here.

	// metricsStore is container-built (registry_wire.go "metricsstore");
	// wireServerFromContainer populates the field.

	// One-shot migration from the legacy embeddings.db SQLite sidecar.
	// Safe to call every startup: a flag key in PebbleDB prevents re-runs.
	// NOTE(fable5 T022): MigrateEmbeddingsFromSQLite was removed from server
	// startup. The embeddings.db file on prod has been stale since 2026-05-11
	// and the migration already ran. Use the 'migrate-embeddings-from-sqlite'
	// admin subcommand if a manual one-off migration is needed.

	// AI cluster (embeddingstore / embedclient / llmparser / chromemstore /
	// aijobsstore / dedup / metadatascorer / metadatallmscorer) is now
	// fully container-driven. dedup.Engine.PostInit wires SetChromemStore,
	// SetAIJobsStore, SetDedupVerdictApplier + launches the chromem hydrate
	// goroutine. metafetch.Service.PostInit wires SetDedupEngine,
	// SetMetadataScorer, SetMetadataLLMScorer, SetActivityService.
	// extraOpsRegistrar's DedupEngine dep gets the container's engine.
	if server.dedupEngine != nil {
		server.extraOpsRegistrar.Deps.DedupEngine = server.dedupEngine
	}

	// Organize collision hook stays inline because it uses serverOrganizeHooks
	// which captures *Server (bgCtx + bgWG + cross-service helpers). Moving
	// it requires further decoupling — tracked under SERVER-LIFECYCLE-FLIP.
	if server.dedupEngine != nil {
		server.organizeService.SetOrganizeHooks(&serverOrganizeHooks{server: server})
		slog.Info("Organize collision hook wired via OrganizeService")
	}

	// --- Managed tool lifecycle ---
	// Wire ToolRegistry, OllamaDaemon, EmbedQueue, and HNSW persist dir.
	// This block runs after wireServerFromContainer so server.embedClient
	// is already populated.
	{
		toolsCfg := &config.AppConfig.Tools
		server.toolRegistry = tools.NewToolRegistry(toolsCfg)

		// Register known tools with their latest releases.
		if rel, ok := tools.LatestRelease("ollama"); ok {
			server.toolRegistry.Register(tools.ToolDef{Name: "ollama", Release: rel})
		}
		if rel, ok := tools.LatestRelease("fpcalc"); ok {
			server.toolRegistry.Register(tools.ToolDef{Name: "fpcalc", Release: rel})
		}

		// Wire fingerprint package so fpcalc resolves via the registry.
		if fpcalcPath, err := server.toolRegistry.Resolve("fpcalc"); err == nil {
			fingerprint.SetResolvedFpcalcPath(fpcalcPath)
		} else {
			slog.Info("fpcalc: not available via registry, fingerprinting disabled", "reason", err)
		}

		// Wire Ollama daemon (only when mode is not disabled).
		if toolsCfg.Ollama.Mode != tools.ToolModeDisabled {
			if ollamaBin, err := server.toolRegistry.Resolve("ollama"); err == nil {
				server.ollamaDaemon = tools.NewOllamaDaemon(tools.OllamaDaemonConfig{
					BinPath: ollamaBin,
					PIDFile: toolsCfg.ManagedDir + "/ollama.pid",
					Port:    11434,
				})
			}
		}

		// Wire embed queue with debounce drain.
		server.embedQueue = tools.NewEmbedQueue(tools.EmbedQueueConfig{
			Capacity:      10_000,
			Debounce:      time.Duration(toolsCfg.OllamaDebounceMin) * time.Minute,
			AllowPeriodic: toolsCfg.AllowPeriodicOllama,
			DrainFn:       server.drainEmbedQueue,
		})

		// Set HNSW persist directory alongside the main database.
		if config.AppConfig.DatabasePath != "" {
			server.hnswPersistDir = filepath.Join(filepath.Dir(config.AppConfig.DatabasePath), "hnsw")
		}

		// Gate embedding client on Ollama availability.
		if server.embedClient != nil {
			server.embedClient.SetOllamaAvailable(server.toolRegistry.Available("ollama"))
		}

		// Wire dedup plugin tool registry so Ollama-gated ops check availability.
		if dedupPlug, ok := serviceregistry.TryGet[*dedupplugin.Plugin](regContainer, "dedupplugin"); ok && dedupPlug != nil {
			dedupPlug.SetToolRegistry(server.toolRegistry)
		}
	}

	// Start embedding backfill if dedup engine is ready. Tracked via
	// bgWG so Shutdown() can wait for it to finish before the database
	// closes — without this, a backfill still iterating Pebble when the
	// server stops will leave iterators open and panic inside Pebble's
	// FileCache.Unref during Close(). Could move into a Starter on
	// dedup.Engine; deferred for now because the goroutine wants the
	// server's bgWG for Shutdown coordination.
	if server.dedupEngine != nil {
		server.bgWG.Add("embedding-backfill")
		go func() {
			defer server.bgWG.Done("embedding-backfill")
			server.runEmbeddingBackfill()
		}()
	}

	// Create hub, batcher, and file I/O pool as Server fields
	server.hub = realtime.NewEventHub()
	realtime.SetGlobalHub(server.hub)

	// The batcher moved under itunesservice.Service in Phase 2 M1 step 2.
	// Server still keeps a typed field for back-compat with the many call
	// sites that were already using server.writeBackBatcher — but it now
	// points at the service-owned instance. When the service is nil (test
	// paths), the field stays nil and enqueues are silent no-ops via the
	// `if batcher != nil` guards already in place.
	server.writeBackBatcher = server.itunesSvc.Batcher
	server.fileIOPool = NewFileIOPool(4)
	server.fileIOPool.SetStore(resolvedStore)

	// writeBackBatcher fan-out into metafetch / merge / quarantine /
	// audiobook now happens in those services' PostInit hooks (they pull
	// "writebackbatcher" via TryGet on their local enqueuer interface).
	// organizeService.SetWriteBackBatcher + ScanEnqueuer stay inline:
	// OrganizeService lives in this package and ScanEnqueuer captures
	// server.opRegistry — not yet a clean container service.
	server.organizeService.SetWriteBackBatcher(server.writeBackBatcher)
	server.organizeService.ScanEnqueuer = func(ctx context.Context) error {
		_, err := server.opRegistry.EnqueueOp(ctx, "library.scan", nil)
		return err
	}

	// Wire iTunes-specific organizer callbacks now that itunesSvc is ready.
	if server.itunesSvc.Enabled() {
		server.organizeService.DiscoverITunesLibraryPath = func() string {
			return server.itunesSvc.Importer.DiscoverLibraryPath()
		}
		server.organizeService.ExecuteITunesSync = func(ctx context.Context, log logger.Logger, libraryPath string) error {
			return server.itunesSvc.Importer.Sync(ctx, libraryPath, nil, server.itunesActivityFn, log)
		}
	}

	// ImportService uses the iTunes service's TrackProvisioner (moved
	// during Phase 2 M1 step 1). Nil provisioner → ITL track provisioning
	// is skipped (service is disabled or construction failed above).
	server.importService.SetTrackProvisioner(server.itunesSvc.Provisioner)
	server.importService.SetDedupEngine(server.dedupEngine)
	// M4: wire the UOS registry so the importer can enqueue dedup.check-book
	// when DedupOnImportViaScheduler is enabled in config (default false).
	server.importService.SetRegistry(server.opRegistry)
	// After M1 step 2, the batcher is owned by itunesservice.Service and
	// Provisioner was wired with the real Enqueuer at Service.New() time.
	// No SetEnqueuer hop needed.

	// Register file-op recovery handler (uses server closure instead of globalServer)
	RegisterFileOpRecovery("apply_metadata", func(bookID string) {
		if server.metadataFetchService == nil {
			slog.Warn("no server instance for apply_metadata recovery of book", "bookID", bookID)
			return
		}
		server.metadataFetchService.ApplyMetadataFileIO(bookID)
		if _, err := server.metadataFetchService.WriteBackMetadataForBook(bookID); err != nil {
			slog.Warn("recovery write-back for", "bookID", bookID, "err", err)
		}
		if server.writeBackBatcher != nil {
			server.writeBackBatcher.Enqueue(bookID)
		}
	})

	// Activity-service fan-out into metafetch / audiobook / scanner /
	// itunesSvc.Repair now happens in those services' PostInit hooks.
	// What's left in this block is genuinely server-internal: starting
	// the writer, the global log.SetOutput, extraOpsRegistrar back-fill,
	// the itunesActivityFn closure, scanner.SetScanHooks (process-global),
	// and the startup-record entry.
	if server.activityService != nil {
		// activityWriter is started by Container.Start in Server.Start
		// (SERVER-LIFECYCLE-FLIP). What stays inline here is the
		// extraOpsRegistrar back-fill + log.SetOutput global, both of
		// which need the writer reference but not its Start.
		if aw := server.activityWriter; aw != nil {
			server.extraOpsRegistrar.Deps.ActivityWriter = aw
			log.SetOutput(aw)
			// Also route the slog default to a text handler writing to
			// the activity writer (in addition to stderr). Without this,
			// the operations registry — which uses slog.Default() —
			// emits "starting run" / "run finished" lines to stderr
			// only, and the Activity Log page shows nothing for
			// scheduler runs. The text handler emits a key=value line
			// per record that the activity writer's line parser routes
			// into an ActivityEntry.
			// activity.Writer.Write already tees to os.Stdout before
			// parsing, so writing to aw alone keeps systemd journal
			// capture intact. Adding os.Stderr to a MultiWriter here
			// produced duplicate journal lines (one via stderr, one
			// via aw → stdout).
			handler := slog.NewTextHandler(aw, &slog.HandlerOptions{Level: slog.LevelInfo})
			slog.SetDefault(slog.New(handler))
		}

		// Task 15: iTunes sync → activity log
		server.itunesActivityFn = func(entry database.ActivityEntry) {
			_ = server.activityService.Record(entry)
		}

		// Task 16: Scanner → activity log (via ScanHooks interface)
		scanner.SetScanHooks(&serverScanHooks{
			activityService: server.activityService,
			dedupFn:         server.fireDedupOnImport,
		})

		// Record server startup in activity log
		_ = server.activityService.Record(database.ActivityEntry{
			Tier:    "debug",
			Type:    "system",
			Level:   "info",
			Source:  "server",
			Summary: "Server started, activity log initialized",
		})
		slog.Info("Activity log service initialized and recording")
	}

	// Wire post-scan auto-quarantine hook.
	server.scanService.PostScanFn = server.quarantineSvc.AutoQuarantineFailedScans

	// Wire post-folder auto-organize hook (breaks scanner→organizer import cycle).
	server.scanService.AutoOrganizeFn = func(ctx context.Context, books []scanner.Book, l logger.Logger) {
		if len(books) == 0 {
			return
		}
		if !config.AppConfig.AutoOrganize || config.AppConfig.RootDir == "" {
			if config.AppConfig.AutoOrganize {
				l.Warn("Auto-organize enabled but root_dir not set")
			}
			return
		}
		org := organizer.NewOrganizer(&config.AppConfig)
		org.SetStore(server.Store())
		organized := 0
		for i := range books {
			if l.IsCanceled() {
				break
			}
			dbBook, err := server.store.GetBookByFilePath(books[i].FilePath)
			if err != nil || dbBook == nil {
				continue
			}
			newPath, _, err := org.OrganizeBook(dbBook)
			if err != nil {
				l.Warn("Organize failed for %s: %v", dbBook.Title, err)
				continue
			}
			if newPath != dbBook.FilePath {
				oldPath := dbBook.FilePath
				dbBook.FilePath = newPath
				scanner.ApplyOrganizedFileMetadata(dbBook, newPath)
				if _, err := server.store.UpdateBook(dbBook.ID, dbBook); err != nil {
					l.Error("Failed to update path for %s: %v — rolling back", dbBook.Title, err)
					if rbErr := os.Rename(newPath, oldPath); rbErr != nil {
						l.Error("CRITICAL: rollback failed for %s: file at %s, DB expects %s", dbBook.ID, newPath, oldPath)
					}
				} else {
					organized++
				}
			}
		}
		l.Info("Auto-organize complete: %d organized", organized)
	}

	// Note: the search index is opened in Start(), not here, so
	// tests that construct a Server without calling Start don't
	// leak Bleve file handles.

	// Initialize the protected-path cache. The cache merges Deluge save_paths
	// (fetched lazily on first IsProtected call) with any static prefixes from
	// config.ProtectedPaths. If Deluge is not configured, the cache still works
	// for the static paths (e.g. iTunes library root).
	{
		dc := deluge.GetClient()
		server.protectedPathCache = deluge.NewProtectedPathCache(dc, config.AppConfig.ProtectedPaths)
		slog.Info("ProtectedPathCache initialized ( static extra paths)", "count", len(config.AppConfig.ProtectedPaths))

		// Wire the pre-flight safe-write guard into the metadata package so that
		// all taglib writes (metadata apply, single-tag patch) check for Deluge-
		// protected paths before writing. This uses the same ProtectedPathCache
		// and a LibraryImporterAdapter backed by the server's store.
		importer := deluge.NewLibraryImporterAdapter(resolvedStore, dc, &config.AppConfig)
		deps := tagger.SafeWriteDeps{
			ProtectedCache: server.protectedPathCache,
			Importer:       importer,
		}
		metadata.SetSafeWriteDeps(deps)
		slog.Info("metadata.SetSafeWriteDeps wired (Deluge pre-flight guard active)")

		// Also wire into the metafetch service so cover-art embeds use the guard.
		server.metadataFetchService.SetSafeWriteDeps(deps)
		slog.Info("metafetch.Service.SetSafeWriteDeps wired (cover embed guard active)")

		// Register the Deluge plugin (UOS-11).
		// Guard on RootDir: tests don't configure AppConfig, so RootDir="" and the
		// mock store has no UpsertOpDefinitionV2 expectations.
		// delugePlugin op-def registration is now done in its PostInit
		// method (W7). Container.PostInit() runs it earlier in NewServer.
	}

	server.setupRoutes()

	// Initialize plugins after routes are set up so plugins can register
	// their own sub-routes under /api/v1/plugins/{id}/.
	server.initPlugins(bgCtx)

	return server
}

// drainEmbedQueue is the EmbedQueue DrainFn: ensures Ollama is running,
// signals the embedding backfill, then stops Ollama when idle.
func (s *Server) drainEmbedQueue(ctx context.Context) error {
	if s.ollamaDaemon != nil {
		if err := s.ollamaDaemon.EnsureRunningOrAdopt(ctx); err != nil {
			return fmt.Errorf("drain embed queue: start ollama: %w", err)
		}
		defer s.ollamaDaemon.StopWhenIdle(ctx) //nolint:errcheck
	}
	return nil
}

// SearchIndex returns the server's Bleve index, or nil if none is
// open. Handlers use this to decide whether to route queries
// through Bleve or fall back to the legacy SearchBooks path.

// safeWriteDeps builds a tagger.SafeWriteDeps from the server's wired
// dependencies. Used by movement_atom_cleanup and any other server-package
// code that calls tag-writing functions directly (outside the metadata
// package path that has its own package-level deps).

// buildSearchIndexIfEmpty runs a full reindex of the library when
// the search index has zero documents. Honors s.bgCtx so shutdown
// stops the backfill cleanly. Page size matches the existing
// backfill code to keep memory bounded.

// IndexBookByID reads a book (plus its related rows) and upserts
// the flat BookDocument into the search index. Best-effort: logs
// and returns nil if the index isn't open or the book is missing.
// Callers: handlers that create or update a book, plus the startup
// full-build goroutine.

// DeleteIndexedBook removes a book from the search index. Called
// after a book delete (soft or hard). Safe when the index isn't
// open.

// serverScanHooks implements scanner.ScanHooks, bridging scanner
// callbacks to the server's activity service and dedup engine.
type serverScanHooks struct {
	activityService *activity.Service
	dedupFn         func(bookID string)
}

// serverOrganizeHooks implements organizer.OrganizeHooks, bridging
// collision callbacks to the server's dedup engine.
type serverOrganizeHooks struct {
	server *Server
}

// fireDedupOnImport runs the dedup engine's Layer 1 + Layer 2 checks for
// a freshly created book, in a bgWG-tracked goroutine so it doesn't
// block the caller and shutdown drains it before closing Pebble.
//
// This is the single entry point used by every CreateBook path —
// scanner imports (via ScanHooks.OnImportDedup), iTunes sync, manual
// book creation, etc. Having every create path fire the hook means new
// books get exact-match hash/ISBN/title checks against the whole
// library immediately, instead of waiting for a user-triggered Re-scan.
//
// In particular this catches the "iTunes sync creates a parallel row
// for a book we already have under audiobook-organizer/" bug — the
// Layer 1 file-hash check fires inside CheckBook, sees the match, and
// records a pending dedup candidate that surfaces in the UI.
//
// Safe to call even when the dedup engine is disabled — it's a no-op.

// resumeInterruptedOperations checks for operations left in running/queued state
// from a previous server lifecycle and re-enqueues them.

// Start starts the HTTP server

// perm returns a Gin middleware that checks the calling user has the
// given permission. It's a thin wrapper around RequirePermission from
// the middleware package, curried with the server's Store. Used inline
// in route registration: `protected.GET("/path", s.perm(P), s.handler)`.

// itunesSvcGuard returns a gin handler that checks s.itunesSvc is non-nil
// and enabled before delegating to fn. Any route that directly dereferences
// a sub-component (Paths.Start, Transfer.*) must be wrapped here so that
// setupRoutes doesn't panic when itunesSvc is nil or disabled.

// setupRoutes configures all the routes

// corsMiddleware adds restrictive CORS headers.

// filesCommonDir finds the common parent directory of all BookFile file paths.

// isProtectedPath returns true if the given file path is under an import path
// or the iTunes library folder. Files in protected paths must NEVER be moved,
// renamed, or deleted — only hardlinked or copied to the organized library.

// loadDismissedDedupGroups loads the set of dismissed dedup group keys from user preferences.

// saveDismissedDedupGroups saves the set of dismissed dedup group keys to user preferences.

// triggerITunesSync finds the library path from DB and enqueues a sync if the file changed.

// --- User tag handlers ---

// ---- Work handlers ----

// extractSeriesNameForDedup tries to extract the actual series name from patterns like
// "Book Title: Series Name" or "Series Name, Book 3". Returns the suggested
// series name and whether a suggestion was made.

// seriesPrunePreviewGroup describes a duplicate group or orphan for the preview endpoint.
type seriesPrunePreviewGroup struct {
	Name        string `json:"name"`
	CanonicalID int    `json:"canonical_id"`
	MergeIDs    []int  `json:"merge_ids"`
	BookCount   int    `json:"book_count"`
	Type        string `json:"type"` // "duplicate" or "orphan"
}

// seriesPrunePreviewResult holds the dry-run result.
type seriesPrunePreviewResult struct {
	Groups         []seriesPrunePreviewGroup `json:"groups"`
	DuplicateCount int                       `json:"duplicate_count"`
	OrphanCount    int                       `json:"orphan_count"`
	TotalCount     int                       `json:"total_count"`
}

// computeSeriesPrunePreview builds the preview of what a series prune would do.

// stripChapterFromTitle removes chapter/book numbers from titles to improve search results
// Examples: "The Odyssey: Book 01" -> "The Odyssey", "Harry Potter - Chapter 5" -> "Harry Potter"

// stripSubtitle removes subtitle portions from a title, e.g.
// "Title: A Subtitle" → "Title", "Title - A Subtitle" → "Title".
// Returns the original title if no subtitle separator is found.

// bulkFetchMetadataRequest / bulkFetchMetadataResult moved to the
// handlers/metadata sub-package (mirrored there) along with the bulkFetchMetadata
// HTTP handler (ADR-003 Phase 4). They were only used by that handler.

// Version Management Handlers

// extractTitleFromSegmentFilename tries to extract a meaningful book title
// from a segment filename like "01 ASoIaF 1 - A Game of Thrones.m4b".

// reassignExternalIDsForFiles moves external ID mappings (iTunes PIDs) from
// sourceBookID to targetBookID for the given files. It matches by file_path or
// ITunesPersistentID on the external_id_map entries.

// GetDefaultServerConfig returns default server configuration

// Author alias handlers

// --- AI Scan Pipeline Handlers ---

// --- Preview Rename & Metadata Writeback Handlers ---

// --- Preview Organize & Single-Book Organize Handlers ---

// markDuplicatesFlaggedDirty marks the duplicates_flagged quick-query cache
// entry dirty. Called from dedup handlers (dismiss, scan upsert) so the count
// is recomputed on the next menu open rather than staying stale.
func (s *Server) markDuplicatesFlaggedDirty(reason string) {
	if s.Store() == nil {
		return
	}
	type quickQueryDirtier interface {
		MarkQuickQueryDirty(id, reason string)
	}
	if qd, ok := s.Store().(quickQueryDirtier); ok {
		qd.MarkQuickQueryDirty("duplicates_flagged", reason)
		return
	}
	// Unwrap if decorated (IndexedStore, etc.)
	type unwrapper interface {
		Unwrap() database.Store
	}
	if uw, ok := s.Store().(unwrapper); ok {
		if qd, ok2 := uw.Unwrap().(quickQueryDirtier); ok2 {
			qd.MarkQuickQueryDirty("duplicates_flagged", reason)
		}
	}
}
