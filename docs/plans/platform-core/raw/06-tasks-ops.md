<!-- file: docs/plans/platform-core/raw/06-tasks-ops.md -->
<!-- version: 1.0.0 -->
<!-- guid: e8be7cec-774d-40d4-97e2-1f4c7cbf5036 -->
<!-- last-edited: 2026-11-03 -->

# 06 - Task / job system (ops) inventory

Evidence commit: `748fd787a` (the checked-out HEAD `57fd77ffc` only adds `docs/plans/platform-core/STATE.md` on top, so every code anchor below is identical at both). Anchors are `path:line`.

## 0. Summary: there are four coexisting generations of "job"

| Gen | Name | Where | Status at HEAD |
|---|---|---|---|
| v1 | "operation" rows, `opstate:` checkpoints, `operation:`/`operationlog:` | `internal/operations/state.go:14`, `internal/database/keyfamilies.go:267-270` | legacy; key families retained; `SaveCheckpoint/LoadCheckpoint` helpers at `internal/operations/state.go:60,78` |
| v2 | `registry.OperationDef` + `Registry` (dispatcher, workers, watchdog, resume) | `internal/operations/registry/` | **the production executor; every op actually runs on it** |
| v2.5 | `maintenance.MaintenanceJob` (34 jobs) wrapped into v2 defs | `internal/maintenance/`, `internal/maintenance/jobs/` | production; each job becomes one `OperationDef` (`internal/server/maintenance_job_op.go:146`) |
| v3 | `pkg/ops` authoring SDK (`Task/Batch/Fixer/Pipeline`), run state `opv3:*` | `pkg/ops/`, `internal/operations/state/v3.go`, `internal/database/pebble_store_ops_v3.go` | executor + storage + HTTP built; **zero native defs exist** (`internal/opscatalog/catalog.go:56` `var All = []func(Deps) ops.Bundle{}`; no non-test, non-testdata caller of `ops.Task/Batch/Fixer/Pipeline` exists outside `pkg/ops`) |

Plus a fifth, separate scheduler layer (`internal/scheduler`) that enqueues v2 ops by name, and the `pkg/plugin/sdk` facade (type aliases over the v2 registry).

Layering (enforced by `internal/arch/layering_test.go`, see `docs/architecture/layering.md:19-28`): `operations/registry`, `scheduler`, `plugins/*`, `maintenance/jobs` are layer 4; `server/**` layer 5. Layer-3 packages (e.g. `remux`, `itunes`) may NOT import the registry, which is why `internal/walkresume` exists as a seam (`internal/walkresume/walkresume.go:5-17`).

## 1. API of each package

### 1.1 `internal/operations/registry` (v2 executor) - key types

| Symbol | Anchor | Notes |
|---|---|---|
| `type OperationDef struct` | `types.go:22` | the unit of registration (fields below) |
| `OperationDef.Run func(ctx, params json.RawMessage, reporter Reporter) error` | `types.go:48` | the job body |
| `.ResumePolicy` (required) | `types.go:59`; consts `ResumeUnspecified/Restart/Requeue/Drop/Ask` at `types.go:256-261` | zero value refused at boot |
| `.Liveness LivenessMode` (required) | `types.go:75`; consts at `types.go:284-` (`RunItems`, `Manual`, `None`) | `LivenessNone` requires `ProgressTimeout` |
| `.ProgressTimeout` | `types.go:70`; default 5m `watchdog.go:18,26` | |
| `.ConcurrencyKey` | `types.go:97` | serialises runs with the same key (dispatcher Gate 3, `dispatcher.go:144`) |
| `.DedupeQueuedRuns`, `.DedupeSameActorOnly`, `.MergeQueuedParams`, `.SummarizeQueued` | `types.go:108,120,125,145` | enqueue-time dedupe/merge |
| `.Isolate bool` (subprocess) / `.Timeout` | `types.go:55-56` | defaults 120m in-process, 6h subprocess, cap 24h; subprocess mode is effectively disabled for `itunes.import` (`internal/plugins/itunes/import.go`: "DISABLED ... Pebble is single-writer") |
| `.Preview PreviewSpec` | `types.go:163` | dry-run/preview contract, validated `registry.go:791` |
| `.Permissions []auth.Permission`, `.Capabilities []Capability`, `.RunsAs ActorMode` | `types.go:166-168` | RBAC hook: `auth.Permission` is imported by the executor (`types.go:17`) |
| `.Schedule *string` (cron text) | `types.go:171` | **declarative only**: never evaluated as a timer (`internal/scheduler/tasks.go:1046-1050`); persisted to `ScheduleCron` (`registry.go:887`), and `EnqueueOp` folds a second start into the active run when non-nil (`registry.go:1059`) |
| `.Triggers []EventSubscription` | `types.go:174` | event names; only `deluge.path-update` declares one |
| `.DependsOn`, `.Writes/.Reads []Resource`, `.Requires []Requirement` | `types.go:177,198,204,211` | DependsOn = "must not be running" (Gate 4 `dispatcher.go:182`); `Writes` drives write-set gate (Gate 3b `dispatcher.go:164`); `Requires` = subject-level deps (`deps.go`) |
| `.Batchable/.BatchWindow/.BatchMaxWait` | `types.go:230-240` | debounce buckets journaled under `op:batch:` |
| `.Phases []Phase`, `.NotifyLevel`, `.FormerIDs`, `.NoWarmupWait`, `.Synchronous` | `types.go:243,249,45,93,84` | |
| `.Cancellable bool` | `types.go:52` | **stale doc**: comment says "false = Cancel API rejects" but `Registry.Cancel` never reads it; it is only persisted (`registry.go:900`) and shown in a view (`internal/server/handlers/operations_v2.go:1377`) |
| `type Reporter interface` | `reporter.go:21-34` | `UpdateProgress, Log, Logger, Checkpoint, IsCanceled, RunPhase, Trigger, SetCurrentItem`; optional capability interfaces by type-assertion: `OpID()` (`reporter.go:49`), `LivenessToucher` (`reporter.go:56`), `ResultSetter` |
| `type Registry struct` / `Options` | `registry.go:47` / `registry.go:227` | |
| `New(store OpsV2Store, logger, workers, bus) *Registry`, `NewWithOptions` | `registry.go:361,366` | production: 8 workers (`register.go:98` `New(store, slog.Default(), 8, hub)`) |
| `ValidateOpDef(def) error` | `registry.go:747` | the boot contract (ID non-empty, no `:`, Run non-nil, ResumePolicy != 0, Liveness declared, Preview valid, FormerIDs) |
| `(*Registry).RegisterOp(def) error` | `registry.go:816` | the ONLY writer of `r.defs` (comment `registry.go:850-857`); also cycle-checks `Requires`, alias collisions, upserts `opv2:def:` (`registry.go:879`) |
| `EnqueueOp(ctx, defID, params, ...EnqueueOption) (opID string, err)` | `registry.go:920` | options: `WithParent/WithActor/WithActorAuthMethod/WithActorPermissions/WithOwnerApply/WithPriority` (`types.go:467-504`) |
| `Cancel(opID)`, `Discard(opID)`, `RetryInterrupted`, `FinishWithoutResuming` | `registry.go:1367,1728`; `retry.go:70`; `finish.go:73` | |
| `Start(ctx)`, `Shutdown(ctx)` | `registry.go:637,1844` | |
| `SetBus`, `SetActivityRecorder`, `SetPluginMaxConcurrent`, `SetDepsScheduler`, `SetRunContextDecorator`, `SetScanStandDownStore` | `registry.go:592,605,628,456,502,436` | late-bound wiring |
| `type EventHub`, `Bus` | `bus.go:29`; `reporter_db.go:30` | in-process fan-out, 64-buffer per subscriber, drops on slow client (`bus.go` Publish/Subscribe) |
| `RunItems`, `RunItemsOptions` | `run_items.go:150`, `:33` | see section 2 |
| `FromV2(def) ops.Definition`, `Register`, `RegisterBundle`, `(*Registry).Catalog()`, `ValidateCatalog` | `v3_adapter.go:43,76,63,130,147` | the bridge that lets v3 and v2 defs share one registry |
| `RegistryWrapper` (`Start/Stop` for serviceregistry) | `register.go:19,25,31` | registered as service `opregistry` (`register.go:94`) and `ophub` (`register.go:84`), group `scheduler` |
| pause gate (global operator pause) | `pause_gate.go:204,220,268` | `PauseOperations`, `OperationsPauseState`, `RestorePauseState`; marker persisted so a restart stays paused |

### 1.2 `pkg/ops` (v3 authoring SDK; "Operations v3")

Note: despite living under `pkg/`, it imports `internal/auth` (`pkg/ops/definition.go:15`, `Common.Permission auth.Permission` at `:96`) and `internal/database` (`pkg/ops/writer.go`, `Writer` methods take `database.Book`/`database.BookFile`), so it is not usable from outside the module as is.

| Symbol | Anchor |
|---|---|
| `interface Definition` (opaque: `ID()`, `Kind()`, unexported `spec()`) | `definition.go:26` |
| `Kind`: `KindTask/KindBatch/KindFixer/KindPipeline/KindAdapted` | `definition.go:33-49` |
| `type Common struct` (Title, Help, Effects, Exclusive, Priority, Timeout, Permission, Schedule, Resume, Cancel, Notify, FormerIDs, Uses) | `definition.go:70-110` |
| `type Bundle struct{Plugin; Defs []Definition; Requires []ServiceKey}` | `definition.go:138` |
| `Task[P,R](id, TaskSpec[P,R])` | `task.go:29`; `TaskSpec` `task.go:17` |
| `Batch[P,T,R](id, BatchSpec[P,T,R])` | `batch.go:65`; `BatchSpec` `batch.go:19` (Source, Item, Finish, Concurrency, PartitionBy, ChunkSize (default 256, `batch.go:40,51`), ItemTimeout, OnItemError, Label) |
| `Fixer[P,C](id, FixerSpec)` plan -> approve -> apply | `fixer.go:149`, `FixerSpec` `:25`, `Row` `:62` |
| `Pipeline[P](id, PipelineSpec)` DAG of stages | `pipeline.go:77`, `Stage` `:24` |
| `Concurrency`: `CPU()`, `Network(n, why)`, `Sequential(why)`, `FromParam(field, def, max)`, `Workers(params)` | `batch.go:130,138,144,150,176`; **omitted = `CPU()`** (`batch.go:123` "the zero value: an omitted Concurrency is CPU()"), `MaxNetworkWorkers = 64` (`batch.go:135`) |
| `Source[T]`/`SourceOrder`/`DirtySet(prefix, load)` | `batch.go:198,216,257` |
| `Schedule`: `Every(d)`, `DailyAt("HH:MM")`, `Cron(expr)`, `InMaintenanceWindow(order)`, `.OnStart()`, `.When(fn)`, `.Params(p)`, `.Live()`, `.Preview()` | `schedule.go:44,53,62,70,79,82,86,99,108`; scheduled writers must say Live or Preview (D8, `schedule.go:96-98`) |
| `CancelContract`: `AtItemBoundaries()`, `Cooperative(grace)`, `Uninterruptible(reason, budget)` | `contract.go:27,31,37` |
| `ResumePolicy`: `ResumeContinue/FromZero/Drop/Ask` | `contract.go:52-65`; defaults by kind `runtime.go:73` |
| `Effects`: `ReadOnly(...)`, `Writes(...)`, `Deletes`, `NeedsReady` | `effects.go:69,75,103,112` |
| `RC` (run context) `Context, OpID, Mode, Trigger, Actor, Log, Progress(), Phase, Writer(), Checkpoint(v), Restore(v), Enqueue, Await, StandDown, Canceled` | `rc.go:115-249` |
| `Host` interface (executor half) | `rc.go:85`; implemented by `v3Host` `internal/operations/registry/v3_adapter.go:393` |
| `Writer` interface (typed, gated, journaled writes; `ModifyBook`, `ModifyBookFile`, `RepointBookFile`, `ModifyBookAuthors`, `Effect`, `MarkRowApplied`, `RowApplied`) | `writer.go:46`; impl `internal/operations/opswriter/writer.go` |
| `Compile(def) Compiled` (executor view; `RunTask`, `OpenBatch`, `Stages`) | `runtime.go:50` |
| `ValidateCatalog(defs, ledgerIDs)` | `validate.go:55` |
| `Lane`/`LaneMac` (remote audio-decode workers) | `lanes.go:10,15` |
| `pkg/ops/opstest` (fake world, conformance suite) | `pkg/ops/opstest/conformance.go`, `world.go` |

### 1.3 `pkg/plugin/sdk` (v2 facade)

`Plugin{ID,Name,Version,Register(Registry)}` (`plugin.go:19`), `Registry{RegisterOp, EnqueueOp}` (`registration.go:12`), and type **aliases** onto `internal/operations/registry` (`operation.go:11-18`, `reporter.go:12`, `events.go:11-15`, `capability.go:11`, `enqueue_options.go:11`). Helpers: `PageBooks` (`iterate.go:41`, keepalive progress while a blocking DB page is read), `Progress` (`progress.go:26`). `sdk` imports `internal/database` (`iterate.go:25`). Stability promise in `doc.go:~60-80`.

### 1.4 `internal/scheduler`

| Symbol | Anchor |
|---|---|
| `SchedulerDeps` (Store, OpRegistry, HasDedupPlugin, HasMetadataFetchSvc, HasActivitySvc, PollBatches, HasBatchPoller, CollectionsRefreshPending) | `scheduler.go:47` |
| `SchedulerStore` (5-method narrow store) | `scheduler.go:40` |
| `TaskDefinition{Name, Description, Category, TriggerFn, IsEnabled, GetInterval, RunOnStart, RunInMaintenanceWindow, DailyAt}` | `scheduler.go:86` |
| `TaskScheduler`, `NewTaskScheduler(deps)`, `RegisterTask`, `Start(shutdown, wg)`, `RunTask/RunTaskManual/RunTaskWithSource`, `ListTasks`, `WaitForOperation` | `scheduler.go:121,192,259,295,464-477,511,623` |
| durable interval clock (`claimIntervalRun`, key `intervalLastTickKey`) | `interval_clock.go:60,146` (stored in settings, survives restart; replaced in-memory `time.Ticker`, see `scheduler.go:344-358`) |
| daily-at clock | `daily_at.go:63-126` |
| cron evaluator `ParseCron`, `CronSpec.Next/Prev` (used only for native v3 defs) | `cron.go:32,98,145` |
| maintenance window (`RunMaintenanceWindow`, `IsInMaintenanceWindow`, `maintenanceOrder` list) | `maintenance.go:28,291`; order list `scheduler.go:197-246` |
| weekly full sweep | `full_sweep.go:48,76-154` |
| v3 schedule loop (`RegisterSchedules(defs []ops.Definition)`, one goroutine for all Every/DailyAt/Cron entries, state `opv3:sched:{def}`) | `v3_schedule.go:193,622,544` |
| `taskV2DefIDs` task-name -> def-ID map; `TaskV2DefIDs()` copy | `maintenance.go:154` (var), `:252` |
| `ExtraOpsRegistrar` (registers `scheduler.metadata-upgrade`) | `extra_ops.go:43,88` |

### 1.5 `internal/maintenance` (+ `jobs/`)

`type MaintenanceJob interface{ID, Name, Description, Category, DefaultParams, CanResume, Policy() ExecutionPolicy, Run(ctx, JobStore, ProgressReporter, dryRun bool) error}` (`job.go:474-499`); `ExecutionPolicy{ResumePolicy, Timeout, ConcurrencyKey, Liveness, Capabilities, Params}`; `DefaultPolicy()` = drop, 4h, LivenessManual, library read+write; `RestartPolicy()` = DefaultPolicy with ResumeRestart (`job.go:182,221`). Global registry `Register/Get/All` with a mutex; **panics on duplicate ID, no unregister** (`registry.go:19-24`). 34 jobs register themselves from `init()` in `jobs/*.go` (blank-imported at `internal/server/server.go:45`). `jobs` take a narrow `JobStore` (`job.go:442`, composed from `jobBookStore` etc.). `internal/server/maintenance_job_op.go:73,146` maps each job to `OperationDef{ID: "maintenance."+jobID, Plugin:"maintenance", Cancellable:true, ConcurrencyKey: policy key or the def ID, Permissions: settings.manage unless `PermissionAware`}`. Jobs have no Concurrency of their own; a few of them (`backfill-itunes-positions`, `backfill-sync-ids`) call `RunItems` with `BackfillConcurrency()` (`jobs/backfill_itunes_positions.go:257`, `jobs/backfill_sync_ids.go:134`).

### 1.6 `internal/plugins/**`

| Package | Plugin type / registration | Ops |
|---|---|---|
| `plugins/acoustid` | `sdk.Plugin`; `init()` -> `serviceregistry.Register("acoustidplugin", group plugins)` (`register.go:24-25`); op defs registered in `PostInit` -> `p.Register(wrapper.Registry)` | 8 |
| `plugins/dedup` | same pattern (`register.go:31-32`), `OperationDefs()` list `plugin.go:88` | 32 |
| `plugins/deluge` | same (`register.go:23-24`); also blank-imported by the server (`internal/server/plugins_init.go:17`) | 4 |
| `plugins/itunes` | same (`register.go:42-43`); `Register` is a no-op unless the service is enabled (`plugin.go:~93`) | 1 registered + 1 excluded stub |
| `plugins/metafetch` | same (`register.go:29-30`) | 3 |
| `plugins/maintenance` | **not** a registry service (`register.go:6-19`: "Intentionally not registered"); constructed by `maintenanceplugin.New(s)` with the whole `*Server` (`ServerDeps`, `internal/plugins/maintenance/deps.go:1178`) and called from `registerAll` (`internal/server/server.go:1637`) | 104 defs + 24 repairs-lane fixers |
| `plugins/webhook` | uses the OTHER plugin system (`internal/plugin.Register`, `plugin.go:33`), event subscriber, no ops | 0 |
| `plugins/runitems_concurrency_lint_test.go` | type-based lint over `./internal/...` and `./pkg/...` (`:106-128`): every `RunItems` call must carry a provably non-zero `Concurrency` or a `// runitems:sequential <reason>` marker (2 markers exist: `deluge/path_update.go:147`, `acoustid/reset_all.go:226`) | - |

### 1.7 Other job-like packages

| Package | What | Anchor |
|---|---|---|
| `internal/operations` | shared v1-era types: `ProgressReporter` (`progress.go:24`), `OperationFunc` (`:21`), `SelectionSpec/FilterSpec` (`types.go:16,24`), `ResolveBookIDs` (`selection.go:16`), trigger-source ctx (`trigger_source.go:25,31`) | |
| `internal/operations/state` | v2 `State` vocabulary + `Classify/IsTerminal/IsResumable` (`state.go:29,150-183`); v3 `V3` ten-state machine (`v3.go:20-31`), `Next(from, ev, stopReason)` (`v3.go:187`), `FromV2/ToV2` mapping (`v3.go:289,344`) | |
| `internal/operations/childop` | `Follow(ctx, Reader, opID, Options)` parent watches child op, feeds parent's watchdog | `follow.go:101` |
| `internal/operations/opmode` | preview/live resolution: every writer defaults to preview (`dryrun.go:8-36`, `ResolveDryRunDefault` `:66`) | |
| `internal/operations/opswriter` | v3 `Writer` implementation, fence, evidence journal | `writer.go`, `fence.go`, `evidence.go` |
| `internal/operations/freshness` | per-item freshness stamps across runs | `freshness.go:31` |
| `internal/opscatalog` | `All` (empty), `Build(Deps)`, `StaticPluginDefs()`, tombstones | `catalog.go:56,73,107` |
| `internal/opsmetrics` | OTel run metrics; closed label set (`RegisterDefID` `defids.go:24`) | |
| `internal/lifecycle` | `ErrShutdown` sentinel / `IsShutdown(ctx)` (`shutdown.go:33`) | |
| `internal/walkresume` | resume plumbing for 4 boot-enqueued full-library walks (`walkresume.go:5-17`), `ErrYield`, `CheckpointEvery = 10` (`:~43`) | |
| `internal/repairs` | "Fixer" framework (plan/replan/apply) behind ops `repairs.plan` / `repairs.apply`: `Fixer` interface `fixer.go:74`, `Registry` `registry.go`, apply uses `RunItems` (`engine.go:130,715`) | 24 fixers registered at `internal/plugins/maintenance/plugin.go` (`repairsReg.Register`) |
| `internal/ai/aijobs` | durable OpenAI Batch-API job type registry (`Register(typ, cb)` `aijobs.go:89`, `Submit`), polled by `BatchPoller` | 2 types register from `init()` (`ai/author_dedup_job.go:132`, `ai/dedup_review.go:242`) |
| `internal/fingerprint/workerapi` + `server/handlers/fpworker` + `fingerprint/workerclient` | remote-worker lease protocol (the `LaneMac` lane), `/api/v1/fingerprint/worker/*` | `handlers/fpworker/handler.go:7-17` |
| `internal/sweep`, `internal/metabatch`, `internal/work`, `internal/batch` | NOT job systems: sweep = temp/backup cleanup helpers; metabatch = candidate-fetch types; `work` = Work entity service; `batch` = bulk book-actions service | |
| HTTP | v1: `/api/v1/operations/v2[/:id]`, `/operations/events` (SSE), `/operations/timeline`, `/operations/pause|resume`, `/op-defs`, `/tasks`, `/tasks/:name/run`, `/maintenance-window/*` (`internal/server/wire_operations_routes.go:24-123`); v3: `/api/v3/ops/{defs,runs,counts,timeline,events}`, `POST /ops/defs/:id/runs`, `POST /ops/runs/:id/cancel|approve` (`internal/server/handlers/opsv3/handler.go:230-248`) | |

## 2. `registry.RunItems` / `RunItemsOptions` - exact semantics

File `internal/operations/registry/run_items.go`.

```
func RunItems[T any](ctx context.Context, r Reporter, items []T,
    fn func(ctx context.Context, item T) error, opts ...RunItemsOptions) error   // :150
```

* `RunItemsOptions.Concurrency int` (`:35`). Comment says "0 or 1 = sequential (default)". **Clamp**: `if opt.Concurrency < 1 { opt.Concurrency = 1 }` (`:155-157`). So omitting the field (zero value) is sequential. `Concurrency == 1` dispatches to `runItemsSeq` (`:295-297`, `:303`), anything greater to `runItemsPar` (`:298`, `:326`).
* Pool shape: `runItemsPar` uses a semaphore channel of size `Concurrency` (`:330`), spawning one `wg.Go` goroutine per item as a slot frees (`:335-358`); not a fixed worker set.
* `Label func(i, total int) string` (`:54`; doc `:45-53`). Defaults to `"item %d/%d"` using `ProgressOffset+i+1` (`:215-218`). **It is invoked inside the worker goroutine**: `runOne` runs in the `wg.Go` closure (`:348-350`) and calls `lbl(i, progTotal)` at `:251` (only if paused), `:268` (pre-work, `r.SetCurrentItem`) and `:284` (post-work, `r.UpdateProgress`). The post-work call is serialised by `progressMu` (`:236,282-285`) but the pre-work call at `:268` is NOT under any lock and runs concurrently with other workers' `fn` bodies (documented `:48-53`). Any counter the Label closure reads therefore needs the same mutex/atomic as the `fn` body. (Contrast `pkg/ops.BatchSpec.Label func(T) string`, which receives only the item so it "cannot race", `pkg/ops/batch.go:~47`.)
* `PerItemTimeout time.Duration` (`:38`): `context.WithTimeout` per item, created AFTER the pause gate (`:255-260`).
* `ErrMode`: `ErrModeFail` (zero, default; cancels remaining on first error, `:27`) / `ErrModeCollect` (`:29`). Note the doc says "Returns the first item error on ErrModeFail" (`:149`) but parallel mode returns `errors.Join(errs...)` of every error that landed before cancel (`:340-342,362-364`); only sequential mode returns the bare first error (`:316`).
* `CheckpointFn func(ctx) error` (`:63`): sequential-only, called after each successful item (`:318-320`); errors ignored.
* `ResumeFrom int` (`:72`): slices `items[ResumeFrom:]`, shifts `ProgressOffset`, pins `ProgressTotal` (`:168-182`).
* `CheckpointEvery int` (`:76`) and `CheckpointStateFn func(ctx, watermark int) error` (`:93`): concurrent-safe, serialised, receives the contiguous-completion watermark (unbroken completed prefix; failed items never advance it) (`completionTracker` `:114-137`, `maybeCheckpoint` `:197-213`).
* `ProgressOffset`, `ProgressTotal` (`:99,104`); progress reports a monotonic atomic completion count, not the index (`:226-285`).
* Every item also: parks while the global operator pause gate is held (`:250-254`), renews a scan stand-down hold (`:263-267`), and stamps liveness through `UpdateProgress`.
* Cancellation: sequential checks `ctx.Done()` per item (`:306-310`); parallel checks in the dispatcher `select` only (`:337-345`), so after a cancel already-leased items still run `fn` and the dispatcher keeps handing out slots at random odds. Known and worked around per-op (`docs/decisions/2026-10-31-lsh-backfill-cancel-guard.md`: per-item `ctx.Err()` guard).
* CLAUDE.md's note is accurate: the sample exemplar in `internal/plugins/acoustid/backfill.go` now passes `Concurrency: backfillWorkers()` (`backfill.go:206-208`), and the lint test above prevents regressions repo-wide.

Call-site census (non-test): about 170 `RunItems` calls across `internal/` (grep). The lint requires non-zero Concurrency at all of them. Observed values: `runtime.NumCPU()` (dominant, ~45 sites incl. all dedup-plugin ops and the fixers), `titleRepairWorkers()` (6 fixers), fixed constants (4: `batch apply`/`embedding backfill`/`deluge centralize`/`itunes enrich`; 8: `repair-transcribe-status`, `intro-migrate-single-file`, `metadata-cache-reap`, `pcache-sweep`; 24: `missingFileStatConcurrency`), config/param-derived (`backfillWorkers()`, `writeBackWorkers()`, `catalogCoverageWorkers()`, `windowWorkers(params.Concurrency)`), `NumCPU()*4` (`tag_backfill.go:568`), and `1` only where deliberate (`extract_wav_clips.go:204`, `auto_match_transcribed.go:310`, sequential markers).

## 3. How ops are registered

1. **Service registry composition**: each plugin/service package has an `init()` that calls `serviceregistry.Register` (see 07). `Container` builds the `plugins` and `scheduler` groups (`internal/server/server.go:814-822`).
2. **v2 op registration paths (three)**, all ending in `Registry.RegisterOp` (`registry.go:816`):
   * `PostInit` of each plugin service -> `p.Register(wrapper.Registry)` (e.g. `plugins/acoustid/register.go:~38-52`).
   * `registerAll(s, reg)` in `internal/server/server.go:1630-1664`: (1) maintenance plugin; (2) the `opRegistrars` slice - `addOpRegistrar(func(s, reg) error)` called from ~40 `init()`s in `internal/server/*_op*.go` (`internal/server/op_registrars.go:23-30`); (3) native v3 `opscatalog.Build(...)` bundles via `opsregistry.RegisterBundle` (currently none).
   * `maintenance.All()` jobs -> `RegisterMaintenanceJobOps` (`internal/server/maintenance_job_op.go:100`), itself an `addOpRegistrar` entry.
3. **Validation**: boot-time `ValidateOpDef`, cycle/alias checks; `opCatalogGate` + `opscatalog.ValidateCatalog` + `pkg/ops.ValidateCatalog` at startup (`internal/server/op_catalog_gate_test.go`, `internal/opscatalog/validate.go`). A registration error is collected, not fatal, in `NewServer` (`internal/server/server.go:353-357` comment).
4. **Persistence of defs**: `opv2:def:{id}` (`internal/database/pebble_store_ops_v2.go:10`) and v3 `opv3:def:{id}` (`pebble_store_ops_v3.go:25`).

## 4. Persistence (Pebble key families)

Defined in `internal/database/keyfamilies.go:258-281` (registry of key families) and key-schema comments.

| Family | Meaning | Anchor |
|---|---|---|
| `opv2:op:{id}` | v2 run row (`OperationV2Row`) | `pebble_store_ops_v2.go:11` |
| `opv2:q:{999-prio}:{ts}:{id}` | queue index (dispatch order) | `:12` |
| `opv2:act:{id}` | active (queued/running) set | `:13` |
| `opv2:state:{id}` | checkpoint blob (`OpStateV2Row`) | `:14` |
| `opv2:log:{id}:{ts}:{seq}`, `opv2:err:`, `opv2:strike:{def}:...` | per-op log/errors/watchdog strikes | `:15-17` |
| `opv2:open:` / `opv2:done:{completed_ns}:{id}` | timeline index (rebuilt/trusted per boot) | `:18-19` |
| `opv2:wait:{id}` | waiting-deps index | `:20` |
| `opv2:row:{op}:{row}`, `opv2:rowix:` | blob rows owned by an op (repairs plan rows) | `:21-22` |
| `opv2:def:{def}` | definition snapshot | `:10` |
| `op:batch:`, `op:completion:`, `op:deprev:` | batchable buckets, per-subject completion stamps, dep revisions | `:1182-1567` |
| `opv3:run:`, `opv3:prog:` | v3 run record split: cold fields vs progress "hot" fields | `pebble_store_ops_v3.go:9-10,58-63` |
| `opv3:q:`, `opv3:ix:state|def|done|parent:` | queue + indexes + exact counters (`opv3:meta:counts`) | `:11-15,25` |
| `opv3:evt:` (transitions audit), `opv3:ckpt:`, `opv3:snap:` (frozen item snapshot pages), `opv3:ledger:` (chunk ledger), `opv3:intent:` (write-ahead intents), `opv3:item:` (failed/skipped outcomes, capped 10,000/run `:~84`), `opv3:log:`, `opv3:rowmark:`, `opv3:sched:{def}`, `opv3:def:` | v3 batch/fixer/task state | `:16-25` |
| `opstate:{op}` / `operation:` / `operationlog:` / `opsummary:` / `op_result:` / `opchange:` | v1 legacy (still live: `opstate:<opID>:params` is a chunk journal for `backfill-file-hashes`) | `keyfamilies.go:262-269` |
| settings keys | interval clocks (`intervalLastTickKey`), scan stand-down marker, pause marker, full-sweep stamp, last maintenance run | `scheduler/interval_clock.go:60`, `scheduler/full_sweep.go:76`, `registry/pause_gate.go:268` |

**Dual-write bridge**: every v2 write stages the v3 projection in the SAME Pebble batch under `opsMu`, and native v3 writes stage the mirrored `opv2:` row, so the previous binary (v2-only reader) can be redeployed (`pebble_store_ops_v3.go:28-45`, owner decision D25: mirror stays until 05-PR16). Store interfaces: `OpsV2Store` (`iface_ops_v2.go:410`, 8 sub-interfaces) and `OpsV3Store` (`iface_ops_v3.go:488`: RunWriter/RunReader/CheckpointStore/ChunkStore/AuxStore/SchedStore). `OpsV3Store` is deliberately not part of `database.Store`; consumers resolve it as a capability (`registry.go:337-346`).

State machine: v2 statuses (`queued, running, completed, failed, canceled, interrupted_quiesced|dropped|ask, waiting_deps ...`) in `internal/operations/state/state.go`; v3 ten states `queued/running/stopping/interrupted/awaiting_decision/succeeded/failed/canceled/timed_out/dropped` with a closed event table and compare-and-set `TransitionRunV3(req)` on (State, Attempt, FenceEpoch) (`internal/database/iface_ops_v3.go:400-405`, `state/v3.go:20-31,187`).

## 5. Lifecycle: dispatch, resume, cancel, report, schedule

**Start** (`registry.go:637-745`): restore operator-pause marker -> `resumeAfterStartup` -> `batchReloadOnStart` -> rebuild deps index -> goroutines: `runDispatcher` (100ms ticker + wake channel, `dispatcher.go:20-35`), `runWatchdog` (30s, `watchdog.go:47`), N workers (`startWorker` `worker.go:279`, each `executeRun` `worker.go:303`), optional deps sweep ticker (5m).

**Dispatch gates** (`dispatchCycle`, `dispatcher.go:38-`): Gate 0 already claimed; 1 def registered; 1b warm-up hold (`warmup_gate.go`); 2 per-plugin `max_concurrent` (`registry.go:628`); 2b abandoned-goroutine cap (default 4/plugin, `registry.go:235`); 3 `ConcurrencyKey` busy (zombie counts); 3.5 scan stand-down; 3b declared `Writes` conflict; 4 `DependsOn`. A refused candidate stays queued.

**Resume after restart** (`resume.go:35 resumeAfterStartup`): candidates from `ListResumableOperationsV2` (not the active index, so `interrupted_quiesced` rows are seen); `bootSweepV3` normalises v3 rows (`:495`); a scan stand-down marker defers the library scan resume (`:44-63`). Per def ResumePolicy: `Restart` -> `resumeRestart` (`:228`, increments resume_count, re-dispatches with last `Checkpoint` merged back into params via `mergeJSONParams` `:452`); `Requeue` -> `resumeRequeue` (`:630`, retire old row, insert fresh queued row, `retireForRequeue` `:608`); `Drop` -> `interrupted_dropped` (`:690`); `Ask` -> `interrupted_ask` awaiting a person (`:702`, resolved via `RetryInterrupted` `retry.go:70` or `FinishWithoutResuming` `finish.go:73`); unknown def or `ResumeUnspecified` -> drop. Infinite-restart guard `checkInfiniteRestart` (`worker.go:1412`). Long walks can `return ErrContinueInPlace` to be re-queued under the SAME op id with a fresh timeout (`continue_in_place.go:66`). Static guard: every `ResumeRestart` def must reference `Checkpoint` (`internal/arch/resume_restart_guard_test.go`, allowlist of 21 per `docs/decisions/2026-10-30-resume-restart-checkpoint-guard.md`). v3 resume uses the chunk ledger + frozen snapshot (`chunk_ledger.go`, `opv3:ledger:`/`opv3:snap:`), `ResumeContinue` re-leases incomplete chunks.

**Cancel** (`registry.go:1367`): lock-free CAS queued->canceled; else live handle context cancel (cause), else row-level CAS (`cancelByRow` `:1491`, up to 4 attempts, `:1454`); zombies (goroutine outlived cancel) have their row cancelled and fence revoked (`worker.go:43-49`). HTTP: `DELETE /operations/v2/:id`, `POST /api/v3/ops/runs/:id/cancel`. Abandon grace 5s (`worker.go:62`), per-plugin abandoned cap 4.

**Shutdown** (`registry.go:1844`): set `shuttingDown`, stop batch timers, release claimed-but-unstarted rows to queued, cancel every handle with cause `lifecycle.ErrShutdown` so `ResumeRestart` ops leave external work running, wait for drain (or ctx expiry), stamp interrupted statuses, stop owned goroutines before the caller closes Pebble.

**Progress / events**: `Reporter.UpdateProgress` writes `OperationV2Row` progress (throttled; shape in `reporter_db.go`) and publishes `op.updated` (`reporter_db.go:464`); `Log` -> `op.log` (`:603`) + durable `opv2:log:`; `SetCurrentItem` is memory-only -> `op.current_item` (`:776`); registry publishes `op.created` (`registry.go:1287`), `op.terminal` (`:1332`), and the dispatcher `op.updated` (`dispatcher.go:549`). Events go through the `Bus` -> `EventHub` -> SSE `/api/v1/operations/events`; they are lossy by design (drop on full 64-buffer). Activity-log mirror via `ActivityRecorder` (`reporter_db.go:36`, `activity_mirror.go`). Metrics via `opsmetrics`. Watchdog strikes (`stuck`, `never_reported`, `uncheckpointed`) in `opv2:strike:`.

**Scheduling**: three mechanisms, none of which reads `OperationDef.Schedule`:
1. `TaskDefinition` ticks (`scheduler.go:295-460`): interval via durable clock, `DailyAt`, `RunOnStart`, maintenance window membership. Config in `scheduled.*` / `maintenance.*` (`internal/config/config.go:2886-3030`).
2. Nightly maintenance window: 60s checker (`scheduler.go:440-455`), runs `maintenanceOrder` tasks in sequence via op `scheduler.maintenance-window` (`internal/server/scheduler_maintenance_window_op.go`), once per day (`hasRunToday` `maintenance.go:126`).
3. Native `ops.Schedule` (v3): one goroutine for all defs (`v3_schedule.go:622`); `RegisterSchedules(s.opRegistry.Catalog())` is called at `internal/server/server_lifecycle.go:399`; adapted v2 defs whose cron nothing drives are shown as "disabled" (`v3_schedule.go:265`). Today it effectively schedules nothing (no native defs).

## 6. Table of every registered op / job / scheduled task

Method and caveats (read before using the table):
* Rows 1-200 are every `OperationDef` struct literal with a resolvable `ID` found by a static extraction over non-test Go files (cross-checked against each plugin's `Register`/`OperationDefs()` list: acoustid 8, dedup 32, deluge 4, metafetch 3, maintenance 104, itunes 1+1 excluded). Server ops (47 literals in `internal/server`), `scheduler.metadata-upgrade`, and `repairs.plan/apply` are included. Rows 201-234 are the 34 `maintenance.MaintenanceJob`s.
* Total = 234 rows (233 registered + 1 excluded stub `itunes.import` in `plugins/itunes`, because `internal/server/itunes_ops.go` registers the live `itunes.import`). No dynamically generated defs were found, but a runtime registry dump was not run (UNKNOWN: whether some conditionally-registered defs, e.g. plugins that return a nil plugin when deps are missing, are absent in a given deployment).
* "Owner" = file:line of the def literal (package = directory). "Trigger": `task X` = a `TaskDefinition` enqueues it (interval/daily/onstart/window index from `internal/scheduler/testdata/cadence.golden`, which is rendered with the test-default config, so enabled-by-default is NOT shown; see 6.1); `manual/internal` = API-triggered (`POST /api/v1/operations/v2`, `POST /api/v3/ops/defs/:id/runs`) or enqueued by another op/handler (not exhaustively traced); `v2 cron ... (declarative only)` = the def carries a cron string that nothing evaluates.
* "Resumable?": `restart (checkpoint)` = resumes from the saved checkpoint; `requeue` = re-run from zero; `drop` = lost on restart; `ask` = waits for a person.
* "Concurrency": union of `Concurrency:` expressions found **in the same Go file** as the def literal (heuristic: the `RunItems` call is sometimes in another file, then shown as `-`; `-` also covers ops with no per-item loop). `NumCPU` = `runtime.NumCPU()`.
* "Generic vs audiobook" is my judgement of whether the op would apply to a Sonarr/Radarr-style app unchanged: `generic` (logs, activity, DB, backups, temp files, tombstones, reports, journals, op framework), `integration` (download client / iTunes), `audiobook` (touches books, authors, series, narrators, fingerprints, metadata, library files). Some `audiobook` items are generic in mechanism (index rebuilds, path repoints) but bound to audiobook entities.
* Liveness column: `RunItems` / `Manual` / `None`.

| # | Def ID | Owner (def literal) | Trigger | Resume | Liveness | Concurrency | Class |
|---|---|---|---|---|---|---|---|
| 1 | `acoustid.backfill` | `internal/plugins/acoustid/backfill.go:269` | task `acoustid_backfill` | restart (checkpoint) | RunItems | backfillWorkers() | audiobook |
| 2 | `acoustid.fingerprint-duration-repair` | `internal/plugins/acoustid/duration_backfill.go:31` | manual/internal | drop | RunItems | - | audiobook |
| 3 | `acoustid.fingerprint-rescan` | `internal/plugins/acoustid/fingerprint_rescan.go:51` | manual/internal | drop | Manual | - | audiobook |
| 4 | `acoustid.lookup-online` | `internal/plugins/acoustid/online_lookup.go:50` | task `acoustid_online_lookup` | requeue (from zero) | Manual | - | audiobook |
| 5 | `acoustid.lsh-backfill` | `internal/plugins/acoustid/lsh_backfill.go:59` | manual/internal | drop | RunItems | lshBackfillConcurrency() | audiobook |
| 6 | `acoustid.reset-all` | `internal/plugins/acoustid/reset_all.go:34` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 7 | `acoustid.scan` | `internal/plugins/acoustid/scan.go:19` | manual/internal | drop | Manual | - | audiobook |
| 8 | `acoustid.window-backfill` | `internal/plugins/acoustid/window_backfill.go:275` | manual/internal | restart (checkpoint) | RunItems | windowPlanWorkers, workers | audiobook |
| 9 | `ai.author-merge-apply` | `internal/server/ai_ops.go:86` | manual/internal | drop | Manual | - | audiobook |
| 10 | `ai.author-review` | `internal/server/ai_ops.go:35` | manual/internal | drop | Manual | - | audiobook |
| 11 | `ai.author-scan` | `internal/server/aiscan_op.go:37` | manual/internal | restart (checkpoint) | Manual | - | audiobook |
| 12 | `ai.author-scan-apply` | `internal/server/ai_scan_apply_op.go:290` | manual/internal | drop | Manual | - | audiobook |
| 13 | `catalog.coverage-report` | `internal/server/catalog_coverage_op.go:56` | manual/internal | restart (checkpoint) | RunItems | catalogCoverageWorkers() | audiobook |
| 14 | `catalog.harvest-authors` | `internal/server/catalog_harvest_op.go:81` | task `catalog_harvest` | restart (checkpoint) | RunItems | p.Concurrency | audiobook |
| 15 | `collections.refresh-relative-dynamic` | `internal/server/collection_refresh_op.go:55` | task `collections_refresh` every 1h0m0s | restart (checkpoint) | Manual | - | audiobook |
| 16 | `dedup.author-scan` | `internal/server/duplicates_ops.go:673` | task `dedup_refresh` | drop | Manual | - | audiobook |
| 17 | `dedup.auto-resolve` | `internal/plugins/dedup/auto_resolve.go:34` | manual/internal | drop | Manual | - | audiobook |
| 18 | `dedup.book-combine` | `internal/server/duplicates_ops.go:290` | manual/internal | drop | Manual | - | audiobook |
| 19 | `dedup.book-merge` | `internal/server/duplicates_ops.go:134` | manual/internal | drop | Manual | - | audiobook |
| 20 | `dedup.book-scan` | `internal/server/duplicates_ops.go:56` | manual/internal | drop | Manual | - | audiobook |
| 21 | `dedup.book-signature-scan` | `internal/plugins/dedup/book_signature_scan.go:19` | manual/internal | requeue (from zero) | RunItems | - | audiobook |
| 22 | `dedup.bookfile-seg-drop` | `internal/plugins/dedup/bookfile_seg_sweep.go:78` | manual/internal | drop | Manual | - | audiobook |
| 23 | `dedup.breakdown-backfill` | `internal/plugins/dedup/breakdown_backfill.go:243` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 24 | `dedup.build-candidate-status-index` | `internal/plugins/dedup/build_candidate_index.go:56` | manual/internal | requeue (from zero) | RunItems | NumCPU | audiobook |
| 25 | `dedup.build-isbn-index` | `internal/plugins/dedup/build_isbn_index.go:46` | manual/internal | drop | Manual | - | audiobook |
| 26 | `dedup.calibrate-composite` | `internal/plugins/dedup/calibrate_composite.go:153` | manual/internal | drop | Manual | - | audiobook |
| 27 | `dedup.calibrate-embedding-thresholds` | `internal/plugins/dedup/calibrate_embedding_thresholds.go:111` | manual/internal | drop | Manual | - | audiobook |
| 28 | `dedup.check-book` | `internal/plugins/dedup/check_book.go:50` | manual/internal | requeue (from zero) | Manual | - | audiobook |
| 29 | `dedup.cleanup-orphan-author-embeddings` | `internal/plugins/dedup/cleanup_orphan_author_embeddings.go:94` | manual/internal | drop | Manual | - | audiobook |
| 30 | `dedup.cleanup-orphan-embeddings` | `internal/plugins/dedup/cleanup_orphan_embeddings.go:85` | manual/internal | drop | Manual | NumCPU | audiobook |
| 31 | `dedup.dataset-backfill` | `internal/plugins/dedup/dataset_backfill.go:50` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 32 | `dedup.drain-stale` | `internal/plugins/dedup/drain_stale.go:67` | manual/internal | drop | Manual | - | audiobook |
| 33 | `dedup.emb-reencode` | `internal/plugins/dedup/emb_reencode.go:63` | manual/internal | drop | Manual | - | audiobook |
| 34 | `dedup.embed-async` | `internal/plugins/dedup/embed_async.go:26` | manual/internal | requeue (from zero) | RunItems | - | audiobook |
| 35 | `dedup.embed-scan` | `internal/plugins/dedup/embed_scan.go:54` | manual/internal | requeue (from zero) | RunItems | workers | audiobook |
| 36 | `dedup.full-scan` | `internal/plugins/dedup/full_scan.go:66` | manual/internal | requeue (from zero) | RunItems | - | audiobook |
| 37 | `dedup.label-refinement` | `internal/plugins/dedup/label_refinement.go:78` | task `label_refinement` | drop | Manual | - | audiobook |
| 38 | `dedup.llm-review` | `internal/plugins/dedup/llm_review.go:22` | task `dedup_llm_review` window#2 | drop | Manual | - | audiobook |
| 39 | `dedup.lsh-index-build` | `internal/plugins/dedup/lsh_index_build.go:70` | manual/internal | requeue (from zero) | RunItems | NumCPU | audiobook |
| 40 | `dedup.mine-gold-labels` | `internal/plugins/dedup/mine_gold_labels.go:44` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 41 | `dedup.purge-legacy-fp-candidates` | `internal/plugins/dedup/purge_legacy_fp.go:70` | manual/internal | drop | Manual | - | audiobook |
| 42 | `dedup.purge-stale` | `internal/plugins/dedup/purge_stale.go:30` | manual/internal | drop | Manual | - | audiobook |
| 43 | `dedup.quarantine-chapter-artifacts` | `internal/plugins/dedup/quarantine_chapter_artifacts.go:70` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 44 | `dedup.rebuild-gold-labels` | `internal/plugins/dedup/rebuild_gold_labels.go:96` | manual/internal | drop | Manual | - | audiobook |
| 45 | `dedup.reembed-embeddings` | `internal/plugins/dedup/reembed_embeddings.go:87` | manual/internal | requeue (from zero) | Manual | NumCPU | audiobook |
| 46 | `dedup.rescore` | `internal/plugins/dedup/rescore_op.go:112` | manual/internal | requeue (from zero) | Manual | - | audiobook |
| 47 | `dedup.rescore-labeled-examples` | `internal/plugins/dedup/rescore_labeled_examples.go:85` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 48 | `dedup.run-all` | `internal/plugins/dedup/run_all.go:238` | manual/internal | restart (checkpoint) | Manual | - | audiobook |
| 49 | `dedup.series-dedup` | `internal/server/duplicates_ops.go:878` | manual/internal | drop | Manual | - | audiobook |
| 50 | `dedup.series-merge` | `internal/server/duplicates_ops.go:1108` | manual/internal | drop | Manual | - | audiobook |
| 51 | `dedup.series-normalize` | `internal/server/duplicates_ops.go:1241` | task `series_normalize` | drop | Manual | - | audiobook |
| 52 | `dedup.series-prune` | `internal/server/duplicates_ops.go:1030` | task `series_prune` | drop | Manual | - | audiobook |
| 53 | `dedup.series-scan` | `internal/server/duplicates_ops.go:783` | manual/internal | drop | Manual | - | audiobook |
| 54 | `dedup.split-book-bulk-merge` | `internal/plugins/dedup/split_book_bulk_merge.go:23` | manual/internal | ask | RunItems | - | audiobook |
| 55 | `dedup.split-book-scan` | `internal/plugins/dedup/split_book_scan.go:24` | manual/internal | requeue (from zero) | Manual | - | audiobook |
| 56 | `dedup.window-index-build` | `internal/plugins/dedup/window_index_build.go:86` | manual/internal | restart (checkpoint) | RunItems | windowIndexBuildConcurrency() | audiobook |
| 57 | `deluge.centralize` | `internal/plugins/deluge/centralization.go:29` | manual/internal | restart (checkpoint) | RunItems | centralizeConcurrency | integration (download client) |
| 58 | `deluge.link-backfill` | `internal/plugins/deluge/link_backfill.go:129` | manual/internal | restart (checkpoint) | RunItems | linkWorkers(workers) | integration (download client) |
| 59 | `deluge.path-update` | `internal/plugins/deluge/path_update.go:24` | manual/internal ; event-subscribed | drop | RunItems | - | integration (download client) |
| 60 | `deluge.protected-paths-sync` | `internal/plugins/deluge/protected_paths.go:17` | manual/internal v2 cron `new("*/30 * * * *")` (declarative only) | requeue (from zero) | Manual | - | integration (download client) |
| 61 | `diagnostics.ExportOpID` | `internal/server/diagnostics_ops.go:32` | manual/internal | drop | Manual | - | audiobook |
| 62 | `diagnostics.ai-analyze` | `internal/server/diagnostics_ai_ops.go:47` | manual/internal | drop | Manual | - | audiobook |
| 63 | `entities.author-merge` | `internal/server/entities_ops.go:48` | manual/internal | drop | Manual | - | audiobook |
| 64 | `entities.author-reclassify` | `internal/server/author_reclassify_op.go:35` | manual/internal | drop | Manual | - | audiobook |
| 65 | `entities.resolve-production-author` | `internal/server/entities_ops.go:524` | manual/internal | drop | Manual | - | audiobook |
| 66 | `entities.series-rename` | `internal/server/series_rename_ops.go:254` | manual/internal | drop | Manual | - | audiobook |
| 67 | `identification.advance` | `internal/server/identification_advance_op.go:72` | task `identification_advance` every 5m0s | restart (checkpoint) | RunItems | adv.Concurrency | audiobook |
| 68 | `itunes.clone-into-library` | `internal/plugins/maintenance/itunes_clone_into_library.go:182` | manual/internal | drop | Manual | icWorkers | integration (audiobook-specific) |
| 69 | `itunes.heal` | `internal/plugins/maintenance/reconcile.go:22` | manual/internal | restart (checkpoint) | RunItems | - | integration (audiobook-specific) |
| 70 | `itunes.import` | `internal/server/itunes_ops.go:42` | manual/internal | drop | Manual | - | integration (audiobook-specific) |
| 71 | `itunes.import` (EXCLUDED stub, not registered) | `internal/plugins/itunes/import.go:18` | manual/internal | restart (checkpoint) | Manual | - | integration (audiobook-specific) |
| 72 | `itunes.playlist-import` | `internal/plugins/maintenance/itunes_playlist_import.go:83` | manual/internal | drop | Manual | - | integration (audiobook-specific) |
| 73 | `itunes.position-sync` | `internal/plugins/itunes/position_sync.go:17` | manual/internal | requeue (from zero) | Manual | - | integration (audiobook-specific) |
| 74 | `itunes.regroup` | `internal/plugins/maintenance/itunes_regroup.go:73` | manual/internal | drop | Manual | - | integration (audiobook-specific) |
| 75 | `library.ai-parse` | `internal/server/library_ai_parse_op.go:42` | manual/internal | drop | Manual | - | audiobook |
| 76 | `library.bulk-metadata-fetch` | `internal/server/metadata_ops.go:735` | manual/internal | restart (checkpoint) | Manual | - | audiobook |
| 77 | `library.bulk-write-back` | `internal/server/library_writeback_op.go:71` | manual/internal | restart (checkpoint) | Manual | - | audiobook |
| 78 | `library.folder-auto-scan` | `internal/server/folder_autoscan_op.go:42` | manual/internal | drop | Manual | - | audiobook |
| 79 | `library.import` | `internal/server/library_core_ops.go:241` | manual/internal | drop | Manual | - | audiobook |
| 80 | `library.organize` | `internal/server/library_core_ops.go:298` | task `library_organize` | drop | Manual | - | audiobook |
| 81 | `library.purge-soft-deleted` | `internal/server/purge_soft_deleted_op.go:62` | manual/internal | restart (checkpoint) | Manual | - | audiobook |
| 82 | `library.scan` | `internal/server/library_core_ops.go:52` | task `library_scan`; task `library_scan_full` | restart (checkpoint) | Manual | - | audiobook |
| 83 | `library.size-refresh` | `internal/server/library_size_refresh_op.go:31` | task `library_size_refresh` | drop | Manual | - | audiobook |
| 84 | `library.transcode` | `internal/server/library_core_ops.go:375` | manual/internal | drop | Manual | - | audiobook |
| 85 | `maintenance.activity-filter-index-backfill` | `internal/plugins/maintenance/activity_filter_index_backfill.go:122` | manual/internal | restart (checkpoint) | Manual | workers | generic |
| 86 | `maintenance.ai-dedup-batch` | `internal/plugins/maintenance/dedup_ops.go:35` | task `ai_dedup_batch` every 24h0m0s v2 cron `0 0 * * *` (declarative only) | restart (checkpoint) | Manual | - | audiobook |
| 87 | `maintenance.author-conjunction-repair` | `internal/plugins/maintenance/author_conjunction_repair.go:108` | manual/internal | restart (checkpoint) | Manual | - | audiobook |
| 88 | `maintenance.author-duplicate-merge` | `internal/plugins/maintenance/author_duplicate_merge.go:92` | manual/internal | restart (checkpoint) | Manual | - | audiobook |
| 89 | `maintenance.author-id-repair` | `internal/plugins/maintenance/author_id_repair.go:146` | manual/internal | restart (checkpoint) | Manual | NumCPU | audiobook |
| 90 | `maintenance.author-path-link` | `internal/plugins/maintenance/author_path_link.go:365` | manual/internal | restart (checkpoint) | Manual | NumCPU | audiobook |
| 91 | `maintenance.author-split-scan` | `internal/plugins/maintenance/author.go:91` | task `author_split_scan` v2 cron `0 2 * * 1` (declarative only) | requeue (from zero) | Manual | authorSplitScanWorkers, resolveProductionAuthorsWorkers() | audiobook |
| 92 | `maintenance.author-strip-merge` | `internal/plugins/maintenance/author_strip_merge.go:403` | manual/internal | drop | Manual | - | audiobook |
| 93 | `maintenance.author-title-fragment-scan` | `internal/plugins/maintenance/author_title_fragment_report.go:156` | manual/internal | drop | Manual | - | audiobook |
| 94 | `maintenance.author-whitespace-collision-report` | `internal/plugins/maintenance/author_whitespace_collision_report.go:167` | manual/internal | drop | Manual | - | audiobook |
| 95 | `maintenance.authority-build` | `internal/plugins/maintenance/authority_build.go:139` | manual/internal | drop | RunItems | workers | audiobook |
| 96 | `maintenance.auto-match-transcribed` | `internal/plugins/maintenance/auto_match_transcribed.go:47` | manual/internal | restart (checkpoint) | RunItems | 1 | audiobook |
| 97 | `maintenance.book-atpath-index-backfill` | `internal/plugins/maintenance/book_atpath_index.go:101` | manual/internal | drop | Manual | - | audiobook |
| 98 | `maintenance.book-atpath-index-verify` | `internal/plugins/maintenance/book_atpath_index.go:78` | manual/internal | drop | RunItems | - | audiobook |
| 99 | `maintenance.book-hash-index-repair` | `internal/plugins/maintenance/book_hash_index_repair.go:91` | manual/internal | restart (checkpoint) | Manual | - | audiobook |
| 100 | `maintenance.book-shape-report` | `internal/plugins/maintenance/book_shape_report.go:344` | manual/internal | drop | RunItems | bookShapeConcurrency(params.Concurrency) | audiobook |
| 101 | `maintenance.bookfile-acoustid-index-repair` | `internal/plugins/maintenance/bookfile_acoustid_index_repair.go:88` | manual/internal | restart (checkpoint) | RunItems | NumCPU | audiobook |
| 102 | `maintenance.booksig-recovery-audit` | `internal/plugins/maintenance/booksig_recovery_audit.go:73` | manual/internal | drop | Manual | - | audiobook |
| 103 | `maintenance.booksig-sidecar-migrate` | `internal/plugins/maintenance/booksig_sidecar_migrate.go:76` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 104 | `maintenance.build-folder-book-files` | `internal/plugins/maintenance/build_folder_book_files.go:187` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 105 | `maintenance.bulk-write-back` | `internal/plugins/maintenance/write_back.go:31` | manual/internal | ask | Manual | - | audiobook |
| 106 | `maintenance.chapters-backfill` | `internal/plugins/maintenance/chapters_backfill.go:233` | manual/internal | restart (checkpoint) | RunItems | NumCPU | audiobook |
| 107 | `maintenance.cleanup-activity-log` | `internal/plugins/maintenance/cleanup.go:255` | task `cleanup_activity_log` every 24h0m0s window#10 v2 cron `0 0 * * *` (declarative only) | drop | Manual | - | generic |
| 108 | `maintenance.cleanup-old-backups` | `internal/plugins/maintenance/cleanup.go:422` | task `cleanup_old_backups` v2 cron `0 5 * * *` (declarative only) | requeue (from zero) | None | - | generic |
| 109 | `maintenance.clear-apply-rename-failures` | `internal/plugins/maintenance/clear_apply_rename_failures.go:69` | manual/internal | drop | None | - | audiobook |
| 110 | `maintenance.compact-activity-log` | `internal/plugins/maintenance/compact_activity_log.go:92` | manual/internal | drop | Manual | - | generic |
| 111 | `maintenance.cover-text-read` | `internal/plugins/maintenance/cover_ops.go:251` | manual/internal | drop | RunItems | clampWorkers(params.Workers, NumCPU*2), workers | audiobook |
| 112 | `maintenance.credit-census` | `internal/plugins/maintenance/credit_census.go:618` | manual/internal | drop | RunItems | concurrency | audiobook |
| 113 | `maintenance.credits-backfill` | `internal/plugins/maintenance/credits_backfill.go:727` | manual/internal | restart (checkpoint) | RunItems | workers | audiobook |
| 114 | `maintenance.db-census-exact` | `internal/plugins/maintenance/db_census_exact.go:45` | manual/internal | restart (checkpoint) | Manual | - | generic |
| 115 | `maintenance.db-optimize` | `internal/plugins/maintenance/db.go:31` | task `db_optimize` v2 cron `0 2 * * 0` (declarative only) | drop | Manual | - | generic |
| 116 | `maintenance.dedup-exact-triage` | `internal/plugins/maintenance/dedup_triage.go:327` | manual/internal | drop | Manual | - | audiobook |
| 117 | `maintenance.dedupe-book-file-rows` | `internal/plugins/maintenance/dedupe_book_file_rows.go:101` | manual/internal | restart (checkpoint) | RunItems | workers | audiobook |
| 118 | `maintenance.duration-backfill` | `internal/plugins/maintenance/duration_backfill.go:259` | manual/internal | drop | Manual | - | audiobook |
| 119 | `maintenance.external-id-backfill` | `internal/plugins/maintenance/backfill.go:169` | manual/internal | restart (checkpoint) | Manual | workers | audiobook |
| 120 | `maintenance.extract-wav-clips` | `internal/plugins/maintenance/extract_wav_clips.go:32` | manual/internal | restart (checkpoint) | RunItems | 1 | audiobook |
| 121 | `maintenance.file-integrity-check` | `internal/plugins/maintenance/integrity_check.go:26` | task `file_integrity_check` window#5 v2 cron `30 2 * * *` (declarative only) | drop | Manual | - | audiobook |
| 122 | `maintenance.file-provenance-capture` | `internal/plugins/maintenance/file_provenance_capture.go:94` | manual/internal | drop | RunItems | - | audiobook |
| 123 | `maintenance.file-provenance-export` | `internal/plugins/maintenance/file_provenance_export.go:119` | manual/internal | drop | RunItems | - | audiobook |
| 124 | `maintenance.filepath-collision-report` | `internal/plugins/maintenance/filepath_collision_report.go:116` | manual/internal | drop | RunItems | filePathCollisionShardCount() | audiobook |
| 125 | `maintenance.folder-cover-backfill` | `internal/plugins/maintenance/cover_ops.go:124` | manual/internal | drop | RunItems | clampWorkers(params.Workers, NumCPU*2), workers | audiobook |
| 126 | `maintenance.fs-regroup-xml` | `internal/plugins/maintenance/fs_regroup_xml.go:182` | manual/internal | drop | Manual | NumCPU | audiobook |
| 127 | `maintenance.ident-index-rebuild` | `internal/plugins/maintenance/ident_index.go:49` | manual/internal | drop | Manual | - | audiobook |
| 128 | `maintenance.intro-migrate-single-file` | `internal/plugins/maintenance/intro_migrate_single_file.go:96` | manual/internal | restart (checkpoint) | RunItems | 8 | audiobook |
| 129 | `maintenance.library-optimize` | `internal/plugins/maintenance/optimize.go:25` | manual/internal | drop | Manual | - | audiobook |
| 130 | `maintenance.malformed-m4b-remux` | `internal/plugins/maintenance/backfill.go:255` | manual/internal | restart (checkpoint) | Manual | workers | audiobook |
| 131 | `maintenance.malformed-m4b-transcode` | `internal/plugins/maintenance/backfill.go:303` | manual/internal | ask | Manual | workers | audiobook |
| 132 | `maintenance.mark-missing-files` | `internal/plugins/maintenance/mark_missing_files.go:167` | manual/internal | drop | RunItems | missingFileStatConcurrency | audiobook |
| 133 | `maintenance.merge-same-path-dupes` | `internal/plugins/maintenance/merge_same_path_dupes.go:159` | manual/internal | drop | RunItems | missingFileStatConcurrency | audiobook |
| 134 | `maintenance.metadata-cache-reap` | `internal/plugins/maintenance/metadata_cache_reap.go:154` | manual/internal | drop | RunItems | metadataCacheReapConcurrency | audiobook |
| 135 | `maintenance.metadata-refresh` | `internal/plugins/maintenance/metadata.go:32` | task `metadata_refresh` v2 cron `0 6 * * *` (declarative only) | requeue (from zero) | Manual | - | audiobook |
| 136 | `maintenance.missing-file-audit` | `internal/plugins/maintenance/missing_file_audit.go:277` | manual/internal | drop | RunItems | missingFileStatConcurrency, the same bounded pool the sweep uses. Every item is one os.Stat | audiobook |
| 137 | `maintenance.missing-file-repair` | `internal/plugins/maintenance/missing_file_repair.go:89` | manual/internal | drop | RunItems | missingFileStatConcurrency | audiobook |
| 138 | `maintenance.missing-file-repoint` | `internal/plugins/maintenance/missing_file_repoint.go:151` | manual/internal | drop | RunItems | missingFileStatConcurrency | audiobook |
| 139 | `maintenance.move-book-file-rows` | `internal/plugins/maintenance/move_book_file_rows.go:165` | manual/internal | drop | Manual | - | audiobook |
| 140 | `maintenance.movement-atom-cleanup` | `internal/plugins/maintenance/backfill.go:215` | manual/internal | restart (checkpoint) | Manual | workers | audiobook |
| 141 | `maintenance.nightly-compact-activity-log` | `internal/plugins/maintenance/nightly_compact_activity_log.go:50` | task `nightly_activity_compaction` daily@00:10 v2 cron `10 0 * * *` (declarative only) | drop | Manual | - | generic |
| 142 | `maintenance.opchange-book-index-rebuild` | `internal/plugins/maintenance/opchange_book_index.go:120` | manual/internal | restart (checkpoint) | Manual | every mode is this one def, so its ConcurrencyKey serializes a | generic |
| 143 | `maintenance.orphan-book-files-cleanup` | `internal/plugins/maintenance/orphan_book_files.go:39` | task `orphan_book_files_cleanup` window#6 v2 cron `15 2 * * *` (declarative only) | drop | Manual | - | audiobook |
| 144 | `maintenance.orphan-book-files-repoint-plan` | `internal/plugins/maintenance/orphan_book_files_repoint_plan.go:127` | manual/internal | drop | Manual | - | audiobook |
| 145 | `maintenance.probe-directory-books` | `internal/plugins/maintenance/probe_directory_books.go:108` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 146 | `maintenance.prune-ai-journal` | `internal/plugins/maintenance/ai_journal_prune.go:63` | task `ai_journal_prune` every 24h0m0s window#11 | drop | None | - | generic |
| 147 | `maintenance.purge-deleted` | `internal/plugins/maintenance/cleanup.go:42` | task `purge_deleted` v2 cron `0 3 * * *` (declarative only) | drop | Manual | - | generic |
| 148 | `maintenance.purge-empty-authors` | `internal/plugins/maintenance/author_purge_empty.go:163` | manual/internal | drop | Manual | - | audiobook |
| 149 | `maintenance.purge-empty-narrators` | `internal/plugins/maintenance/narrator_purge_empty.go:147` | manual/internal | drop | Manual | - | audiobook |
| 150 | `maintenance.purge-old-logs` | `internal/plugins/maintenance/cleanup.go:329` | task `purge_old_logs` every 168h0m0s v2 cron `0 2 * * 0` (declarative only) | drop | Manual | - | generic |
| 151 | `maintenance.recompact-activity-digests` | `internal/plugins/maintenance/recompact_activity_digests.go:57` | manual/internal | drop | None | - | generic |
| 152 | `maintenance.recover-missing-files` | `internal/plugins/maintenance/recover_missing_files.go:224` | manual/internal | drop | RunItems | missingFileStatConcurrency | audiobook |
| 153 | `maintenance.regroup-shattered-ai` | `internal/plugins/maintenance/regroup_shattered_ai.go:115` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 154 | `maintenance.relink-unlinked-books` | `internal/plugins/maintenance/relink_unlinked.go:71` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 155 | `maintenance.repair-library-state` | `internal/plugins/maintenance/library_state_repair.go:183` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 156 | `maintenance.repair-merged-user-state` | `internal/plugins/maintenance/repair_merged_user_state.go:53` | manual/internal | drop | None | - | audiobook |
| 157 | `maintenance.repair-transcribe-status` | `internal/plugins/maintenance/repair_transcribe_status.go:194` | manual/internal | restart (checkpoint) | RunItems | 8 | audiobook |
| 158 | `maintenance.repoint-book-file-rows` | `internal/plugins/maintenance/repoint_book_file_rows.go:254` | manual/internal | drop | Manual | - | audiobook |
| 159 | `maintenance.repoint-missing-to-folder-audio` | `internal/plugins/maintenance/repoint_missing_to_folder_audio.go:332` | manual/internal | drop | RunItems | rfPlanWorkers, rfStatWorkers, rfWriteWorkers | audiobook |
| 160 | `maintenance.repoint-unrecorded-renames` | `internal/plugins/maintenance/repoint_unrecorded_renames.go:97` | manual/internal | drop | RunItems | repointUnrecordedRenamesConcurrency, ErrMode: opsregistry.ErrModeCollect}), the set is normally tiny (writes that | audiobook |
| 161 | `maintenance.reports-prune` | `internal/plugins/maintenance/reports_prune.go:104` | task `reports_prune` every 24h0m0s window#15 | drop | None | - | generic |
| 162 | `maintenance.resolve-production-authors` | `internal/plugins/maintenance/author.go:582` | task `resolve_production_authors` | drop | Manual | authorSplitScanWorkers, resolveProductionAuthorsWorkers() | audiobook |
| 163 | `maintenance.review-status-index-repair` | `internal/plugins/maintenance/review_status_index_repair.go:51` | manual/internal | drop | Manual | - | audiobook |
| 164 | `maintenance.rewrite-path-prefix` | `internal/plugins/maintenance/rewrite_path_prefix.go:364` | manual/internal | drop | RunItems | rewritePathPrefixConcurrency | audiobook |
| 165 | `maintenance.series-denumber` | `internal/plugins/maintenance/series_denumber_op.go:75` | manual/internal | drop | Manual | - | audiobook |
| 166 | `maintenance.series-phantom-repair` | `internal/plugins/maintenance/series_phantom_repair.go:166` | manual/internal | restart (checkpoint) | RunItems | NumCPU | audiobook |
| 167 | `maintenance.split-joined-narrators` | `internal/plugins/maintenance/narrator_split_joined.go:165` | manual/internal | drop | Manual | - | audiobook |
| 168 | `maintenance.tag-backfill` | `internal/plugins/maintenance/tag_backfill.go:192` | manual/internal | drop | RunItems | NumCPU * 4 | audiobook |
| 169 | `maintenance.temp-file-cleanup` | `internal/plugins/maintenance/cleanup.go:140` | task `temp_file_cleanup` window#13 +onstart v2 cron `30 1 * * *` (declarative only) | drop | None | - | generic |
| 170 | `maintenance.throttle-reset` | `internal/plugins/maintenance/throttle_reset.go:70` | manual/internal | drop | None | - | generic |
| 171 | `maintenance.title-backfill` | `internal/plugins/maintenance/title_backfill.go:40` | manual/internal | drop | Manual | - | audiobook |
| 172 | `maintenance.title-repair` | `internal/plugins/maintenance/title_repair.go:145` | manual/internal | drop | RunItems | workers | audiobook |
| 173 | `maintenance.tombstone-cleanup` | `internal/plugins/maintenance/cleanup.go:92` | task `tombstone_cleanup` every 24h0m0s v2 cron `0 4 * * *` (declarative only) | drop | Manual | - | generic |
| 174 | `maintenance.transcode-temp-cleanup` | `internal/plugins/maintenance/cleanup.go:193` | task `transcode_temp_cleanup` every 1h0m0s +onstart | drop | None | - | generic |
| 175 | `maintenance.transcribe-book-intros` | `internal/plugins/maintenance/intro_transcribe.go:195` | manual/internal | restart (checkpoint) | RunItems | introTranscribePageConc | audiobook |
| 176 | `maintenance.transcribe-file-intros` | `internal/plugins/maintenance/file_intro_transcribe.go:109` | manual/internal | restart (checkpoint) | RunItems | slots | audiobook |
| 177 | `maintenance.trash-cleanup` | `internal/plugins/maintenance/cleanup.go:489` | task `trash_cleanup` window#14 v2 cron `0 6 * * *` (declarative only) | drop | None | - | generic |
| 178 | `maintenance.unknown-author-audit` | `internal/plugins/maintenance/unknown_author_audit.go:104` | manual/internal | drop | Manual | - | audiobook |
| 179 | `maintenance.version-group-primary-repair` | `internal/plugins/maintenance/version_group_primary_repair.go:284` | manual/internal | drop | Manual | vgRepairWorkers | audiobook |
| 180 | `maintenance.version-group-primary-report` | `internal/plugins/maintenance/version_group_primary_report.go:161` | manual/internal | drop | Manual | - | audiobook |
| 181 | `maintenance.window` | `internal/server/scheduler_maintenance_window_op.go:29` | manual/internal | drop | Manual | - | audiobook |
| 182 | `merge.CombineJournalCheckOp` | `internal/plugins/maintenance/combine_journal_check.go:43` | manual/internal | drop | Manual | - | audiobook |
| 183 | `metadata.batch-apply-cached` | `internal/server/batch_apply_op.go:313` | manual/internal | restart (checkpoint) | RunItems | writeBackWorkers() | audiobook |
| 184 | `metadata.batch-apply-refusals-reset` | `internal/server/batch_apply_refusal_reset_op.go:101` | manual/internal | drop | Manual | - | audiobook |
| 185 | `metadata.batch-save` | `internal/server/batch_save_op.go:131` | manual/internal | drop | RunItems | writeBackWorkers() | audiobook |
| 186 | `metadata.bulk-apply-preview` | `internal/server/bulk_apply_preview.go:261` | manual/internal | requeue (from zero) | RunItems | NumCPU | audiobook |
| 187 | `metadata.candidate-fetch` | `internal/server/metadata_candidate_op.go:112` | task `candidate_fetch` | restart (checkpoint) | Manual | - | audiobook |
| 188 | `metafetch.asin-backfill` | `internal/plugins/metafetch/asin_backfill.go:213` | task `asin_backfill` | restart (checkpoint) | RunItems | workers | audiobook |
| 189 | `metafetch.calibrate-scoring` | `internal/plugins/metafetch/calibrate_scoring.go:120` | manual/internal | drop | RunItems | NumCPU | audiobook |
| 190 | `metafetch.pcache-sweep` | `internal/plugins/metafetch/pcache_sweep.go:115` | manual/internal | drop | Manual | - | audiobook |
| 191 | `openlibrary.download` | `internal/server/openlibrary_ops.go:32` | manual/internal | drop | Manual | - | audiobook |
| 192 | `openlibrary.import` | `internal/server/openlibrary_ops.go:74` | manual/internal | drop | Manual | - | audiobook |
| 193 | `operations.backfill-legacy-status` | `internal/server/legacy_backfill_op.go:40` | manual/internal | drop | Manual | - | audiobook |
| 194 | `p.LegacyOpID` | `internal/server/apply_when_scanned_op.go:123` | manual/internal | requeue (from zero) | Manual | - | audiobook |
| 195 | `reconcile.apply` | `internal/server/reconcile_ops.go:121` | manual/internal | drop | Manual | - | audiobook |
| 196 | `reconcile.scan` | `internal/server/reconcile_ops.go:42` | task `reconcile_scan` | drop | Manual | - | audiobook |
| 197 | `repairs.apply` | `internal/plugins/maintenance/repairs_ops.go:54` | manual/internal | restart (checkpoint) | Manual | - | generic |
| 198 | `repairs.plan` | `internal/plugins/maintenance/repairs_ops.go:35` | manual/internal | drop | Manual | - | generic |
| 199 | `scheduler.metadata-upgrade` | `internal/scheduler/extra_ops.go:89` | task `metadata_upgrade` | drop | Manual | - | audiobook |
| 200 | `usermerge.OpDefID` | `internal/plugins/maintenance/user_merge.go:63` | manual/internal | drop | None | - | audiobook |
| 201 | `maintenance.backfill-book-files` | `internal/maintenance/jobs/backfill_book_files.go:30` | manual only | drop | Manual | - | audiobook |
| 202 | `maintenance.backfill-file-hashes` | `internal/maintenance/jobs/backfill_file_hashes.go:42` | manual only | restart (checkpoint) | Manual | - | audiobook |
| 203 | `maintenance.backfill-itunes-positions` | `internal/maintenance/jobs/backfill_itunes_positions.go:25` | manual only | drop | Manual | BackfillConcurrency() | audiobook |
| 204 | `maintenance.backfill-merge-undo-pointers` | `internal/maintenance/jobs/backfill_merge_undo_pointers.go:20` | manual only | drop | Manual | - | audiobook |
| 205 | `maintenance.backfill-metadata-source-hash` | `internal/maintenance/jobs/backfill_metadata_source_hash.go:19` | manual only | drop | Manual | - | audiobook |
| 206 | `maintenance.backfill-sync-ids` | `internal/maintenance/jobs/backfill_sync_ids.go:19` | manual only | drop | Manual | BackfillConcurrency() | audiobook |
| 207 | `maintenance.bulk-deluge-import` | `internal/maintenance/jobs/bulk_deluge_import.go:27` | manual only | restart (checkpoint) | Manual | - | audiobook |
| 208 | `maintenance.cleanup-empty-folders` | `internal/maintenance/jobs/cleanup_empty_folders.go:23` | manual only | restart (checkpoint) | Manual | - | generic |
| 209 | `maintenance.cleanup-organize-mess` | `internal/maintenance/jobs/cleanup_organize_mess.go:26` | manual only | drop | Manual | - | audiobook |
| 210 | `maintenance.cleanup-series` | `internal/maintenance/jobs/cleanup_series.go:21` | manual only | drop | Manual | - | audiobook |
| 211 | `maintenance.dedup-books` | `internal/maintenance/jobs/dedup_books.go:29` | manual only | drop | Manual | - | audiobook |
| 212 | `maintenance.enrich-book-files` | `internal/maintenance/jobs/enrich_book_files.go:21` | manual only | drop | Manual | - | audiobook |
| 213 | `maintenance.fix-author-narrator-swap` | `internal/maintenance/jobs/fix_author_narrator_swap.go:19` | manual only | drop | Manual | - | audiobook |
| 214 | `maintenance.fix-file-modes` | `internal/maintenance/jobs/fix_file_modes.go:17` | manual only | drop | Manual | - | generic |
| 215 | `maintenance.fix-read-by-narrator` | `internal/maintenance/jobs/fix_read_by_narrator.go:20` | manual only | drop | Manual | - | audiobook |
| 216 | `maintenance.fix-version-groups` | `internal/maintenance/jobs/fix_version_groups.go:31` | manual only | drop | Manual | - | audiobook |
| 217 | `maintenance.merge-chapter-groups` | `internal/maintenance/jobs/merge_chapter_groups.go:30` | manual only | drop | Manual | - | audiobook |
| 218 | `maintenance.normalize-primary-flags` | `internal/maintenance/jobs/normalize_primary_flags.go:20` | manual only | drop | Manual | - | audiobook |
| 219 | `maintenance.prune-book-snapshots` | `internal/maintenance/jobs/prune_book_snapshots.go:21` | manual only | drop | Manual | - | audiobook |
| 220 | `maintenance.purge-unknown-author-duplicates` | `internal/maintenance/jobs/purge_ua_duplicates.go:31` | manual only | drop | Manual | - | audiobook |
| 221 | `maintenance.recompute-book-aggregates` | `internal/maintenance/jobs/recompute_book_aggregates.go:46` | manual only | restart (checkpoint) | Manual | - | audiobook |
| 222 | `maintenance.recompute-itunes-paths` | `internal/maintenance/jobs/recompute_itunes_paths.go:20` | manual only | drop | Manual | - | audiobook |
| 223 | `maintenance.refetch-missing-authors` | `internal/maintenance/jobs/refetch_missing_authors.go:24` | manual only | restart (checkpoint) | Manual | - | audiobook |
| 224 | `maintenance.relink-missing-to-itunes` | `internal/maintenance/jobs/relink_missing_to_itunes.go:26` | manual only | drop | Manual | - | audiobook |
| 225 | `maintenance.relink-report` | `internal/maintenance/jobs/relink_report.go:25` | manual only | drop | Manual | - | audiobook |
| 226 | `maintenance.repoint-version-primary` | `internal/maintenance/jobs/repoint_version_primary.go:20` | manual only | drop | Manual | - | audiobook |
| 227 | `maintenance.retention-and-hygiene` | `internal/maintenance/jobs/retention_and_hygiene.go:21` | manual only | restart (checkpoint) | Manual | - | generic |
| 228 | `maintenance.revert-metadata-fetch` | `internal/maintenance/jobs/revert_metadata_fetch.go:20` | manual only | drop | Manual | - | audiobook |
| 229 | `maintenance.scan-chapter-groups` | `internal/maintenance/jobs/scan_chapter_groups.go:14` | manual only | drop | Manual | - | audiobook |
| 230 | `maintenance.scan-composer-tags` | `internal/maintenance/jobs/scan_composer_tags.go:28` | manual only | restart (checkpoint) | Manual | - | audiobook |
| 231 | `maintenance.scan-duplicate-files` | `internal/maintenance/jobs/scan_duplicate_files.go:17` | manual only | drop | Manual | - | audiobook |
| 232 | `maintenance.scan-duration-mismatch` | `internal/maintenance/jobs/scan_duration_mismatch.go:17` | manual only | drop | Manual | - | audiobook |
| 233 | `maintenance.scan-metadata-hash-dups` | `internal/maintenance/jobs/scan_metadata_hash_dups.go:17` | manual only | drop | Manual | - | audiobook |
| 234 | `maintenance.sweep-pebble-metrics-ttl` | `internal/maintenance/jobs/sweep_pebble_metrics_ttl.go:23` | manual only | drop | Manual | - | generic |

### 6.1 Scheduler tasks (`TaskDefinition`, 38 registered)

Source: `internal/scheduler/testdata/cadence.golden` (one line per task: name|interval|daily_at|run_on_start|in_window|window_index|enabled_default, produced by `cadenceLines` in `cadence_golden_test.go:25`). The golden is rendered with the test-default config (all `scheduled.*` zero), so intervals read `0s` for config-driven tasks; production defaults come from `internal/config/config.go:2886-3030`:

* `scheduled.library_scan`: enabled, every 360 min, not on startup (`config.go:2886-2888`); `library_scan_full`: enabled, hourly due-check, 168h period (`:2892-2894`).
* `scheduled.asin_backfill`, `candidate_fetch`, `catalog_harvest`: interval 360 min (`:2907-2913`).
* Disabled by default: `dedup_refresh` (360), `acoustid_backfill` (1440), `label_refinement` (10080), `db_optimize` (1440), `ai_dedup_batch` (1440), `metadata_refresh`, `resolve_production_authors`, `series_prune`, `reconcile` (interval 0) (`:2895-2939`).
* `maintenance.enabled=true`, window hours 1-4 (`:3003-3005`) with per-task toggles (`maintenance.dedup_refresh|series_prune|author_split|tombstone_cleanup|reconcile|purge_deleted|purge_old_logs|db_optimize|library_size_refresh` default true; `library_scan|library_organize|metadata_refresh|acoustid_*` default false, `:3007-3030`).
* Hard-coded cadences (visible in the golden): `identification_advance` 5m, `collections_refresh` 1h, `transcode_temp_cleanup` 1h (+onstart), `reports_prune` 24h, `tombstone_cleanup` 24h, `ai_dedup_batch` 24h, `cleanup_activity_log` 24h, `ai_journal_prune` 24h, `purge_old_logs` 168h, `batch_poller` 5m (+onstart), `nightly_activity_compaction` daily 00:10.

Window idx is the position in `maintenanceOrder` (`scheduler.go:197-246`); `-1` = not in the window list.

| # | Task name | tasks.go line | Enqueues def | Interval (golden, test cfg) | DailyAt | OnStart | Window idx | Config keys read |
|---|---|---|---|---|---|---|---|---|
| 1 | `library_scan` | `internal/scheduler/tasks.go:280` | `library.scan` | 0s | - | false | 19 | LibraryScan |
| 2 | `library_scan_full` | `internal/scheduler/tasks.go:361` | `library.scan` | 0s | - | false | -1 | LibraryScanFull |
| 3 | `library_organize` | `internal/scheduler/tasks.go:487` | `library.organize` | 0s | - | false | 18 | LibraryOrganize |
| 4 | `library_size_refresh` | `internal/scheduler/tasks.go:508` | `library.size-refresh` | 0s | - | false | 16 | LibrarySizeRefresh |
| 5 | `transcode` | `internal/scheduler/tasks.go:529` | none (see note) | 0s | - | false | -1 | - |
| 6 | `dedup_refresh` | `internal/scheduler/tasks.go:565` | `dedup.author-scan` | 0s | - | false | 1 | DedupRefresh, label_refinement |
| 7 | `label_refinement` | `internal/scheduler/tasks.go:598` | `dedup.label-refinement` | 0s | - | false | -1 | LabelRefinement |
| 8 | `dedup_llm_review` | `internal/scheduler/tasks.go:635` | `dedup.llm-review` | 0s | - | false | 2 | - |
| 9 | `series_prune` | `internal/scheduler/tasks.go:660` | `dedup.series-prune` | 0s | - | false | 4 | SeriesPrune |
| 10 | `series_normalize` | `internal/scheduler/tasks.go:687` | `dedup.series-normalize` | 0s | - | false | -1 | - |
| 11 | `asin_backfill` | `internal/scheduler/tasks.go:730` | `metafetch.asin-backfill` | 0s | - | false | -1 | asin_backfill |
| 12 | `candidate_fetch` | `internal/scheduler/tasks.go:803` | `metadata.candidate-fetch` | 0s | - | false | -1 | candidate_fetch |
| 13 | `identification_advance` | `internal/scheduler/tasks.go:866` | `identification.advance` | 5m0s | - | false | -1 | - |
| 14 | `collections_refresh` | `internal/scheduler/tasks.go:926` | `collections.refresh-relative-dynamic` | 1h0m0s | - | false | -1 | - |
| 15 | `catalog_harvest` | `internal/scheduler/tasks.go:996` | `catalog.harvest-authors` | 0s | - | false | -1 | acoustid_backfill, catalog_harvest |
| 16 | `acoustid_backfill` | `internal/scheduler/tasks.go:1059` | `acoustid.backfill` | 0s | - | false | -1 | AcoustIDBackfill |
| 17 | `temp_file_cleanup` | `internal/scheduler/tasks.go:1102` | `maintenance.temp-file-cleanup` | 0s | - | true | 13 | - |
| 18 | `transcode_temp_cleanup` | `internal/scheduler/tasks.go:1132` | `maintenance.transcode-temp-cleanup` | 1h0m0s | - | true | -1 | - |
| 19 | `trash_cleanup` | `internal/scheduler/tasks.go:1153` | `maintenance.trash-cleanup` | 0s | - | false | 14 | - |
| 20 | `reports_prune` | `internal/scheduler/tasks.go:1177` | `maintenance.reports-prune` | 24h0m0s | - | false | 15 | - |
| 21 | `metadata_upgrade` | `internal/scheduler/tasks.go:1194` | `scheduler.metadata-upgrade` | 0s | - | false | -1 | MetadataUpgrade, metadata_upgrade |
| 22 | `author_split_scan` | `internal/scheduler/tasks.go:1231` | `maintenance.author-split-scan` | 0s | - | false | 3 | AuthorSplit |
| 23 | `db_optimize` | `internal/scheduler/tasks.go:1261` | `maintenance.db-optimize` | 0s | - | false | 17 | DbOptimize |
| 24 | `acoustid_online_lookup` | `internal/scheduler/tasks.go:1292` | `acoustid.lookup-online` | 0s | - | false | -1 | AcoustIDNightlyLimit, AcoustIDOnlineLookup |
| 25 | `cleanup_old_backups` | `internal/scheduler/tasks.go:1317` | `maintenance.cleanup-old-backups` | 0s | - | false | 12 | DbOptimize |
| 26 | `purge_deleted` | `internal/scheduler/tasks.go:1338` | `maintenance.purge-deleted` | 0s | - | false | 8 | PurgeDeleted |
| 27 | `tombstone_cleanup` | `internal/scheduler/tasks.go:1364` | `maintenance.tombstone-cleanup` | 24h0m0s | - | false | 7 | TombstoneCleanup |
| 28 | `file_integrity_check` | `internal/scheduler/tasks.go:1385` | `maintenance.file-integrity-check` | 0s | - | false | 5 | - |
| 29 | `orphan_book_files_cleanup` | `internal/scheduler/tasks.go:1408` | `maintenance.orphan-book-files-cleanup` | 0s | - | false | 6 | - |
| 30 | `resolve_production_authors` | `internal/scheduler/tasks.go:1432` | `maintenance.resolve-production-authors` | 0s | - | false | -1 | ResolveProductionAuthors |
| 31 | `metadata_refresh` | `internal/scheduler/tasks.go:1461` | `maintenance.metadata-refresh` | 0s | - | false | -1 | MetadataRefresh |
| 32 | `reconcile_scan` | `internal/scheduler/tasks.go:1489` | `reconcile.scan` | 0s | - | false | 0 | Reconcile |
| 33 | `ai_dedup_batch` | `internal/scheduler/tasks.go:1517` | `maintenance.ai-dedup-batch` | 24h0m0s | - | false | -1 | AIDedupBatch |
| 34 | `batch_poller` | `internal/scheduler/tasks.go:1554` | none (see note) | 5m0s | - | true | -1 | - |
| 35 | `purge_old_logs` | `internal/scheduler/tasks.go:1585` | `maintenance.purge-old-logs` | 168h0m0s | - | false | 9 | PurgeOldLogs |
| 36 | `cleanup_activity_log` | `internal/scheduler/tasks.go:1609` | `maintenance.cleanup-activity-log` | 24h0m0s | - | false | 10 | - |
| 37 | `nightly_activity_compaction` | `internal/scheduler/tasks.go:1640` | `maintenance.nightly-compact-activity-log` | 0s | 00:10 | false | -1 | - |
| 38 | `ai_journal_prune` | `internal/scheduler/tasks.go:1667` | `maintenance.prune-ai-journal` | 24h0m0s | - | false | 11 | - |
Notes: (a) `transcode` (`tasks.go:529`) enqueues nothing by design (fails without a book_id; `maintenance.go:~140-153` comment); `batch_poller` (`tasks.go:1554`) calls `deps.PollBatches` directly, not an op. (b) `library_scan` and `library_scan_full` share the def `library.scan` and its ConcurrencyKey; the sweep differs only in params (`tasks.go:~320-360` comment). (c) `archive_sweep` and `isbn_enrichment` were retired from the list (comments `scheduler.go:~210-215`). (d) `deluge.protected-paths-sync` declares cron `*/30 * * * *` but has no task and no native `ops.Schedule`, so it never runs unattended (v2 cron is declarative, `tasks.go:1046-1050`; related tombstone note `internal/opscatalog/tombstones.go:24`). (e) The `scheduled.*` config also feeds the Settings "Tasks" page via `GET /tasks`, `PUT /tasks/:name` (`internal/server/wire_operations_routes.go:118-120`).

### 6.2 Counts (from the table in section 6)

| Dimension | Counts |
|---|---|
| Rows | 234 (233 registered + 1 excluded stub) |
| Class | audiobook 199, generic 24, integration (iTunes) 7, integration (download client / Deluge) 4 |
| Resume | drop 162, restart (checkpoint) 51, requeue 18, ask 3 |
| Liveness | Manual 155, RunItems 68, None 11 |
| Native `pkg/ops` v3 defs | 0 |
| Scheduler `TaskDefinition`s | 38 (36 enqueue a def; `transcode` and `batch_poller` do not) |
| v2 defs carrying a cron string | 15 (14 maintenance + `deluge.protected-paths-sync`); all declarative |
| Defs with `Triggers` (event subscription) | 1 (`deluge.path-update`) |
| `RunItems` call sites | ~170, all with non-zero Concurrency or a sequential marker (lint) |

## 7. Design-relevant observations for a shared "platform core"

1. The reusable executor is `internal/operations/registry` + `internal/operations/state` + `internal/scheduler` + the opv2/opv3 Pebble stores; but the registry's `Options`, `Registry` wiring and `types.go` import `internal/database`, `internal/auth`, `internal/logger`, `internal/opsmetrics`, `internal/lifecycle` (`types.go:17`, `resume.go:10-14`), and `OpsV2Store`/`OpsV3Store` are `internal/database` interfaces. Platform extraction must move or interface-ify these.
2. `pkg/ops` is audiobook-shaped at the edges: `Writer` is hard-typed to `database.Book`, `database.BookFile`, `database.BookAuthor` (`pkg/ops/writer.go:46-90`), `SubjectType` has `book`/`book_file` (`writer.go:112-121`), `Lane` has `LaneMac` for audio decode (`lanes.go`). The Task/Batch/Fixer/Pipeline/Schedule/Effects/RC core is domain-neutral.
3. Three parallel definition shapes (v2 `OperationDef`, `maintenance.MaintenanceJob`, v3 `ops.Definition`) are all live; the v3 adapter `FromV2` + `RegisterBundle` is the migration path, and no def has migrated (`opscatalog.All` empty).
4. Registration is through global package state populated in `init()` (serviceregistry factories, `maintenance.Register`, `addOpRegistrar`, `plugin.Register`, `aijobs.Register`): see file 07 for the test-isolation consequences.
5. The scheduler is split: durable-clock TaskDefinitions (production) vs `ops.Schedule` (built, unused); v2 `Schedule` cron strings are dead metadata. A core needs one declared schedule mechanism.
6. Concurrency policy is enforced repo-wide by a type-based lint (`internal/plugins/runitems_concurrency_lint_test.go`), and by `pkg/ops` making `CPU()` the omitted default and `Sequential(why)` require a reason (validated `batch.go:160-175`).
7. `Cancellable` on v2 defs is dead (never consulted); `Isolate` subprocess mode is disabled in practice.
8. Persistence leaks audiobook concerns into the generic run store only lightly (subjects `book`/`book_file` in deps/completions: `op:completion:{type}:{id}:{opType}` at `pebble_store_ops_v2.go:1188`), otherwise opv2/opv3 keys are generic run state.
