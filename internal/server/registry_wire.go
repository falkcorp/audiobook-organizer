// file: internal/server/registry_wire.go
// version: 1.11.0

package server

import (
	"log/slog"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/quarantine"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/falkcorp/audiobook-organizer/internal/sysinfo"
	"github.com/falkcorp/audiobook-organizer/internal/updater"
	"github.com/falkcorp/audiobook-organizer/internal/work"
)

// init registers services that can't live in their domain packages due
// to import cycles or because they need package-private symbols from
// internal/server.
//
//   - `system` — needs appVersion + calculateLibrarySizes from this pkg.
//   - `embeddingstore`, `chromemstore`, `aijobsstore` — live in
//     internal/database which can't import internal/config (cycle).
//   - `dedup` (the engine) — needs *config.Config to read thresholds;
//     internal/dedup doesn't already import internal/config, so registering
//     here avoids forcing a new dependency on that pkg.
func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "system",
		Needs:  []string{"store"},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, "store")
			return sysinfo.NewSystemService(store, appVersion, calculateLibrarySizes), nil
		},
	})

	// embeddingstore — Pebble-backed key namespace for dedup embeddings.
	// Returns nil if the underlying store isn't *PebbleStore (e.g. tests).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "embeddingstore",
		Needs:  []string{"store"},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, "store")
			ps, ok := store.(*database.PebbleStore)
			if !ok {
				return (*database.EmbeddingStore)(nil), nil
			}
			return database.NewEmbeddingStore(ps.DB()), nil
		},
	})

	// chromemstore — in-memory ANN vector store for dedup Layer 2. The backend
	// is config-selectable (config.VectorIndexBackend): "chromem" (default,
	// brute-force cosine scan) or "hnsw" (coder/hnsw graph, sub-linear search).
	// Both satisfy database.VectorANNStore. Optional; on disabled/error the Build
	// returns an UNTYPED nil so TryGet[database.VectorANNStore] yields ok=false
	// (returning a typed-nil pointer would assert non-nil to the interface and
	// trip the engine's chromemStore != nil guards — the classic Go nil-iface
	// trap). Dedup then falls back to the Pebble linear scan.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "chromemstore",
		Needs:  []string{"config"},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, "config")
			if cfg.DatabasePath == "" {
				return nil, nil
			}
			dir := filepath.Dir(cfg.DatabasePath)
			// Dimension is config-driven so a local model (e.g. bge-m3 = 1024)
			// can replace OpenAI text-embedding-3-large (3072). Guard against a
			// zero value: an upgraded install loads an older config_blob that
			// predates EmbeddingDimensions (whole-struct replace zeroes it), so
			// fall back to the historical 3072 default rather than build a
			// 0-dim store.
			dims := cfg.EmbeddingDimensions
			if dims <= 0 {
				dims = 3072
			}
			if cfg.VectorIndexBackend == "hnsw" {
				return database.NewHNSWEmbeddingStore(dims), nil
			}
			store, err := database.NewChromemEmbeddingStore(dir, dims)
			if err != nil {
				return nil, nil
			}
			return store, nil
		},
	})

	// aijobsstore — interface assertion on the main store.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "aijobsstore",
		Needs:  []string{"store"},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, "store")
			if s, ok := store.(database.AIJobsStore); ok {
				return s, nil
			}
			return database.AIJobsStore(nil), nil
		},
	})

	// dedup — the duplicate detection engine.
	// Returns nil if any required dep is missing (no API key, no embed
	// client, etc.) — matches the existing inline conditional construction
	// in NewServer.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "dedup",
		Needs:  []string{"store", "config", "embeddingstore", "embedclient", "llmparser", "merge"},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, "config")
			if cfg.OpenAIAPIKey == "" || !cfg.EmbeddingEnabled {
				return (*dedup.Engine)(nil), nil
			}
			embStore, _ := serviceregistry.TryGet[*database.EmbeddingStore](c, "embeddingstore")
			embClient, _ := serviceregistry.TryGet[*ai.EmbeddingClient](c, "embedclient")
			llmParser, _ := serviceregistry.TryGet[*ai.OpenAIParser](c, "llmparser")
			if embStore == nil || embClient == nil || llmParser == nil {
				return (*dedup.Engine)(nil), nil
			}
			store := serviceregistry.Get[database.Store](c, "store")
			mergeSvc := serviceregistry.Get[*merge.Service](c, "merge")
			engine := dedup.NewEngine(embStore, store, embClient, llmParser, mergeSvc)
			engine.BookHighThreshold = cfg.DedupBookHighThreshold
			engine.BookLowThreshold = cfg.DedupBookLowThreshold
			engine.AuthorHighThreshold = cfg.DedupAuthorHighThreshold
			engine.AuthorLowThreshold = cfg.DedupAuthorLowThreshold
			engine.AutoMergeEnabled = cfg.DedupAutoMergeEnabled
			return engine, nil
		},
	})

	// metricsstore — NutsDB-backed cache-stats snapshot store. Lives at
	// {dirname(DatabasePath)}/metrics.nutsdb. Returns nil + logs when
	// DatabasePath is empty (test paths) or open fails — server code
	// nil-checks before use.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "metricsstore",
		Needs:  []string{"config"},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, "config")
			if cfg.DatabasePath == "" {
				return (*database.NutsMetricsStore)(nil), nil
			}
			dir := filepath.Join(filepath.Dir(cfg.DatabasePath), "metrics.nutsdb")
			store, err := database.NewNutsMetricsStore(dir)
			if err != nil {
				slog.Warn("Failed to open metrics store", "err", err)
				return (*database.NutsMetricsStore)(nil), nil
			}
			return store, nil
		},
	})

	// aiscanstore — AI scan history/phases/results, sharing the main
	// PebbleDB under the "aiscan:" key prefix. Returns nil when the
	// store isn't a PebbleStore (test paths).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "aiscanstore",
		Needs:  []string{"store"},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, "store")
			ps, ok := store.(*database.PebbleStore)
			if !ok {
				return (*database.AIScanStore)(nil), nil
			}
			s, err := database.NewAIScanStoreFromDB(ps.DB())
			if err != nil {
				slog.Warn("Failed to init AI scan store", "err", err)
				return (*database.AIScanStore)(nil), nil
			}
			return s, nil
		},
	})

	// pipelinemanager — AI scan pipeline coordinator. Needs aiscanstore +
	// the main store + an *ai.OpenAIParser (llmparser). When the parser
	// is nil (no OpenAI key) or aiscanstore is nil, returns nil.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "pipelinemanager",
		Needs:  []string{"store", "aiscanstore", "llmparser"},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			scanStore, _ := serviceregistry.TryGet[*database.AIScanStore](c, "aiscanstore")
			parser, _ := serviceregistry.TryGet[*ai.OpenAIParser](c, "llmparser")
			if scanStore == nil || parser == nil {
				return (*aiscan.PipelineManager)(nil), nil
			}
			store := serviceregistry.Get[database.Store](c, "store")
			return aiscan.NewPipelineManager(scanStore, store, parser), nil
		},
	})

	// itunes — the iTunes integration service. Registered here (rather
	// than in internal/itunes/service/register.go) because the
	// OrganizerFactory closure needs internal/organizer + internal/config,
	// and itunesservice deliberately avoids importing internal/organizer
	// (see internal/itunes/service/types.go BookOrganizer comment).
	//
	// Construction never returns an error in practice: itunesservice.New
	// returns NewDisabled() when cfg.Enabled is false. The "Enabled: true"
	// flag here mirrors the pre-container inline construction in NewServer
	// — the per-feature toggles (AutoWriteBack, ITLWriteBackEnabled) come
	// from AppConfig.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "itunes",
		Needs:  []string{"store", "config", "eventbus", "metafetch"},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.Store](c, "store")
			cfg := serviceregistry.Get[*config.Config](c, "config")
			bus := serviceregistry.Get[*plugin.EventBus](c, "eventbus")
			mf := serviceregistry.Get[*metafetch.Service](c, "metafetch")
			svc, err := itunesservice.New(itunesservice.Deps{
				Store: store,
				Config: itunesservice.Config{
					Enabled:             true,
					LibraryReadPath:     cfg.ITunesLibraryReadPath,
					LibraryWritePath:    cfg.ITunesLibraryWritePath,
					AutoWriteBack:       cfg.ITunesAutoWriteBack,
					ITLWriteBackEnabled: cfg.ITLWriteBackEnabled,
				},
				AudiobookRoot: cfg.RootDir,
				ReportDir:     filepath.Join(cfg.RootDir, "reports"),
				EventBus:      bus,
				Metafetch:     mf,
				OrganizerFactory: func() itunesservice.BookOrganizer {
					org := organizer.NewOrganizer(cfg)
					org.SetStore(store)
					return org
				},
			})
			if err != nil {
				slog.Warn("iTunes service construction failed, falling back to disabled", "err", err)
				return itunesservice.NewDisabled(), nil
			}
			return svc, nil
		},
	})
}

// wireServerFromContainer populates the typed service fields on *Server
// from the built container. Called once during NewServer after
// Container.Build() and Container.PostInit() succeed. Adding a future
// service is one new line here + one new register.go in the domain pkg.
//
// W2 services use TryGet because "activity" / "activitystore" are only
// Included when config.DatabasePath is set (the NutsDB sidecar can't open
// without a path). All other W1+W2 services are unconditional and Get
// could safely be used — TryGet is used consistently here to keep the
// wire-up uniform and tolerant of further phased Include() decisions.
func wireServerFromContainer(s *Server, c *serviceregistry.Container) {
	// W1 services (unconditional)
	s.audiobookService = serviceregistry.Get[*audiobookspkg.AudiobookService](c, "audiobook")
	s.batchService = serviceregistry.Get[*batch.BatchService](c, "batch")
	s.workService = serviceregistry.Get[*work.WorkService](c, "work")
	s.filesystemService = serviceregistry.Get[*fileops.FilesystemService](c, "filesystem")
	s.importPathService = serviceregistry.Get[*importer.ImportPathService](c, "importpath")
	s.scanService = serviceregistry.Get[*scanner.ScanService](c, "scan")
	s.dashboardService = serviceregistry.Get[*sysinfo.DashboardService](c, "dashboard")
	s.systemService = serviceregistry.Get[*sysinfo.SystemService](c, "system")
	s.configUpdateService = serviceregistry.Get[*config.UpdateService](c, "configupdate")
	s.metadataStateService = serviceregistry.Get[*metafetch.MetadataStateService](c, "metadatastate")

	// W2 services
	s.metadataFetchService = serviceregistry.Get[*metafetch.Service](c, "metafetch")
	if ol, ok := serviceregistry.TryGet[*metafetch.OpenLibraryService](c, "olservice"); ok && ol != nil {
		s.olService = ol
	}
	s.mergeService = serviceregistry.Get[*merge.Service](c, "merge")
	s.organizeService = serviceregistry.Get[*OrganizeService](c, "organize")
	s.quarantineSvc = serviceregistry.Get[*quarantine.QuarantineService](c, "quarantine")
	s.eventBus = serviceregistry.Get[*plugin.EventBus](c, "eventbus")
	// activity is conditional on config.DatabasePath — pull via TryGet so
	// tests that don't configure a DB path still build.
	if svc, ok := serviceregistry.TryGet[*activity.Service](c, "activity"); ok {
		s.activityService = svc
	}
	// activitywriter is in the "activity" group (same conditional). NewServer
	// drives Start inline today; SERVER-LIFECYCLE-FLIP will hand that off to
	// Container.Start (Writer.Start/Stop already match the Starter/Stopper
	// signatures so no adapter is needed).
	if aw, ok := serviceregistry.TryGet[*activity.Writer](c, "activitywriter"); ok {
		s.activityWriter = aw
	}

	// W3 services
	// batchpoller is conditional on OpenAI config — pull via TryGet.
	if poller, ok := serviceregistry.TryGet[*BatchPoller](c, "batchpoller"); ok {
		s.batchPoller = poller
	}
	// opRegistry — Get'd via the RegistryWrapper that exposes Start/Stop;
	// callers use the embedded *opsregistry.Registry. Always present.
	if wrapper, ok := serviceregistry.TryGet[*opsregistry.RegistryWrapper](c, "opregistry"); ok && wrapper != nil {
		s.opRegistry = wrapper.Registry
		if s.activityService != nil {
			s.opRegistry.SetActivityRecorder(s.activityService)
		}
	}
	if hub, ok := serviceregistry.TryGet[*opsregistry.EventHub](c, "ophub"); ok {
		s.opHub = hub
	}

	// W4 services — embedding/AI cluster.
	if embStore, ok := serviceregistry.TryGet[*database.EmbeddingStore](c, "embeddingstore"); ok {
		s.embeddingStore = embStore
	}
	if ec, ok := serviceregistry.TryGet[*ai.EmbeddingClient](c, "embedclient"); ok {
		s.embedClient = ec
	}
	if engine, ok := serviceregistry.TryGet[*dedup.Engine](c, "dedup"); ok {
		s.dedupEngine = engine
	}
	if scanStore, ok := serviceregistry.TryGet[*database.AIScanStore](c, "aiscanstore"); ok && scanStore != nil {
		s.aiScanStore = scanStore
	}
	// WHY MetricsStorer interface: the metricsstore may be a NutsMetricsStore or
	// a PebbleMetricsStore — both implement MetricsStorer.
	if ms, ok := serviceregistry.TryGet[database.MetricsStorer](c, "metricsstore"); ok && ms != nil {
		s.metricsStore = ms
	}
	if pm, ok := serviceregistry.TryGet[*aiscan.PipelineManager](c, "pipelinemanager"); ok && pm != nil {
		s.pipelineManager = pm
	}

	// itunesservice.Service — container-built since PLUGIN-DECOUPLE-CLOSURES
	// (May 13, 2026). Replaces the prior inline itunesservice.New(...) call
	// in NewServer. Always present (Build returns NewDisabled() on error).
	s.itunesSvc = serviceregistry.Get[*itunesservice.Service](c, "itunes")

	// updater + updateScheduler — container-built since the updater
	// LIFECYCLE-FLIP prep (May 13, 2026). Real version flows in via the
	// "appversion" Override that NewServer sets to appVersion. The
	// SchedulerStarterAdapter wraps Scheduler.Start/Stop for the eventual
	// Container.Start hand-off; until then NewServer calls .Start()
	// inline against the embedded scheduler.
	if upd, ok := serviceregistry.TryGet[*updater.Updater](c, "updater"); ok {
		s.updater = upd
	}
	if adapter, ok := serviceregistry.TryGet[*updater.SchedulerStarterAdapter](c, "updatescheduler"); ok && adapter != nil {
		s.updateScheduler = adapter.Scheduler()
	}
}
