// file: internal/server/registry_wire.go
// version: 1.27.0
// guid: e2c1977d-0023-498f-81bd-76e9912eec89
// last-edited: 2026-09-02

package server

import (
	"fmt"
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

// resolveVectorBackend maps a configured embedding.vector_backend value onto
// the backend that will actually be built, and makes the slow choice audible.
//
// WHY this exists rather than a bare `== "hnsw"` comparison: chromem is a
// brute-force O(n·d) cosine scan, not an approximate index, so its per-query
// cost grows linearly with the corpus. Measured on this repo at 1024 dims with
// the is_primary_version filter: 10K vectors 9.18ms/op vs HNSW 0.51ms/op
// (18x); 50K vectors 111.9ms/op vs 0.53ms/op (210x). dedup.full-scan issues
// roughly one query per book, so across a ~61K-book library that is ~32
// seconds on HNSW against ~1.9 CPU-hours on chromem. It never errors and it
// never logged anything unusual — the whole cost was silent.
//
// chromem remains a deliberately selectable fallback (coder/hnsw had a SIGSEGV
// crash loop in June 2026 and chromem is the simple escape hatch), but a
// fallback nobody can tell they are running is a trap rather than a fallback,
// so selecting it now emits a WARN naming the backend and its scaling.
//
// The vector count is deliberately NOT included: the store is constructed
// empty here and hydrated later (server.go / server_lifecycle.go), so there is
// no count to read at this point that would not require doing real work.
//
// An empty value resolves to the default rather than falling through to
// chromem: an upgraded install whose stored config_blob predates the field
// carries "" forward out of migrateEmbeddingBlob, and viper.SetDefault never
// runs on that path. Config.Validate() normalizes the same case; this is the
// belt-and-braces at the one site that actually picks an implementation.
func resolveVectorBackend(backend string) string {
	switch backend {
	case "hnsw":
		return "hnsw"
	case "":
		return "hnsw"
	case "chromem":
		slog.Warn("dedup vector index: using the chromem backend, which is a BRUTE-FORCE cosine scan, not an approximate index",
			"backend", "chromem",
			"scaling", "query cost grows linearly with the number of indexed vectors",
			"alternative", "set embedding.vector_backend=hnsw (or VECTOR_INDEX_BACKEND=hnsw) for sub-linear search")
		return "chromem"
	default:
		slog.Warn("dedup vector index: unknown embedding.vector_backend, falling back to the default",
			"configured", backend,
			"using", "hnsw",
			"valid", "hnsw, chromem")
		return "hnsw"
	}
}

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
		Name:   serviceregistry.KeySystem,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[sysinfo.SystemServiceStore](c, serviceregistry.KeyStore)
			return sysinfo.NewSystemService(store, appVersion, calculateLibrarySizes), nil
		},
	})

	// embeddingstore — Pebble-backed key namespace for dedup embeddings.
	// Returns nil if the underlying store isn't *PebbleStore (e.g. tests).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyEmbeddingStore,
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[any](c, serviceregistry.KeyStore)
			// FromStore, not AsPebbleStore + DB(): the unwrap lives in
			// internal/database so this package does not name *PebbleStore.
			if es := database.NewEmbeddingStoreFromStore(store); es != nil {
				return es, nil
			}
			return (*database.EmbeddingStore)(nil), nil
		},
	})

	// chromemstore — in-memory ANN vector store for dedup Layer 2. The backend
	// is config-selectable (config.VectorIndexBackend): "hnsw" (default,
	// coder/hnsw graph, sub-linear search) or "chromem" (brute-force cosine
	// scan — see resolveVectorBackend for why picking it is now audible).
	// Both satisfy database.VectorANNStore. Optional; on disabled/error the Build
	// returns an UNTYPED nil so TryGet[database.VectorANNStore] yields ok=false
	// (returning a typed-nil pointer would assert non-nil to the interface and
	// trip the engine's chromemStore != nil guards — the classic Go nil-iface
	// trap). Dedup then falls back to the Pebble linear scan.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "chromemstore",
		Needs:  []string{serviceregistry.KeyConfig},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := config.GetConfig(c)
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
			dims := cfg.Embedding.Dimensions
			if dims <= 0 {
				dims = 3072
			}
			if resolveVectorBackend(cfg.Embedding.VectorBackend) == "hnsw" {
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
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[any](c, serviceregistry.KeyStore)
			return database.GetAIJobs(store), nil
		},
	})

	// dedup — the duplicate detection engine.
	// Returns nil if any required dep is missing (no API key, no embed
	// client, etc.) — matches the existing inline conditional construction
	// in NewServer.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyDedup,
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyConfig, serviceregistry.KeyEmbeddingStore, "embedclient", "llmparser", serviceregistry.KeyMerge},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := config.GetConfig(c)
			// Keyless local backends are valid (TOGGLE-1/TASK-10): gate on the
			// resolved effective mode, not on OpenAIAPIKey presence. The old
			// key check silently disabled the whole dedup plugin on keyless
			// prod once TASK-10 stopped injecting a dummy key into cfg.
			if cfg.EffectiveEmbeddingMode() == config.AIBackendModeDisabled {
				return (*dedup.Engine)(nil), nil
			}
			embStore, _ := serviceregistry.TryGet[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore)
			embClient, _ := serviceregistry.TryGet[*ai.EmbeddingClient](c, "embedclient")
			if embStore == nil || embClient == nil {
				return (*dedup.Engine)(nil), nil
			}
			// llmParser may legitimately be nil (EffectiveLLMMode disabled on a
			// keyless local-embedding box) — NewEngine documents nil as "Layer 3
			// LLM review disabled" and nil-guards every use.
			llmParser, _ := serviceregistry.TryGet[*ai.OpenAIParser](c, "llmparser")
			if llmParser == nil {
				slog.Info("dedup engine: LLM parser unavailable (LLM mode disabled) — Layer 3 LLM review off")
			}
			store := serviceregistry.Get[dedup.Store](c, serviceregistry.KeyStore)
			mergeSvc := serviceregistry.Get[*merge.Service](c, serviceregistry.KeyMerge)
			// The unified scorer's bands + per-kind confidence bounds come from
			// the persisted dedup.signals settings (config.yaml overlaid by the DB
			// settings blob) and are handed to the engine HERE, at construction.
			// An invalid config fails the build — and so server startup — loudly.
			// Until 2026-09-02 this block wrote the same values into two unified
			// package globals that the engine never read, so every configured
			// band threshold was inert and scoring ran on the compiled-in
			// 97/90/75/60.
			//
			// Fail-closed is only safe because the SAME conversion now runs
			// before anything is persisted (config.UpdateService.UpdateConfig
			// rejects a bad ladder with 400 and writes nothing), so a ladder that
			// trips this at startup can only have arrived via a hand-edited
			// config.yaml or a settings blob written by an older build.
			scoreCfg, err := cfg.Dedup.Signals.ScoreConfig()
			if err != nil {
				return nil, fmt.Errorf("dedup engine: %w — refusing to start on default thresholds. The effective value is config.yaml's dedup.signals overlaid by the settings blob persisted in the database (written by PUT /api/v1/config, Settings → Dedup, and calibrate-composite apply); the persisted blob wins, so re-save a valid ladder through the API/Settings page, or clear its dedup.signals keys, if config.yaml already looks right", err)
			}
			engine, err := dedup.NewEngine(embStore, store, embClient, llmParser, mergeSvc, scoreCfg)
			if err != nil {
				return nil, fmt.Errorf("dedup engine: %w", err)
			}
			// Log the ladder the engine will actually score on, once, so a
			// deploy can be checked against the persisted settings without
			// reading the DB blob. (Review-round M5.)
			slog.Info("dedup engine: effective score ladder",
				"certain", scoreCfg.BandCertainMin,
				"high", scoreCfg.BandHighMin,
				"medium", scoreCfg.BandMediumMin,
				"review", scoreCfg.BandReviewMin,
				"confidence_overrides", len(cfg.Dedup.Signals.Confidence),
				"source", "config.yaml dedup.signals overlaid by the persisted settings blob")
			// Every later PUT /api/v1/config that changes dedup.signals reaches
			// the live engine through the UpdateService's dedup sink (review-
			// round H2), which also re-bands the stored candidate rows under
			// the new ladder (H3) — AutoResolveCertain reads the STORED band,
			// so swapping the ladder without a rescore would leave rows
			// auto-merging on the old one. The sink is installed by the dedup
			// PLUGIN (internal/plugins/dedup/register.go PostInit), not here:
			// it queues the re-band as the dedup.rescore operation, which
			// needs the ops registry — a plugin-level dependency this Build
			// does not have. It used to be an inline closure here that ran the
			// whole re-band synchronously inside the HTTP PUT with a
			// Background context and no concurrency key against a running
			// dedup.full-scan (PR #3052 follow-up, D4).
			engine.BookHighThreshold = cfg.Dedup.BookHighThreshold
			engine.BookLowThreshold = cfg.Dedup.BookLowThreshold
			engine.AuthorHighThreshold = cfg.Dedup.AuthorHighThreshold
			engine.AuthorLowThreshold = cfg.Dedup.AuthorLowThreshold
			engine.AutoMergeEnabled = cfg.Dedup.AutoMergeEnabled
			return engine, nil
		},
	})

	// metricsstore — Pebble-backed cache-stats snapshot store (TASK-22 cutover
	// from NutsMetricsStore). Shares the main PebbleDB instance under the
	// "met:" key prefix; TTL is emulated via the sweep-pebble-metrics-ttl
	// maintenance job rather than NutsDB's native per-key expiry. Returns a
	// nil *PebbleMetricsStore + logs when DatabasePath is empty (test paths)
	// or the store isn't a *database.PebbleStore — server code nil-checks
	// before use.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "metricsstore",
		Needs:  []string{serviceregistry.KeyConfig, serviceregistry.KeyStore},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := config.GetConfig(c)
			if cfg.DatabasePath == "" {
				return (*database.PebbleMetricsStore)(nil), nil
			}
			store := serviceregistry.Get[any](c, serviceregistry.KeyStore)
			ms := database.NewPebbleMetricsStoreFromStore(store)
			if ms == nil {
				slog.Warn("metricsstore: backend is not PebbleStore, metrics disabled")
				return (*database.PebbleMetricsStore)(nil), nil
			}
			return ms, nil
		},
	})

	// aiscanstore — AI scan history/phases/results, sharing the main
	// PebbleDB under the "aiscan:" key prefix. Returns nil when the
	// store isn't a PebbleStore (test paths).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "aiscanstore",
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[any](c, serviceregistry.KeyStore)
			s, err := database.NewAIScanStoreFromStore(store)
			if err != nil {
				slog.Warn("Failed to init AI scan store", "err", err)
				return (*database.AIScanStore)(nil), nil
			}
			if s == nil {
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
		Needs:  []string{serviceregistry.KeyStore, "aiscanstore", "llmparser"},
		Groups: []string{"ai"},
		Build: func(c *serviceregistry.Container) (any, error) {
			scanStore, _ := serviceregistry.TryGet[*database.AIScanStore](c, "aiscanstore")
			parser, _ := serviceregistry.TryGet[*ai.OpenAIParser](c, "llmparser")
			if scanStore == nil || parser == nil {
				return (*aiscan.PipelineManager)(nil), nil
			}
			store := serviceregistry.Get[aiscan.Store](c, serviceregistry.KeyStore)
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
		Name:   serviceregistry.KeyITunes,
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyConfig, serviceregistry.KeyEventBus, serviceregistry.KeyMetaFetch},
		Groups: []string{"core"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[itunesWireStore](c, serviceregistry.KeyStore)
			cfg := config.GetConfig(c)
			bus := plugin.GetEventBus(c)
			mf := serviceregistry.Get[*metafetch.Service](c, serviceregistry.KeyMetaFetch)
			svc, err := itunesservice.New(itunesservice.Deps{
				Store: store,
				Config: itunesservice.Config{
					Enabled:             true,
					LibraryReadPath:     cfg.ITunes.LibraryReadPath,
					LibraryWritePath:    cfg.ITunes.LibraryWritePath,
					AutoWriteBack:       cfg.ITunes.AutoWriteBack,
					ITLWriteBackEnabled: cfg.ITunes.WriteBackEnabled,
					WriteBackDryRun:     cfg.ITunes.WriteBackDryRun,
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
// W2 services use TryGet because serviceregistry.KeyActivity / serviceregistry.KeyActivityStore are only
// Included when config.DatabasePath is set (the NutsDB sidecar can't open
// without a path). All other W1+W2 services are unconditional and Get
// could safely be used — TryGet is used consistently here to keep the
// wire-up uniform and tolerant of further phased Include() decisions.
func wireServerFromContainer(s *Server, c *serviceregistry.Container) {
	// W1 services (unconditional)
	s.audiobookService = serviceregistry.Get[*audiobookspkg.AudiobookService](c, serviceregistry.KeyAudiobook)
	s.batchService = serviceregistry.Get[*batch.BatchService](c, serviceregistry.KeyBatch)
	s.workService = serviceregistry.Get[*work.WorkService](c, serviceregistry.KeyWork)
	s.filesystemService = serviceregistry.Get[*fileops.FilesystemService](c, serviceregistry.KeyFilesystem)
	s.importPathService = serviceregistry.Get[*importer.ImportPathService](c, serviceregistry.KeyImportPath)
	s.scanService = serviceregistry.Get[*scanner.ScanService](c, serviceregistry.KeyScan)
	s.dashboardService = serviceregistry.Get[*sysinfo.DashboardService](c, serviceregistry.KeyDashboard)
	s.systemService = serviceregistry.Get[*sysinfo.SystemService](c, serviceregistry.KeySystem)
	s.configUpdateService = serviceregistry.Get[*config.UpdateService](c, serviceregistry.KeyConfigUpdate)
	s.metadataStateService = serviceregistry.Get[*metafetch.MetadataStateService](c, serviceregistry.KeyMetadataState)

	// W2 services
	s.metadataFetchService = serviceregistry.Get[*metafetch.Service](c, serviceregistry.KeyMetaFetch)
	if ol, ok := serviceregistry.TryGet[*metafetch.OpenLibraryService](c, "olservice"); ok && ol != nil {
		s.olService = ol
	}
	s.mergeService = serviceregistry.Get[*merge.Service](c, serviceregistry.KeyMerge)
	s.organizeService = serviceregistry.Get[*OrganizeService](c, serviceregistry.KeyOrganize)
	s.quarantineSvc = serviceregistry.Get[*quarantine.QuarantineService](c, serviceregistry.KeyQuarantine)
	s.eventBus = plugin.GetEventBus(c)
	// activity is conditional on config.DatabasePath — pull via TryGet so
	// tests that don't configure a DB path still build.
	if svc, ok := serviceregistry.TryGet[*activity.Service](c, serviceregistry.KeyActivity); ok {
		s.activityService = svc
	}
	// activitywriter is in the serviceregistry.KeyActivity group (same conditional). NewServer
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
		// Decorates every run's context with the op ID, both as a slog
		// attribute and in the form maintenance.OpIDFromContext reads. See
		// opRunContextDecorator in op_run_context.go for why the second one
		// matters — without it the ops that record undo history recorded
		// nothing.
		s.opRegistry.SetRunContextDecorator(opRunContextDecorator)
	}
	if hub, ok := serviceregistry.TryGet[*opsregistry.EventHub](c, serviceregistry.KeyOpHub); ok {
		s.opHub = hub
	}

	// W4 services — embedding/AI cluster.
	if embStore, ok := serviceregistry.TryGet[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore); ok {
		s.embeddingStore = embStore
	}
	if ec, ok := serviceregistry.TryGet[*ai.EmbeddingClient](c, "embedclient"); ok {
		s.embedClient = ec
	}
	if engine, ok := serviceregistry.TryGet[*dedup.Engine](c, serviceregistry.KeyDedup); ok {
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
	s.itunesSvc = serviceregistry.Get[*itunesservice.Service](c, serviceregistry.KeyITunes)

	// updater + updateScheduler — container-built since the updater
	// LIFECYCLE-FLIP prep (May 13, 2026). Real version flows in via the
	// "appversion" Override that NewServer sets to appVersion. The
	// SchedulerStarterAdapter wraps Scheduler.Start/Stop for the eventual
	// Container.Start hand-off; until then NewServer calls .Start()
	// inline against the embedded scheduler.
	if upd, ok := serviceregistry.TryGet[*updater.Updater](c, serviceregistry.KeyUpdater); ok {
		s.updater = upd
	}
	if adapter, ok := serviceregistry.TryGet[*updater.SchedulerStarterAdapter](c, "updatescheduler"); ok && adapter != nil {
		s.updateScheduler = adapter.Scheduler()
	}
}

// itunesWireStore is the iTunes service's own store plus the organizer surface
// its path-repair tier hands to org.SetStore. Two entries, both named, so this
// re-narrows on its own when either package does.
type itunesWireStore interface {
	itunesservice.Store
	organizer.OrganizerStore
}
