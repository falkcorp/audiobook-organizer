<!-- file: docs/agent-tasks/platform-core/PC-12-08-s5-flip-t0-t1.md -->
<!-- version: 1.0.0 -->
<!-- guid: eafe6c5f-6262-48c5-b871-4e47a538e661 -->
<!-- last-edited: 2026-11-04 -->

# PC-12-08 — S5 flip (T0+T1): opsbridge wraps every v2 definition; books supplies its task store; the legacy-stream bridge; opsmetrics shrinks to ops.abandoned_calls; operations routes call the tasks client

| Field | Value |
|---|---|
| ID | PC-12-08 |
| Tier | **T4 Opus/xhigh–max** — every run of every definition changes engine in one PR; metric ownership, stream parity, the operations ServeMux component and the rollback format must all hold at once |
| Wave | W12 (S5), **the flip**: sets `shipped_stage: S5` and `release: S5r1` |
| Depends on | PC-12-02, PC-12-03, PC-12-04, PC-12-05, PC-12-06, PC-12-07 |
| Serialized | last PR of W12 |
| Trailer | `Stage-Release: S5 flip` (instead of `S5 1`, never both) |
| Digest | `docs/plans/platform-core/digests/S5.md` (all sections; §7 "T0+T1 flip PR") |
| Owning spec | 09 S5 (every row); 05-tasks §5T.8.1, §5T.8.2 row T0+T1, §5T.4 step 3, §5T.9, §5T.11; 05-realtime §4.9; 05-events §5E.13; 05-metrics §6, §13; 05-logging §11 step 1 (S5 bullet); 09 §9.2.1 rows 5 and 6 |

## Goal

Make ABO run on `core/tasks`. In one PR: books supplies `legacy.Stores.Tasks` (the one taskstore instance) and
`legacy.Stores.Logs`; opsbridge (the `internal/operations/registry` path, re-implemented in place) wraps every v2
`OperationDef` as a core task with `LegacyDefID` = its def ID; the v2 engine stops executing runs; the books ticker
enqueues through `Deps.Tasks`; HEAD's ops series move to the executor and `opsmetrics` shrinks to `ops.abandoned_calls`;
books bus subscribers feed the three legacy streams, the v1 status bridge, the activity mirror and the five
`operations_*` client_golang families; the operations routes call the tasks client; the task aliases mount; the journal
starts and its predecessors are deleted. Stored format: unchanged (rollback = binary swap).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-12-08" -b feat/pc-12-08-s5-flip origin/main
cd "$REPO/../audiobook-organizer-pc-12-08"
git rebase origin/main
make web-deps
git worktree list
grep -E '^[a-z-]+:' Makefile | grep -E 'compat-rehearsal|ci:|test-e2e' # confirm the targets this brief uses
```

Before dispatch the coordinator confirms: PC-12-01 … PC-12-07 merged; OWNER-12's `S4r2` row current and ≥ 14 days old;
the owner has read the P1 census of `opv2:`/`opv3:` undecodable rows (09 §9.7 P1); no open PR touches
`internal/server` or `internal/operations/registry`.

## Exact files

Create: `internal/activity/` additions (`mirror.go` receiving `registry/activity_mirror.go` and the mirror part of
`reporter_db.go`; the package exists at HEAD with 18 files), `internal/books/stream_bridge.go`,
`internal/books/stream_bridge_test.go`, `internal/books/metrics_gather_test.go`.

Modify: `internal/operations/registry/` (opsbridge), `internal/opsmetrics/`, `internal/metrics/metrics.go` (calls move to
the books subscriber; families unchanged), `internal/scheduler/` (ticker on `Deps.Tasks` and `ClockStore`; v3 loop's two
`opsmetrics` calls become `ReportScheduleTick`), `internal/server/` (operations, maintenance-window, op-defs, tasks and
v3 handlers; registry and activity-writer wiring; status of `legacy_backfill_op.go`), `internal/books/module.go`,
`cmd/root.go`, `ci/stage.txt`.

Also modify (not in the inventory row; see Planner notes): `internal/activity/writer.go` (delete the line parser
`Write`, HEAD :149-215, and its tap), `internal/logger/standard.go` (delete the `AddSystemActivityLog` write, HEAD :51),
`internal/logger/retention.go` (delete), `internal/plugins/maintenance/cleanup.go` and `deps.go` (the `PruneOldLogs`
caller), `internal/telemetry/contract/` or `internal/telemetrypolicy/` (series goldens' S5 additions),
`.github/workflows/extraction-gates.yml` (adds `TestABOMetricsGatherNoDuplicateOrMissing`).

## Re-verify greps (identical at `748fd787a` and the plan checkout unless marked)

```bash
grep -h 'protected.*("/operations' internal/server/*.go | wc -l                                   # expect 38 (generated count for TestS5_OperationsComponentStaysBooks)
grep -rn '"/maintenance-window' internal/server/*.go | grep -v _test | wc -l                     # expect 3
grep -n "metrics.IncOperationStarted\|metrics.ObserveOperationDuration\|metrics.IncOperationCompleted\|metrics.IncOperationFailed\|metrics.IncOperationCanceled" internal/operations/registry/worker.go | grep -v '^.*://' | wc -l   # expect 5 (HEAD :611, :1204, :1293-1297)
grep -c 'Name: *"operations_\(started\|completed\|failed\|canceled\)_total"\|Name: *"operation_duration_seconds"\|Name: *"op_items_\(processed\|total\)"' internal/metrics/metrics.go   # expect 7
grep -n "^func New(mp metric.MeterProvider) \*Recorder\|^func Default() \*Recorder" internal/opsmetrics/opsmetrics.go | wc -l   # expect 2
grep -c "opsmetrics.Default().ScheduleMissed\|opsmetrics.Default().ScheduleLag" internal/scheduler/v3_schedule.go   # expect 2
grep -n "required = \[\]auth.Permission{auth.PermSettingsManage}" internal/server/handlers/operations_v2.go   # expect 1 (implicit default, HEAD :781-800)
grep -n "func (r \*Registry) propagateLegacyOpStatus" internal/operations/registry/legacy_op_status.go      # expect 1
grep -n "func newActivityMirror" internal/operations/registry/activity_mirror.go                            # expect 1
grep -rln 'text/event-stream' --include=*.go internal | grep -v _test | sort                                # expect 3: operations_v2.go, opsv3/events.go, realtime/events.go
grep -n "^shipped_stage: S4$\|^release: S4r2$" ci/stage.txt | wc -l                                          # at this wave's base: expect 2
test -d internal/books/taskstore && test -d core/internal/executor && test -d core/internal/scheduler && test -d core/internal/taskhttp && test -d core/internal/logjournal && echo ok   # expect ok
```

Any moved count: STOP and report (the done-checks use the generated count at the base; record both in the stage ledger).

## Invariants (each has a named test; a failing one blocks the merge)

1. **One run engine.** After this PR no run executes on the v2 engine; opsbridge only adapts definitions. Every
   registered definition (233 at `748fd787a`, generated count at base) runs through `core/tasks` with declared
   `Parallelism`: legacy `registry.RunItems` maps to `tasks.Fixed(n, "legacy Concurrency")` from a non-zero
   `Concurrency`, else `tasks.Sequential("legacy zero Concurrency")`, 1:1 (CLAUDE.md; no def's concurrency changes here).
2. **One store instance, one lock.** The P4c `legacy.Stores.Tasks` result, the store books' `Register` receives, and
   the stores opsbridge, `childop` and the books ticker use are one instance with one `opsMu`
   (`TestLegacyStoresAreSingletons`). The ticker commits clock batches through `storage.CommitLocked` with that
   instance's `WriteLock` locks; opsbridge and the ticker reach the ops families only through taskstore's staging
   methods (no HEAD method that takes `opsMu` inside a hold).
3. **One scheduler per kind.** The books ticker keeps ticking all 38 `TaskDefinition`s, enqueuing through `Deps.Tasks`
   and advancing clocks through `ClockStore`; core's scheduler polls only kinds that declare a `Schedule` (none of ABO's
   until T7). `TestScheduleParity_AllDefinitions` (golden from PC-12-04, unchanged) and `TestOneSchedulerPerKind` (now
   with its second half) pass.
4. **One owner per metric family** (05-tasks §5T.11 table). The executor records the seven run families; core's
   scheduler records `ops.schedule_lag`/`ops.schedule_missed` (the v3 loop calls `ReportScheduleTick(ctx, kind, dueAt,
   folded)` on `Deps.Tasks`, asserted as `legacy.TasksScheduleTicks`, exactly where HEAD recorded); `opsmetrics.New`/
   `Default()` create and observe **only** `ops.abandoned_calls` (both `source` values); one books subscriber on
   `core.task.run-changed.v1`/`core.task.finished.v1` records the seven `internal/metrics` operations families for kinds
   with a `LegacyDefID`, under that ID; the executor records none of them.
5. **Streams keep HEAD's view.** `internal/books/stream_bridge.go`, registered with `Registrar.Subscribe` on
   `core.task.run-changed.v1` and `core.task.log-appended.v1`, feeds stream A (hub A, `operation.*`, after
   `tasks.RedactRunView(nil, v)`), stream B (`registry.EventHub`, `op.*` names) and stream C (the v3 broker's `run`
   events). It forwards **only** kinds with a `LegacyDefID`, translated to that ID; never a `core.*` kind; core calls no
   ABO code.
6. **Operations routes stay books'.** All 38 `/api/v1/operations` gin routes and the three `/api/v1/maintenance-window/*`
   routes stay books' gin routes; no core route or alias in the prefix. From this PR: `POST …/v2` → `Client.Start`;
   `DELETE …/v2/:id` → `Client.Cancel`; `…/v2/:id/retry` and `…/finish` → `Client.Decide(Resume|Finish)`;
   `POST …/pause`/`…/resume` → `Client.Pause`/`Unpause`; `GET …/pause` → `Client.PauseState`; `GET …/timeline` →
   `Client.Timeline`; `GET …/v2/:id/logs/download` → `Client.Log`; `DELETE …/v2/:id/record` → `Client.Discard`; the
   five component routes (`GET …/{id}/{activity,changes,result,results}`, `GET …/v2/{id}`) read through
   `Client.Get`/`Client.Runs`, the v2 handler rendering HEAD's v2 shape from the `RunView`; each keeps HEAD's body and
   HEAD's `s.perm` atom (`library.view` on the five). `maintenance-window/run` calls `Client.Start`.
7. **Permissions.** Every definition that declared no permission gets an explicit `Permissions.Start` of the
   atom-carrying legacy requirement for `settings.manage` (HEAD's implicit default, `operations_v2.go:781-800`), so
   auth-off still starts it and a scoped legacy key keeps HEAD's intersection.
8. **Journal, one writer, one pruner.** Books passes its `JournalStore` (PC-12-07) as `Manifest.LegacyStores.Logs`; this
   PR deletes the activity `Writer`'s line parsing and tap, `StandardLogger`'s `syslog:` writes and
   `internal/logger/retention.go` with its callers; `operationlog:` pruning is the run store's `Prune` under
   `core.tasks.history_retention`.
9. **Rollback = binary swap** (09 §9.2.1 rows 5, 6): runs carry `LegacyDefID`, clocks and stamps are written in HEAD's
   format, nothing native-only is stored beside a legacy row.

## Steps

1. Read the digest and 09 S5 end to end. List HEAD's sources of every frame on streams A, B and C and every caller of
   the five `operations_*` helpers; write that list into the PR description.
2. Write the tests below first.
3. Books composition (`internal/books/module.go`, `cmd/root.go`): `Manifest.LegacyStores.Tasks` returns the memoised
   taskstore instance; `Manifest.LegacyStores.Logs` returns books' journal store; `Registrar.Health` declares
   `books.memdb` (Readiness, not Critical) and the warm-up pushes it through `Deps.Health`; every `books.*` kind that
   reads books declares `Ready: {"core.storage", "books.memdb"}`; books' start hook stops calling the ops reconcile
   (the executor's `Recover` runs it).
4. opsbridge (`internal/operations/registry/`, in place): each v2 `OperationDef` → a core task registered by books with
   `Common.LegacyDefID` = the def ID and `Common.LegacyPlugin` = HEAD's plugin ID; `Run(ctx, params, Reporter)` called
   with a `Reporter` over `RC`; legacy `RunItems` per invariant 1; the dispatcher, worker, watchdog, resume and shutdown
   paths stop running runs; `legacy_op_status.go`/`legacy_backfill.go` stay, driven by a books subscriber on
   `core.task.finished.v1` (only `LegacyDefID` kinds) and by `internal/server/legacy_backfill_op.go`, calling taskstore's
   v1 methods directly (the `legacyOpStore`/`legacyBackfillStore` assertions go).
5. Activity mirror: move `activity_mirror.go` and the mirror part of `reporter_db.go` into `internal/activity`, as a
   books subscriber on `core.task.log-appended.v1` keeping HEAD's bounded queue and counted drops.
6. Metrics per invariant 4: delete the five `metrics.*Operation*` calls from `worker.go`; add the books subscriber
   (it may live in `internal/books`); shrink `opsmetrics`; replace the v3 loop's calls with `ReportScheduleTick`; keep
   `internal/metrics/metrics.go`'s families, names, help strings and labels byte-identical.
7. Stream bridge per invariant 5 (`internal/books/stream_bridge.go`).
8. Routes per invariant 6; register PC-12-06's `RegisterLegacyAliases` and remove HEAD's gin registrations of the 16
   aliased paths (`/api/v3/ops/*` except `GET /api/v3/ops/events`, `/api/v1/op-defs`, `/api/v1/tasks`); the stream route
   `GET /api/v3/ops/events` stays HEAD's broker until S6.
9. Permissions per invariant 7. Journal per invariant 8. Dial `core.tasks.kinds` is honoured (from PC-12-02).
10. `ci/stage.txt`: `shipped_stage: S5`, `release: S5r1`.
11. Series goldens: the seeded series contract test's golden adds only the nine new executor families
    (`ops.queue_depth`, `dispatch_deferred`, `cpu_slots_in_use`, `lane_in_use`, `ref_lock_wait`, `held_runs`, `yields`,
    `schedule_errors`, `undecodable_records`); `histogram_buckets.golden` unchanged; add
    `TestABOMetricsGatherNoDuplicateOrMissing` to `extraction-gates`.
12. Prepare release notes (prepended to the generated notes), from 09 §9.2.1: row 5 "Runs of new task kinds restart after
    a rollback; one checkpointed job may start over." and row 6 "System-log entries written on this release are not
    shown after a rollback; they reappear after the upgrade." Put them in the PR description for the owner.
13. `make compat-rehearsal` (required on this PR), `make ci`, `make -C core ci`.

## Code contract

```go
// 05-tasks §5T.3.1 / §5T.3.13 / 06 §6.4.11
Common.LegacyDefID Kind; Common.LegacyPlugin string
Stores.Tasks func(legacy.Handles) (tasks.Stores, error)   // legacy.Stores field
Stores.Logs  func(legacy.Handles) (log.JournalStore, error)
type TasksScheduleTicks interface { ReportScheduleTick(ctx context.Context, kind tasks.Kind, dueAt time.Time, folded int) error }
// 05-tasks §5T.3.10 Client methods named in invariant 6
Start, Get, Runs, Cancel, Decide, Pause, Unpause, PauseState, Timeline, Log, Discard
func RedactRunView(who rbac.Checker, v RunView) RunView // nil who blanks Current
```

## Tests (row done-tests; names from the spec)

- `TestS5_OperationsComponentStaysBooks` — real engine, ABS on and off: five routes on the gin engine, exactly the
  generated count of `/api/v1/operations` routes (38), no core route or alias in the component, `/api/v1/operations/v2/x`
  reaches the v2 handler, `/api/v1/operations/x/activity` the activity handler, `/api/v1/operations/x/y` 404 in the
  legacy shape. `TestLegacyMount_GeneralisedPatternAcceptsAnyMember` unchanged.
- `TestS5_OperationsTimelineLogDiscardParity` — HEAD's timeline rows for a finished-at window with in-flight runs; HEAD's
  log body; 409 for discarding a running run; a terminal run's discard removes every key and the resume sweep does not
  resurrect it.
- `TestABOMetricsGatherNoDuplicateOrMissing` (in `internal/books/metrics_gather_test.go`, added to `extraction-gates`).
- `TestHistogramBucketsPinned` with `histogram_buckets.golden` unchanged (HEAD `internal/telemetry/contract/series_contract_test.go:1125`;
  `internal/telemetrypolicy` from S1).
- The op-ID ledger (`internal/server/op_id_aliases_test.go`) green.
- `TestS5Bridge_LegacyStreamsShowCoreRuns`, `TestS5Bridge_RepairsRunHiddenFromViewer` (in `stream_bridge_test.go`).
- The G5 rollback test of 05-tasks §5T.9 and the prev-binary rows there: `TestLegacyFormat_RoundTripWithHEAD`,
  `TestLegacyStoreRefusesNonHEADRecords`, `TestLegacyFormat_RetryOnPrevRunsOnce`,
  `TestUnknownKindRun_DroppedByPrevVisibleAfterRollForward`, `TestClock_InPlace_RollbackNoStampedeNoDoubleFire`,
  `TestReqStamps_InPlace_HonouredOnPrev`, `TestLegacySteps59to66_RunBeforeExecutorStart`,
  `TestLegacyHistory_ReadInPlaceUndecodableHidden`, `TestLegacyTaskAliases`.
- `TestLegacyStoresAreSingletons`, `TestLegacyStoreMissingFailsBoot`, `TestDispatch_WaitsForReadySignal` (books instance:
  a `books.*` run is not dispatched before `books.memdb` is Up), `TestOneSchedulerPerKind` (complete),
  `TestScheduleParity_AllDefinitions`, `TestOpsFamiliesWrittenOnlyByTaskstore`, `TestLegacyOpsWriters` (ratchet at its
  T0+T1 target).
- Resume rehearsal: `kill -9` mid `books.acoustid.backfill`, restart, the checkpoint resumes (scripted test; name coined
  here `TestResumeRehearsal_AcoustidBackfillKill9`).
- `opsgen`'s freshness gate shows `web/src/generated/ops.ts` byte-identical.
- The R-test task cells (`make compat-rehearsal`): "a run started on cur with a `LegacyDefID` resumes on prev", "a
  `Requires` stamp written on cur is honoured on prev", "a schedule clock advanced on prev neither double-fires nor
  stampedes on cur", "store at books v64 → cur". Cells a/b only if a UI call moved (none should).

## Acceptance

```bash
grep -n "^shipped_stage: S5$\|^release: S5r1$" ci/stage.txt | wc -l                         # expect 2
go test ./internal/server/ -run 'TestS5_OperationsComponentStaysBooks|TestS5_OperationsTimelineLogDiscardParity' -count=1 -v | grep -c '^--- PASS'   # expect 2
go test ./internal/books/ -run 'TestABOMetricsGatherNoDuplicateOrMissing|TestS5Bridge_' -count=1 -v | grep -c '^--- PASS'   # expect 3
go test ./internal/scheduler/ -run 'TestScheduleParity_AllDefinitions|TestOneSchedulerPerKind' -count=1    # ok
grep -rn 'metrics.IncOperation\|metrics.ObserveOperationDuration' internal/operations/registry --include=*.go | grep -v _test | wc -l   # expect 0
grep -rn 'opsmetrics.Default().Schedule' internal/scheduler --include=*.go | grep -v _test | wc -l   # expect 0
test ! -e internal/logger/retention.go && echo ok                                            # expect ok
git diff --exit-code origin/main -- web/src/generated/ops.ts && echo unchanged                # expect unchanged
make compat-rehearsal      # exit 0, both PREV_REF rows
make -C core ci            # exit 0
make ci                    # exit 0
make test-e2e              # exit 0; Activity page shows live progress; the request log shows no enqueue on task_kind beyond the Activity page's aliases
```

## Out of scope

- T2–T7 batches (W13); converting any definition to a native task or `ScheduleFromConfig`.
- Any parallelism change (separate PR under CLAUDE.md).
- The S6 projections; deleting `registry.EventHub`, hub A or the v3 broker (PC-15-04).
- Deleting `OperationDef`, opsbridge, `childop`, `opsmetrics` (T8, PC-14-01).

## STOP conditions

- `TestABOMetricsGatherNoDuplicateOrMissing` shows a family with two emitters or a changed label value.
- `TestScheduleParity_AllDefinitions` needs its golden regenerated.
- A definition cannot be adapted without changing its concurrency or its stored def ID.
- `compat-rehearsal` shows `prev` misreading any row.

## Rollback

Binary swap to the `S4r2` build (09 §9.2.1 rows 5 and 6): the previous build reads every run, clock, pause marker and
stamp in its own format; runs of kinds it does not register are dropped by its resume sweep and re-run on schedule after
roll-forward; system-log entries written on S5 are not shown by it. A revert PR is the exact inverse diff
(`check_soak.py` admits only that).

## Repo rules

- File headers on every changed file; `changelog.d/` fragment without a header.
- Commits end with `Stage-Release: S5 flip` then `Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds;
  required checks include `compat-rehearsal` and `unit-lifecycle` (every flip PR) besides `extraction-gates`,
  `docker-image`, `vet-darwin`, `vet-integration`, `core-ci`; `gh pr merge <n> --rebase`, never squash, never `--admin`.
  `check_soak.py` check (a) needs OWNER-12's current `S4r2` row ≥ 14 days old.
- Boot writers changed here (books' start hook, `Recover`) extend `TestBootCrashMatrix_ABO` in this PR.
- After merge: the coordinator hands off to OWNER-13 with the release-note lines and the done-check list.

## Planner notes

- The row's file list lacks the journal-coupled deletions moved here from PC-12-07 (`internal/activity/writer.go`,
  `internal/logger/standard.go`, `internal/logger/retention.go`, `internal/plugins/maintenance/cleanup.go` and `deps.go`)
  and the series-golden and workflow files (`internal/telemetrypolicy/` or `internal/telemetry/contract/`,
  `.github/workflows/extraction-gates.yml`).
- `internal/activity/` exists at HEAD (18 files), so "create" means adding the mirror files to it.
