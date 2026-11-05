<!-- file: docs/agent-tasks/platform-core/F5-18-tasks-flip-engine-deleted.md -->
<!-- version: 1.1.7 -->
<!-- guid: ec04b9ab-cec9-4e59-bc6b-19f091354077 -->
<!-- last-edited: 2026-11-05 -->

# F5-18 — The F5 flip, one PR: core's executor and scheduler started in ABO, every converted definition and the three late-owner kinds registered, M4 and M5 registered, HEAD's engine and task routes deleted, the Activity page on core's task routes, the ops metric families with their dashboards and rules, the journal the only system-log writer

| Field | Value |
|---|---|
| ID | F5-18 |
| Release / flip | R2, **flip F5** (tasks and the log journal); one atomic PR (09 §9.2.1) |
| Wave | W35 (merges alone; its siblings rebase after it) |
| Tier | **T4** (Opus / xhigh–max): the flip deletes HEAD's engine and moves every definition, schedule, route, metric family and system-log writer at once; every intermediate `main` must boot and pass e2e |
| Depends on | F4-08 (the identity flip); F5-01 … F5-16 and F5-19 (every dark F5 PR); F2-16 (the kernel's task start rule, `Registrar.Tasks`); F2-17 (`tasklint` and its exemption directory), F5-01, F5-02, F5-03, F5-04, F5-05, F5-06, F5-07, F5-08, F5-09, F5-10, F5-11, F5-12, F5-13, F5-14, F5-15, F5-16, F2-16, F2-17, F5-19 |
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
The R2 freeze is on (F3-08 switched it on before F3-07 merged); this PR ships in R2, not in a production minor.

## Goal

The tasks flip of 03 §3.8 and 09 §9.4 (R2 flips table, F5 row): after this PR ABO runs every task on core's
executor and scheduler, HEAD's engine and task routes are gone, the first R2 boot imports HEAD's runs and
system log (M4, M5), the Activity page reads core's task routes, the ops metric families carry core's names
and labels with every dashboard and rule moved in the same PR, the log journal is the only writer and
pruner of the system log and is read through its native query route (F5-19's, mounted here, replacing HEAD's
`GET /api/v1/system/logs` in the same PR), and `tasklint`'s exemption directory is empty. Not deleted here: `tools/cmd/opsgen`, `web/src/generated/ops.ts` and
`internal/operations/state` (one R3 PR, R3-02; 05-tasks §5T.8.1).

## Spec to read first (this PR's contract; read all of it)

1. 09 §9.4 R2 flips table (F5 row), §9.2.1 (merge gate, required checks, "one atomic PR"), §9.2.3 (step order),
   §9.6 rows 2, 13, 17, §9.9.
2. 03 §3.8 (the F5 row; "each later flip that adds a step raises M7's target in the same PR, … and registers a new
   M8"), and 05-storage §5S.3.13 (M7, M8 and their step IDs). The way a flip adds level steps (M7) and replaces M8 is
   fixed in F3-04 and F3-06 (order M2); commit 3 repeats it.
3. 05-tasks §5T.8.1 (every row: what each HEAD package becomes), §5T.8.2 (F5 flip row; the three late-owner kinds;
   the kind-rename map), §5T.3.10 (the route table: every "Replaces" route deleted; books' domain routes under
   `/api/v1/operations` stay), §5T.11 (metrics), §5T.9 (the flip's tests).
4. 05-metrics §13 items 1 and 3 (the `ops_*` rows, the `operations_*`/`operation_*`/`op_*` deletions one by one,
   `ops.abandoned_calls`), §6 (`TestDeployReferencedSeriesServed`, `TestABOMetricsGatherNoDuplicate`).
5. 05-logging §11 item 1 (R2 part: delete the activity `Writer`'s line parsing and its tap, `StandardLogger`'s
   system-log writes, `internal/logger/retention.go`), §5 (the query route, its auth-off tightening and the SPA's
   move land here: "HEAD's route and the native route switch in one PR"); 05-tasks §5T.8.4 (`tasklint`: "the F5 flip requires the list to be empty") and §5T.8.2 conversion rules 1–4
   (what the flip folds: the HEAD shells deleted, each shared body into its native shell).
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
test -d core/archtest/tasklint && test ! -e tools/cmd/oplint && test -f internal/books/tasklint_test.go && echo tasklint   # tasklint (F2-17)
ls internal/books/testdata/tasklint_exempt.d/                                                         # F5-18.txt only (every conversion row deleted its own entry)
test -d core/internal/loghttp && echo loghttp                                                          # loghttp (F5-19)
grep -rn '"books.library.scan-full"' internal/server/*.go | grep -v _test | wc -l                     # >=1 (F5-13)
grep -rn 'InterimHolds\|InterimPoll\|InterimExport' core/ --include=*.go | grep -v _test | wc -l      # >=3 (F2's minimal owner registrations)
grep -rn 'InterimBackends' core/app/options.go | wc -l                                                # 0 or >=1: record (this PR adds the field if F2 did not)
# HEAD's engine is still present (this PR deletes it)
for d in internal/operations/registry internal/operations/childop internal/operations/opmode internal/opsmetrics \
         internal/scheduler internal/opscatalog internal/maintenance internal/walkresume internal/server/handlers/opsv3; do test -d $d || echo "GONE $d"; done   # no output
```

## Files

The inventory row's files, by action. Every path is touched by the commit named in "Ordered commits" below; touch
nothing outside this list (STOP and report if a commit needs another path).

| Action | Path |
|---|---|
| modify | `main.go` (sets `app.Options.InterimBackends`), `core/app/options.go` (adds the field if F2 did not) |
| modify | `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/module/manifest.go`, `core/module/registrar.go`, `core/module/deps.go` (commit 2: boot calls, members, mounts; this PR is their one owner in W35) |
| modify | `core/tasks/` (run fencing made public, only if F2 did not provide it; commit 1) |
| regenerate | `core/openapi/core.json`, `core/web/packages/api-client/src/generated/` (commit 5) |
| create | `internal/books/contract/` (commit 1) |
| create (rename) | `internal/books/writer/` from `internal/operations/opswriter` (commit 1) |
| modify | `internal/books/store/` (`storetest` takes the book-typed fakes; the run-row store files leave for `m4legacy`) |
| create | `internal/books/cutover/m4legacy/` (commit 3) |
| modify | `internal/books/module.go`, `internal/books/services.go` (commit 2) |
| modify | `internal/books/cutover/steps.go` (commit 3: M4 and M5, this flip's level step, the new M8) |
| modify | `internal/books/cutover/stepids.go` (commit 3: add this flip's level constant and level-step ID; replace the M8 ID) |
| modify | `internal/books/cutover_suite_test.go` (this flip's `TestCutoverFlipUpgrade` cell, F2-15's harness) |
| create | `internal/books/schedules_registered_test.go` (name coined here; commit 2: `TestF5_ScheduledKindsRegistered`) |
| modify | `core/internal/loghttp/` test files (commit 5: delete the `t.Skip("needs stage 13: F4-08")` of F5-19's permission, guard and 401 cells; no other change to the package) |
| delete | `internal/books/testdata/tasklint_exempt.d/F5-18.txt`; modify `internal/books/tasklint_test.go` (commit 9) |
| delete or move | `internal/operations/` (keeping `internal/operations/state`), `internal/scheduler/`, `internal/opscatalog/`, `internal/maintenance/` (root package; `jobs` keeps its package), `internal/opsmetrics/`, `internal/walkresume/` (commit 4) |
| modify | `internal/server/`, `pkg/` (`pkg/ops` deleted; `pkg/plugin/sdk` callers moved), `internal/plugins/`, `internal/repairs/`, `internal/serviceregistry/` (commits 1, 4, 5) |
| modify | `internal/activity/`, `internal/logger/` (commit 8; `internal/logger/mocks` deleted in commit 4) |
| modify | `internal/sysinfo/` (commit 10) |
| modify | `web/src/` (commit 6) |
| modify | `deploy/grafana/dashboards/`, `deploy/prometheus/` (commit 7) |
| modify | `tools/cmd/sdkguard/internal-deps.txt`, `internal/arch/layering_test.go`, `scripts/ci/short_test_weights.json`, `.mockery.yaml`, `tygo.yaml` (commit 10) |
| create | one `changelog.d/` fragment (headerless) |

## Ordered commits (one PR; every commit builds and passes its package tests — `scripts/ci/check_commits_build.py`)

1. **Contracts first.** Create `internal/books/contract` (04 §2.4: `selection.go`, `types.go` from
   `internal/operations`; `PageBooks` from `pkg/plugin/sdk`; the typed-writer contract interface from
   `pkg/ops/writer.go`) and rename `internal/operations/opswriter` → `internal/books/writer` (implementation:
   `writer.go`, `evidence.go`, `history.go`, `intent.go`, `evidence_journal.go`) over `tasks.Writer`; run fencing
   (`fence.go`: `ErrFenced`, `WithFence`, `FenceFromContext`) → public `tasks` if F2 did not already provide it.
   Point the narrow interfaces the conversions declared (plan §(l) item 4; F5-01's `jobs.Store`, the repairs shell's
   writer) at the shared contract; every conversion's owner-declared dependency value (05-tasks §5T.8.2 conversion
   rule 3: `jobs.KindDeps`, `repairs.KindDeps`, the plugins' `KindDeps`, the `t6g<k>Deps`, `t7Deps`) gets
   `NewWriter`/`NewStore` over `internal/books/writer` and books' stores. `BulkMetadataFetchParams` → `internal/server`. Book-typed fakes of
   `pkg/ops/opstest` → `internal/books/store/storetest`.
2. **Registration.** In `internal/books/module.go`: `r.Resources(...)` for every resource kind the conversions use
   (`books`, `book_files`, `authors`, `series`, `review_items`, `embeddings`, `operations`, `catalog`,
   `fingerprint_index`, `aux_stores`); `r.Lanes(...)` (HEAD's `LaneMac` as a books lane); `r.ChangeSink(...)`
   (books' operation-change journal); `r.Health(...)` with `books.memdb`; `r.Tasks(...)` with every list function
   (`jobs.Kinds`, `repairs.Kinds`, the seven `t6g<k>Kinds`, the plugins' `Kinds`, `t7Kinds`, the books half of
   `db_optimize`), each passed its dependency value (above) and `books.ScheduleFor` as its schedule hook (conversion
   rule 4); `Manifest.TaskLedger` and `TaskStarts`; `r.LogOtherUserData(...)` with books' predicate over
   `activity.NamesOthersUserLog` (05-logging §5, the journal route's other-user rule). The kernel and module wiring
   files (`core/kernel/boot.go`, `kernel.go`, `core/module/manifest.go`, `registrar.go`, `deps.go`) and
   `internal/books/services.go` take the boot calls, members and mounts (this PR is their one owner in W35).
   The kernel registers the core kinds (`core.storage.*`, `core.log.journal.prune`, `core.window.nightly`,
   `core.tasks.prune`) and, from `app.Options.InterimBackends` set by `main.go`, the three late-owner kinds over
   the books backends (`providerhttp.InterimHolds`, `ai.InterimPoll`, `diagnostics.InterimExport`; 05-tasks §5T.8.2).
   Start the executor and scheduler in ABO's kernel (P6, 06 §6.5.1); create the `<ns>.ops` instruments here and
   nowhere earlier (05-metrics §13 item 1). This adds a core metrics scope: add its `testdata/series.golden`,
   asserted by `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5).
3. **Cutover steps.** In `internal/books/cutover/stepids.go`, add this flip's books level constant and level-step ID
   (and core's, when core's level changes) beside F3-06's and F4-08's, and replace the M8 ID; never edit an earlier
   flip's constant or ID (F3-06). Register M4 and M5 in `internal/books/cutover/steps.go` in 03 §3.8's order (after
   M3, before M6 and M8) and pass `Importers.Tasks` and `Importers.Log`, plus this flip's books level constant as
   `RaisesLevel` and its new books level step as the `After` ref: F5-16's and F5-15's constructors take both from the
   caller. Then apply the per-flip level mechanism (order M2,
   stated in F3-04 and F3-06): **add** a new level step in `steps.go` for each owner whose level this flip raises
   (books' `books.level-<n>`; core's `core.level-<n>` when core's level changes), named for the raised target level,
   at that owner's next contiguous version, numbered immediately before M4 (which runs `After` it); the level steps
   F3-07 and F4-08 registered stay registered unchanged (05-storage §5S.3.5). **Replace** the M8 entry with a newly
   named one whose step ID names M5 as the last step it compacts after, so exactly one M8 is registered and it runs
   last; and bump the raised owners' declared `Spec.Level` in the same commit. F3-06's test checks the registered
   list (levels raised to the build's target before the first step of each flip, versions contiguous per owner, one
   M8, last). On a store that F4-08's `main` migrated, the earlier level steps are applied and the new M8 ignores the
   previous M8's `done` record and `applied` journal (03 §3.8; 05-storage §5S.3.13; 09 §9.2.3). Move HEAD's run-row store files (under
   `internal/books/store/` since R1-26's rename: `pebble_store_ops_v2.go`, `pebble_store_ops_v3*.go`, `pebble_store_op_rows.go`, `ReconcileOpsV3WithV2`) into
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
   each shared core folds into its native shell, conversion rule 2), `maintenance_job_op.go` (whose
   activity-summary entry is not reproduced unless books' activity subscriber covers it; decide it here and say
   which in the PR), `repairs.plan`/`repairs.apply`,
   `scheduler_maintenance_window_op.go`, `diagnostics_ops.go`'s v2 def, `addOpRegistrar` and the
   `serviceregistry` op wiring. Re-sign `repairs.Fixer` without the v2 reporter. Every caller of
   `pkg/plugin/sdk` moves to `core/tasks` + `module.Registrar` (the package itself and `sdkguard` go in R3,
   05-tasks §5T.8.1). Keep `internal/operations/state`, `tools/cmd/opsgen` and `ops.ts`.
5. **Routes.** Mount `internal/taskhttp` at `/api/core/v1/tasks`; mount F5-19's `core/internal/loghttp` on
   `CoreGroup()` (`loghttp.Deps{Journal: <the native journal store>, OtherUser: <books' registered predicate>}`)
   and **un-skip** F5-19's stage-13 cells: its permission, guard and 401 tables were written as
   `t.Skip("needs stage 13: F4-08")` because no stage 13 ran before F4-08; delete those skips (stage 13 is live from
   F4-08, W34) and run them through `coretest`'s completed `As`/`AssertPermission` on the mounted route. Then
   delete HEAD's `GET /api/v1/system/logs` (`internal/server/wire_system_routes.go`, its handler
   `GetSystemLogs` and its tests) in the same commit (05-logging §5; no alias, D10); regenerate
   `core/openapi/core.json` and `core/web/packages/api-client/src/generated/`; delete every HEAD route 05-tasks §5T.3.10's
   "Replaces" column names, `/api/v3/ops/*`, `internal/server/handlers/opsv3`, the v2 operations handlers (`internal/server/handlers/operations*.go`) and the
   scheduler task routes (05-tasks §5T.8.1 names `internal/server/handlers/scheduler`; that directory does not exist
   at `ac4368bdb`, so find the `/api/v1/tasks` handlers with `git grep -n '"/tasks' internal/server/`), and the two streams `/api/v1/operations/events` and `/api/v3/ops/events`
   (05-realtime §11). Books' domain routes under `/api/v1/operations` (`stale`, `clear-stale`, … §5T.3.10) stay and
   read runs through `Deps.Tasks`. No alias, no `Deprecation` header (D10).
6. **SPA and callers.** The Activity page, `useOperationsStore` and MaintenanceTab on the generated tasks client,
   polling `GET /api/core/v1/tasks/runs` until F6; `api.ts`, `operationPolling.ts` and its users off `ops.ts`; the
   first-party scripts and agent skills that called a deleted route (05-tasks §5T.3.10; 07). The SPA's system-log
   view on `GET /api/core/v1/system/logs` through the generated client, polling until F6 (05-logging §5).
7. **Metrics** (05-metrics §13 item 3): the executor's `ops_*` families with core's labels (`def_id` = native kinds,
   `plugin` → `module`); `operations_*`, `operation_*`, `op_*` deleted; every row of 05-metrics' one-by-one table
   applied to `deploy/grafana/dashboards/*.json`, `deploy/prometheus/*.yml` and `alert-rules.test.yml`
   (`AudiobookOrganizerOpFailuresHigh` and `AudiobookOrganizerOpStalled` deleted, their `V2` twins kept and re-joined
   on `def_id`/`module`; the `label_replace` compat rule deleted).
8. **Logging** (05-logging §11 item 1, R2 part): start the journal tap; delete `StandardLogger`'s system-log writes,
   the activity `Writer`'s line parsing (`internal/activity/writer.go`) and its tap, and `internal/logger/retention.go`.
   `core.log.journal.prune` is the only pruner; `operationlog:` history is pruned by `core.tasks.prune`. Drop the
   operation-record half from `books.maintenance.retention-and-hygiene` (it is `core.tasks.prune`'s now).
9. **The exemption directory empty** (05-tasks §5T.8.4; plan §(l) item 5). Delete
   `internal/books/testdata/tasklint_exempt.d/F5-18.txt` (the files whose HEAD definitions this PR deletes, T4's
   among them) and switch the ratchet in `internal/books/tasklint_test.go` to require the directory empty: any
   entry file present from here on fails `make ci`. `tasklint` then runs over every books task package with no
   exemption; rule 1's widened write sets start gating dispatch (an R2 release-note line).
10. **Bookkeeping.** `tools/cmd/sdkguard/internal-deps.txt` (regenerated: every `pkg/plugin/sdk` caller moved),
   `internal/arch/layering_test.go` (`layerOf` entries for created, moved and deleted packages),
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
| F5-14's stub kind list is the list this flip registers | `TestF5_ScheduledKindsRegistered` (name coined here): every kind in `books.ScheduledKinds()` is registered by one of the list functions `r.Tasks(...)` receives (the seven `t6g<k>Kinds`, `t7Kinds`, `jobs.Kinds`, `repairs.Kinds`, the plugins' `Kinds`), and each registered books kind's `Common.Schedule` is the schedule `books.ScheduleFor(k)` returns (nil exactly for the kinds outside the list). F5-14's parity test ran over stubs built from that list; this test is the re-check over the real kinds |
| Every converted `ResumeContinue` kind resumes from a HEAD checkpoint (`v1` included) or is listed as from-zero once | `TestCutover_CheckpointsResume` **(ABO)** |
| M4's run import then the first resume sweep acts per policy; an interrupted repairs apply re-applies no marked row | extend `TestCutover_TasksFixtureFromR0` and `TestCutover_RepairsRowsAndMarksImported` (F5-16's) with their registered-kind halves |
| No HEAD def ID left outside the retired claims | `TestCutover_KindReferencesRewritten` (F5-16's), green on this build |
| No series read by a dashboard or rule is unserved; the rules' tests pass | `TestDeployReferencedSeriesServed`; `promtool test rules` |
| No duplicate family on the scrape (the executor's `ops.*` vs. `opsmetrics`) | `TestABOMetricsGatherNoDuplicate` |
| The `ops` scope's series are the reviewed set (family, type, label set) | `metricstest.AssertSeries` over the scope's `testdata/series.golden` (05-metrics §12) |
| A store migrated by F4-08's `main` and booted by this `main` has its levels raised before M4, runs exactly one M8, and F4-08's binary then exits 78 on it | `TestCutoverFlipUpgrade`: this flip's cell in F2-15's cutover-suite harness, `internal/books/cutover_suite_test.go` (order M2; the cell shape is F3-04's) |
| The registered list raises the levels to the build's target before the first step of each flip, its versions are contiguous per owner, and it holds exactly one M8, last | F3-06's registered-list test, green on this build |
| A module whose `TaskStarts` names a kind of a disabled module still boots, and its start of that kind is refused at runtime (COR3-10; `TaskStarts` lands here, 06 §6.4.10 F5 row) | `TestTaskStarts_DisabledTargetModuleBootsAndRefusesAtRuntime` (05-tasks §5T.9), owned by this row |
| No gin route remains at a path this flip's native routes replace, and no route lies under `/api/v3/` | extend `TestCoreRoutesReplaceGin` (F4-08's) with the F5 cell (05-http-api §12) |
| Unknown kinds after the cutover are dropped, not fatal | `TestUnknownKindRun_DroppedAtResume` (core's), with an imported tombstoned run |
| A `books.*` run waits for `books.memdb` | `TestDispatch_WaitsForReadySignal` |
| The T5 gate | `go list -deps ./internal/plugins/... | grep -c pkg/plugin/sdk` is 0 |
| `tasklint` with an empty exemption list (05-tasks §5T.8.2 F5 row gate; §5T.8.4) | `internal/books/tasklint_test.go` requires `internal/books/testdata/tasklint_exempt.d/` empty |
| The journal's route replaces HEAD's | F5-19's `TestJournal_RequiresSystemManage` and `TestJournal_OtherUsersLinesNeedUsersView` (their stage-13 cells un-skipped here), plus a kernel mount cell (name coined here: `TestF5_SystemLogsRouteMounted`) proving `GET /api/core/v1/system/logs` answers 403 to a `core:system.view`-only principal on the booted ABO and `GET /api/v1/system/logs` answers 404 |
| Every intermediate `main` boots and passes e2e | `make test-e2e` |

## Acceptance (commands and expected output)

```bash
go build ./... && go vet ./...                                                                 # exit 0
go test ./internal/books/... -race -count=1 -run 'TestKindRenameMapCoversLedger|TestSecondModuleNoAudiobookEdit|TestParity_ResumeCancelShutdown|TestScheduleParity_AllDefinitions|TestCutover_|TestCutoverFlipUpgrade' -v | grep -c '^--- FAIL'   # 0
for d in internal/operations/registry internal/operations/childop internal/operations/opmode internal/opsmetrics \
         internal/scheduler internal/opscatalog internal/maintenance/job.go internal/walkresume pkg/ops \
         internal/server/handlers/opsv3 internal/logger/retention.go internal/logger/mocks; do test -e $d && echo "LEFT $d"; done   # no output
test -d internal/operations/state && test -d tools/cmd/opsgen && test -f web/src/generated/ops.ts && echo kept-for-R3   # kept-for-R3
git grep -n '/api/v3/ops\|/api/v1/operations/events\|/api/v1/operations/v2\|/api/v1/op-defs' -- ':!docs/' ':!CHANGELOG.md' ':!changelog.d/' | wc -l   # 0
git grep -n 'generated/ops' -- web/src/ | grep -v 'generated/ops.ts:' | wc -l                 # 0 (no SPA importer left)
git grep -n 'audiobook_organizer_operations_\|audiobook_organizer_operation_\|audiobook_organizer_op_' -- deploy/ | wc -l   # 0
go list -deps ./internal/plugins/... | grep -c 'pkg/plugin/sdk'                                 # 0
ls internal/books/testdata/tasklint_exempt.d/ 2>/dev/null | wc -l                               # 0 (the directory is empty or gone)
go test ./internal/books/ -count=1 -run "$(grep -o '^func Test[A-Za-z0-9_]*' internal/books/tasklint_test.go | sed 's/^func //' | paste -sd'|')"   # ok (no exemption)
git grep -n '"/system/logs"' -- internal/server/ | wc -l                                       # 0 (HEAD's route deleted)
git grep -n 'loghttp' -- core/kernel/ | wc -l                                                   # >=1 (mounted)
git grep -n 'needs stage 13' -- core/internal/loghttp/ | wc -l                                  # 0 (stage-13 cells un-skipped)
(cd core && go test ./internal/loghttp/ -count=1 -v | grep -c -- '--- SKIP')                    # 0
go test ./internal/books/ -run 'TestF5_ScheduledKindsRegistered|TestScheduleFor_KindListMatchesConversions' -count=1   # ok
grep -c '/system/logs' core/openapi/core.json                                                  # >=1 (regenerated)
promtool test rules deploy/prometheus/alert-rules.test.yml                                      # SUCCESS
make ci                                                                                         # exit 0
make -C core ci                                                                                 # exit 0
make test-e2e                                                                                   # exit 0
```

## STOP conditions

- A Base-state line fails (a dark F5 PR missing, an exemption entry file other than `F5-18.txt` present, F5-19's
  package absent).
- `tasklint` reports a finding once the directory is empty: fix the definition by 05-tasks conversion rule 3 if this
  PR's fold caused it; if the finding is in a converted package's unchanged code, STOP and report which row's
  conversion left it.
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

