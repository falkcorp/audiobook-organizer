<!-- file: docs/agent-tasks/platform-core/F5-18-tasks-flip-engine-deleted.md -->
<!-- version: 1.0.0 -->
<!-- guid: ec04b9ab-cec9-4e59-bc6b-19f091354077 -->
<!-- last-edited: 2026-11-05 -->

# F5-18 — The F5 flip, one PR: core's executor and scheduler started in ABO, every converted definition and the three late-owner kinds registered, M4 and M5 registered, HEAD's engine and task routes deleted, the Activity page on core's task routes, the ops metric families with their dashboards and rules, the journal the only system-log writer

| Field | Value |
|---|---|
| ID | F5-18 |
| Release / flip | R2, **flip F5** (tasks and the log journal); one atomic PR (09 §9.2.1) |
| Wave | W32 (merges alone; its siblings rebase after it) |
| Tier | **T4** (Opus / xhigh–max): the flip deletes HEAD's engine and moves every definition, schedule, route, metric family and system-log writer at once; every intermediate `main` must boot and pass e2e |
| Depends on | F4-08 (the identity flip), F5-01 … F5-17 (every dark F5 PR) |
| Gate | Plan, then execute autonomously in a worktree with its own PR; inside an announced move window (09 §9.2.10); the plan is reviewed before code |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-18-tasks-flip
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` (CLAUDE.md). Announce the move window for every 04 row this PR moves (list below)
at least one working day ahead in the tracking issue; feature PRs touching them are held until this merges.
The R2 freeze is on (F3-07); this PR ships in R2, not in a production minor.

## Goal

The tasks flip of 03 §3.8 and 09 §9.4 (R2 flips table, F5 row): after this PR ABO runs every task on core's
executor and scheduler, HEAD's engine and task routes are gone, the first R2 boot imports HEAD's runs and
system log (M4, M5), the Activity page reads core's task routes, the ops metric families carry core's names
and labels with every dashboard and rule moved in the same PR, and the log journal is the only writer and
pruner of the system log. Not deleted here: `tools/cmd/opsgen`, `web/src/generated/ops.ts` and
`internal/operations/state` (one R3 PR, R3-02; 05-tasks §5T.8.1).

## Spec to read first (this PR's contract; read all of it)

1. 09 §9.4 R2 flips table (F5 row), §9.2.1 (merge gate, required checks, "one atomic PR"), §9.2.3 (step order),
   §9.6 rows 2, 13, 17, §9.9.
2. 03 §3.8 (the F5 row; "each later flip that adds a step raises M7's target in the same PR, … and registers a new
   M8").
3. 05-tasks §5T.8.1 (every row: what each HEAD package becomes), §5T.8.2 (F5 flip row; the three late-owner kinds;
   the kind-rename map), §5T.3.10 (the route table: every "Replaces" route deleted; books' domain routes under
   `/api/v1/operations` stay), §5T.11 (metrics), §5T.9 (the flip's tests).
4. 05-metrics §13 items 1 and 3 (the `ops_*` rows, the `operations_*`/`operation_*`/`op_*` deletions one by one,
   `ops.abandoned_calls`), §6 (`TestDeployReferencedSeriesServed`, `TestABOMetricsGatherNoDuplicate`).
5. 05-logging §11 item 1 (R2 part: delete the activity `Writer`'s line parsing and its tap, `StandardLogger`'s
   system-log writes, `internal/logger/retention.go`), §5.
6. 05-events §5E.13 (the activity mirror becomes a books subscriber on `core.task.log-appended.v1` in
   `internal/activity`, keeping HEAD's bounded queue and counted drops); 05-realtime §11 (the two operation
   streams deleted at F5).
7. 04 §8.1 F5 row, §8.2, §8.6 (re-implemented split parts), §2.4 (`internal/books/contract`,
   `internal/books/writer`), §6 (`pkg/ops`, `pkg/ops/opstest`, `pkg/plugin/sdk`).
8. 06 §6.4.10 F5 row (`TaskLedger`, `TaskStarts`, `Importers.Tasks`, `Importers.Log`, `Registrar.Tasks`,
   `Resources`, `Lanes`, `ChangeSink`, `Health`, `Deps.Tasks`, `Deps.Health`, `Options.InterimBackends`), §6.10 (G6/G7),
   the `TestSecondModuleNoAudiobookEdit` row.
9. 07 (the Activity page on the generated tasks client); 05-http-api §7, §8.4 (`ops.ts` importers).

## Base state: re-verify before editing (every line must hold; else STOP)

```bash
# Every dark F5 PR merged
for f in internal/maintenance/jobs/kinds.go internal/repairs/kinds.go internal/plugins/dedup/kinds.go \
         internal/plugins/acoustid/kinds.go internal/plugins/metafetch/kinds.go internal/plugins/itunes/kinds.go \
         internal/plugins/deluge/kinds.go internal/server/kinds_t7.go internal/scheduler/kinds_t7.go \
         internal/books/schedules.go; do test -f $f || echo "MISSING $f"; done                         # no output
ls internal/plugins/maintenance/kinds_t6_g*.go | wc -l                                                # 7
test -d internal/books/cutover/m4 && test -d internal/books/cutover/m5 && test -d internal/books/cutover/kindrename && echo steps   # steps
test -d core/internal/logjournal && test -d core/internal/storagetasks && echo core-kinds              # core-kinds (F5-15, F5-03)
test -d core/archtest/tasklint && test ! -e tools/cmd/oplint && echo tasklint                         # tasklint (F5-17)
grep -rn 'InterimHolds\|InterimPoll\|InterimExport' core/ --include=*.go | grep -v _test | wc -l      # >=3 (F2's minimal owner registrations)
grep -rn 'InterimBackends' core/app/options.go | wc -l                                                # 0 or >=1: record (this PR adds the field if F2 did not)
# The open gaps the dark PRs reported (each must be RESOLVED at your base, else STOP)
#  1. library_scan_full's declaration (F5-14 Step 0 / F5-16): its parity case is no longer skipped
go test ./internal/books/ -run TestScheduleParity_AllDefinitions -count=1 -v | grep -c 'SKIP.*SPEC-GATED'   # 0
#  2. tasklint's rule set over core (F5-17 Step 0): tasklint runs in make ci
#  3. the journal's query route and SPA move (F5-15 Planner notes): a row has built GET /api/core/v1/system/logs
grep -rn '/api/core/v1/system/logs\|"system/logs"' core/internal/ --include=*.go | grep -v _test | wc -l   # >=1
# HEAD's engine is still present (this PR deletes it)
for d in internal/operations/registry internal/operations/childop internal/operations/opmode internal/opsmetrics \
         internal/scheduler internal/opscatalog internal/maintenance internal/walkresume internal/server/handlers/opsv3; do test -d $d || echo "GONE $d"; done   # no output
```

## Ordered commits (one PR; every commit builds and passes its package tests — `scripts/ci/check_commits_build.py`)

1. **Contracts first.** Create `internal/books/contract` (04 §2.4: `selection.go`, `types.go` from
   `internal/operations`; `PageBooks` from `pkg/plugin/sdk`; the typed-writer contract interface from
   `pkg/ops/writer.go`) and rename `internal/operations/opswriter` → `internal/books/writer` (implementation:
   `writer.go`, `evidence.go`, `history.go`, `intent.go`, `evidence_journal.go`) over `tasks.Writer`; run fencing
   (`fence.go`: `ErrFenced`, `WithFence`, `FenceFromContext`) → public `tasks` if F2 did not already provide it.
   Point the narrow interfaces the conversions declared (plan §(l) item 4; F5-01's `jobs.Store`, the repairs shell's
   writer) at the shared contract. `BulkMetadataFetchParams` → `internal/server`. Book-typed fakes of
   `pkg/ops/opstest` → `internal/books/store/storetest`.
2. **Registration.** In `internal/books/module.go`: `r.Resources(...)` for every resource kind the conversions use
   (`books`, `book_files`, `authors`, `series`, `review_items`, `embeddings`, `operations`, `catalog`,
   `fingerprint_index`, `aux_stores`); `r.Lanes(...)` (HEAD's `LaneMac` as a books lane); `r.ChangeSink(...)`
   (books' operation-change journal); `r.Health(...)` with `books.memdb`; `r.Tasks(...)` with every list function
   (`jobs.Kinds`, `repairs.Kinds`, the seven `t6g<k>Kinds`, the plugins' `Kinds`, `t7Kinds`, the books half of
   `db_optimize`), each passed `books.ScheduleFor` as its schedule hook; `Manifest.TaskLedger` and `TaskStarts`.
   The kernel registers the core kinds (`core.storage.*`, `core.log.journal.prune`, `core.window.nightly`,
   `core.tasks.prune`) and, from `app.Options.InterimBackends` set by `main.go`, the three late-owner kinds over
   the books backends (`providerhttp.InterimHolds`, `ai.InterimPoll`, `diagnostics.InterimExport`; 05-tasks §5T.8.2).
   Start the executor and scheduler in ABO's kernel (P6, 06 §6.5.1); create the `<ns>.ops` instruments here and
   nowhere earlier (05-metrics §13 item 1).
3. **Cutover steps.** Register M4 and M5 in `internal/books/cutover/steps.go` in 03 §3.8's order (after M3, before
   M6 and M8), pass `Importers.Tasks` and `Importers.Log`, raise M7's target, and register the new M8 whose step ID
   names M5 as the last step it compacts after (03 §3.8; 09 §9.2.3). Move HEAD's run-row store files
   (`pebble_store_ops_v2.go`, `pebble_store_ops_v3*.go`, `pebble_store_op_rows.go`, `ReconcileOpsV3WithV2`) into
   `internal/books/cutover/m4legacy/` and repoint M4 (05-tasks §5T.8.1, `internal/database` ops storage row;
   04 key `internal/database → internal/books/cutover`). Regenerate the kind-rename map at this commit.
4. **Delete HEAD's engine** (05-tasks §5T.8.1, §5T.8.2 F5 row): `internal/operations/registry` (with
   `legacy_op_status.go`, `legacy_backfill.go`; its activity mirror re-implemented first as a books subscriber on
   `core.task.log-appended.v1` in `internal/activity`, 05-events §5E.13), `childop`, `opmode`, the v1 checkpoint
   helpers of `internal/operations/state.go`, `internal/opsmetrics` (its `ops.abandoned_calls` → a books
   observable on a books scope, both sources repointed: `internal/repairs/guards.go`,
   `internal/plugins/maintenance/retired_versions_fixer.go`), `internal/scheduler`, `internal/opscatalog`,
   `internal/maintenance` (root package; the jobs keep their package), `internal/walkresume`, `pkg/ops`
   (successor `core/tasks`), the root test-only `internal/plugins` lint, `internal/logger/mocks`,
   `internal/server/legacy_backfill_op.go`, every HEAD v2 definition the conversions shadowed (their v2 shells;
   the shared cores fold into the native shells), `maintenance_job_op.go`, `repairs.plan`/`repairs.apply`,
   `scheduler_maintenance_window_op.go`, `diagnostics_ops.go`'s v2 def, `addOpRegistrar` and the
   `serviceregistry` op wiring. Re-sign `repairs.Fixer` without the v2 reporter. Every caller of
   `pkg/plugin/sdk` moves to `core/tasks` + `module.Registrar` (the package itself and `sdkguard` go in R3,
   05-tasks §5T.8.1). Keep `internal/operations/state`, `tools/cmd/opsgen` and `ops.ts`.
5. **Routes.** Mount `internal/taskhttp` at `/api/core/v1/tasks`; delete every HEAD route 05-tasks §5T.3.10's
   "Replaces" column names, `/api/v3/ops/*`, `internal/server/handlers/opsv3`, the v2 operations handlers (`internal/server/handlers/operations*.go`) and the
   scheduler task routes (05-tasks §5T.8.1 names `internal/server/handlers/scheduler`; that directory does not exist
   at `f7ac5b11e`, so find the `/api/v1/tasks` handlers with `git grep -n '"/tasks' internal/server/`), and the two streams `/api/v1/operations/events` and `/api/v3/ops/events`
   (05-realtime §11). Books' domain routes under `/api/v1/operations` (`stale`, `clear-stale`, … §5T.3.10) stay and
   read runs through `Deps.Tasks`. No alias, no `Deprecation` header (D10).
6. **SPA and callers.** The Activity page, `useOperationsStore` and MaintenanceTab on the generated tasks client,
   polling `GET /api/core/v1/tasks/runs` until F6; `api.ts`, `operationPolling.ts` and its users off `ops.ts`; the
   first-party scripts and agent skills that called a deleted route (05-tasks §5T.3.10; 07). The system-log page on
   the journal's route (Base state gap 3).
7. **Metrics** (05-metrics §13 item 3): the executor's `ops_*` families with core's labels (`def_id` = native kinds,
   `plugin` → `module`); `operations_*`, `operation_*`, `op_*` deleted; every row of 05-metrics' one-by-one table
   applied to `deploy/grafana/dashboards/*.json`, `deploy/prometheus/*.yml` and `alert-rules.test.yml`
   (`AudiobookOrganizerOpFailuresHigh` and `AudiobookOrganizerOpStalled` deleted, their `V2` twins kept and re-joined
   on `def_id`/`module`; the `label_replace` compat rule deleted).
8. **Logging** (05-logging §11 item 1, R2 part): start the journal tap; delete `StandardLogger`'s system-log writes,
   the activity `Writer`'s line parsing (`internal/activity/writer.go`) and its tap, and `internal/logger/retention.go`.
   `core.log.journal.prune` is the only pruner; `operationlog:` history is pruned by `core.tasks.prune`. Drop the
   operation-record half from `books.maintenance.retention-and-hygiene` (it is `core.tasks.prune`'s now).
9. **Bookkeeping.** `internal/arch/layering_test.go` (`layerOf` entries for created, moved and deleted packages),
   `scripts/ci/short_test_weights.json`, `.mockery.yaml` (mocks of deleted interfaces), `tygo.yaml` (types of
   deleted packages), `internal/sysinfo` (`OperationReadCheck` onto `Deps.Tasks`).

## 04 rows this PR moves (list them in the move window and the PR description as `old ==> new`)

create `internal/books/contract`; rename `internal/operations/opswriter → internal/books/writer`; move
`internal/operations`, `internal/operations/childop`, `internal/operations/freshness`, `internal/operations/opmode`,
`internal/operations/opswriter`, `internal/walkresume`, `pkg/ops`, `pkg/plugin/sdk`, `internal/maintenance` →
`mediacore/tasks`; `pkg/ops/opstest → mediacore/tasks/taskstest`; `internal/operations/registry`,
`internal/opsmetrics → mediacore/internal/executor`; `internal/scheduler → mediacore/internal/scheduler` and
`→ internal/books`; `internal/server/handlers{,/operations,/opsv3} → mediacore/internal/taskhttp`;
`internal/server → internal/books`; `internal/operations/registry → internal/books/contract` and
`→ internal/activity`; `internal/database → internal/books/cutover`; delete `internal/maintenance`,
`internal/opscatalog`, `internal/plugins` (root lint), `internal/logger/mocks`. The `mediacore/*` targets are
re-implementations F2 already landed (04 §8.6): for them "move" means delete HEAD's package and point callers at
core.

## Invariants and the tests that prove them (spec names copied exactly)

| Invariant | Test |
|---|---|
| Every HEAD op ID and tombstone maps to exactly one registered kind (or `books.retired.*`), no two canonical IDs to one kind, core targets exactly 05-tasks' table, the three late-owner kinds registered | `TestKindRenameMapCoversLedger` **(ABO)** on this build |
| A second module boots beside books with no books edit (G6/G7, from here on) | `TestSecondModuleNoAudiobookEdit` (in `internal/books`; `coretest.New(t, WithModules(books.Module(…), hello.New()))`) |
| Resume, cancel and shutdown behave as HEAD's suites say | `TestParity_ResumeCancelShutdown` **(ABO)** |
| Schedules: HEAD-equivalent, no first-boot storm | `TestScheduleParity_AllDefinitions`, `TestCutover_NoCatchUpStorm` (F5-14's, now over the registered kinds) |
| Every converted `ResumeContinue` kind resumes from a HEAD checkpoint (`v1` included) or is listed as from-zero once | `TestCutover_CheckpointsResume` **(ABO)** |
| M4's run import then the first resume sweep acts per policy; an interrupted repairs apply re-applies no marked row | extend `TestCutover_TasksFixtureFromR0` and `TestCutover_RepairsRowsAndMarksImported` (F5-16's) with their registered-kind halves |
| No HEAD def ID left outside the retired claims | `TestCutover_KindReferencesRewritten` (F5-16's), green on this build |
| No series read by a dashboard or rule is unserved; the rules' tests pass | `TestDeployReferencedSeriesServed`; `promtool test rules` |
| No duplicate family on the scrape (the executor's `ops.*` vs. `opsmetrics`) | `TestABOMetricsGatherNoDuplicate` |
| Unknown kinds after the cutover are dropped, not fatal | `TestUnknownKindRun_DroppedAtResume` (core's), with an imported tombstoned run |
| A `books.*` run waits for `books.memdb` | `TestDispatch_WaitsForReadySignal` |
| The T5 gate | `go list -deps ./internal/plugins/... | grep -c pkg/plugin/sdk` is 0 |
| Every intermediate `main` boots and passes e2e | `make test-e2e` |

## Acceptance (commands and expected output)

```bash
go build ./... && go vet ./...                                                                 # exit 0
go test ./internal/books/... -race -count=1 -run 'TestKindRenameMapCoversLedger|TestSecondModuleNoAudiobookEdit|TestParity_ResumeCancelShutdown|TestScheduleParity_AllDefinitions|TestCutover_' -v | grep -c '^--- FAIL'   # 0
for d in internal/operations/registry internal/operations/childop internal/operations/opmode internal/opsmetrics \
         internal/scheduler internal/opscatalog internal/maintenance/job.go internal/walkresume pkg/ops \
         internal/server/handlers/opsv3 internal/logger/retention.go internal/logger/mocks; do test -e $d && echo "LEFT $d"; done   # no output
test -d internal/operations/state && test -d tools/cmd/opsgen && test -f web/src/generated/ops.ts && echo kept-for-R3   # kept-for-R3
git grep -n '/api/v3/ops\|/api/v1/operations/events\|/api/v1/operations/v2\|/api/v1/op-defs' -- ':!docs/' ':!CHANGELOG.md' ':!changelog.d/' | wc -l   # 0
git grep -n 'generated/ops' -- web/src/ | grep -v 'generated/ops.ts:' | wc -l                 # 0 (no SPA importer left)
git grep -n 'audiobook_organizer_operations_\|audiobook_organizer_operation_\|audiobook_organizer_op_' -- deploy/ | wc -l   # 0
go list -deps ./internal/plugins/... | grep -c 'pkg/plugin/sdk'                                 # 0
promtool test rules deploy/prometheus/alert-rules.test.yml                                      # SUCCESS
make ci                                                                                         # exit 0
make -C core ci                                                                                 # exit 0
make test-e2e                                                                                   # exit 0
```

## STOP conditions

- Any Base-state gap unresolved (the full-sweep declaration; tasklint's rules; the journal's route).
- A converted kind fails registration (`app.New` fails closed): report the problem list, do not patch the kind here
  beyond the registration call.
- `TestKindRenameMapCoversLedger` finds an unmapped ledger line or two IDs on one kind.
- A dashboard or rule reads a series the build no longer serves and 05-metrics §13 gives no successor.
- A commit that does not build on its own (split it differently; never squash the PR).

## Out of scope

`tools/cmd/opsgen`, `ops.ts`, `internal/operations/state` and `pkg/plugin/sdk`'s deletion (R3); the events flip's
SSE move of the Activity page (F6); Deluge on core's download contract and the native bodies of the late-owner
kinds (F7, F8); any parallelism or write-set change of a converted kind.

## Rollback

Before the R2 release: revert the PR (it is one PR; every dark PR it registers stays dark). After R2 ships: the
offline restore of the pre-migration checkpoint (or the owner's off-host archive) plus the pre-freeze release's
binary (`make rollback-restore`; 05-tasks §5T.8.3; 09 §9.4 R2 Rollback); runs started after the upgrade are lost.

## Repo rules

- Never edit `main` or the primary checkout; one worktree; the move window announced; this PR merges alone.
- File headers on every created, moved or modified file, version bumped; `changelog.d/` fragment without a header;
  the R2 release-note lines this PR's rows feed (§9.6 rows 2, 13, 17; the dropped v2 crons; the renamed and
  deleted metric families) listed in the PR description for R2-01.
- Move PR rules: `old ==> new` paths in the description; every commit builds; `go fix` on moved files (04 §2.2).
- Conventional commits, each body ending `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no flip trailer.
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- CLAUDE.md concurrency rules for every new loop; the conversions' parallelism stays 1:1.
- Report with exact counts (`registered kinds: <n>`, `HEAD routes deleted: <n>`, `metric families renamed/deleted:
  <n>`) and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- **Three gaps from the dark rows block this flip until resolved** (Base state): `library_scan_full`'s declaration
  (F5-14, F5-16), tasklint's rule set over core (F5-17, SPEC-GATED), and the journal's query route and the SPA's move
  to it (F5-15's notes: no row builds `GET /api/core/v1/system/logs`, yet after M5 HEAD's route reads nothing).
- **`pkg/plugin/sdk`**: 04's F5 key moves it to `mediacore/tasks`, 05-tasks §5T.8.1 deletes it in R3; the brief
  repoints every caller here and leaves the package to R3 (with `sdkguard`, 09 §9.4 R1 CI classification).
- **The conversions' plan rules land here**: the shared cores fold into the native shells, the `sched` hooks receive
  `books.ScheduleFor`, rule R7's widened write sets start gating dispatch (a behaviour change to call out in the R2
  note), and HEAD's maintenance-job wrapper's activity-summary entry is not reproduced unless books' activity
  subscriber covers it.
- The row's files do not list `internal/database/` (the run-row store files move out of it to `m4legacy`),
  `internal/activity/` is listed; add `internal/database/` and `internal/serviceregistry/`.
