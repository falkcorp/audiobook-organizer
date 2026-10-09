<!-- file: docs/proposals/2026-10-holistic/05-operations-v3.md -->
<!-- version: 1.2.0 -->
<!-- guid: fe9f7129-1fc9-41d5-9d0c-1fec611d8147 -->
<!-- last-edited: 2026-10-08 -->

# 05 — Operations v3

Status: complete; census applied at 04 v1.1.0, design (07) hand-offs applied (analyst `opsv3`).

> **Coordinator note (08, 2026-10-08).** Amended by the coordinator: F9's reader list is replaced by 01 M3's HEAD re-measure; R19 is scoped so adapted v2 defs cannot crash-loop startup; §3.9 gains the four requirements workstream 02 sent (they never reached this analyst); the fix-library-states risk row is corrected; and two port rules are added for wave 12E (existing `book_file` delete sites, and in-process audio decode). Details in `08-integrated-roadmap.md` §3.

Measured at HEAD `f7211eb39` in the worktree `aorg-holistic`. Every count below
was taken with the command next to it. Paths are repo-relative.

## 1. Summary

- **Five frameworks run "operations" today** (registry, `internal/maintenance` jobs, TaskScheduler, Repairs engine, childop). A minimal new op takes 4 files, ~110 lines and 3 names (§2.1). v3 brings these together under one SDK (`pkg/ops`) with four kinds: Task, Batch, Fixer and Pipeline. Only `ID`, `Title` and `Effects` are required.
- **Evolve the executor, replace the authoring surface** (§3.2). The registry's dispatcher, worker, resume and stand-down code carries 57 post-incident fixes. It stays in place and gains typed states, a fence and `opv3:` records. An adapter runs unported v2 defs on the same executor, so the `library.scan` key is never split.
- **30 findings checked at HEAD** (§2.2). Among them: 14 cron-only schedules with no timer path (F1, census F2); four status classifiers that disagree (F2); a timeout recorded as a cancel (F3); writes after cancel and a slot freed while the goroutine still runs (F4, F5); 5 truly sequential `RunItems` calls (F7); and `oplint` dead and absent from CI (F29). The memory note "RootDir gates 105 ops" is stale; that gate is fixed (F8).
- **Writes go through a fenced Writer:** fence → intent (write-ahead) → write → history, with "previous" captured inside the critical section and no delete method. Preview is structural, the default for every writer. Any writer can require an approved plan, stored on the run with the approver and a verified auth method (§3.5-3.6).
- **One 12-state machine** is generated into Go and TypeScript. It includes `stopping` (holds the key until the goroutine exits), `timed_out` and `superseded`, and replaces the four classifiers (`state-and-persistence.md`).
- **Batches are parallel by default** (`Concurrency` defaults to CPU). Counters belong to the runner. Resume legality comes from the source order (Snapshot/AppendOnly/Unordered), not from per-call opt-in (§3.7-3.8).
- **One schedule declaration** replaces cron fields plus TaskScheduler entries. `maintenance.window` becomes a generated Pipeline. Census endpoints (exact totals) and timeline endpoints (time windows) have different names (§3.10-3.11).
- **Registration fails early:** `oplint` is rewritten as `go/analysis` and runs in `make ci`. One catalog (`internal/opscatalog`) holds every op. Startup is refused for a ledger ID that resolves to nothing, an empty `Permission` (R19), or an ops `Deps` field typed as `database.Store` (R21). Ops wait for the store readiness signal (R20).
- **Persisted state:** migration 065 adds `opv3:` with every op id unchanged. `opv2:` is dual-written until PR 16, so rollback means deploying the old binary. `ops export-v2` covers rollback after the mirror is removed.
- **About 30 PRs:** PR 0-11 build the platform, waves 12A-H port ops family by family, and PR 13-16 retire the old paths. Waves begin with read-only reports and end with `library.*`. 216 of 234 defs are ported; 13 are deleted per the census, and 5 stay on a frozen `v2compat` allowlist (2 write-back ops under the ban, 2 census conditionals, `repair-library-state` pending the owner). iTunes op bodies stay untouched (`migration-guide.md` §3, `implementation-briefs.md`).

## 2. Findings

### 2.1 Map of the current system

**Packages and sizes** (`find internal/operations -name '*.go' -not -name '*_test.go' | xargs wc -l`):
10,541 non-test lines. The registry package alone (`internal/operations/registry/`) is
~9,300 of them; `registry.go` is 1,654, `worker.go` 907, `reporter_db.go` 874,
`scan_standdown.go` 583, `resume.go` 539. The public SDK `pkg/plugin/sdk/` is 662 lines
and is almost entirely type aliases onto the registry (`pkg/plugin/sdk/operation.go:8`
`type OperationDef = registry.OperationDef`), so the "stable SDK" and the internal
backplane are the same type.

**There are four op-adjacent frameworks, not one:**

| layer | where | role today |
|---|---|---|
| v1 `operation:` rows | `internal/database/pebble_store_operations.go` | write-dead since 2026-08-23, still read (see F9) |
| v2 registry ("UOS") | `internal/operations/registry/` | the real executor: queue, dispatcher, workers, watchdog, resume |
| `internal/maintenance` job registry | `internal/maintenance/job.go` (447 lines) + `jobs/` (44 files) | a second plug-in framework run *inside* one v2 op (`maintenance.job`), with its own `Policy()`, `DefaultParams()`, context-carried op id and params |
| `internal/scheduler` TaskScheduler | `internal/scheduler/` (3,935 non-test lines) | the only thing that actually runs ops on a timer; 31 `TaskDefinition`s (`grep -c 'Name: *"' internal/scheduler/tasks.go`) |
| Repairs engine | `internal/repairs/` (3,908 non-test lines) | trial → approve → apply for fixers; plans and applies run as `repairs.plan` / `repairs.apply` ops |

**Registration paths: five.** (1) `maintenanceplugin.New(server).Register(...)` inline in
`internal/server/server.go:754-763`; (2) `opRegistrars` filled by `addOpRegistrar` from
`init()` in 27 files under `internal/server` (`grep -rln 'addOpRegistrar(' internal/server | grep -v _test | wc -l`);
(3) container plugins blank-imported through `internal/plugins/plugins.go` and registered in
`PostInit`; (4) `scheduler.ExtraOpsRegistrar` methods (`internal/scheduler/extra_ops.go`, 12
`OperationDef{}` literals); (5) the Repairs fixer registry
(`internal/plugins/maintenance/plugin.go` `Repairs()`), whose fixers are not ops themselves.
Errors from (1) and (2) are collected and `opRegistrationGate()` refuses to start
(`internal/server/server_lifecycle.go:61-67`).

**Inventory.** 205 `OperationDef{` literals in 162 non-test files
(`grep -rn --include='*.go' 'OperationDef{' internal cmd | grep -v _test.go | wc -l`); the
append-only ledger `internal/server/testdata/op_ids.golden` lists 247 IDs including
FormerIDs aliases (`grep -v '^#' … | grep -c .`). Analyst `census` owns the per-op inventory
(workstream 04).

**Lifecycle and state machine (as implemented).** Status is an untyped `string` on
`database.OperationV2Row.Status` (`internal/database/iface_ops_v2.go:57`). Statuses actually
minted: `queued`, `waiting_deps`, `running`, `completed`, `failed`, `canceled`,
`interrupted_quiesced`, `interrupted_dropped`, `interrupted_ask`
(`internal/operations/registry/registry.go:1631` `interruptedStatus`, `resume.go:534`);
`interrupted` and `interrupted_restart` are legacy spellings still accepted on read
(`registry.go:1290-1294`). The paths:

```text
EnqueueOp ──► (Requires unmet) waiting_deps ──DepsScheduler──► queued
         └──► (Batchable) in-memory bucket ──timer──► queued     (no op id returned)
queued ──dispatcher gates (plugin cap, ConcurrencyKey, Writes∩Writes, DependsOn,
          scan stand-down 3.5, pause)──► claimed ──CAS SetOperationV2StatusIfQueued──► running
running ──Run returns nil──► completed
        ──Run returns err──► failed
        ──ctx canceled by user / timeout / watchdog stuck──► canceled   (timeout is NOT distinct)
        ──quiesced by stand-down / shutdown──► interrupted_<policy>
        ──goroutine ignores ctx > 5s──► "abandoned": status written, slot freed, goroutine keeps running
boot: resumeAfterStartup reads queued|running|interrupted_quiesced
        ResumeRestart → requeueInPlace with checkpoint merged into params
        ResumeRequeue → new row (new ULID); old row interrupted_dropped "requeued: original op replaced"
        ResumeDrop    → interrupted_dropped (terminal)
        ResumeAsk     → interrupted_ask (waits for POST /operations/v2/:id/retry)
```

**Developer experience — "hello world" today.** The SDK doc's own minimal example is 30
lines of which 17 are the def literal (`pkg/plugin/sdk/doc.go:17-60`). A real small op:
`library.size-refresh` (`internal/server/library_size_refresh_op.go`, 83 lines): 16 lines of
def boilerplate (`ID … Capabilities`), a `Run` closure, and an `init()` with `addOpRegistrar`.
To make it run on a timer it also needs a 20-line `TaskDefinition` in
`internal/scheduler/tasks.go:451-470` and a line in the `taskV2DefIDs` map
(`internal/scheduler/maintenance.go:163`), and one line appended to
`internal/server/testdata/op_ids.golden`. **Total: 4 files, ~110 lines, three names for one
op** (`library.size-refresh`, task `library_size_refresh`, legacy label
`library-size-refresh` passed to `v2ScheduledOp`). A plugin op such as
`maintenance.file-integrity-check` (`internal/plugins/maintenance/integrity_check.go`, 105
lines) also touches `internal/plugins/maintenance/plugin.go:151` and the golden file. Every
author must answer 15 required-or-defaulted questions (ID, Plugin, DisplayName, Description,
Liveness, ResumePolicy, Priority, ConcurrencyKey, Cancellable, Isolate, Timeout,
ProgressTimeout, Capabilities, Permissions, Writes) before writing one line of work.

### 2.2 Findings table

| ID | Finding | Evidence | Conf. | Impact |
|---|---|---|---|---|
| F1 | **`OperationDef.Schedule` (cron) has no timer path at HEAD.** No cron parser exists; the field is only copied to `op_definitions_v2` and used as a dedupe hint. 19 defs set it. For each, the def-ID string was grepped across `internal/`, `cmd/`, `web/src/`, `configs/` (script in §2.3), and against the 33 `EnqueueOp` targets of `internal/scheduler/tasks.go` (which is also the job list `maintenance.window` walks). **13 of the 19 have no timer path** (census 04 F2 counts 14: it adds `deluge.protected-paths-sync`, which this list omits: `grep -rn protected-paths-sync internal cmd web/src` finds only its own def file and a comment in `internal/plugins/plugins.go:23`, so it has no timer path either; v3 uses 14) — they are reachable only by a manual `POST /operations/v2` or by name from another op: `maintenance.file-integrity-check`, `.metadata-refresh`, `.tombstone-cleanup`, `.cleanup-old-backups`, `.trash-cleanup`, `.purge-deleted`, `.temp-file-cleanup`, `.db-optimize`, `.orphan-book-files-cleanup`, `.series-prune`, `.author-dedup-scan`, `.author-split-scan`, `.batch-poller`. Nine of those have a scheduled twin under another ID (`scheduler.purge-deleted`, `scheduler.temp-file-cleanup`, `scheduler.db-optimize`, `dedup.series-prune`, …; `internal/scheduler/tasks.go:906-1168`), and the `batch_poller` task calls `PollBatches` directly, outside the op system (`tasks.go:1252-1257`). The 6 that *are* scheduled run on the TaskDefinition's cadence, which can contradict the cron (`maintenance.purge-old-logs` declares `"0 2 * * 0"`, `internal/plugins/maintenance/cleanup.go:308`; the task runs it every 7 days from the interval clock, `tasks.go:1292`). | `grep -rn 'robfig\|cronexpr\|gocron\|ParseStandard' --include='*.go' internal cmd pkg` → 0; `grep -n cron go.mod` → 0; only readers `registry.go:707,848`; `grep -n 'EnqueueOp(' internal/scheduler/tasks.go` | high (no timer path); census owns whether the twins are duplicates | declared schedules are fiction; duplicate op pairs |
| F2 | **Status vocabulary has four disagreeing classifiers.** `database.isTerminalV2Status` and `registry.isTerminalStatus` say terminal = completed/failed/canceled/interrupted_dropped; `registry.IsTerminalStatus` (exported, same package) says terminal = completed/failed/canceled + every `interrupted*`; the frontend `isOperationTerminal` uses the prefix rule; the TS `OperationV2Status` union lacks `waiting_deps` and has `interrupting`/`interrupted`, which the backend never mints. | `internal/database/pebble_store_ops_v2.go:1274`; `internal/operations/registry/registry.go:1230`; `legacy_op_status.go:164`; `retry.go:29`; `web/src/services/api.ts:523-563` | high | resumable runs shown as finished; pollers spin or stop early |
| F3 | **Timeout is recorded as `canceled`.** `finalStatusForCanceledRun` returns `"canceled"` for `DeadlineExceeded`; `recordRunMetrics` has a `"timeout"` case that can never fire, so timeouts count as cancels, not failures. | `internal/operations/registry/worker.go:882-888`, `:674-683` | high | alerts on `operations_failed_total` miss timeouts |
| F4 | **Cancel is cooperative only, and abandonment leaves a writer running.** After `defaultAbandonGrace` = 5s the row is written terminal and the worker slot freed while the goroutine keeps executing (`worker.go:33`, `:553-590`). Nothing fences its writes. The 2026-10-01 fragment apply kept retiring books after cancel until a restart. | `worker.go:553-590`; memory `project_fragment_apply_2026_10_01_incident.md` | high | "canceled" rows with writes after the cancel; a second run can overlap the zombie |
| F5 | **Repairs apply checks cancel only between partitions.** `RunApply` hands RunItems whole partitions; the inner `for _, planned := range part` has no `ctx.Err()` check, and `Writer.Modify` takes no ctx, so a cancel is honored only when a partition finishes. | `internal/repairs/engine.go:667-672`; `internal/repairs/writer.go:126`; `grep -n 'ctx.Err' internal/repairs/engine.go` → 0 | high | F4's incident shape, still present at HEAD |
| F6 | **Declared cancellability is not verified.** `library.size-refresh` declares `Cancellable: true` but its work `calculateLibrarySizes(rootDir, folders)` takes no ctx. 25 defs declare `Cancellable: false`. | `internal/server/library_size_refresh_op.go:31,49`; `internal/server/server_helpers.go:86`; `grep -rn --include='*.go' -E 'Cancellable: *false' internal pkg \| grep -v _test \| wc -l` → 25 | high | cancel button that does nothing |
| F7 | **`RunItems` is sequential unless the caller says otherwise, and the Label closure runs inside workers.** `Concurrency < 1` is clamped to 1 (`run_items.go:178`). At HEAD there are 122 non-test `RunItems(` calls; a paren-matching scan (§2.3) found 10 whose call text has no `Concurrency`. Read by eye: 5 pass an options helper or variable that does set it (`acoustid/backfill.go:391` → `backfillRunOptions` `:208`; `acoustid/window_backfill.go:582,591` → `windowRunOptions` `:659`; `metafetch/asin_backfill.go:557,588` → `options(…, workers)` `:527`). **5 are sequential:** `acoustid/lsh_backfill.go:109` (every book file; its plain `int` tallies are safe only *because* it is sequential), `acoustid/reset_all.go:182` (dedup candidates), `acoustid/reset_all.go:111` (mock/sqlite fallback, comment says test-only), `deluge/path_update.go:118` (one book's versions, small), `deluge/centralization.go:104` (deliberate, commented). The CLAUDE.md exemplar now sets the field. | `internal/operations/registry/run_items.go:176-180`, `:62-71`; scan script in §2.3 | high | single-core full-library jobs; tallies that become races the day someone adds `Concurrency` |
| F8 | **RootDir no longer gates registration — fixed.** The memory note says 105 maintenance ops vanish when RootDir is unset; HEAD registers the maintenance plugin unconditionally and collects errors. | `internal/server/server.go:745-763`; memory `project_operations_registry_state.md` (stale) | high | closed; v3 keeps the "startup refuses on registration error" property |
| F9 | **v1 is write-dead, read-alive.** Zero `CreateOperation(` writers; readers remain. *Coordinator: the list below came from the 2026-09-07 note and is stale. At HEAD there are no readers in `handlers/system/handler.go`, `diagnostics/service.go` or `reconcile_ops_index.go`. The HEAD list is 01 M3: the reaper, `pause.go:124`, `CancelOperation`, `sysinfo/service.go:324`, three history bridges, the retention purge, `legacy_op_status.go` and `legacy_backfill.go`. Retirement is 01 P3 plus 01 P74.* Original list: (`sysinfo/service.go`, `handlers/system/handler.go`, `diagnostics/service.go`, `metabatch/fetch_ops_index.go`, `server/reconcile_ops_index.go`, `server_lifecycle.go`, `handlers/operations/handler.go`, `registry/legacy_backfill.go`). The `operation:`, `operationlog:`, `opstate:<id>:params` families are v1; `opchange:`, `op_result:`, `opsummary:`, `opstate:<id>` (checkpoint) are live and used by v2 runs. The frontend still types 26 API functions as `Promise<Operation>` (the v1 shape) and maps v2 rows into it (`api.ts:2702`). | memory `project_operations_v1_retirement_state.md` (re-measure in PR 0); `internal/database/keyfamilies.go:221-243`; `grep -rn 'Promise<Operation>' web/src/services/*.ts \| wc -l` → 26 | medium (reader list from 2026-09-07 memory, keyspace list high) | two shapes on the wire; workstream 01 overlap |
| F10 | **The ledger is written by hand at 64 sites with no ordering contract.** `CreateOperationChange(` 64 non-test sites, `CreateOperationResult(` 25, `RecordMetadataChange(` 36. Two ledger-before-write bugs were found and fixed (#3437, `recordIDChange`). Only `repairs.Writer` enforces "history after the write" by construction (`internal/repairs/writer.go:48-58`). | `grep -rn --include='*.go' 'CreateOperationChange(' internal \| grep -v _test \| grep -v mock \| wc -l`; memory `project_ledger_before_write_bug_class.md` | high (counts), medium (no further instances checked) | history that claims writes that never landed |
| F11 | **Resume cursors are unsafe when items can appear below the cursor.** The activity migration's `digest` tier writes backdated keys and must never resume (`activityNonResumableTiers`). The SDK has no way to declare "this source is not append-only". | memory `project_activity_migration_digest_tier_hazard.md`; `internal/database/sql_activity_progress.go` | high | silent skips reported `clean` |
| F12 | **Timeline is a window, not a census.** `GET /operations/timeline` reads `since` (default 15m), caps the scan at 5,000 and the page at 1,000; it is a census only when `truncated=false && scan_capped=false && matched<limit`. There is no `GET /operations/v2` list. A delete sized off it removed 70 rows instead of 1 (since then `DELETE /operations/history` was retired, `wire_operations_routes.go:87`). | `internal/server/wire_operations_routes.go:24-35`; memory `project_operations_timeline_is_not_a_census.md` | high | wrong counts presented as totals |
| F13 | **Stand-down vs resumed scan — fixed; general abandon hole narrowed.** Root cause was un-checked ctx in scan discovery (#3085). `parked` is now closed by a deferred close at the goroutine's true exit (`worker.go:497-501`), so a waiter is released when the zombie really exits, but an abandoned holder still keeps the stand-down waiter blocked until that exit or the 5m lease. | `worker.go:497-501`; memory `project_scan_standdown_fails_on_resumed_scan.md` | medium | closed for scans; class remains for any op ignoring ctx (F4) |
| F14 | **"Registered in tests" ≠ "registered in prod" — fixed with a guard.** `TestEveryRegistryPluginIsLinkedIntoTheBinary` runs `go list -deps` on the main package. Still, the five registration paths each need their own guard. | `internal/plugins/plugins_wiring_test.go:23`; `internal/plugins/plugins.go:6-16` | high | v3: one registration path makes the guard total |
| F15 | **Dry-run default is a convention enforced by a param-shape lint.** Owner rule 2026-09-25: omitted mode = preview (`internal/operations/opmode/dryrun.go:8-24`). Each op decodes `*bool` itself; `guard_test.go` forbids a plain `bool`. | `internal/operations/opmode/dryrun.go` | high | v3 makes mode a framework field |
| F16 | **The bulk-apply gate and the prod apply review gate are policy outside the op system.** `internal/applygate` (4 legs since #3380) gates three entry points; "dry-run then human approval" is enforced by people and by the Repairs engine's stored plan + explicit row ids. No generic "apply requires an approved plan id" exists for non-Repairs ops. | memories `project_bulk_apply_gate_is_three_legs_only.md`, `feedback_prod_apply_review_gate.md`; `internal/repairs/fixer.go:12-24` | high | ad-hoc approval per op |
| F17 | **The Repairs engine already is the right trial → approve → apply shape** (stored plan, fingerprint re-check `changed_since_plan`, owner grants, Writer without delete, stand-down with lease renewal per write) but it is bespoke to `repairs.*`; other preview/apply ops (`metadata.batch-apply-cached`, `version-group-primary-repair`, 30+ `dry_run` ops) re-implement fragments of it. | `internal/repairs/engine.go`, `fixer.go`; `grep -rln 'opmode.ResolveDryRun' internal \| wc -l` (see §2.3) | high | duplicated safety logic |
| F18 | **Most declared features are unused; the def is wider than its users.** Across non-registry, non-SDK code: `Isolate: true` 0 (subprocess runner, 327 lines, wired in `cmd/child_mode.go`, unused); `ParamsSchema` 0; `RunsAs` 0; `Synchronous: true` 0; `reporter.Trigger` 0; `RunPhase` 1; `Phases:` 3; `Triggers:` 2; `Batchable` 2; `Requires:` 4; `DependsOn:` 5 (grep counts include comments; census CSV `writes` column: 27 of 234 defs declare a write set). | `for f in …; do grep -rn --include='*.go' -E "$f" internal pkg \| grep -v _test.go \| grep -v internal/operations/registry/ \| grep -v pkg/plugin/sdk/ \| wc -l; done` (§2.3) | high | surface to delete or redesign |
| F19 | **Write-set conflict gate is opt-in and coarse.** `Writes` is declared on 27 of 234 defs (census `ops-census.csv`, `writes` column); an undeclared op is invisible to Gate 3b; granularity is the whole table. | `internal/operations/registry/types.go:142-170`; count in F18 | high | lost updates between undeclared ops (the 2026-08-07 incident class) |
| F20 | **Params are untyped at the boundary.** `Run func(ctx, json.RawMessage, Reporter)`; every op unmarshals by hand; `ParamsSchema` is never set, so `POST /operations/v2` validates nothing and the UI cannot render a form. | `types.go:46`; F18 | high | malformed params fail at run time, after queueing |
| F21 | **Reporter is a 8-method interface plus four side-interfaces discovered by type assertion** (`OpID`, `TouchLiveness`, `SetResult`, `InvalidateLibraryStats`), each added to avoid "twenty-four edits". Fakes silently lose them (`ReporterOpID` returns ""). 16 hand-written fake reporters in tests. | `internal/operations/registry/reporter.go:20-120`; `grep -rn 'type fakeReporter\|type stubReporter\|…' --include='*_test.go' internal pkg \| wc -l` → 16 | high | behavior differs between test and prod |
| F22 | **Progress is untyped `(current, total int, message string)`.** Ops that cannot know a total send `0/0` (activity backfill: "There is no denominator"); phases are encoded into the message string; `sdk.Progress` adds a +2 step scheme to avoid 0/0. | `reporter.go:22`; `pkg/plugin/sdk/progress.go:9-30`; memory digest note | high | no ETA, unparseable progress |
| F23 | **Metrics are thin.** Four counters + one duration histogram per def (`internal/metrics/metrics.go:18-50`), wired only on 2026-10-03; no queue-depth, in-flight, abandoned, items-processed, item-error, checkpoint-age or stand-down-wait series. Interrupted outcomes are not counted. | `worker.go:670-683` | high | Grafana cannot answer "is it stuck" |
| F24 | **Three naming schemes for one op** (def ID, scheduler task name, legacy label) joined by a hand-kept map `taskV2DefIDs`. | `internal/scheduler/maintenance.go:160-187` | high | drift; `isTaskRunning` silently false for an unmapped task |
| F25 | **Spec anchor is stale.** `registry/types.go:7` cites `docs/superpowers/specs/2026-05-04-unified-operations-system.md`, which does not exist at HEAD; `docs/AI-REFERENCE.md:103-105` still documents a v1 `queue.go` that is gone. | `find . -name '*2026-05-04*'` → none; `ls internal/operations` | high | onboarding reads fiction |
| F26 | **Churn evidence.** 92 commits touched `internal/operations/registry` since 2026-08-01, 57 with `fix` in the subject (double-run from stale snapshot, resume undone by stale checkpoint, shutdown status, abandoned-cancel reason, pause marker restore, completed_at stamping, …). | `git log --oneline --since=2026-08-01 -- internal/operations/registry \| wc -l`; `… \| grep -i fix \| wc -l` | high | the executor's invariants live in comments and post-incident patches |
| F27 | **Maintenance jobs are a framework inside an op.** 44 job files run through one v2 op `maintenance.job`; they take op id and params from context values (`internal/maintenance/job.go:17-60`), which the compiler cannot see (the #2784 regression broke resume for six jobs). | `ls internal/maintenance/jobs \| grep -v _test \| wc -l` → 44; memory v1 note | high | invisible dependencies |
| F28 | **Concurrency hotspots audit: items 1-5 fixed, item 6 open.** `internal/dedup/auto_resolve.go` still has no `RunItems`/`errgroup`/`go func`. | `grep -n 'RunItems\|errgroup\|go func' internal/dedup/auto_resolve.go` → 0; `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md:8-30` | medium (whether it is hot at prod size is unmeasured) | v3 default concurrency removes the class for ported ops |
| F29 | **The op import lint is dead.** `make oplint` passes `./internal/plugins/...` to a tool that `lstat`s its arguments, so it exits on the first argument; it is in neither `make ci` (`Makefile:668`) nor any workflow (`grep -rn oplint .github` → 0). Run on the directory, it reports 538 forbidden imports. | `go run ./tools/cmd/oplint ./internal/plugins/...` → `lstat … no such file`; `go run ./tools/cmd/oplint ./internal/plugins 2>&1 \| grep -c forbidden` → 538; `Makefile:446-449` | high | a lint that looks like a guard and is not one |
| F30 | **Resume policies skew to "drop".** Census `resume` column: drop 171, restart 38, requeue 22, ask 3, of 234. A deploy discards most in-flight work, including whole-library batch writers (`acoustid.lsh-backfill` is `ResumeDrop`, `internal/plugins/acoustid/lsh_backfill.go:63`). The watermark machinery that would make them resumable exists (`run_items.go:73-100`) but is opt-in per call. | `04-operations-census/ops-census.csv` | high | deploys cost hours of rework |

### 2.3 Commands used (for re-verification)

```sh
# F7 RunItems without Concurrency (python, prints total, missing, sites)
python3 - <<'EOF'
import subprocess,re
files=subprocess.check_output(["grep","-rln","--include=*.go","RunItems(","internal","pkg"],text=True).split()
tot=0;miss=[]
for f in files:
    if f.endswith('_test.go'): continue
    src=open(f).read()
    for m in re.finditer(r'RunItems(\[[^\]]*\])?\(',src):
        ls=src.rfind('\n',0,m.start())+1; pre=src[ls:m.start()]
        if 'func ' in pre or pre.strip().startswith('//'): continue
        tot+=1; i=m.end(); d=1
        while i<len(src) and d>0:
            d+= {'(':1,')':-1}.get(src[i],0); i+=1
        if 'Concurrency' not in src[m.start():i]:
            miss.append(f"{f}:{src.count(chr(10),0,m.start())+1}")
print(tot,len(miss)); print('\n'.join(miss))
EOF
# F1 cron defs vs timer paths
grep -rn --include='*.go' -E 'Schedule:\s+&' internal pkg | grep -v _test
grep -n 'EnqueueOp(' internal/scheduler/tasks.go
# F18 feature usage
for f in 'Isolate: *true' 'Phases:' 'Triggers:' 'Batchable: *true' 'Requires:' 'DependsOn:' 'Writes:' 'ParamsSchema:' 'RunsAs:' 'Synchronous: *true'; do
  echo "$(grep -rn --include='*.go' -E "$f" internal pkg | grep -v _test.go | grep -v internal/operations/registry/ | grep -v pkg/plugin/sdk/ | wc -l) $f"; done
```

## 3. Proposed specification

The full SDK sketch is in [`05-operations-v3/sdk-api.md`](05-operations-v3/sdk-api.md), the
state machine and keyspace in
[`05-operations-v3/state-and-persistence.md`](05-operations-v3/state-and-persistence.md), the
worked examples in [`05-operations-v3/examples.md`](05-operations-v3/examples.md), the
porting rules in [`05-operations-v3/migration-guide.md`](05-operations-v3/migration-guide.md)
and per-PR briefs in
[`05-operations-v3/implementation-briefs.md`](05-operations-v3/implementation-briefs.md).
This section states the requirements and the decisions.

### 3.1 Requirements (each traced to a finding)

| R | requirement | from |
|---|---|---|
| R1 | One declaration of when an op runs; the declared schedule is the schedule. | F1, F24 |
| R2 | One typed run state machine; every classifier (Go and TS) generated from it. | F2, F3 |
| R3 | A canceled, timed-out, abandoned or lease-lost run can no longer write, and its exclusive key stays held until its goroutine really exits. | F4, F5, F13 |
| R4 | Cancel is checked by the runner between items and by the Writer on every write; "cancellable" is verified by a test, not declared. | F5, F6 |
| R5 | Batch work is parallel by default; sequential needs a reason; counters are runner-owned atomics. | F7, F28 |
| R6 | Every store write goes through a Writer that orders intent → write → history, captures "previous" inside the critical section, and has no delete. | F10 |
| R7 | Resume legality is declared by the source (`Snapshot`/`AppendOnly`/`Unordered`) and fault-tested. | F11, F30 |
| R8 | Census and timeline are different endpoints with different names; census totals are exact. | F12, F23 |
| R9 | One registration path; a def that is not linked, duplicated, or missing from the ID ledger fails CI or startup. | F8, F14, F29 |
| R10 | Preview is the default mode of every writer, enforced by the Writer, not by params. | F15 |
| R11 | Any writer can require an approved plan; the approval (plan, rows, approver, verified auth method) is stored on the run. | F16, F17 |
| R12 | Params are typed and schema-validated before queueing; the UI form and API docs are generated. | F20 |
| R13 | The run context is one concrete type; no capability discovered by type assertion. | F21 |
| R14 | Progress is structured: phase, done, total-or-unknown, unit, counters, rate, ETA. | F22 |
| R15 | Metrics cover queue depth, in-flight, items, errors, zombies, fenced writes, checkpoint age, schedule lag. | F23 |
| R16 | Parent/child pipelines with typed hand-off and derived liveness. | `childop` (`internal/operations/childop/follow.go:6-30`), workstream 02 |
| R17 | Features nobody uses are not carried forward (subprocess isolation, `RunsAs`, `Synchronous`, event `Triggers`, `reporter.Trigger`). | F18 |
| R18 | Op ids never change across the migration; every key family that hangs off an op id keeps working. | §4 persisted state |
| R19 | Every def declares a `Permission`; startup refuses one without. *Coordinator: at HEAD 147 of 198 `OperationDef` literals declare no `Permissions`, which is every def under `internal/plugins/*`. Only `scheduler` (12 of 12) and `server` (39 of 39) declare them. So R19's startup refusal applies to **native v3 defs only** until wave 12H. Adapted v2 defs get the default from 08's early PR X2 (an empty list means `settings.manage` on the generic trigger route). Otherwise PR 5 or PR 7 would refuse to boot.* | census 04 §2.2: surviving `maintenance.*` twins declare none; coordinator count (08 §2 f) |
| R20 | Ops that read the store wait for a readiness signal instead of racing the memdb warmup (~130-200 s per 07). | workstream 07 (readiness PRs R2-R4) |
| R21 | Op dependencies are narrow capability interfaces declared by the consuming package, never `database.Store`; domain packages register through one `Ops(Deps) Bundle`. | workstream 07 (`indexedStore` assertions 3→14; `plugins/maintenance` split) |

### 3.2 The central decision: evolve the executor, replace the authoring surface

- **Keep** `internal/operations/registry` as the executor ("runtime") at its current path.
  Its dispatcher, CAS start, resume sweep, stand-down and pause gate carry 57 incident fixes
  since 2026-08-01 (F26). Rewriting them would re-open those incidents.
- **Replace** what authors touch: the 16-field `OperationDef` literal, the `Reporter`, the
  hand loop, the hand checkpoint, the hand dry-run param, the hand schedule, the hand ledger.
  That is the new SDK `pkg/ops`.
- **Bridge** with an adapter that runs an unchanged v2 `OperationDef` as a v3 definition, so
  from the adapter PR onward there is exactly **one** executor, one queue and one
  exclusive-key map. Two engines side by side would split the `library.scan` key across
  them, which the charter forbids.

### 3.3 Definition model

Four kinds (`sdk-api.md` §1): **Task** (one function), **Batch** (source + per-item function;
the runner owns the loop), **Fixer** (candidates → evaluate → approve → apply; the
`internal/repairs` engine generalized), **Pipeline** (DAG of child defs). Only `ID`, `Title`
and `Effects` are required; the kind supplies defaults for timeout, resume, cancel contract,
concurrency, permission and exclusive key. A def is opaque (built by constructor, never a
struct literal), so a default can change without touching 234 call sites.

### 3.4 State machine

Twelve states, one table, one transition function, CAS on `(state, attempt, fence_epoch)`
(`state-and-persistence.md` §1). New compared with v2: `stopping` (a stop was requested and
the goroutine is still alive; slot and key still held; writes refused), `timed_out`
(distinct from `canceled`), `superseded` (closed in favor of a successor), and
`awaiting_decision` as a named state instead of `interrupted_ask`. Every transition is logged
to `opv3:evt:`.

### 3.5 Writes: fence first, then intent → write → history

The `opswriter.Writer` (`sdk-api.md` §3) is generalized from `internal/repairs/writer.go`,
which already records history after the write and has no delete:
1. **Fence check.** Each attempt has a fence epoch; cancel, timeout, abandonment and a lost
   stand-down lease revoke it. A revoked fence makes every Writer call return `ErrFenced`.
2. **Intent (write-ahead).** `opv3:intent:{op}:{seq}` names the subject and effect before the
   store is touched. It is for crash recovery only, and it is never shown as history.
3. **Write.** Inside the store's critical section (`ModifyBook` callback); the previous value
   is captured there.
4. **History (after commit).** `opchange:` / metadata history rows, then the intent is cleared.
A crash between 2 and 4 leaves intents; the recovery report for that run lists those subjects
as "may be partially applied". This is what "journaling done write-ahead and in the correct
order" means here: the write-ahead record is an intent, never a ledger entry, so the
ledger-before-write class (F10) cannot recur through the Writer.

Writes outside the typed surface (a reflink, a settings key) use `Writer.Effect`, which wraps
the same fence/intent/history steps around an op-supplied function.

### 3.6 Preview, approval and the existing gates

- **Preview** is the default mode for every def whose `Effects` write (owner rule 2026-09-25,
  `internal/operations/opmode/dryrun.go:8-24`). In Preview the Writer records each intended
  write as a plan line and refuses it, so "dry run" is enforced by the write path rather than
  by each op honoring a flag. Scheduled writers must say `.Live()`.
- **Approval** (`sdk-api.md` §6): `PlanRequired` means a Live run must name a Preview run of
  the same def and the row ids chosen from it; every row is re-evaluated and refused on a
  fingerprint change. The approver needs the permission and a verified auth method (the
  existing `ActorAuthMethod` path, `internal/database/iface_ops_v2.go:47-54`); the unsigned
  Cloudflare email header is never identity.
- **`applygate`** (`internal/applygate`, four legs since #3380) becomes a `Guard` on the
  metadata apply family, unchanged in logic, so its per-check results land on each plan row
  in the same shape the Repairs lane already shows.
- The prod-apply review gate (a person sees the numbers before an apply) is satisfied
  structurally: an apply that requires a plan cannot run without a stored approval naming
  the plan.
- Every Fixer renders in the Repairs lane of `/review` from def metadata (owner rule
  2026-09-27: every fixer gets a trial → per-row preview → approve → apply UI).

### 3.7 Concurrency and partitioning

`Batch` and `Fixer` default to `CPU()` workers. `Network(n, why)` for rate-limited backends,
`Sequential(why)` when order matters, `FromParam` for operator tuning. `PartitionBy` routes
items with the same key to one worker in source order; the Fixer default is the row id. The
`Label` callback receives the item only, and counters are runner-owned atomics, so the v2
Label race (CLAUDE.md) has nothing left to race on. `opstest` runs every Batch with at least
4 workers under `-race`.

### 3.8 Resume

Resume keeps the op id in every policy (`sdk-api.md` §5). For `Batch` and Fixer apply the
runner freezes the item keys at start (`opv3:snap:`) and resumes from the contiguous
watermark (the v2 `completionTracker` algorithm, `run_items.go:113-140`), so every whole-library
batch is resumable by default (today 171 of 234 defs drop on restart, F30). A source declared
`Unordered` (the activity digest-tier shape, F11) restarts from zero; `ValidateCatalog`
refuses `ResumeContinue` on it. `library.scan`'s own resume, quiesce and stand-down semantics
are carried over unchanged; a deploy still resumes it, and a deploy is still the owner's call.

### 3.9 Pipelines

`Pipeline` stages name child defs, `After` dependencies, a typed `Params` builder from earlier
stage results, optional `PerSubject` fan-out bounded by `FanOut`, optional `Gate` (approval
before a stage) and `OnFail`. The parent's liveness and progress are derived from its
children (absorbing `childop.Follow`), so a parent is neither reaped while a child is healthy
nor kept alive by a wedged one. Standing per-subject prerequisites (`Requires`, `op:deprev:`,
`op:completion:`) carry over as `ops.AfterFor` / `ops.FieldSet`.

**Workstream 02's four requirements (relayed by the coordinator, 2026-10-08; 02 §6 could not reach this analyst):**
1. *Per-subject data prerequisites for stages.* **Met** by `AfterFor` / `FieldSet` plus Pipeline `After`.
2. *A durable per-subject dirty-set source.* **Not met at v1.1.0.** Add `ops.DirtySet(prefix)`: a `Source` whose `Pages` drains `<prefix><subjectID>` keys (the `idx:sidx:dirty:` pattern), `Order: Unordered` (re-marks can land below the cursor), and whose items are cleared only after `Item` succeeds. `identification.advance` is then a **Batch** over the dirty set with `Schedule.Every`, not a `PerSubject` Pipeline: about 11k child rows would bring back the batch-poller noise problem.
3. *Per-provider budget shared across ops.* **Partly met already, outside the SDK.** `internal/metadata/throttle_registry.go` is process-wide and evaluated at call time, but it is a hold-off for errors, not a rate budget. Add one `ops.Budget("audible")` declaration that names the existing provider limiter, so the dispatcher can see contention. Do not add a second limiter.
4. *A "surfaced" terminal state per subject, with a reason, countable in the UI.* **Assigned to 02's `ident` index** (`ident_reason`), not to run state. A Batch item that ends surfaced reports `Skip(reason)`. The countable number lives in the book index, where the goal count also lives.

02 PR 14 ships **first as a v2 op with its own dirty set** (02's stated fallback), so the metadata goal does not wait for this platform. It is ported to the v3 Batch in wave 12F.

**Assumption for workstream 02 (`search`), original text:** the identification pipeline is a per-book DAG —
parse/transcribe-intro → candidate fetch → score → gate → apply — run either for one book or
fanned out over a selection, where each stage is an existing def, the gate is a Fixer-style
approval for anything below the auto-apply threshold, and a stage re-runs only when its
inputs changed (`AfterFor` + `dep_rev`). If workstream 02 needs something else (streaming
between stages, cross-book stages), §6 records it as a dependency to resolve in the roadmap.

### 3.10 Scheduling

`Schedule` (`sdk-api.md` §8) is the only timer: `Every`, `DailyAt`, `Cron`,
`InMaintenanceWindow(order)`, with `OnStart`, `When(config gate)`, `Params`, `Live`. The
scheduler keeps the durable interval clock (`internal/scheduler/interval_clock.go`) and daily
clock (`daily_at.go`) and gains a cron evaluator. `maintenance.window` becomes a Pipeline whose
stages are every def declaring `InMaintenanceWindow`, in `order`. `TaskDefinition`, task names
and `taskV2DefIDs` disappear. **No schedule changes behavior during the port:** each port PR
copies the cadence the op runs on today; a declared cron that does not run today stays
`Disabled` until the owner answers Q2.

### 3.11 Observability

**Metrics** (Prometheus, `internal/metrics`), labels limited to `def` and `outcome` to bound
cardinality:

| metric | type | answers |
|---|---|---|
| `ops_runs_total{def,outcome}` | counter | outcome = succeeded/failed/canceled/timed_out/dropped/interrupted |
| `ops_run_duration_seconds{def,outcome}` | histogram | |
| `ops_runs{def,state}` | gauge from `opv3:meta:counts` | **census**: how many runs are in each state now |
| `ops_inflight{def}` / `ops_zombies{def}` | gauge | is anything stuck in `stopping` |
| `ops_items_total{def,result}` | counter | ok / failed / skipped |
| `ops_item_duration_seconds{def}` | histogram | per-item latency |
| `ops_last_progress_age_seconds{def}` | gauge | the watchdog's view, exported |
| `ops_checkpoint_age_seconds{def}` | gauge | how much a restart would redo |
| `ops_fenced_writes_total{def}` | counter | writes refused after a stop; should stay 0 |
| `ops_intents_unresolved{def}` | gauge | possible partial applies after a crash |
| `ops_standdown_wait_seconds` | histogram | how long writers wait for the scan to park |
| `ops_schedule_lag_seconds{def}` / `ops_schedule_missed_total{def}` | gauge / counter | is the schedule real |

**Logs**: `rc.Log()` pre-attaches `op_id`, `def_id`, `attempt`, `phase`; item logs add
`item`. Values are sanitized the same way v2 does since the 2026-09-13 log-injection fix.
Logs go to the op log (`opv3:log:`) and, for manual runs, to the activity log, as today.

**Traces**: one span per run, one per stage of a pipeline, one per item only when sampled.

**Census vs timeline, made explicit.** Census = `GET /api/v3/ops/runs` and
`GET /api/v3/ops/counts`, exact from indexes, paginated by cursor, `total` always exact.
Timeline = `GET /api/v3/ops/timeline`, a time window that returns `complete: true|false`. The
Grafana dashboard (`deploy/grafana/dashboards/operations.json`, new) reads census gauges from
metrics and never derives counts from the timeline.

### 3.12 Generated API and UI

`GET /api/v3/ops/defs` lists every def with its params JSON Schema, effects, schedule (and
next fire), permission, approval and kind. `POST /api/v3/ops/runs` validates params against
the schema before queueing. `web/src/generated/ops.ts` carries the state union and
predicates. `OpForm` renders any def's params; the Operations page gets a generic "Run…"
action; Fixers appear in the Repairs lane from the same metadata. Bespoke trigger routes
become thin wrappers or go away (37 non-test files under `internal/server` call `EnqueueOp`:
`grep -rln --include='*.go' 'EnqueueOp(' internal/server | grep -v _test | wc -l`).

### 3.13 Test harness

`pkg/ops/opstest` (`sdk-api.md` §12): an in-memory world, a seeded completion order, fault
injection (`CancelAt`, `CrashAt`, `AbandonAt`, `LoseLeaseAt`, `InsertBelowCursor`) and a
`Conformance` test every def gets for one line: preview writes nothing; history follows the
write; nothing is written after the fence; crash + resume skips nothing; progress is
monotone; runs under `-race` with ≥4 workers. This replaces the 16 hand-written fake
reporters, which silently lack the side-interfaces the production reporter has (F21).

### 3.14 Registration that fails early

One list, `internal/opscatalog.All`, imported by the server. CI: the rewritten `oplint`
(`go/analysis`) flags defs not in their package bundle, bundles not in the catalog, store
writes that bypass the Writer, and empty `Sequential` reasons; `TestCatalogLinkedIntoBinary`
runs `go list -deps` on the main package. Startup: `ValidateCatalog` refuses duplicates,
invalid schedules, illegal resume/source pairs, writers without effects, and any ID in the
embedded `op_ids.golden` that resolves to no def, alias or tombstone.

### 3.15 Size of the change for authors

| | v2 | v3 |
|---|---|---|
| trivial op (`library.size-refresh`) | 4 files, ~110 lines, 3 names | 1 file + 1 bundle line, ~25 lines |
| batch op (`acoustid.lsh-backfill`) | 167 lines, sequential, drop on restart | ~40 lines, NumCPU, resumable, fenced |
| fixer (`normalize-letter-l-ordinals`) | 168 lines + shared ops/routes | ~50 lines, generic API |
| required decisions before writing work | 15 fields | 3 (`ID`, `Title`, `Effects`) |

Full comparison, including failure modes: `05-operations-v3/examples.md`.

## 4. Implementation plan

Sizes: S < 300 changed lines, M 300-1,500, L > 1,500. Every PR is behind no feature flag
unless stated; each is revertable on its own because the adapter keeps every un-ported op
running unchanged. Exact per-PR file lists, tests and rollback are in
[`05-operations-v3/implementation-briefs.md`](05-operations-v3/implementation-briefs.md);
the table is the index.

| PR | title | main files | size | depends on |
|---|---|---|---|---|
| 0 | Docs truth pass for operations | `docs/AI-REFERENCE.md`, `internal/operations/registry/types.go` (header), `docs/development/writing-a-plugin.md` | S | — |
| 1 | Typed run state table + generated TS | `internal/operations/state/*` (new), `internal/database/pebble_store_ops_v2.go`, `internal/operations/registry/{registry,legacy_op_status,retry}.go`, `tools/cmd/opsgen/main.go` (new), `web/src/generated/ops.ts` (new), `web/src/services/api.ts`, `Makefile` | M | 0 |
| 2 | `timed_out` status and lifecycle metrics | `internal/operations/registry/worker.go`, `internal/metrics/metrics.go`, `internal/operations/state/*`, `deploy/prometheus/alert-rules.yml` | S | 1 |
| 3 | Fence + hold the exclusive key until exit; per-row cancel in Repairs apply | `internal/operations/registry/{worker,registry,reporter_db,scan_standdown}.go`, `internal/repairs/{engine,writer}.go` | M | 1 |
| 4 | `opv3:` keyspace, migration 065, v2 mirror | `internal/database/{iface_ops_v3,pebble_store_ops_v3,migrations,keyfamilies,pebble_store_ops_v2}.go` | L | 1 |
| 5 | `pkg/ops` SDK + Batch runner + adapter | `pkg/ops/*` (new), `internal/operations/registry/{v3_adapter,batch_runner}.go` (new), `internal/operations/opswriter/*` (new), `internal/repairs/writer.go` | L | 3, 4 |
| 6 | `opstest` harness + Conformance | `pkg/ops/opstest/*` (new) | M | 5 |
| 7 | One catalog, rewritten `oplint`, startup ledger check | `internal/opscatalog/*` (new), `tools/cmd/oplint/*`, `internal/server/{server,op_registrars,server_lifecycle}.go`, `internal/plugins/plugins.go`, `internal/plugins/plugins_wiring_test.go`, `Makefile` | M | 5 |
| 8 | Single scheduler source + cron evaluator | `internal/scheduler/{scheduler,tasks,maintenance}.go`, `internal/scheduler/cron.go` (new), `go.mod` | M | 5 |
| 9 | `/api/v3/ops/*` + census endpoints + v3 SSE | `internal/server/handlers/opsv3/*` (new), `internal/server/wire_ops_v3_routes.go` (new), `internal/server/server.go` | M | 4, 5 |
| 10 | Frontend on v3 (generated types, OpForm, progress, Repairs lane) | `web/src/services/api.ts`, `web/src/stores/{useOperationsStore,operationGrouping}.ts`, `web/src/components/OperationActivityPanel.tsx`, `web/src/pages/ActivityLog.tsx`, `web/src/components/review/**`, `web/src/components/ops/OpForm.tsx` (new) | L | 9 |
| 11 | Grafana operations dashboard + alerts | `deploy/grafana/dashboards/operations.json` (new), `deploy/prometheus/alert-rules.yml` | S | 2, 9 |
| 12A-H | Port waves A-H (`migration-guide.md` §3) | per wave, listed in the briefs | L each | 6, 7, 8 |
| 13 | Retire `internal/maintenance` job framework + `maintenance.job` op | `internal/maintenance/**`, `internal/server/maintenance_job_op.go`, `internal/server/maintenance_dispatcher.go` | M | 12C |
| 14 | Retire `repairs.plan`/`repairs.apply` + `/api/v1/repairs` | `internal/plugins/maintenance/repairs_ops.go`, `internal/server/wire_repairs_routes.go`, `internal/server/handlers/repairs/**`, `internal/repairs/engine.go` | M | 12D, 10 |
| 15 | Delete adapter, `pkg/plugin/sdk`, legacy `RunItems`, `TaskDefinition` | `internal/operations/registry/{v3_adapter,run_items,types}.go`, `pkg/plugin/sdk/**`, `internal/scheduler/tasks.go`, `tools/cmd/sdkguard/**` | L | 12A-H |
| 16 | Remove the `opv2:` mirror; ship `ops export-v2` | `internal/database/pebble_store_ops_v3.go`, `cmd/ops_export.go` (new) | S | 15 + soak (Q4) |

Retiring the `opv2:`/v1 key families after the soak is workstream 01's PR, not listed here.

## 5. Risks and what must not break

| risk / invariant | how v3 keeps it |
|---|---|
| **`library.scan` keeps one exclusive key** (charter ban: never split the scan ConcurrencyKey) | one executor from PR 5 on; the scan def is ported last (wave H) with its key string unchanged; a test asserts every def that shares the key in v2 shares it in v3 |
| scan stand-down semantics (quiesce → park → write → resume; lease renewal per write) | stand-down code is not rewritten; the Writer renews the lease per write, as `repairs.Writer` does since #3650 |
| a deploy resumes the scan; a deploy is the owner's call | unchanged; migration 065 runs at boot and maps `interrupted_quiesced` 1:1 |
| op ids (ULIDs) and everything keyed by them (`opchange:`, `opchange_by_book:`, `op_result:`, `opsummary:`, `opstate:`, `act:op:`) | never re-keyed (R18); revert/undo read the same rows |
| never delete `book_file` rows | the Writer has no delete; `Deletes(...)` needs a declared effect and is flagged by `oplint` for review |
| no iTunes writes, removals or rebuilds; `internal/writeback/` untouched | no port touches `internal/writeback`; iTunes ops keep their bodies; the existing iTunes guards become framework Guards unchanged |
| no audio decoding on the server | `LaneMac` is the only lane allowed to decode; no in-process fallback |
| never run fix-library-states | *Coordinator:* the `fix-library-states` maintenance job was **deleted** on 2026-09-10, and `internal/maintenance/jobs/fix_library_states_test.go` pins its absence. Nothing ports it. `maintenance.repair-library-state` is a different op: dry-run by default, API-only, with an evidence gate (`OrganizedFileHash`) and an iTunes exclusion. It stays on the frozen allowlist with no schedule, and porting it is owner decision 08 §7. |
| owner-manual franchise rule and iTunes path guard on fixers | carried over as framework Guards from `internal/repairs/guards.go` |
| AudioBooth (Swift, external) may call `/api/v1/operations/*` | v1 endpoints stay as adapters until checked (Q6) |
| zombie holds a key forever | `ops_zombies` alert after 10m; the UI offers restart; a restart clears it (boot sweep → `interrupted{crash}`) |
| migration 065 slows boot | batched 500, progress logged per 10k; PR 0 adds the `opv2:` row count to the db census so the cost is known before PR 4 ships |
| a port silently changes a schedule | port checklist: today's cadence is copied; declared crons that never ran stay disabled until Q2 |
| more parallelism exposes races in op bodies | Conformance runs ≥4 workers under `-race`; ops with shared state must partition or declare `Sequential(why)` |

## 6. Dependencies on other workstreams

- **01 legacy/dead code** — owns deleting the v1 `operation:` family and its readers
  (`sysinfo/service.go`, `handlers/system/handler.go`, `diagnostics/service.go`, the history
  bridges), the `opstate:<id>:params` pair, `failStaleOperations` and the duplicate startup
  resume sweep. v3 assumes those are deleted, not ported. 01 also owns the `opv2:` key
  retirement after the soak (after PR 16).
- **02 identification pipeline** — assumption in §3.9; if it needs streaming between stages or
  cross-book stages, the Pipeline kind needs a design change before wave G.
- **03 dedup page retirement** — the dedup ops that only the old `/dedup` page triggers
  (`dedup.book-merge` from `DedupBookTab.tsx`, census) are ported only if 03 keeps them.
- **04 census** — read at its final version 1.1.0 (234 production defs). Applied here:
  the 13 high-confidence prunes (11 twins → `FormerIDs` aliases on the survivor; 2 ISBN stubs
  → `retiredOpIDs`/tombstones) are **not ported**; the 2 conditional ones
  (`maintenance.batch-poller`, `operations.backfill-legacy-status`) stay on the adapter until
  their condition resolves; the 8 table-C near-duplicates wait for the owner. Census P4a-P4i
  (twin merges, carrying `Permissions: settings.manage` to the survivor) land **before**
  wave 12B. Census F2 (14 cron-only defs) is this doc's F1. The 11 non-op background jobs the
  census converts become v3 defs in the wave of their family.
- **06 bleeding-edge Go/Node** — the SDK uses generics and `iter.Seq` would fit `Source.Pages`;
  `testing/synctest` fits `opstest`'s fake clock. Whatever 06 recommends for the Go version
  applies to `pkg/ops`.
- **07 design decisions** — readiness signal (R20) is a prerequisite of PR 5's dispatcher
  wait; the `plugins/maintenance` domain split uses `sdk-api.md` §11.1's package shape and
  should land per wave so each moved op is ported once; R21 lets 07 delete `indexedStore`.
  The store-interface width work (narrow interfaces per consumer)
  applies to what `pkg/ops` exposes; the `ModifyBook` migration is a prerequisite for the
  Writer's "previous value inside the critical section" rule.

## 7. Open questions for the owner

| Q | question | recommended answer |
|---|---|---|
| Q1 | Keep the SDK public at `pkg/ops`, or make it `internal/ops`? No external plugin exists. | `pkg/ops`: keeps the plugin story open at no cost; the guard is the linter, not the path. |
| Q2 | 14 defs declare a cron that has never run on a timer (F1, census F2). When v3 makes `Schedule` real, should those start running? | No, with two exceptions. They stay manual; the twin survivors (census P4) keep today's scheduled cadence. Switch on `maintenance.file-integrity-check` and `maintenance.orphan-book-files-cleanup`: they are report-only health checks that never run today (census F2). *Coordinator verified the orphan op: its Run passes the store only to `findOrphanBookFiles`, whose parameter type `orphanFileScanner` has three read methods; `delete:true` returns an error; and `DeleteBookFilesByIDs` has no caller from this op or its plan sibling. Neither def declares `Permissions`; give both `settings.manage` when scheduling.* Any other cron to switch on is listed for you individually. |
| Q3 | Make `Batch` parallel by default (`NumCPU`), with sequential needing a reason? | Yes (CLAUDE.md concurrency rule); network-bound defs declare `Network(n, why)`. |
| Q4 | How long to keep dual-writing `opv2:` rows (the rollback window)? | 30 days after the last port wave ships. |
| Q5 | Scheduled writers must declare `.Live()`; otherwise a scheduled run is a preview. OK? | Yes; it is the 2026-09-25 rule applied to schedules. |
| Q6 | Keep `/api/v1/operations/*` as adapters until AudioBooth is checked? | Yes; delete only after AudioBooth's calls are listed. |
| Q7 | A re-save that changes no tracked field (e.g. an index rebuild): history row or not? | No history row; the intent and its clear are still recorded. |
| Q8 | Switch whole-library batches from "drop on restart" to "resume from watermark" as they are ported? | Yes, per def in its port PR, listed in the PR body; `library.scan` resume rules stay as they are. |
| Q9 | Should manual bulk metadata apply require an approved plan (Approval), while the nightly metadata upgrade keeps running on `applygate` alone? | Yes. |
| Q10 | Drop subprocess isolation (`Isolate`, 0 users)? | Yes. |
| Q11 | Hold a zombie's exclusive key until its goroutine exits (safe) instead of freeing it after 5s (today)? | Yes, with the 10-minute zombie alert and restart as the way out. |
