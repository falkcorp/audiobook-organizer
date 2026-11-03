<!-- file: docs/plans/platform-core/raw/07-plugin-registry-sdk.md -->
<!-- version: 1.0.0 -->
<!-- guid: 88aa18a9-6806-4b37-ab0b-de6dbf132c87 -->
<!-- last-edited: 2026-11-03 -->

# 07 - Plugin / registry / SDK inventory

Evidence commit: `748fd787a` (checked-out HEAD `57fd77ffc` only adds `docs/plans/platform-core/STATE.md`; code anchors identical). Anchors are `path:line`.

## 0. Summary

There are **five** different "registry/plugin" mechanisms, all populated through package-level state:

| # | Mechanism | Package | Populated by | Consumed by | Purpose |
|---|---|---|---|---|---|
| 1 | Service factory list + per-instance `Container` | `internal/serviceregistry` | `init()` -> `serviceregistry.Register` (43 calls in 24 init funcs) | `NewServer` (`internal/server/server.go:814`) | dependency-ordered construction of in-process services + lifecycle |
| 2 | Runtime plugin registry + event bus + scoped HTTP routes | `internal/plugin` | `init()` -> `plugin.Register` (1 caller: webhook) | `Server.initPlugins` (`internal/server/plugins_init.go:25`), `PluginsHandler` | enable flag, health, config, `/api/v1/plugins/{id}` routes, notification events |
| 3 | Operation-registrar SDK | `pkg/plugin/sdk` | each `internal/plugins/*` implements `sdk.Plugin` (6 do) | `Plugin.Register(reg)` called from service `PostInit` or `registerAll` | plugin = a bundle of `OperationDef`s (aliases onto the v2 op registry) |
| 4 | Op catalogs | `internal/maintenance` (`Register`), `internal/server` (`addOpRegistrar`), `internal/opscatalog` (`All`), `pkg/ops` (`Bundle`) | `init()` and package vars | `registerAll` (`internal/server/server.go:1630`) | enumerate jobs/ops |
| 5 | Misc hook/registries | `aijobs`, `dailyquota`, `opsmetrics.RegisterDefID`, `server.RegisterFileOpRecovery`, `database.SetCollectionPerViewerClassifier` | `init()` or `NewServer` | various | callbacks |

The pre-existing design docs call the first three "three overlapping extension systems" (`docs/architecture/2026-09-18-pluggable-media-platform-evaluation.md`, "The problem is that three overlapping..."); that is still true at HEAD.

## 1. `internal/serviceregistry`

Files: `registry.go` (62 lines), `container.go` (281), `graph.go` (110), `lifecycle.go` (28), `keys.go` (36), `errors.go` (15).

### 1.1 Proof that it is a package-level `init()` factory list

* The list: `var registered = map[string]ServiceDef{}` (`registry.go:38`) - a plain map, **no mutex**.
* The writer: `func Register(def ServiceDef)` (`registry.go:44`), "Called from init() in a domain package's register.go" (`registry.go:~42`); panics on empty name, nil `Build`, or duplicate name (`registry.go:46,49,52`).
* The readers: `Container.IncludeAll` (`container.go:57`), `IncludeGroup` (`:73`), `Build` (`:115`, `registered[name]`), `Get` (`container.go:~246`, `registered[c.activeBuilder]`) - all read the global map without locking.
* Test reset: `ResetForTest()` replaces the map with an empty one (`registry.go:60`).
* Caller census: 43 `serviceregistry.Register(` calls in non-test code, all inside `func init()` bodies of 24 files (table 1.3). No registration happens in a constructor; the only non-init writers are tests (`internal/config/register_test.go:65`, `internal/plugin/register_test.go:21,68`).

### 1.2 API

| Symbol | Anchor | Notes |
|---|---|---|
| `type ServiceDef{Name, Needs []string, Groups []string, Build func(*Container)(any,error)}` | `registry.go:11-35` | `Needs` is enforced: `Get[T]` inside a `Build` panics if the name is not in the active builder's `Needs` (`container.go:~247-254`) |
| `NewContainer()` | `container.go:37` | per-instance |
| `Include(names...)`, `IncludeAll()`, `IncludeGroup(groups...)`, `Override(name, instance)` | `container.go:47,57,73,92` | `Override` = leaf, `Needs` ignored; test-only per comment, **but production uses it** for `store`, `config`, `appversion` (`internal/server/server.go:815-817`) |
| `Resolve()` (Kahn, lex-stable; `ErrCycle`, `ErrUnknownService`) | `graph.go:19` | |
| `Build(ctx)`, `PostInit(ctx)`, `Start(ctx)`, `Stop(ctx)`, `Names()` | `container.go:115,142,164,195,99` | `Start` rolls back already-started services in reverse on failure (`:178-186`); `Stop` is reverse order, best effort |
| `Get[T](c, name) T` (panics), `TryGet[T](c, name) (T, bool)` | `container.go:245,270` | 113 `Get/TryGet` call sites in non-test code |
| Optional lifecycle ifaces `PostIniter`, `Starter`, `Stopper` (by type assertion) | `lifecycle.go` | |
| `Key*` constants (24) | `keys.go:7-35` | `KeyStore="store"`, `KeyConfig="config"`, `KeyEventBus`, `KeyOpHub`, ... |
| Typed accessors (wrap `Get[T]` with the key fixed) | `config.GetConfig` (`internal/config/register.go:49`), `plugin.GetEventBus` (`internal/plugin/register.go:45`) | cannot live in `serviceregistry` (would be an import cycle, comment `plugin/register.go:~38-44`) |
| `ErrCycle`, `ErrUnknownService`, `ErrUndeclaredDep`, `ErrNotBuilt`, `ErrTypeMismatch`, `ErrWrongPhase` | `errors.go` | |

Group conventions (`registry.go:21-28`): `core`, `ai`, `activity`, `plugins`, `scheduler`. Production selection: `IncludeGroup("core","ai","scheduler","plugins")` and `activity` only when `DatabasePath != ""` (`internal/server/server.go:818-829`). Phases after Build: `wireServerFromContainer` (`internal/server/registry_wire.go:385`) copies built services into typed `*Server` fields; `container.Start/Stop` are called from `internal/server/server_lifecycle.go:279,693`.

### 1.3 Every `serviceregistry.Register` registrant (43)

All are in `init()`; "Group" blank means no group.

| # | Service name | file:line | Group | Needs |
|---|---|---|---|---|
| 1 | `metadatastate` | `internal/metafetch/register.go:12` | core | store |
| 2 | `metafetch` | `internal/metafetch/register.go:22` | core | store |
| 3 | `olservice` | `internal/metafetch/register.go:35` | core | - |
| 4 | `dashboard` | `internal/sysinfo/register.go:12` | core | store |
| 5 | `system` | `internal/server/registry_wire.go:97` | core | store |
| 6 | `embeddingstore` | `internal/server/registry_wire.go:109` | ai | store |
| 7 | `chromemstore` | `internal/server/registry_wire.go:133` | ai | config |
| 8 | `aijobsstore` | `internal/server/registry_wire.go:165` | ai | store |
| 9 | `dedup` | `internal/server/registry_wire.go:179` | ai | store, config, embeddingstore, embedclient, llmparser, merge |
| 10 | `metricsstore` | `internal/server/registry_wire.go:276` | core | config, store |
| 11 | `aiscanstore` | `internal/server/registry_wire.go:298` | ai | store |
| 12 | `pipelinemanager` | `internal/server/registry_wire.go:319` | ai | store, aiscanstore, llmparser |
| 13 | `itunes` | `internal/server/registry_wire.go:343` | core | store, config, eventbus, metafetch |
| 14 | `batchpoller` | `internal/server/batch_poller_register.go:18` | **none** | store, config |
| 15 | `embedclient` | `internal/ai/register.go:33` | ai | config, embeddingstore |
| 16 | `llmparser` | `internal/ai/register.go:105` | ai | config |
| 17 | `metadatascorer` | `internal/ai/register.go:136` | ai | config, embedclient, embeddingstore |
| 18 | `metadatallmscorer` | `internal/ai/register.go:158` | ai | config, llmparser |
| 19 | `pebble-activitystore` | `internal/activity/register.go:37` | activity | store |
| 20 | `activitystore` | `internal/activity/register.go:75` | activity | config, pebble-activitystore |
| 21 | `activity` | `internal/activity/register.go:105` | activity | activitystore |
| 22 | `activitywriter` | `internal/activity/register.go:120` | activity | activity |
| 23 | `updater` | `internal/updater/register.go:60` | scheduler | - |
| 24 | `updatescheduler` | `internal/updater/register.go:76` | scheduler | updater |
| 25 | `filesystem` | `internal/fileops/register.go:13` | core | store |
| 26 | `configupdate` | `internal/config/register.go:13` | core | store |
| 27 | `searchindex` | `internal/search/register.go:106` | scheduler | config |
| 28 | `eventbus` | `internal/plugin/register.go:14` | core | - |
| 29 | `work` | `internal/work/register.go:13` | core | store |
| 30 | `audiobook` | `internal/audiobooks/register.go:12` | core | store |
| 31 | `organize` | `internal/audiobooks/register.go:21` | core | store |
| 32 | `ophub` | `internal/operations/registry/register.go:84` | scheduler | - |
| 33 | `opregistry` | `internal/operations/registry/register.go:93` | scheduler | store, ophub |
| 34 | `quarantine` | `internal/quarantine/register.go:14` | core | store, config, eventbus |
| 35 | `scan` | `internal/scanner/register.go:13` | core | store, embeddingstore |
| 36 | `merge` | `internal/merge/register.go:12` | core | store |
| 37 | `importpath` | `internal/importer/register.go:12` | core | store |
| 38 | `batch` | `internal/batch/register.go:13` | core | store, audiobook |
| 39 | `metafetchplugin` | `internal/plugins/metafetch/register.go:30` | plugins | store, metafetch |
| 40 | `itunesplugin` | `internal/plugins/itunes/register.go:43` | plugins | itunes, config |
| 41 | `delugeplugin` | `internal/plugins/deluge/register.go:24` | plugins | store, config |
| 42 | `dedupplugin` | `internal/plugins/dedup/register.go:32` | plugins | store, dedup, embeddingstore, configupdate |
| 43 | `acoustidplugin` | `internal/plugins/acoustid/register.go:25` | plugins | store, dedup, embeddingstore |

Group counts: **core 18, ai 10, scheduler 5, plugins 5, activity 4, none 1** (total 43).

Findings on this list:

* **`batchpoller` has no `Groups`** (`batch_poller_register.go:18-20`), yet its header comment and the design say it is in the `scheduler` group. `NewServer` only includes `core, ai, scheduler, plugins` (+`activity`) groups and no explicit `Include("batchpoller")` exists (grep over `internal`, `cmd`), so the container never builds it; `TryGet[*BatchPoller](c, "batchpoller")` at `internal/server/registry_wire.go:422` can only return `(nil,false)` in production. UNKNOWN whether the batch poller is constructed some other way (the scheduler `batch_poller` task is wired through `SchedulerDeps.PollBatches`/`HasBatchPoller`, `internal/scheduler/scheduler.go:47-85`).
* The design doc lists services that do not exist as registrations: `writebackbatcher`, `librarywatcher` (no `Name:` with those strings anywhere), and `maintenanceplugin` (deliberately unregistered, `internal/plugins/maintenance/register.go:6-19`).
* `internal/server` itself registers 10 services (rows 5-14) from `init()`, i.e. layer-5 code registering infrastructure (`embeddingstore`, `chromemstore`, `dedup`, `metricsstore`, `itunes`...) that sits beside the domain packages.
* `internal/operations/registry` (layer 4) registers `ophub`/`opregistry`; `internal/search` (layer 2) registers `searchindex`.

## 2. `internal/plugin` (runtime plugin registry; webhook-only)

| Symbol | Anchor |
|---|---|
| `Plugin` interface `ID, Name, Version, Capabilities, Init(ctx, Deps), Shutdown, HealthCheck` | `plugin.go:25` |
| `Capability` consts `metadata_source, download_client, media_player, notifier, event_subscriber` | `plugin.go:17-21` |
| `Deps{Events *EventBus, Config map[string]string, Logger, Router PluginRouter}` | `plugin.go:44` |
| `DownloadClient`, `MediaPlayer`, `Notifier` interfaces | `plugin.go:58,76,98`; `DownloadClient` is documented VESTIGIAL, zero implementors (`plugin.go:52-57`) |
| global `globalRegistry`; `Register(p)` (duplicate -> WARN and skip, not panic) | `registry.go:15,33-42` |
| `Global()`, `Get`, `All`, `Enable/Disable/IsEnabled`, `ByCapability`, `InitAll`, `InitAllScoped`, `ShutdownAll` (reverse init order), `HealthCheckAll` | `registry.go:45-170` |
| `ResetForTesting()` | `registry.go:177` |
| `EventBus`: `Subscribe`, `Publish` (async goroutine per handler, panic-recovered, errors logged, no ack/replay), `SubscriberCount` | `events.go:61-106` (Publish `:85`) |
| Event types + `NewEvent` | `events.go:16-50` |
| `PluginRouter` (GET/POST/PUT/DELETE/Group), `NewPluginRouter(parent, pluginID)` | `router.go:10,24` |
| the only implementor: `internal/plugins/webhook.Plugin` (`init(){ plugin.Register(&Plugin{}) }`, `plugin.go:33`; capability `event_subscriber`) | |

Wiring: `server.pluginRegistry = plugin.Global()` (`internal/server/server.go:903`); `initPlugins` enables plugins per `config.AppConfig.Plugins`, then `InitAllScoped` once (`plugins_init.go:25-47`). Enable/Disable only flip a boolean map (`registry.go:69-81`); `PluginsHandler` (`internal/server/wire_handlers.go:153`) does not call Init/Shutdown, so enablement is effectively restart-bound (adversarial review "Workflow 1", `...-adversarial-review.md`). A stale comment says blank imports of `plugins/deluge` register with `plugin.Global()` (`plugins_init.go:16`) while deluge actually registers through serviceregistry (`plugins/deluge/register.go:23`).

The `EventBus` also underlies `Deps.Events`, `plugin.GetEventBus`, and the `eventbus` service used by `quarantine` and `itunes` (rows 13, 34 in 1.3). Design Q1 (`docs/architecture/server-plugin-registry-deferred-work.md:482-499`) decided "bounded-channel dispatch, subscribers get the bus lifecycle ctx, not the publisher ctx"; the implementation at `events.go:85-103` instead spawns an unbounded goroutine per handler and passes the **publisher's** ctx to it - not what was decided.

## 3. `pkg/plugin/sdk` (v2 operation SDK)

| Item | Anchor |
|---|---|
| `Plugin{ID,Name,Version,Register(Registry) error}`; `DisableMode` consts | `plugin.go:9-28` |
| `Registry{RegisterOp(OperationDef), EnqueueOp(ctx, defID, params, ...EnqueueOption) (string, error)}` | `registration.go:12-17` |
| Aliases: `Capability`, `OperationDef`, `ResumePolicy`, `LivenessMode`, `Priority`, `ActorMode`, `Phase`, `Resource`, `PreviewSpec`, `Reporter`, `EnqueueOption`, `EventSubscription`, `Bus` = `internal/operations/registry` types | `capability.go:11`, `operation.go:11-18`, `reporter.go:12`, `enqueue_options.go:11`, `events.go:11-15` |
| Re-exported consts/option funcs (`Resume*`, `Priority*`, `Liveness*`, `Cap*`, `WithParent/WithActor/WithPriority`) | `operation.go:20-40`, `capability.go`, `enqueue_options.go:14` |
| `PageBooks(ctx, BookStore, Reporter, pageSize, fn)` with 60s keepalive progress | `iterate.go:41` (`BookStore` `iterate.go:25` uses `database.Book`) |
| `Progress`/`NewProgress` helper | `progress.go:26,36` |
| `ErrXxx` | `errors.go` |

Users: `internal/plugins/{acoustid,dedup,deluge,itunes,maintenance,metafetch}` implement `sdk.Plugin` (comments `// ID implements sdk.Plugin.` in each `plugin.go`), ~210 `sdk.OperationDef` literals (file 06). External consumption is impossible today: aliases point at `internal/...` packages and `iterate.go` needs `internal/database` (adversarial review "Workflow 4": "The current SDK is not a platform SDK"). The docs promise stability ("STABLE as of UOS-15", `doc.go:~60`) but there is no external consumer or versioning mechanism.

## 4. `pkg/ops` (v3 SDK) and op catalogs

See file 06 section 1.2 for API. Registry-relevant points:

* Exports `Bundle{Plugin, Defs, Requires}` (`pkg/ops/definition.go:138`); catalog list `opscatalog.All []func(Deps) ops.Bundle` is empty (`internal/opscatalog/catalog.go:56`). `opscatalog.Build` rejects bundles without a plugin name, duplicate bundles and missing required services (`catalog.go:73-100`).
* `opscatalog.StaticPluginDefs()` enumerates the 5 serviceregistry-backed plugins' defs without a server (`catalog.go:107-115`); used by the op-ID ledger tests (`internal/server/op_id_aliases_test.go`).
* `TestCatalogLinkedIntoBinary` (`internal/opscatalog/catalog_link_test.go:23`) inspects the real import graph to ensure every package that `serviceregistry.Register`s or declares an `Ops(...) ops.Bundle` is linked into the server binary (the init-registration hazard: a package whose `init()` is never imported silently registers nothing).
* `tools/cmd/oplint` (analyzer: `tools/cmd/oplint/analyzer.go`) lints `ops.Task/Batch/Fixer/Pipeline` bundles (unbundled-def, writer-bypass, store-in-deps, missing-help, sequential-empty-reason, deletes-owner-review).

## 5. Every `func init()` in the module

Method: `grep -rn '^func init()' --include=*.go` excluding `node_modules`; bodies classified by what they call. Totals: **111 non-test** init functions in 111 files, **16 real test** init functions (+1 grep false positive: `internal/telemetry/ratchet_test.go:262` is inside a raw-string fixture, not a real init).

### 5.1 Non-test inits by class

| Class | Count | Files |
|---|---|---|
| A. `serviceregistry.Register` | 24 files / 43 calls | table 1.3 |
| B. `maintenance.Register(&job{})` (one-liner) | 34 | `internal/maintenance/jobs/*.go` (backfill_book_files.go:30, backfill_file_hashes.go:42, backfill_itunes_positions.go:25, backfill_merge_undo_pointers.go:20, backfill_metadata_source_hash.go:19, backfill_sync_ids.go:19, bulk_deluge_import.go:27, cleanup_empty_folders.go:23, cleanup_organize_mess.go:26, cleanup_series.go:21, dedup_books.go:29, enrich_book_files.go:21, fix_author_narrator_swap.go:19, fix_file_modes.go:17, fix_read_by_narrator.go:20, fix_version_groups.go:31, merge_chapter_groups.go:30, normalize_primary_flags.go:20, prune_book_snapshots.go:21, purge_ua_duplicates.go:31, recompute_book_aggregates.go:46, recompute_itunes_paths.go:20, refetch_missing_authors.go:24, relink_missing_to_itunes.go:26, relink_report.go:25, repoint_version_primary.go:20, retention_and_hygiene.go:21, revert_metadata_fetch.go:20, scan_chapter_groups.go:14, scan_composer_tags.go:28, scan_duplicate_files.go:17, scan_duration_mismatch.go:17, scan_metadata_hash_dups.go:17, sweep_pebble_metrics_ttl.go:23) |
| C. `addOpRegistrar(...)` (append to `opRegistrars` slice) | 33 files / 48 calls | all in `internal/server/`: ai_ops.go:289, ai_scan_apply_op.go:544, aiscan_op.go:103, apply_when_scanned_op.go:194, author_reclassify_op.go:154, batch_apply_op.go:843, batch_apply_refusal_reset_op.go:145, batch_save_op.go:295, bulk_apply_preview.go:507, catalog_coverage_op.go:46, catalog_harvest_op.go:69, collection_refresh_op.go:36, diagnostics_ai_ops.go:195, diagnostics_ops.go:146, duplicates_ops.go:1475 (9 calls), entities_ops.go:729, folder_autoscan_op.go:174, identification_advance_op.go:64, itunes_ops.go:82, legacy_backfill_op.go:145, library_ai_parse_op.go:221, library_core_ops.go:467 (4), library_size_refresh_op.go:81, library_writeback_op.go:165, maintenance_job_op.go:315, metadata_candidate_op.go:451, metadata_ops.go:874, openlibrary_ops.go:105, purge_soft_deleted_op.go:54, reconcile_ops.go:176, scheduler_extra_ops.go:23, scheduler_maintenance_window_op.go:265, series_rename_ops.go:311 |
| D. `aijobs.Register(type, callback)` | 2 | `internal/ai/author_dedup_job.go:132`, `internal/ai/dedup_review.go:242` |
| E. `plugin.Register(&Plugin{})` | 1 | `internal/plugins/webhook/plugin.go:33` |
| F. Hook / package-global population | 5 | `cmd/child_mode.go:24` (sets `registry.ChildEnvFunc`), `internal/metadata/google_quota.go:61` (`dailyquota.Install`), `internal/search/bleve_index.go:68` (adds a callback to bleve `scorch.RegistryAsyncErrorCallbacks`), `internal/search/field_routing.go:159` (`database.SetCollectionPerViewerClassifier`), `internal/server/handlers/metadata_cache_query.go:398` (fills `reviewSearchFields` map) |
| G. Other (not registration) | 3 | `internal/database/embedding_store.go:60` (zstd encoder), `internal/server/csp.go:85` (initial CSP state), `pprof_debug.go:105` (env-driven pprof start) |
| H. cobra command/flag wiring | 9 | `cmd/root.go:474`, `cmd/dedup_bench.go:62`, `cmd/dedup_bench_crossval.go:40`, `cmd/dedup_bench_pass2.go:46`, `cmd/diagnostics.go:92`, `cmd/fp_worker.go:74`, `cmd/seed.go:46`, `cmd/mtls-bridge/main.go:52`, `tools/cmd/oplint/analyzer.go:96` |
| **Total** | **111** | registration (A-E) = 94, hook/other = 17 |

Plus, outside `init()`: `server.RegisterFileOpRecovery` is called from `NewServer` (`internal/server/server.go:1112,1119`); `opsmetrics.RegisterDefID` is called from `RegisterOp` (`registry.go:857`); `maintenanceplugin.New(s).Register(reg)` is explicit in `registerAll` (`server.go:1637`).

### 5.2 Test-file inits (16)

| File:line | What | Isolation impact |
|---|---|---|
| `internal/server/maintenance_orphan_row_test.go:60` | `maintenance.Register(probeBlocking)` | pollutes the process-global maintenance registry for the whole `internal/server` test binary (panics on duplicate, no unregister; comment in file) |
| `internal/server/maintenance_force_param_test.go:98` | `maintenance.Register(forceProbe)` | same |
| `internal/server/maintenance_dryrun_default_test.go:115` | 3x `maintenance.Register(probe...)` | same; comment relies on "no test asserts a job count" |
| `internal/server/maintenance_custom_params_test.go:123` | `maintenance.Register(customProbe)` | same |
| `internal/server/maintenance_result_bridge_test.go:39` | `maintenance.Register(resultProbeJob{})` | same |
| `internal/database/pebble_store_ops_v2_timeline_test.go:1045` | extends `opv2KeySinks` map for an AST ratchet | test-local data |
| `internal/operations/registry/reporter_db_test.go:141` | `gob.Register(checkpointState{})` | global gob registry |
| `internal/operations/registry/reporter_result_test.go:44` | compile-time assertion panic | none |
| `internal/server/handlers/{metadata,repairs,review,dedup,opsv3,audiobooks,duplicates}/handler_test.go` and `.../opsv3/helpers_test.go:29` (7 files) | `gin.SetMode(gin.TestMode)` | global gin mode |
| `internal/itunes/service/transfer_handler_test.go:22` | `gin.SetMode(gin.TestMode)` | global gin mode |

## 6. Test-isolation issues (evidence-backed)

1. **Global factory map is unsynchronised and shared by the whole test binary.** `registered` is a bare map (`serviceregistry/registry.go:38`); `Register`/`ResetForTest` (`:44,60`) and every `Container` reader touch it without a lock, so `t.Parallel()` tests that construct containers while another test registers/resets race.
2. **`ResetForTest` is destructive.** It drops the production registrations that `init()` installed; after `serviceregistry.ResetForTest` in `internal/plugin/register_test.go:36,62` (with `t.Cleanup(ResetForTest)` which resets to EMPTY, not to the original) the `eventbus` init registration is gone for the rest of that test binary, so each test must re-register by hand (`registerEventBusDef`, `plugin/register_test.go:20-31`). Only `serviceregistry`'s own tests and these two packages use it.
3. **Leaking registration.** `internal/config/register_test.go:65` registers `naughty-config-consumer` with no cleanup; a `-count=2` run would panic with "duplicate name" (`registry.go:52`). The file's comment (`:57-62`) acknowledges not resetting.
4. **`Container.Override` is the supported per-test seam** (leaf, ignores `Needs`, `container.go:92`) and works with `Include(...)`; but `IncludeGroup` pulls in every `init()` registrant of that group in the test binary, so what a given test builds depends on which packages the test binary happens to import (init-by-import coupling; `TestCatalogLinkedIntoBinary` exists because of it).
5. **`maintenance.Register` panics on duplicates and has no unregister** (`maintenance/registry.go:19-24`), so test probes are registered once per binary and permanently visible to `maintenance.All()` (see 5.2).
6. **`opRegistrars` (`internal/server/op_registrars.go:23`) is a package-level slice appended by `init()`**; every `NewServer` in a test registers every op (hence `expectOpDefinitionUpserts` helper expectations noted in `registerAll` comment, `server.go:~1623`). No per-test subset mechanism.
7. **`plugin.Global()` is a process singleton** (`internal/plugin/registry.go:15,45`); `Register` warns and skips duplicates (`:37-39`) and `ResetForTesting` (`:177`) is the only seam. `Server.pluginRegistry` is the singleton (`server.go:903`), not a fresh instance per server.
8. **`aijobs.Register`/`ClearRegistryForTest`** exist (`aijobs.go:89-100`), mutex-guarded.
9. **Other process-global hooks** each with a restore function: `dailyquota.Install` returns `restore` (`dailyquota.go:315`), `database.SetWarmupStartHookForTest`, `SetProbeForTesting` etc. (grep: ~15 `...ForTest`/`SetX(...) (restore func())` helpers) - a pattern, but each is individually global so tests must not run in parallel with each other.
10. **Typed nil registrations.** `acoustidplugin`, `dedupplugin` etc. return `(*Plugin)(nil), nil` when their engine/store dependencies are absent (`plugins/acoustid/register.go:~30-34`); `PostInit` nil-checks (`:~38`). Consumers must tolerate nil-in-interface.

## 7. Summary of `docs/architecture/server-plugin-registry-design.md` and `...-deferred-work.md`

### 7.1 Design (2026-05-11; 585 lines)

Goals: unblock the scheduler residual, decouple `*Server` for tests, shrink `server.go` (`NewServer` <= 50 lines), prepare for compile-time third-party plugins (`design.md:11-21`). Non-goals: runtime `.so` loading, reflection DI, HTTP-handler migration. Concepts: `ServiceDef` registered via `init()` into a global factory list; per-instance `Container`; phases Resolve/Build/PostInit/Start/Stop (`:23-36`). Migration: 7 waves (W0 foundation; W1 leaf services; W2 cross-wired; W3 start/stop; W4 AI cluster; W5 plugin migrations; W6 scheduler residual; W7 cleanup) (`:439-533`). Future work: SERVER-HANDLER-MIGRATE, SERVER-SDK-INTERFACES (`pkg/serverapi`), REGISTRY-METRICS (`/api/v1/system/services`), REGISTRY-CONFIG-RELOAD (`Reloader`), CLI-CONTAINER-INSPECT (`:560-570`). Locked decisions: init() auto-register, typed `Get[T]`, handlers keep using `*Server` fields, optional lifecycle interfaces, `Needs` enforced during Build only, Kahn lex-stable cycle detection, `Include`+`Override` for tests (`:576-585`).

### 7.2 Deferred-work plan (598 lines)

Seven tickets (`deferred-work.md:23-40`): REGISTRY-NAMED-GROUPS, PLUGIN-DECOUPLE-SERVER-CLOSURES, PROMOTE-STUB-REGISTRATIONS, SERVER-LIFECYCLE-FLIP, W7.1 NEWSERVER-TRIM, SERVER-GLOBAL-STORE-AUDIT, TEST-GLOBAL-STORE-MIGRATION; plus resolved design Q1-Q4 (event-bus semantics, NewServer lifecycle, GetGlobalStore lifecycle, named groups) (`:480-537`).

### 7.3 Built vs not built (verified in code)

| Item | Designed | Status at HEAD | Evidence |
|---|---|---|---|
| W0 registry foundation | 5 files | **Built** (+ `keys.go`) | `internal/serviceregistry/*` |
| W1 leaf services (audiobook, batch, work, filesystem, importpath, scan, dashboard, system, configupdate, metadatastate) | 10 | **Built** | table 1.3 rows 1,4,5,25-26,29-31,35,37,38 |
| W2 cross-wired (metafetch, activity, merge, quarantine, organize) | 5 | **Built** | rows 2,19-22,31,34,36 |
| W3 `writebackbatcher` | Start/Stop service | **Not found** (no registrant) | grep over `internal` |
| W3 `librarywatcher` | Start/Stop service | **Not found** | grep |
| W3 `updatescheduler`, `activitywriter`, `searchindex`, `opregistry` | | **Built** | rows 24,22,27,33 |
| W3 `batchpoller` | | **Registered but never built** (no group) | `batch_poller_register.go:18` |
| W4 AI cluster (embeddingstore, embedclient, llmparser, chromemstore, aijobsstore, dedup, metadatascorer, metadatallmscorer) | 8 | **Built** (+ extras `aiscanstore`, `pipelinemanager`, `metricsstore`) | rows 6-12,15-18 |
| W5 plugin migrations | maintenance, itunes, deluge, dedup, acoustid | **4 of 5 built + metafetchplugin extra**; `maintenanceplugin` intentionally NOT registered | `plugins/maintenance/register.go:6-19`; rows 39-43 |
| W6 scheduler residual | move `scheduler_extra_ops.go` into `internal/scheduler` | **Built**; a 23-line shim remains in server (`scheduler_extra_ops.go:23`) | `internal/scheduler/extra_ops.go:43-88` |
| W7.1 `NewServer` <= 50 lines | | **Not met**: `NewServer` is ~612 lines (`internal/server/server.go:653`) | measured by brace match |
| W7.2 drop `database.GetGlobalStore()` fallbacks | | **Effectively done**: only the definition remains (`internal/database/store.go:1813`); 0 non-test callers | grep; `scanner.SetStore` comment `server.go:~802` ("SERVER-GLOBAL-STORE-AUDIT phase 7") |
| REGISTRY-NAMED-GROUPS | `Groups` + `IncludeGroup` | **Built**; production uses it (`server.go:818`) | `container.go:73` |
| PLUGIN-DECOUPLE-SERVER-CLOSURES | itunes `Deps` + `maintenance.ServerDeps` | **Partly**: itunes plugin builds from the container (`itunesplugin` needs `itunes`); maintenance still takes the whole server via `ServerDeps` (`plugins/maintenance/deps.go:1178`) | |
| PROMOTE-STUB-REGISTRATIONS | 3 stubs | itunesplugin promoted; `writebackbatcher` gone; maintenance stays outside the registry | |
| SERVER-LIFECYCLE-FLIP | `container.Start/Stop` driven by server | **Built** | `server_lifecycle.go:279,693` |
| TEST-GLOBAL-STORE-MIGRATION | tests off the global store | **Not done**: 88 test files still reference `GetGlobalStore()`/`SetGlobalStore(` | grep (`store.go:1821` `SetGlobalStore` exists) |
| Q1 event-bus semantics | bounded channel, bus lifecycle ctx | **Not as decided** (goroutine per handler, publisher ctx) | `internal/plugin/events.go:85-103` |
| SERVER-HANDLER-MIGRATE | handlers into domain packages | **Partly**: 18 subpackages under `internal/server/handlers/` | `ls internal/server/handlers` |
| SERVER-SDK-INTERFACES (`pkg/serverapi`) | | **Not built** (`pkg/` has only `ops`, `plugin`) | |
| REGISTRY-METRICS (`/api/v1/system/services`) | | **Not built** (no such route) | grep |
| REGISTRY-CONFIG-RELOAD (`Reloader`) | | **Not built** | grep in `serviceregistry` |
| CLI-CONTAINER-INSPECT | | **Not built** | grep `cmd` |

## 8. Observations for the platform-core design

1. The init-time global factory list is the documented composition mechanism, but the platform evaluation recommends an explicit builtin list (`evaluation.md`, "Use an explicit builtin list rather than discovery by init()"). A shared core consumed by several apps (Sonarr/Radarr-likes) would have each app register into the same process-global map if linked together; per-app composition needs `NewContainer`-scoped definitions rather than global ones.
2. `ServiceDef.Build` returns `any` and `Get[T]` is runtime-checked; the `Key*` constants include audiobook-specific names (`KeyAudiobook`, `KeyOrganize`, `KeyImportPath`, `KeyITunes`) in the generic package (`keys.go`).
3. Core vs app split already visible: generic services (`eventbus`, `ophub`, `opregistry`, `activity*`, `config*`, `updater`, `searchindex`, `metricsstore`, `store`) vs audiobook services (`audiobook`, `organize`, `scan`, `merge`, `dedup`, `itunes`, `metafetch`, `quarantine`, `batch`, `work`).
4. Three plugin vocabularies persist: `sdk.Plugin` (ops), `plugin.Plugin` (runtime/events/routes), `serviceregistry.ServiceDef` (construction). Only webhook uses the second; six use the first; every one of the six also needs the third to be constructed.
