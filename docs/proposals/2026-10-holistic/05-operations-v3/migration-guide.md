<!-- file: docs/proposals/2026-10-holistic/05-operations-v3/migration-guide.md -->
<!-- version: 1.2.0 -->
<!-- guid: e8493f08-8cd4-4429-83ec-d6b6fd01e206 -->
<!-- last-edited: 2026-10-08 -->

# Operations v3 — migration guide

Parent: [`../05-operations-v3.md`](../05-operations-v3.md) §4. State and keyspace details:
[`state-and-persistence.md`](state-and-persistence.md). API: [`sdk-api.md`](sdk-api.md).

## 0. Strategy in one paragraph

v3 is **not a rewrite of the executor**. The v2 registry's dispatcher, worker, watchdog,
resume, stand-down and pause code carries 57 post-incident fixes since 2026-08-01
(`git log --oneline --since=2026-08-01 -- internal/operations/registry | grep -i fix | wc -l`).
It stays in `internal/operations/registry` and is changed in place: typed states, the fence,
`opv3:` records and the single scheduler. On top of it sit the new SDK (`pkg/ops`) and an
**adapter** that registers an unchanged v2 `OperationDef` as a v3 definition. From the
adapter PR on, **every op runs on one executor** — one queue, one exclusive-key map (so the
`library.scan` key is never split across two engines), one state machine. Ops are then
ported family by family to native kinds, and the adapter is deleted when the last one moves.

## 1. Code: porting an op

### 1.1 Step zero for every op

1. Check the census verdict (`docs/proposals/2026-10-holistic/04-operations-census/ops-census.csv`,
   column `verdict`). **`prune` → do not port.** Delete it in the census workstream's PR, add
   its ID to the tombstone list in `internal/opscatalog/tombstones.go` so the ledger check keeps
   resolving it, and stop. **`merge` → port only the survivor**; list the other ID in the
   survivor's `FormerIDs`.
2. Check the op does not run code in `internal/writeback/`. None does at HEAD
   (`grep -rln 'internal/writeback"' internal cmd --include='*.go' | grep -v _test` → only the
   package itself), but iTunes write-back drains its outbox; any op that comes to call it stays
   on the adapter, body untouched (charter ban).
3. Note the op's non-terminal runs: a native port needs `LegacyResume` (see
   `state-and-persistence.md` §3).

### 1.2 Shape → kind

| v2 shape | how to recognise it | v3 kind | port notes |
|---|---|---|---|
| single function, `LivenessManual` or `LivenessNone`, no `RunItems` | 154 `manual` + 17 `none` in census `live` column | `ops.Task` | thread `rc.Context()` into blocking calls; `Uninterruptible` only with a reason and budget |
| `RunItems` over a slice | 63 `run_items` in census | `ops.Batch` | move the loop body into `Item`; tallies → `rc.Count`; delete `Label` counters; delete hand `NewProgress`; pick `SourceOrder` |
| `sdk.PageBooks` / paged walk | `grep -rn 'PageBooks(' internal` | `ops.Batch` with `Source.Pages` | keep the page size |
| `opmode.ResolveDryRun` + per-row plan in the result | 26 files | `ops.Fixer` if a person picks rows; else `ops.Batch` with framework `Mode` | delete `dry_run`/`dryRun` params; preview is default |
| `repairs.Fixer` | 19 fixers via 2 defs (`repairs.plan`/`apply`) | `ops.Fixer` | `Plan`+`Replan` → `Candidates`+`Load`+`Evaluate`; fingerprint inputs → `Row.Inputs`; `RetryFingerprint` deleted |
| parent that `EnqueueOp`s children and follows them (`childop.Follow`) | `grep -rln 'childop\.' internal` | `ops.Pipeline` | stage per child; delete the hand-written follow loop |
| `maintenance.window` | `internal/server/scheduler_maintenance_window_op.go` | `ops.Pipeline` built from every def with `InMaintenanceWindow(order)` | the hard-coded job order becomes `order` values |
| `internal/maintenance` job (`MaintenanceJob`, 37 in census as `UI:/maintenance/jobs`) | `internal/maintenance/jobs/*.go` | `ops.Task` or `ops.Batch` each | keep the job's ID; op id and params come from `rc`, not context values (`maintenance.OperationIDFromCtx`, `RawParamsFromCtx` die) |
| `scheduler.ExtraOpsRegistrar` op | `internal/scheduler/extra_ops.go` | as per shape | its `TaskDefinition` becomes the def's `Schedule` |
| `Batchable` (2) / `Requires` (4) | `grep -rn 'Batchable: *true\|Requires:' internal` | `ops.Batch` + `ops.AfterFor` | keep the `op:batch:` / `op:deprev:` keys |

### 1.3 Mechanical checklist per port PR

- [ ] Def built with `ops.Task/Batch/Fixer/Pipeline`; placed in the package `Ops()` bundle.
- [ ] `Effects` declared; for writers, every resource the body writes is listed.
- [ ] Every store write goes through `rc.Writer()`; `oplint` clean.
- [ ] `Concurrency` is the default or carries a reason.
- [ ] `Schedule` copied from the op's `TaskDefinition` **cadence as it runs today**, not from
      its declared cron (F1). A cron that differs from today's cadence is listed in the PR body
      and needs the owner's answer (parent §7 Q2) before it is switched on.
- [ ] `LegacyResume` set if the def can have non-terminal migrated runs.
- [ ] `opstest.Conformance(t, Def, params)` passes under `-race`.
- [ ] The op's ID is unchanged (or the old one is in `FormerIDs`); `op_ids.golden` untouched.
- [ ] Any frontend caller of the op's old bespoke route is pointed at `/api/v3/ops/runs`.

## 2. Compat shim period

| item | during the shim | removed in |
|---|---|---|
| `registry.OperationDef` + `RegisterOp` | adapter onto the runtime; unchanged signature | PR 15 (a `v2compat` subset stays for the 5 frozen IDs) |
| `pkg/plugin/sdk` | kept, re-pointed at the adapter; marked Deprecated | PR 15 |
| `registry.RunItems` | kept for un-ported ops; gains a `Concurrency` default of 1 with a WARN per call naming the def, so the remaining sequential sites are visible on every run | PR 15 |
| `opv2:op:` mirror rows | dual-written in the same batch | PR 16 |
| `/api/v1/operations/*` | served from v3 records mapped back to v2 vocabulary | after AudioBooth is checked (Q6) |
| `TaskScheduler` | reads `Schedule` from v3 defs; un-ported ops keep their `TaskDefinition` | PR 15 |

## 3. Order (by family, then by risk)

The order puts the new mechanisms on low-blast-radius ops first and leaves `library.scan` and
the metadata apply family for last, when the runtime has carried the rest for weeks.

| wave | family | count at HEAD (census) | why here |
|---|---|---|---|
| A | read-only reports and censuses (`*-report`, `*-audit`, `*-census`, `*-verify`) | 12 | no writes: proves Task/Batch, progress, schedule, generated UI with zero data risk |
| B | housekeeping with a schedule (`scheduler.*`, `maintenance.purge-*`, `*-cleanup`, activity compaction, AI-journal prune) | 22 after the census twin merges | removes the TaskScheduler/cron duplication (F1, F24); census decides twin survivors |
| C | `internal/maintenance` jobs | 37 | kills the context-value plumbing (F27) |
| D | Repairs fixers | 19 fixers via 2 defs (`repairs.plan`/`apply`) | the Fixer kind's reference users; owner already uses this flow |
| E | batch writers (acoustid, dedup scans, backfills, repoint/recover/mark-missing) | 115 | fence + journal + default concurrency pay off most here |
| F | AI and metadata (`ai.*`, `metadata.*`, `library.bulk-metadata-fetch`, `metafetch.*`) | 16 | approval gate for bulk apply (F16); needs `applygate` as a Guard |
| G | pipelines (`maintenance.window`, `maintenance.library-optimize`, `dedup.run-all`, identification pipeline if workstream 02 wants one) | — | needs every child already native |
| H | `library.*` incl. `library.scan`, organize, import | 9 | last: scan stand-down, resume and the single scan key must behave the same under v3 |

**Census hook (applied, census v1.1.0, 234 defs).** Before each wave's PRs are cut, re-read
`ops-census.csv` and drop new `prune` rows. At census v1.1.0 the result is:

| census verdict | ops | v3 handling |
|---|---|---|
| prune, twin (11) | `scheduler.{purge-deleted, temp-file-cleanup, trash-cleanup, tombstone-cleanup, db-optimize, cleanup-old-backups, metadata-refresh, author-split-scan, resolve-production-authors}`, `maintenance.{series-prune, series-normalize}` | not ported; merged by census PRs P4a-P4i **before** wave B; each ID becomes a `FormerIDs` alias on its survivor; survivor gains `Permissions: settings.manage` (R19) and the larger timeout |
| prune, retired stub (2) | `scheduler.isbn-enrichment`, `maintenance.isbn-enrichment` | not ported; ID → `internal/opscatalog/tombstones.go` (fails loudly) |
| conditional (2) | `maintenance.batch-poller` (census Q3), `operations.backfill-legacy-status` (removed with the v1 keyspace, workstream 01) | stay on the adapter; never ported natively |
| owner review (8, census §2.2 C) | near-duplicates | ported as-is only if the owner keeps both; otherwise the survivor only |
| out of scope (2) | `library.bulk-write-back`, `maintenance.bulk-write-back` | stay on the adapter, bodies untouched (write-back ban) |

So 18 of 234 defs are never ported natively: 13 are deleted (aliases/tombstones), and 5 stay on
the frozen `v2compat` allowlist after PR 15 (the 2 conditionals, the 2 write-back ops, and
`maintenance.repair-library-state` pending the owner). The other 216 port as measured in
`implementation-briefs.md` PR 12A-H: A 12, B 22, C 37, D 2, E 115, F 16, G 3, H 9. Census F2's 14
cron-only defs are this guide's F1 list. Their `Schedule` is ported **disabled** until the owner answers
parent Q2. The exception is `maintenance.file-integrity-check` and `maintenance.orphan-book-files-cleanup`,
which census flags as report-only health checks that never run; the recommended answer is to
switch those on. The census's 11 non-op background jobs (boot goroutines and similar) become
v3 defs in their family's wave, with an enqueue-on-boot `Schedule` instead of a raw goroutine.

## 4. Persisted state

See [`state-and-persistence.md`](state-and-persistence.md) §3-§4 for the mapping table,
migration 065, dual-write and rollback. Summary:
- op ids never change; `opchange:`, `op_result:`, `opsummary:`, `opstate:`, `act:op:` and the
  dependency keys are not touched;
- `opv3:` is added; `opv2:` stays and is mirrored during the shim;
- rollback during the shim = deploy the old binary; after the mirror is gone = run
  `ops export-v2` first.

## 5. API and UI

| v2 | v3 | note |
|---|---|---|
| `POST /api/v1/operations/v2 {def_id, params}` | `POST /api/v3/ops/runs {def, params, mode, plan_ref?, rows?}` | v1 path kept as adapter; omitted `mode` = preview for writers |
| `GET /api/v1/operations/v2/:id` | `GET /api/v3/ops/runs/:id` | adds typed progress, approval, transitions |
| `GET /api/v1/operations/timeline` | `GET /api/v3/ops/timeline?since=&until=` → `{runs, window, complete}` | `complete` replaces reading three flags |
| (none) | `GET /api/v3/ops/runs?def=&state=&cursor=` → `{runs, total, next}` | **census**: `total` exact from `opv3:ix:state` / `ix:def` |
| (none) | `GET /api/v3/ops/counts` → per state, per def | for dashboards and Grafana |
| `DELETE /api/v1/operations/v2/:id` | `POST /api/v3/ops/runs/:id/cancel` | cancel is a verb, not a DELETE |
| `DELETE /api/v1/operations/v2/:id/record` | `DELETE /api/v3/ops/runs/:id` | discard; allowed per the state table |
| `POST /api/v1/operations/v2/:id/retry` | `POST /api/v3/ops/runs/:id/retry` | |
| `GET /api/v1/operations/events` (SSE) | `GET /api/v3/ops/events` | event payload = RunRecord delta; v1 stream kept during shim |
| `/api/v1/repairs/*` | `/api/v3/ops/runs/:id/rows`, `/api/v3/ops/defs?kind=fixer` | Repairs lane reads the generic surface |
| `/api/v1/maintenance/jobs` | `/api/v3/ops/defs` | |

Frontend files that change (wave-independent, PRs 9-10): `web/src/services/api.ts`
(`Operation`, `OperationV2`, `OperationV2Status`, `isOperationTerminal`, `getOperationStatus`,
`pollOperation`, 26 functions typed `Promise<Operation>`), `web/src/stores/useOperationsStore.ts`,
`web/src/stores/operationGrouping.ts`, `web/src/components/OperationActivityPanel.tsx`,
`web/src/pages/ActivityLog.tsx`, `web/src/components/review/` (Repairs lane), the 27 files that
render a progress bar from `progress_current` (`grep -rln 'progress_current\|ProgressBar\|LinearProgress' web/src --include='*.tsx' | grep -v test`), plus the new
`web/src/generated/ops.ts` and `web/src/components/ops/OpForm.tsx`.
