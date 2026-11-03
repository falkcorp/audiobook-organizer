<!-- file: docs/plans/platform-core/raw/12-cross-cutting.md -->
<!-- version: 1.0.0 -->
<!-- guid: d8e3826c-e00b-4336-8019-0197f42069f9 -->
<!-- last-edited: 2026-11-03 -->

# 12 - Cross-cutting subsystems inventory

Anchors are against the checked-out HEAD `57fd77ffc` (the brief said `748fd787a`; the tree is at `57fd77ffc`, a later docs-only commit). LOC = non-test Go lines.

## 1. Summary table

| Subsystem | Package (LOC) | Generic or audiobook | Global state that conflicts with multi-app-in-one-binary |
|---|---|---|---|
| Config | `internal/config` (10.5k; `config.go` alone 4,286 lines) | Mixed: ~all keys are in one struct | `var AppConfig Config` (`config.go:1939`), package `mu`, `generation` (:2035), process-wide viper (`config.go:2590`) |
| Logging | `internal/logger` (877), `internal/logging` (529) | Generic, with a book-flavoured `Change` type | `slog.Default()` (set at `cmd/root.go:718` and `internal/server/server.go:1154`); 207 package-level `logger.New(...)` vars |
| Metrics | `internal/metrics` (840) | Prefix and many series audiobook | Prometheus default registry via `prometheus.MustRegister`, `registerOnce` (`metrics.go:300`) |
| Telemetry (OTel) | `internal/telemetry` (1.4k) | Generic mechanism, audiobook naming | `otel.SetTracerProvider`/`SetMeterProvider` (`telemetry.go:287,461`), `instanceID` var (`resource.go:31`) |
| Events / realtime | `internal/realtime` (426), `internal/plugin/events.go`, `internal/operations/registry/bus.go` | Three separate buses | none global (instances), but plugin registry is global |
| Backup | `internal/backup` (1.6k) | Generic (DB dir tar + codec) | none found |
| Updater | `internal/updater` (547) | Generic mechanism, hard-coded repo | none found |
| Diagnostics export | `internal/diagnostics` (799) | Audiobook (SlimBook, iTunes albums) | none found |
| Sysinfo / dashboard | `internal/sysinfo` (819) | Mostly generic (memory, uptime) + library stats | none found |
| Startup / lifecycle | `internal/startup` (211), `internal/lifecycle` (35), `internal/sdnotify` (149), `cmd/root.go`, `internal/server/server_lifecycle.go` (2.4k) | Generic pattern | `startup.Table` package var |
| Service container | `internal/serviceregistry` (532) | Generic | package `registered` map (`registry.go:38`) |
| Undo / provenance | `internal/undo` (7.2k) + `database.OperationChange` | Audiobook | none global |

## 2. Config (`internal/config`)

- Struct: `type Config struct` spans `config.go:1048-1912` (864 lines, 341 `json:` tags in the file including sub-structs); sub-config files: `ai_endpoints.go`, `abs_config.go`, `invite_config.go`, `dedup_score_config.go`, `chapter_consolidation.go`, `naming_patterns.go`, `itunes_libraries.go`, `log_retention.go`, `update_service.go`. Key count: ~251 `SetDefault(...)` registrations in `registerConfigDefaults` (`config.go:2621`); top-level prefix histogram: `metadata` 30, `auto` 10, `ai` 9, `oauth` 8, `whisper` 6, `scheduled.*` ~40, `otel` 4, plus `dedup`, `acoustid`, `openlibrary`, `deluge`, `itunes` groups.
- Audiobook keys mixed with platform keys in the same struct: `RootDir`, `FolderNamingPattern`/`FileNamingPattern`, `OrganizationStrategy`, `WriteBackMetadata`, `EmbedCoverArt`, `OpenLibraryDump*`, `HardcoverAPIToken`, `GoogleBooksAPIKey`, `DelugeWeb*`, `AcoustID*`, `SupportedExtensions`, dedup scoring, whisper endpoints (`config.go:1048-1130, 1138, 484-489, 1775-1783`). Platform-ish keys in the same struct: `DatabasePath/DatabaseType`, `ActivityBackend`, backup compression/retention, OAuth, otel, disk quota, auto-update.
- Sources (precedence layers): (1) cobra flags - only 2 persistent flags are bound (`cmd/root.go`, `MarkFlagExplicit` `config.go:2203`); (2) env via `viper.AutomaticEnv()` plus explicit `BindEnv` per key (`cmd/root.go:~625`, e.g. `config.go:2702`); (3) YAML file, default `~/.audiobook-organizer.yaml` (`cmd/root.go:~611`; `ConfigFilePath`/`LoadConfigFromFile`/`SaveConfigToFile` `persistence.go:29,42,121`); (4) DB-persisted settings: the whole non-secret config is stored as one JSON `config_blob` setting (`persistence.go:884`) plus per-key rows, with secret rows encrypted (`database/settings.go:39`, `InitEncryption` :76-109). `LoadConfigFromDatabase` (`persistence.go:915`) overwrites the struct from the blob, then `applyEnvAuthoritativeConfig` (`config.go:2435`) re-applies env-authoritative keys. Auth-surface keys (OAuth, Cloudflare Access, ABS) can never be enabled by the blob; some operational keys are env-wins-only-when-supplied (comment block at `config.go:~2420-2434`). A lock-reason mechanism reports which layer forces a setting (`SettingLockedBy` `config.go:2391`, `SettingLocks` :2412). Protected-field classification for owner trust (`protected_fields.go:38,170-250`). Removed/unknown key warnings (`removed_keys.go`, `unknown_keys.go`).
- Hot reload: there is NO file watcher and no SIGHUP handler (grep for fsnotify/WatchConfig/SIGHUP/OnConfigChange finds only unrelated watcher/comment hits). Reload is write-through: all writes go through `config.Mutate(fn)` (`config.go:2011`), bumping `config.Generation()` (:2035) which consumers poll with one atomic load (documented use: `internal/ai` metric label set). Readers use `config.Snapshot()` (`:1944`, value copy under RLock) or read `config.AppConfig` directly (documented as racy-but-accepted at hot paths, `config.go:~1930-1938`). Usage: 214 non-test files reference `config.AppConfig`, 53 use `config.Snapshot()`.
- DI side door: `config/register.go:12-49` registers config in `serviceregistry` (`GetConfig(c) *Config`), but it returns the global, not an instance.
- Migrations of stored blobs: ten `migrate*Blob` funcs (`persistence.go:359-824`), all audiobook-key specific.

Platform-core implication: a single mutable global `AppConfig` keyed to a ~860-line struct cannot host two apps; a core would need a typed per-app config + a generic layered loader (flags/env/file/DB-blob/secret-rows with lock reasons and generation counter). The loader pieces (layering, locks, encrypted secret rows, generation) are generic; the struct and the blob migrations are not.

## 3. Logging

- Two packages. `internal/logger`: `Logger` interface = `LevelLogger` (Trace/Debug/Info/Warn/Error printf-style) + `ProgressReporter` + `ChangeRecorder` + `CancellationReporter` + `SubLoggerFactory` (`logger/logger.go:54-98`). `Change` has `BookID` (:40-47 area) so the contract leaks the audiobook domain. Implementation `StandardLogger` (`standard.go`) writes through `slog.Default()` after `sanitizeLogLine`, optionally to the activity log via `ActivityLogWriter` (`AddSystemActivityLog(source, level, message)`, `standard.go:13`); progress/changes/cancel are no-ops there. Also `AttrLogger` (`attrs.go:22`), `RetentionStore` (`retention.go:28`), a slog guard ratchet test (`slog_guard_ratchet_test.go`).
- `internal/logging`: context-based structured helpers (`logging.Info(ctx,msg,attrs...)`, `structured.go:47-68`), `OpContext` carried in ctx (`opcontext.go:15`), `Sanitize`/`SanitizeErr` (`sanitize.go:28,38`), invite-token path redaction (`redact_path.go:107`; an app-specific feature).
- Usage: 382 non-test files import `log/slog`; 342 import `internal/logger`; no zap/zerolog/logrus (`go-logr/logr` is a direct dependency at `go.mod:14` but not used for app logging as far as grep shows). File logging via `setupFileLogging` (`cmd/root.go:698-718`) installs the slog default.
- Global state: `slog.SetDefault` is process-global (also called inside `NewServer` path at `server.go:1154`); `var x = logger.New("subsystem")` package vars (207). Multi-app impact: subsystem labels are free strings, so apps can coexist, but one default handler/level/file for all.

## 4. Metrics and telemetry

- Prometheus (`internal/metrics`): ~34 collectors declared as package vars with fixed `Namespace: "audiobook_organizer"` (`metrics.go:15-60`), registered in `Register()` with `prometheus.MustRegister` on the global registry (`:300-326`); includes generic ones (operations started/completed/failed/canceled/duration, cache hit/miss/set/evict/size, goroutines, memory, Pebble collector `pebble_collector.go`) and audiobook ones (books gauge, folders, search index, ABS listening stats, organize path collisions, catalog harvest). A second Prometheus bridge comes from OTel's Prometheus exporter registering on the same default registry (`telemetry.go:32-42` comment); `/metrics` served with `promhttp` (`server_lifecycle.go:45`, `server.go:708`).
- OpenTelemetry (`internal/telemetry`): `InitOTEL(ctx,cfg)` returns a shutdown func (`telemetry.go:46`), sets global tracer/meter providers (`:287,:461`), `Meter(name)` prefixes scope `audiobook-organizer/` (`meter.go:17,40`), custom histogram views (`views.go:37-80`), attribute allow-list and Prometheus label mapping (`attr.go:91-122`), OTLP endpoint validation (`endpoint.go`), error scrubbing (`errorhandler.go`), `telemetry/contract` subpackage. Instruments exist in `internal/ai` (`ai_metrics.go`, `telemetry.go:27`: `otel.Tracer("audiobook-organizer/ai")`), `aidispatch/metrics.go`, and others (5 files reference the scope string). Startup treats InitOTEL as Degraded (`startup/steps.go` Table, token `metrics_disabled`).
- Multi-app impact: metric names/namespace and scope prefix are hard-coded constants; two apps in one process would collide on the default Prometheus registry (duplicate `MustRegister` panics) and the global OTel providers.

## 5. Events / realtime (three buses, none unified)

| Bus | Where | Shape | Delivery |
|---|---|---|---|
| Plugin lifecycle `EventBus` | `internal/plugin/events.go:55-80` | `Subscribe(EventType, handler)`, `Publish(ctx, Event)`; narrow `EventPublisher` iface (`:51`) | in-process, handlers in goroutines, notification-only, 12 audiobook event types (`:15-28`); 41 references to `NewEvent`/`EventPublisher` |
| UI SSE hub `realtime.EventHub` | `internal/realtime/events.go:78-216` | `RegisterClient`, `Broadcast`, `SendOperationProgress/Status/Log`, `SendSystemStatus`, `HandleSSE(gin)`; `Client` has per-operation subscriptions (:37-64); `BookChangeCoalescer` (`book_changes.go:32`) | SSE at `/api/events` (`server.go:719` comment), bounded 100-slot channel per client |
| Operations bus `opsregistry.EventHub` | `internal/operations/registry/bus.go:29` | map of subscribers, `Bus` interface | SSE for ops events (UOS-06, `server.go:365-367`); looked up via `serviceregistry.KeyOpHub` (`registry_wire.go:448`) |

All three are notification-only (no durable queue, no replay).

## 6. Backup, updater, diagnostics, sysinfo

- Backup (`internal/backup`, 1.6k): `CreateBackup(dbPath, dbType, cfg)` (`backup.go:283`), `CreateBackupWithCheckpoint(store Checkpointable, ...)` (:428; `Checkpointable` :32), `RestoreBackupIn` (:732), `ListBackups[WithChecksums]` (:904,:914), `DeleteBackupIn` (:983), pluggable `Codec` (gzip, zstd; `codec.go:48`, `ResolveCodec` :65), progress callback `BackupProgress` (:82), disk-budget `ResolveMaxTotalBytes` (:174). Generic: it archives the DB directory; no book logic. Callers: system handler (`handlers/system/handler.go:769-776`) and organizer pre-run backup (`organizer/service.go:742`). Retention by age/count handled in maintenance ops.
- Updater (`internal/updater`, 547): `Updater.CheckForUpdate(channel)` (`updater.go:86`), `DownloadAndReplace` (:214), `RestartSelf` (:331), cron-like `Scheduler` (`scheduler.go:23`), `SchedulerStarterAdapter` implementing the container `Starter/Stopper` (`register.go:29-51`). GitHub Releases API (`updater.go:109,295`) with the repo slug hard-coded to the single app (`updater.go:72`); must be parameterized per app.
- Diagnostics (`internal/diagnostics`, 799): builds an AI-assisted diagnostic export zip of books/iTunes albums/logs (`service.go:166` `GenerateExport`, `batch.go:52` `BuildBatchJSONL`, `SlimBook` :45). Audiobook-specific. `internal/diagnosis` (295) is a per-audio-file probe (`probe.go:92`).
- Sysinfo (`internal/sysinfo`, 819): `SystemService` / `DashboardService` (`service.go:103`, `dashboard_service.go:27`), `HealthCheckResponse` with degraded tokens (`dashboard_service.go:47`), platform-specific memory/uptime (`memory_*.go`, `uptime_*.go`). Host stats are generic; library stats are audiobook.
- Plugin/provider health: `plugin.Registry.HealthCheckAll` (`registry.go:163`); `/health` reports degraded tokens from `startup.Table` (`startup/steps.go:57-118`).

## 7. Startup and lifecycle

Order (from `startup.Table` `internal/startup/steps.go`, run sites in `cmd/root.go`):

1. `main.go`: set versions on cmd/server/telemetry, embed web FS, fast-exit flags `--print-storage-format`, `--print-capabilities`, operation-runner child mode (`registry.IsChildMode`) (`main.go` lines ~25-70).
2. cobra `initConfig` (flags, YAML, AutomaticEnv, `config.InitConfig()`, `cmd/root.go:603-648`), file logging (`:698`).
3. `steps.Run` wrappers (`cmd/root.go`): `initializeStore` (:281, Fatal) -> `InitOTEL` (:305, Degraded) -> `EnsureSecureStateDir` (:334) -> `guardAgainstKeyRegeneration` (:345) -> `initEncryption` (:350) -> `loadConfigFromDB` (:364) -> `config.Validate` (:385), all Fatal; then provider throttle/quota store attach (`:412-418`, deliberately outside NewServer).
4. `server.NewServer(store)` (`server.go:653`; `server.go` 1,674 lines, `server_lifecycle.go` 2,426 lines; 583 files in `internal/server`, 182 non-test).
5. `Server.Start(cfg)` (`server_lifecycle.go:245`): `opRegistrationGate` (:66) -> `container.Start` -> `opCatalogGate` (:88) -> search index (Degraded) -> HTTP -> `sdnotify.Ready` (:340).
6. Shutdown: `signal.Notify(SIGINT, SIGTERM)` (`server_lifecycle.go:390`), then `sdnotify.Stopping` (:552), `httpServer.Shutdown` (:589), `opRegistry.Shutdown` (:613), iTunes svc (:662), `pluginRegistry.ShutdownAll` (:670), `container.Stop` (:693), watcher supervisor (:709); OTel shutdown and store close are deferred in `cmd/root.go`. `lifecycle.ErrShutdown`/`IsShutdown(ctx)` distinguishes shutdown cancellation from other cancels (`lifecycle/shutdown.go:33`).
- Service container: `serviceregistry.ServiceDef{Name, Needs, Groups, Build...}`, `Register` panics on duplicate (`registry.go:44-52`), `Container` phases resolve/build/postinit/start/stop (`container.go:13-20`), optional lifecycle ifaces `PostIniter`, `Starter`, `Stopper` (`lifecycle.go:11-27`), typed keys in `keys.go` (24 key constants, several audiobook: `KeyAudiobook`). Factory list global, instances per container (`container.go:23-24`) - the best candidate for the core's composition root. 43 `serviceregistry.Register` call sites.
- Notable: `startup.Table` is a code-reviewed class registry (Fatal/Degraded with fixed /health tokens, `steps.go:35-57`); generic and reusable as-is.

## 8. Undo / provenance ledger

- Journal rows: `database.OperationChange` (`internal/database/store.go:634-656+`): OperationID, UserID, **BookID**, ChangeType (`file_move`, `metadata_update`, `tag_write`, `series_rename`...), FieldName, **SeriesID**, Old/NewValue, RevertedAt, monotonic `CreatedSeq`/`RevertedSeq` from a journal clock (`pebble_store_journal_clock.go`). Entity model is book/series hard-wired (no generic entity type/id).
- `internal/undo` (7.2k, 25 files): preflight `PreflightUndoConflictsFromChanges` (`engine.go:123`) with `ConflictChecker` (:79) and report `UndoConflictReport` (:30); restorability classifier `restorable.go` (`CheckRestoreBook`/`CheckRestoreReferent`, `SeriesLookup`/`BookLookup` ifaces :489,:505); per-domain handlers `author_merge`, `author_rename`, `book_create`, `book_tag`, `junk_author`, `retire`, `user_book_state`, `merge_*_journal`, `field_lock`, `list_lock`. Actual revert executes in `internal/audiobooks/revert.go` (engine.go header :14-16). Fully audiobook; the reusable idea is the journal + preflight + sequence clock, the code is not.
- Activity log (`internal/activity`, 2.5k) and `AddSystemActivityLog` (`database/iface_system.go`) are the human-facing audit trail; operations v2 rows (`database.OperationV2Row`, used by `sysinfo/service.go:48,77`) are the job ledger.

## 9. Global-state checklist (multi-app-in-one-binary hazards)

| Item | Anchor | Hazard |
|---|---|---|
| `config.AppConfig` + `mu` + `generation` | `config.go:1939,2011,2035` | one config per process |
| process-wide viper | `config.go:2590` | key namespace shared |
| `slog.SetDefault` | `cmd/root.go:718`, `server.go:1154` | one handler |
| Prometheus default registry + hard-coded namespace | `metrics.go:300` | duplicate registration panic |
| OTel global providers, `telemetry.version`, `instanceID` | `telemetry.go:287,461`, `meter.go:67`, `resource.go:31` | one provider/resource |
| `database.globalStore` + `GetGlobalStore()` | `database/store.go:1809-1813` | single global store (audit comments show ongoing removal; still referenced) |
| `plugin.Global()` registry | `internal/plugin/registry.go:45` | shared plugin ids |
| `serviceregistry.registered` | `registry.go:38` | factory names shared (instances are per-container, OK) |
| `aijobs.registry` callbacks | `ai/aijobs/aijobs.go:~96` | feature-type string namespace |
| `aidispatch.defaultHealth` | `aidispatch/health.go:46` | endpoint-id keyed |
| `metadata.defaultThrottles`, `providerhttp` limits/clients, `dailyquota.For` | `throttle_registry.go:69`, `providerhttp.go:60-81`, `dailyquota.go:333` | keyed by provider string |
| `startup.Table` | `startup/steps.go` | static table |
| `transcribe` endpoint cooldown maps | `transcribe/dispatcher.go:77-92` | keyed by URL |
