<!-- file: docs/proposals/2026-10-holistic/04-operations-census.md -->
<!-- version: 1.2.0 -->
<!-- guid: 3b8e1f52-6c4d-4a97-9e20-7d1c5a4f8b63 -->
<!-- last-edited: 2026-10-08 -->

# 04: Operations census, prune list and non-op jobs

**Analyst:** `census`. **Base commit:** `f7211eb39`.

**What this is.** A planning exercise only. No code was changed.

> **Coordinator note (08, 2026-10-08).** (1) The missing `Permissions` are wider than the twins. At HEAD 147 of 198 `OperationDef` literals declare none, which is every def under `internal/plugins/*`, and `defPermissionsHeld` (`handlers/operations_v2.go:648`) passes an empty list. So the seeded editor role can run every plugin op through `POST /operations/v2`, destructive ones included. 08 adds early PR X2 (an empty list means `settings.manage`). P4 still ports `settings.manage` explicitly. (2) Q2 is verified safe: `orphan-book-files-cleanup` cannot write `book_file` rows (08 §2 a). (3) P3 is split. P3a is a one-line fix to `extra_ops.go:686`, gated on Q4, and goes in wave 0. P3b, the consolidation, stays after P4f. (4) P8 is gated on 01 Q2: if the SQLite backend is deleted, P8 is dropped. (5) The v1 retention purge is owned by 01 P74.

**The full table.**
- [`04-operations-census/ops-census.md`](04-operations-census/ops-census.md) has one row per op, 234 rows in all.
- [`04-operations-census/ops-census.csv`](04-operations-census/ops-census.csv) holds the same data for filtering.
- Each row gives: the def's file and line, what it does, its trigger, RunItems use and Concurrency, resume policy, whether it can be cancelled, liveness, ConcurrencyKey, idempotency, last-known usage and a verdict.

**How the census was taken.**
- It comes from a real registry boot, not from grep.
- A test was supplied through `go test -overlay`, so no repo file was touched.
- It called `bootRegisteredOpIDs`, the same helper `TestOpIDs_NoRenameWithoutAlias` uses (`internal/server/op_id_aliases_test.go`), and dumped `ActiveDefs()`.
- It resolved each `Run` func's source location with `runtime.FuncForPC`.
- Command: `go test -overlay overlay.json ./internal/server -run TestZZCensusDump`. The overlay file lives in the analyst scratchpad.

## 1. Summary

- **234 production op defs are registered.**
  - The boot registers 241. Seven of those are registered only by `_test.go` files: the six `maintenance.test-probe-*` jobs, plus `maintenance.orphan-row-blocking-probe`, which `maintenance_orphan_row_test.go:64` registers.
  - The 234 by plugin:

    | Plugin | Defs |
    |---|---|
    | maintenance | 129 (92 plugin ops and 37 `internal/maintenance/jobs`) |
    | dedup | 38 |
    | scheduler | 12 |
    | library | 10 |
    | acoustid | 8 |
    | metadata | 5 |
    | other | 32 |

  - Resume policy across the 234: 171 drop, 38 restart, 22 requeue, 3 ask.
- **There are two scheduling systems, and the cron one does nothing.** (F1, F2)
  - 20 defs set `OperationDef.Schedule`, but nothing evaluates it. The code says so itself in three places: `internal/scheduler/tasks.go:1377`, `internal/plugins/maintenance/nightly_compact_activity_log.go:31` and `internal/plugins/acoustid/backfill.go:285`.
  - **14 of those 20 have no TaskScheduler task**, so they never run on a schedule.
  - Everything that really runs on a timer goes through the 33-task `TaskScheduler` (`internal/scheduler/tasks.go`).
- **11 twin pairs: the same job is registered twice under two IDs.** (F3)
  - Each pair has two ConcurrencyKeys and no `Writes`, so the two twins can run at the same time over the same rows.
  - Usually only one twin is reachable. The other appears nowhere except its own definition.
  - One pair has drifted apart. The **scheduled** `scheduler.cleanup-old-backups` reads `PurgeSoftDeletedAfterDays` as its backup-retention setting (`internal/scheduler/extra_ops.go:686`). Its unscheduled twin reads `BackupRetentionDays`.
- **Recommended for pruning: 13 defs with high confidence, 2 more conditional on other work, and 8 near-duplicates for the owner to decide** (§2.2).
- **10 boot goroutines duplicate an op that is already registered.** They do the same work outside the operations system, so nobody can see progress, cancel them or resume them. (F5)
  - external-id backfill
  - movement-atom strip
  - malformed-M4B remux
  - malformed-M4B transcode
  - book_atpath index
  - opchange index
  - activity filter-index reconcile
  - merge-user-state repair loop
  - embedding backfill
  - version-group index
- **15 non-op jobs should be converted into ops or deleted:** 11 definite, 4 candidates, plus 1 deletion (the inert v1 stale-op reaper). See §2.4.
- **The RootDir gate is mostly gone, and the memory note about it is stale.**
  - The note `project_operations_registry_state.md` says "RootDir gates 105 ops". That gate was removed and `server_op_registration_test.go:30` pins the removal.
  - What remains: deluge (3 ops) and itunes (6 ops) still require RootDir. acoustid and dedup register only when their engines are wired.
- **CLAUDE.md's RunItems rule is almost met.** Of 122 `RunItems` call sites outside the registry, only 5 run sequentially because they omit `Concurrency`:
  - `acoustid/lsh_backfill.go:109`
  - `acoustid/reset_all.go:111,182`
  - `deluge/centralization.go:104`
  - `deluge/path_update.go:118`

  Found with an AST scan (`go/parser` over every non-test file).
- **96 defs have no trigger in the repo at all.** No UI, no task, and no Go code enqueues them; only a hand-typed `POST /api/v1/operations/v2` runs them. The automatic classifier found 103, and 7 were corrected by hand.
  - Most are deliberate operator tools. The missing-file repoint and recover ops, for example, are recorded in memory as run against prod.
  - "API-only" does **not** mean prunable. It does mean v3 needs a way to list and run these ops (handed to 05).

## 2. Findings

### 2.1 Findings table

| ID | Finding | Evidence | Conf. | Impact |
|---|---|---|---|---|
| F1 | `OperationDef.Schedule` is stored but never evaluated. | The only reads are `registry.go:707` (it upserts the def into `op_definitions_v2.ScheduleCron`) and `registry.go:848` (enqueue dedupe). The comments at `tasks.go:1377-1381`, `nightly_compact_activity_log.go:31` and `acoustid/backfill.go:285` agree. Proof: `grep -rn '\.Schedule\b' internal --include=*.go \| grep -v _test`. | high | Anyone reading a def believes it is scheduled. |
| F2 | 14 defs declare a cron schedule and have no TaskScheduler task. | `deluge.protected-paths-sync`, `maintenance.{author-dedup-scan, author-split-scan, batch-poller, cleanup-old-backups, db-optimize, file-integrity-check, metadata-refresh, orphan-book-files-cleanup, purge-deleted, series-prune, temp-file-cleanup, tombstone-cleanup, trash-cleanup}`. The script cross-joins the `Schedule` field with the `EnqueueOp` targets parsed from `tasks.go`. | high | Two report-only health checks never run: `file-integrity-check` and `orphan-book-files-cleanup`. The other 12 are covered by a twin (F3) or are harmless. |
| F3 | There are 11 twin pairs, each registered under a `scheduler.*` and a `maintenance.*` ID (or `dedup.*` and `maintenance.*`). | See §2.2 table A. `taskV2DefIDs` (`scheduler/maintenance.go:152-188`) points at the `scheduler.*` side. The `maintenance.*` side appears only at its own definition: run `git grep -n '"maintenance.purge-deleted"'` and get 2 hits, both in `cleanup.go`. | high | Every fix has to land twice (the author-split `ModifyBook` fix is in both `extra_ops.go:476` and `maintenance/author.go:306`). The twins carry different ConcurrencyKeys, so they can run at the same time. One pair has already drifted (F4). |
| F4 | The backup cleanup that actually runs uses the wrong retention setting. | `extra_ops.go:686` sets `retentionDays := config.AppConfig.PurgeSoftDeletedAfterDays`, while `maintenance/cleanup.go:370` uses `p.deps.BackupRetentionDays()`. `extra_ops.go:719` notes there are three implementations. The third is `internal/maintenance/jobs/cleanup_backups.go`. | high | Backup files can be deleted earlier than the backup-retention setting allows. |
| F5 | 10 boot goroutines duplicate registered ops. | `server_lifecycle.go`: `:971` (external-id-backfill), `:1024` (versiongroup-backfill), `:1055` (book-atpath-backfill), `:1140` (opchange-index-backfill), `:1178` (strip-movement-atoms), `:1186` (remux-malformed-m4b), `:1233` (transcode+quarantine) and `:1891` (merge-user-state-repair). Also `server.go:935` (embedding-backfill) and `activity/register.go:60` (reconcileFilterIndexAtBoot). The comment at `server_lifecycle.go:972-979` admits it is "unclear whether that path [the op] is ever enqueued". | high | Hours-long walks run invisibly. They log progress only through slog (`startupProgressLogger`) and cannot be cancelled except by restarting the process. |
| F6 | The batch poller exists twice. | The `batch_poller` task calls `PollBatches` inline every 5 minutes (`tasks.go:1248-1275`). The `maintenance.batch-poller` def (`maintenance/batch_poller.go:22`) is reached by nothing. | high | Low impact; it is dead weight. |
| F7 | There are two retired ISBN-enrichment stubs. | `scheduler.isbn-enrichment` (`extra_ops.go:752`) and `maintenance.isbn-enrichment` (`maintenance/metadata.go:74`) both return `ErrISBNEnrichmentRetired`. Neither appears in `taskV2DefIDs`. | high | Prunable. The `retiredOpIDs` mechanism (`op_id_aliases_test.go:52`) now covers the "fail loudly" case they exist for. |
| F8 | The v1 stale-operation reaper still runs every minute and does nothing. | The ticker is at `server_lifecycle.go:450-465`. `failStaleOperations` (`:1706`) reads and writes only the v1 `operation:` keyspace, which has had zero writers since 2026-08-23 (memory note `project_operations_v1_retirement_state`, which matches HEAD). The startup resume sweep it shipped with is already gone: `grep resumeInterruptedOperations` finds no definition. | high | A useless goroutine plus a misleading `operation_timeout_minutes` setting. Deleting it is a config-deprecation decision (hand to 01). |
| F9 | The label-refinement chain is a hand-rolled parent op. | `tasks.go:553` runs `go ts.runLabelRefinementChain()`, which enqueues two ops in turn and polls `WaitForOperation` (`tasks.go:1411-1450`). | high | The parent cannot be seen or cancelled from the UI. `dedup.run-all` and `maintenance.library-optimize` already show the right pattern (`childop.Follow`). |
| F10 | dedup-on-import has three paths, and two of them are not ops. | `importer/service.go:411-419` uses the op only when `dedup.on_import_via_scheduler=true` and otherwise starts a goroutine. `server_search.go:404` (`fireDedupOnImport`, organize hook) and `metafetch/service.go:1047` (after a metadata apply) are bare goroutines with `context.Background()`. | high | Bursts are not coalesced, and the work is invisible and cannot be cancelled. The batchable `dedup.check-book` op exists for exactly this job. |
| F11 | The activity SQLite migration runs as a starter goroutine, not as an op. | `activity/sql_migration.go:60-78`. In prod it restarted 15 times with no tier completions (memory note `project_activity_migration_digest_tier_hazard`). It has a checkpoint but no op row, progress total or cancel button. | med | It is visible only through journal greps. Converting it gives a denominator and a cancel button for free. |
| F12 | Five RunItems calls are sequential. | AST scan (§1). `deluge/centralization.go:99-101` says so in a comment, "Sequential (Concurrency=0 default)". | high | `acoustid.reset-all` and `lsh-backfill` walk every book file on one core. |
| F13 | There are 8 near-duplicates the owner should decide on. | §2.2 table C. | med | Each has a real behavioral difference, so none is an automatic prune. |
| F14 | The memory note "cancel can't stop a repairs apply" may still be true at HEAD. | `grep -n 'ctx.Err\|ctx.Done' internal/repairs/engine.go` finds nothing. Per-item cancel depends on RunItems only (`engine.go:140,667`). | low | Hand to 05: cancellation inside an item. |
| F15 | A third temp-file cleaner runs as an untracked ticker. | `server_lifecycle.go:262`, `transcode.StartCleanupTicker`, runs once per import path every hour, outside both schedulers. The other two cleaners are `scheduler.temp-file-cleanup` and `maintenance.temp-file-cleanup`. | med | The predicate is different (transcode temp files older than 2h), but it is the same "delete on a timer" class, with no visibility. |

### 2.2 Ops to prune

**A. Twin pairs.** The recommended survivor is the side whose body delegates to the single shared implementation. The TaskScheduler entry moves to the survivor, and the loser's ID becomes a `FormerIDs` alias on it. An alias, not a deletion, because `scheduler.*` rows exist from nightly runs and resume and timeline filters must keep resolving them.

| Loser (delete) | Survivor | Behavioral diff found | Def-level diffs to port (loser vs survivor) | Safe? |
|---|---|---|---|---|
| `scheduler.purge-deleted` (`extra_ops.go:815`) | `maintenance.purge-deleted` | The scheduler has its own copy of `runAutoPurgeSoftDeleted` (`extra_ops.go:1023`), separate from `audiobooks_helpers.go:253`. | Permissions settings.manage vs none; Timeout 2h vs 30m | Yes, after a body diff in the PR. |
| `scheduler.temp-file-cleanup` (`:778`) | `maintenance.temp-file-cleanup` | None: both call `sweep.CleanupOrphanedTempFiles`. | Permissions settings.manage vs none; Timeout and ProgressTimeout 1h vs 20m | Yes |
| `scheduler.trash-cleanup` (`:191`) | `maintenance.trash-cleanup` | None: both call `CleanupTrashedVersions`. | Permissions settings.manage vs none; Timeout and ProgressTimeout 1h vs 20m | Yes |
| `scheduler.tombstone-cleanup` (`:852`) | `maintenance.tombstone-cleanup` | None: both call `ResolveTombstoneChains`. | Permissions settings.manage vs none; Timeout 1h vs 45m | Yes |
| `scheduler.db-optimize` (`:541`) | `maintenance.db-optimize` | `maintenance/db.go` comment: "same work under another ID". | Permissions settings.manage vs none; Timeout 2h vs 1h | Yes |
| `scheduler.cleanup-old-backups` (`:648`) | `maintenance.cleanup-old-backups` | **Retention setting differs (F4).** The survivor's behavior is the correct one. | Permissions settings.manage vs none; Timeout and ProgressTimeout 1h vs 30m | Yes. This is a behavior change: retention follows the backup-retention setting. |
| `scheduler.metadata-refresh` (`:991`) | `maintenance.metadata-refresh` | Separate copies of `runMetadataRefreshScan` (`extra_ops.go:1060` and `metadata_ops.go:1186`). | Permissions settings.manage vs none; Resume drop vs requeue; Capability network.openai vs network.generic; Timeout 4h vs 2h | Yes, after a body diff. |
| `scheduler.author-split-scan` (`:296`) | `maintenance.author-split-scan` | Two inline copies of about 240 lines. Both carry the ModifyBook fix. | Permissions settings.manage vs none; Resume drop vs requeue; Timeout 2h vs 1h | Yes, after a body diff. |
| `scheduler.resolve-production-authors` (`:891`) | `maintenance.resolve-production-authors` | Parallel copies. | Permissions settings.manage vs none; Resume drop vs requeue; Capability network.openai vs network.generic; Timeout 2h vs 1h | Yes, after a body diff. |
| `maintenance.series-prune` (`maintenance/series.go:64`) | `dedup.series-prune` | None: both call `executeSeriesPrune`. `dedup.*` is the side the UI and the task use. | Survivor is stricter: library.edit_metadata, cancellable, Liveness manual, Timeout 2h. Nothing to port | Yes |
| `maintenance.series-normalize` (`series.go:23`) | `dedup.series-normalize` | None: both call `executeSeriesNormalizeCore`. | Survivor is stricter: library.edit_metadata, cancellable, Liveness manual, Timeout 4h. Nothing to port | Yes |

**Survivor direction for the nine `scheduler.*` pairs.**
- For 7 of the 9 pairs, the bodies that remain in `maintenance/` delegate to `Server` methods or `sweep` helpers.
- `author-split-scan` and `resolve-production-authors` carry inline logic over `OpsStore` on **both** sides (`maintenance/author.go:130,394`). For those two pairs the merge is a body diff, not a delete.

**Def-level diffs, from a field-by-field comparison of the census dump.**
- **Every surviving `maintenance.*` twin declares no `Permissions`.** If they survive as they are, the only gate left is the generic trigger route's `scan.trigger`, and the seeded editor role holds that. Editors could then run purge-deleted and cleanup-old-backups, both of which delete. **Each P4 PR must carry `Permissions: settings.manage` over to the survivor.** This blocks the PR.
- The dry-run default is the same on both sides. Both resolve `{}` through `opmode.ParseDryRun` and land on preview (`opmode/dryrun.go:53`). The scheduler sends `schedulerExtraOpParams{}` (`tasks.go:44`), so the scheduled author-split and resolve-production-authors runs are previews today, and they stay previews after the merge.
- The resume policy changes from drop to requeue for three pairs. Requeue declares the op idempotent, so the P4 PR must justify it or keep drop.
- Keep the larger `Timeout` of the two. Prod runs were sized against the scheduler's budget. The `scheduler.*` bodies in `extra_ops.go`, a 1,116-line file, duplicate those methods instead. If 05 retires the `scheduler` plugin namespace, this is the direction that agrees with it. **Open question Q1** lets the owner reverse the choice of which ID is canonical; the bodies merge either way.

**B. Retired, finished or unreached.**

| Op | Evidence | Safe? | Marker to keep |
|---|---|---|---|
| `scheduler.isbn-enrichment` | A stub that returns an error (F7). | Yes | Add to `retiredOpIDs`. Keep the ledger line. |
| `maintenance.isbn-enrichment` | Same. | Yes | Same. |
| `maintenance.batch-poller` | Unreached (F6). | Yes, unless Q3 adopts it. | Add to `retiredOpIDs`. |
| `operations.backfill-legacy-status` (`legacy_backfill_op.go:40`) | Applied on 2026-08-22: 1,737 rows. A re-run found 0 of 10,245 non-terminal (memory note `project_operations_v1_retirement_state`). | **Conditional:** remove only together with the v1 `operation:` keyspace (01/05). Until then it doubles as a "did someone start writing v1 rows again" detector. | None. It writes v1 rows only. |

**C. Near-duplicates for the owner (not auto-prunable).**

| Op | Overlaps with | Why it isn't automatic |
|---|---|---|
| `maintenance.author-dedup-scan` | `dedup.author-scan` (the scheduled one) | Different store paths. Its cron is unread, so today it runs only through the API. |
| `scheduler.dedup-llm-review` | `dedup.llm-review` | Both run `Engine.RunLLMReview`. They have different keys, so both can run at once. |
| `maintenance.reconcile-scan` (scheduled) | `reconcile.scan` (UI) | They call `BuildReconcilePreviewWithProgress` and `RunReconcileScan` respectively. One saves results, the other previews. |
| `maintenance.cleanup-backups` (a job) | The two cleaners in table A | Matches `.backup` and `.bak` with a different regex. |
| `maintenance.fix-book-file-paths` (a job) | `maintenance.mark-missing-files` | Both write `book_file.Missing`. Only the latter clears stale flags in both directions (`mark_missing_files.go:364`). |
| `maintenance.repair-missing-files` (a job) | `missing-file-repoint` and `recover-missing-files` | The newer ops hold the scan stand-down. Check whether the job does. |
| `maintenance.bulk-fetch-metadata` (a job) | `library.bulk-metadata-fetch`, `metadata.candidate-fetch` | Three whole-library fetchers. |
| `maintenance.author-title-fragment-scan` | The Repairs fixers' plan phase | Report-only. It could become a fixer, following the precedent of `maintenance.repair-junk-titles` (`op_id_aliases_test.go:58`). |

**Excluded on purpose:**
- `library.bulk-write-back` and `maintenance.bulk-write-back` are covered by the write-back ban.
- The iTunes plugin ops are covered by the ban on iTunes writes. `itunes.heal` and `itunes.clone-into-library` touch iTunes-sourced files.
- `scan` ConcurrencyKey sharing is left alone.

**Prune count:**
- 13 high-confidence: 11 twins from table A and 2 ISBN stubs.
- 2 conditional: `batch-poller` and `backfill-legacy-status`.
- 8 for the owner to review (table C).

### 2.3 What is not a prune

- **One-off migrations that finished but must stay registered.** `maintenance.booksig-sidecar-migrate`, `maintenance.intro-migrate-single-file`, `maintenance.opchange-book-index-rebuild` and `maintenance.book-atpath-index-verify`. They are sentinel-gated, so a re-run is cheap, and the index ops are rollback-runbook tools (see the comments in `plugin.go` beside their registration). Keep them.
- **Ops that only `POST /operations/v2` can run.** Keep them as an operator toolbox. The census `usage` column shows prod runs for `recover-missing-files`, `mark-missing-files`, `missing-file-repoint`, `merge-same-path-dupes`, `duration-backfill`, `itunes.regroup` and others.

### 2.4 Jobs outside the operations system

The triage started from 167 `go`/`NewTicker`/`AfterFunc` sites, found with `grep -nE '^\s*go (func|…)|time\.NewTicker|time\.AfterFunc'` over non-test files, plus the 30 named `bgWG.Go(` sites in `internal/server`. Excluded as plumbing:
- the registry's own 24 sites;
- batchers, writers and pools;
- mtls and HTTP listeners;
- ratelimit, realtime heartbeats and SSE;
- per-request fan-out workers inside handlers;
- ffmpeg stderr pipes;
- dedup chromem hydration;
- the fingerprint `workerclient`, which is the Mac-side worker client, so audio decoding stays off the server.

#### 2.4.1 Convert to ops

| # | Where | What it does | Why it should be an op | Move size |
|---|---|---|---|---|
| C1 | `server_lifecycle.go:971` | External-ID backfill at every boot. | Twin of `maintenance.external-id-backfill`. Boot can enqueue the op with `force=false`. | S |
| C2 | `:1178` | Strips movement atoms. | Twin of `maintenance.movement-atom-cleanup`. A whole-library walk with no progress or cancel. | S |
| C3 | `:1186` | Remuxes malformed M4B files. | Twin of `maintenance.malformed-m4b-remux`. | S |
| C4 | `:1233` | Transcodes malformed M4B files, then quarantines them. | Twin of `maintenance.malformed-m4b-transcode`. The quarantine step needs a home inside the op. | S/M |
| C5 | `:1055` | Builds the book_atpath index (sentinel-gated). | Twin of `maintenance.book-atpath-index-backfill`. Needs a v3 "requires memdb warm" gate (05). | S |
| C6 | `:1140` | Ensures the opchange_by_book index every boot (backfill, verify, rebuild). | Twin of `maintenance.opchange-book-index-rebuild`. Needs an `ensure` mode. | M |
| C7 | `activity/register.go:60` | Reconciles the activity filter index at boot. | Twin of `maintenance.activity-filter-index-backfill`; its error text already tells the user to run that op. | S |
| C8 | `activity/sql_migration.go:60` | Pebble-to-SQLite activity backfill that can run for hours. | F11: no op row, no total, no cancel. It already checkpoints every 500 rows. The `digest` tier must stay non-resumable (memory note `project_activity_migration_digest_tier_hazard`). | M |
| C9 | `tasks.go:553` | Label-refinement chain. | F9: rewrite as a parent op using `childop.Follow`. | S |
| C10 | `importer/service.go:417`, `server_search.go:404`, `metafetch/service.go:1047` | Dedup check after import, organize and apply. | F10: always enqueue the batchable `dedup.check-book`. Retire the flag after a soak period. | M |
| C11 | `server_lifecycle.go:262` | Per-import-path hourly transcode temp cleanup. | F15: fold into `maintenance.temp-file-cleanup` with an import-paths parameter, on a TaskScheduler interval. | S |
| C12 (candidate) | `server_lifecycle.go:1024` | Builds the version-group index (sentinel-gated). | Same shape as C5, but there is no op twin yet. | S |
| C13 (candidate) | `server.go:935` | Embedding backfill (marker `dedup.BackfillVersionMarker`). | Overlaps `dedup.embed-scan`, which takes no marker. Make boot enqueue `dedup.embed-scan` when the marker is unset. | M |
| C14 (candidate) | `tasks.go:1248` | OpenAI batch poll every 5 minutes, inline. | F6. Either adopt `maintenance.batch-poller` (a new op row every 5 minutes, so it needs v3 retention or `NotifyActivity`) or keep it inline as a v3 "service loop". **Q3**. | S |
| C15 (candidate) | `server_lifecycle.go:1198` | Builds the search index on first boot, then a coverage reconcile. | A one-time bulk build that cannot be cancelled. The reconciler itself (`search_reconciler.go:292`) should stay a service loop. | M |
| D1 (delete) | `server_lifecycle.go:450` | v1 stale-op reaper. | F8: it does nothing. Deleting it deprecates `operation_timeout_minutes`. | S (01 owns) |

#### 2.4.2 Keep outside the op system

These are run-forever loops that support the system; none has a natural end. They belong in a v3 "service" registry that is visible on the operations page but has no op rows (hand to 05).
- The cache warmers: `server_lifecycle.go:715-777`, and the trickle warmer at `library_list_warmer.go:607`.
- Search `index-worker`, `search-reconciler` and `search-index-watchdog` (`:92-100`).
- Session cleanup (`:436`) and the API-key expiry sweep (`apikey_expiry_sweep.go:60`).
- The status heartbeat (`:310`).
- The watcher supervisor (`watcher/supervisor.go:101`). It already enqueues `library.scan`.
- The updater ticker (`updater/scheduler.go:48`). It must outlive the registry in order to restart.
- `merge-user-state-repair` (`:1891`). It is deliberately not an op: a scheduled run with `{}` must preview, but this loop must write (comment at `:1883`).
- memdb warmup (`pebble_store.go:657`). It runs before the registry exists.
- `opsv2-timeline-reconcile` (`:1096`). It runs before the timeline is trusted, so it cannot report through the timeline.

#### 2.4.3 The TaskScheduler itself

`internal/scheduler` (6,921 lines including tests) is a second scheduler beside the registry. It has:
- 33 tasks, with intervals, `DailyAt`, run-on-start and maintenance-window membership;
- a durable interval clock (`interval_clock.go`);
- a `taskV2DefIDs` map that has to be kept in step by hand (`maintenance.go:152`);
- `taskConcurrencySiblings`, which re-implements ConcurrencyKey sharing.

It works. Its weak point is that a def's own declaration says nothing about when the def runs. That is how F2 happened.

**The census's recommendation to 05.** Make the trigger part of the def. Either the registry evaluates `Schedule`, using `DailyAt` and interval semantics, or `Schedule` is deleted and the TaskScheduler becomes the only declared source with a startup check that every def appears in it. Size: L.

## 3. Proposed specification

1. Every op ID belongs to exactly one def, and every unit of work is registered exactly once. A guard test fails when two defs share a `Run` target, using the `runtime.FuncForPC` technique this census used. Shared helpers called through closures need an allowlist.
2. A def's schedule is declared in exactly one place, and the build fails when a declared schedule has no driver. The interim form, before 05's v3 lands: a test that walks `ActiveDefs()` and fails on any `Schedule != nil` def that is missing from `taskV2DefIDs`'s values.
3. Boot-time work goes through `EnqueueOp`. A `bgWG.Go` that loops over the whole library is a review-blocking smell, unless it is on the §2.4.2 allowlist.
4. Every whole-library `RunItems` call sets `Concurrency`, or carries a comment explaining why it is sequential. An AST lint like the one in §1 enforces this.
5. Retiring an op always means a `retiredOpIDs` entry or a `FormerIDs` alias. The `op_ids.golden` line stays.

## 4. Implementation plan (phased PRs)

| PR | Size | Touches | Tests | Rollback |
|---|---|---|---|---|
| P1: drop the unread `Schedule` or wire the missing tasks; add the schedule-has-driver guard | S | `internal/plugins/maintenance/{cleanup.go, db.go, metadata.go, author.go, series.go, batch_poller.go, integrity_check.go, orphan_book_files.go}`, `internal/plugins/deluge/protected_paths.go`, new `internal/server/op_schedule_driver_test.go`, and `internal/scheduler/tasks.go` if Q2 says to wire `file-integrity-check` and `orphan-book-files-cleanup` | The guard test fails before the fix and passes after; `go test ./internal/scheduler ./internal/server` | Revert. Behavior is unchanged unless tasks are added. |
| P2: retire the two ISBN stubs | S | `internal/scheduler/extra_ops.go`, `internal/scheduler/scheduler.go` (registrar list), `internal/plugins/maintenance/{metadata.go, plugin.go}`, `internal/server/op_id_aliases_test.go` (`retiredOpIDs` +2) | `TestOpIDs_NoRenameWithoutAlias` | Revert |
| P3: fix backup retention and consolidate the three `.bak` cleaners into one helper | M | `internal/scheduler/extra_ops.go`, `internal/plugins/maintenance/cleanup.go`, `internal/maintenance/jobs/cleanup_backups.go`, `internal/sweep/` (new shared helper), `changelog.d/` | A table test on retention days; the app-dir guard tests keep passing | Revert. Note: it changes what gets deleted, so it needs owner sign-off (Q4). |
| P4a–P4i: one PR per `scheduler.*` twin. Diff the bodies, port anything missing into the survivor, **port `Permissions: settings.manage` and the larger Timeout**, decide on drop or requeue, delete the loser, add the `FormerIDs` alias, retarget the task | M each (P4h author-split is L) | `internal/scheduler/{extra_ops.go, tasks.go, maintenance.go}`, the matching `internal/plugins/maintenance/*.go`, `internal/server/testdata/op_ids.golden` (unchanged), and tests under `internal/scheduler/*_test.go` that name the loser ID | Existing scheduler tests retargeted; an alias-resolve test; a permission test that an editor gets 403 on the survivor; `hasActiveV2Op` canonicalizes aliases already (`maintenance.go:113`) | Revert the PR. The alias keeps old rows resolving. |
| P5: series twins | S | `internal/plugins/maintenance/{series.go, plugin.go}`, `internal/server/duplicates_ops.go` (`FormerIDs`) | Alias test | Revert |
| P6: boot twins enqueue their op (C1–C5, C7) | M | `internal/server/server_lifecycle.go` (`startBackfills`), `internal/activity/register.go`, `internal/plugins/maintenance/{backfill.go, book_atpath_index.go, activity_filter_index_backfill.go}` (params: `force`, `if_needed`) | A boot test that the ops get enqueued; the sentinel short-circuit stays inside each op | Revert. The goroutines come back. |
| P7: opchange `ensure` mode (C6) | M | `internal/server/server_lifecycle.go`, `internal/plugins/maintenance/opchange_book_index.go` | Run the existing index tests through the op | Revert |
| P8: activity SQLite migration as an op (C8) | M | `internal/activity/sql_migration.go`, new `internal/plugins/maintenance/activity_sql_migrate.go`, `internal/plugins/maintenance/plugin.go` | Resume test; check that `digest` still never resumes (`sql_activity_progress.go` `activityNonResumableTiers`) | Revert to the starter |
| P9: label refinement as a parent op (C9) | S | `internal/scheduler/tasks.go`, new `internal/plugins/dedup/label_refinement.go` | `childop` test | Revert |
| P10: dedup-on-import always uses the op (C10) | M | `internal/importer/service.go`, `internal/server/server_search.go`, `internal/metafetch/service.go`, `internal/config/config.go` (deprecate the flag) | Batch coalescing test | Flip the flag back |
| P11: set `Concurrency` on the 5 sequential RunItems calls, plus the AST lint | S | `internal/plugins/acoustid/{lsh_backfill.go, reset_all.go}`, `internal/plugins/deluge/{centralization.go, path_update.go}` (respect Deluge rate limits), new `scripts/lint_runitems_concurrency.go` or a test | `-race` test running each op concurrently (CLAUDE.md: the Label closure runs in workers) | Revert |
| P12: batch poller decision (C14) | S | `internal/scheduler/tasks.go`, `internal/plugins/maintenance/batch_poller.go` | — | Revert |
| P13: fold the transcode temp ticker into the temp-file op (C11) | S | `internal/server/server_lifecycle.go`, `internal/plugins/maintenance/cleanup.go`, `internal/transcode/transcode.go` | Cleanup predicate test | Revert |

**Order:** P1 → P2 → P5 → P4a–i (one at a time, because they all touch `extra_ops.go` and `tasks.go`) → P3 (after the P4 cleanup-old-backups PR) → P11 → P6 → P7 → P9 → P12 → P13 → P10 → P8.

**Collision note.** P1, P2, P4, P9, P12 and P13 all touch `internal/scheduler/tasks.go` or `extra_ops.go`. Serialize them.

## 5. Risks and what must not break

- **Resume and history.** A deleted ID with no alias strands its rows. Every PR here adds `FormerIDs` or `retiredOpIDs`. `TestOpIDs_NoRenameWithoutAlias` is the gate.
- **Twins that run at the same time today.** Collapsing them is safer, not riskier. But until P4 lands, never run both IDs of a pair together.
- **P3 changes what gets deleted.** Get owner sign-off first.
- **Moving work out of boot goroutines (P6, P7) changes timing.** It moves into the registry's worker pool of 8. A boot enqueue must not starve user-triggered ops: use `PriorityLow`. C5 and C6 must keep their "wait for memdb warmup" behavior (memory note `project_memdb_warmup_is_async_after_restart`).
- **The scan ConcurrencyKey stays as it is.** No proposal here touches `library.scan` or its key.
- **Untouched:** `internal/writeback/`, the write-back ops, and the iTunes ops.
- **No work moves onto the server's own CPU decoding audio.** The fingerprint `workerclient` is client-side and stays as it is.

## 6. Dependencies on other workstreams

- **05 (opsv3):**
  - F1/F2/§2.4.3, the single declared trigger;
  - §2.4.2, a service-loop registry;
  - C5 and C6, a "requires memdb warm" gate;
  - F14, cancellation inside an item;
  - a list-and-run surface for API-only ops;
  - op-row retention for 5-minute pollers (C14).

  The census table is 05's input.
- **01 (legacy):** D1 (the v1 reaper) and the v1 keyspace deletion that `operations.backfill-legacy-status` depends on.
- **03 (dedup):** C10, C13 and the table-C rows `scheduler.dedup-llm-review` and `maintenance.author-dedup-scan`. Coordinate if the `/dedup` page retirement removes `dedup.series-*` UI triggers.
- **07 (design):** the scheduler package split (§2.4.3).

## 7. Open questions for the owner

**Q1. Which ID survives for the nine `scheduler.*` twins?**
Recommended: `maintenance.*`, with `scheduler.*` kept as `FormerIDs`.
- 7 of the 9 surviving bodies already delegate to the shared Server code.
- Whichever ID survives, it must carry `settings.manage`.
- The duplicate `scheduler.*` bodies in `extra_ops.go`, a 1,116-line file, go away.
- The aliases keep old rows and the tasks page working.

**Q2. Should `maintenance.file-integrity-check` and `maintenance.orphan-book-files-cleanup` actually run nightly?** Their defs claim they do, and they never have.
Recommended: yes, as TaskScheduler tasks inside the maintenance window. Both are report-only. *(Coordinator verified the orphan op cannot delete or modify `book_file` rows. Its only store access is the 3-method read interface `orphanFileScanner`, `delete:true` errors out, and no caller of `DeleteBookFilesByIDs` exists in either orphan op. Add `Permissions: settings.manage` to both defs in the same PR.)*

**Q3. Batch poller: should it be an op every 5 minutes, or stay an inline loop?**
Recommended: keep it inline as a v3 "service loop" and delete `maintenance.batch-poller`. 288 op rows a day add noise and no value.

**Q4. Backup retention.** Today the scheduled cleanup deletes `.bak-*` files after `purge_soft_deleted_after_days`.
Recommended: switch to `backup_retention_days`, as the twin does, and say so in the changelog. First check that the two values differ in prod; if they don't, nothing changes.

**Q5. Table C near-duplicates.**
Recommended: decide each one in its own small PR after P4. Default to keeping the newer op, which holds the scan stand-down, and moving the older job to `retiredOpIDs`.

**Q6. Should `operations.backfill-legacy-status` stay until the v1 keyspace is deleted?**
Recommended: yes. Today it is a cheap regression detector.
