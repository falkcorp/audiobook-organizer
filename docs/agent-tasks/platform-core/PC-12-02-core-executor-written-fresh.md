<!-- file: docs/agent-tasks/platform-core/PC-12-02-core-executor-written-fresh.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1ec38075-5ffb-43d7-b15c-cce5a1549a4e -->
<!-- last-edited: 2026-11-04 -->

# PC-12-02 — Core executor written fresh over tasks.RunStore and RunState, with rc.Await, RunItems Parallelism, WriteRuns through CommitLocked and HEAD-named instruments

| Field | Value |
|---|---|
| ID | PC-12-02 |
| Tier | **T4 Opus/xhigh–max** — fresh code that will run every one of the 233 definitions (informative count at `748fd787a`); cancel, resume and shutdown must match HEAD, and the run lock must span core's commit or early fix 6's race class reopens |
| Wave | W12 (S5, release `S5r1`, dark until PC-12-08) |
| Depends on | PC-12-01, PC-12-03 |
| Serialized after | PC-12-01 → {PC-12-03, PC-12-04, PC-12-05, PC-12-07} → **PC-12-02** → PC-12-06 → PC-12-08 |
| Trailer | `Stage-Release: S5 1` |
| Digest | `docs/plans/platform-core/digests/S5.md` §4, §5 rules 1–13 |
| Owning spec | 05-tasks §5T.3.2, §5T.3.3, §5T.3.5–§5T.3.7, §5T.3.10, §5T.3.13, §5T.4, §5T.5, §5T.7, §5T.11; 09 S5 "Mechanism"; 09 §9.9; 05-storage §5S.3.2 (`CommitLocked`, `LocksHeld`, `RetryOnConflict`, lock ranks) |

## Goal

Write `core/internal/executor` from scratch against `tasks.RunStore`/`tasks.RunState` (it is a re-implementation, an
04 §8.6 row: no file of `internal/operations/registry` moves). Add the run-context half of the `tasks` API (`RC`,
`ItemRC`, `RunItems`, `ForEach`, `RunSeq`, `rc.Await`). Every run, clock, pause and stand-down write goes through
`WriteRuns`, which commits with `storage.CommitLocked` under `storage.RetryOnConflict` holding the kernel-resolved
`RunLocks`. The executor records HEAD's run instruments under HEAD's names. PC-12-03's parity suites, already green
on the v2 engine, must turn green on this executor. ABO still runs the v2 engine after this PR.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-12-02" -b feat/pc-12-02-core-executor origin/main
cd "$REPO/../audiobook-organizer-pc-12-02"
git rebase origin/main
make web-deps
git worktree list
```

Suggested split (CLAUDE.md subagent guidance): a read-only exploration pass over `internal/operations/registry`
(dispatcher, worker, watchdog, resume, shutdown, batch runner, chunk ledger, pause gate, stand-down, warm-up gate)
to list HEAD's behaviours as test cases, then implementation, then an independent review of the lock and drop rules.

## Exact files

Create:

- `core/internal/executor/` — dispatcher, workers, watchdog, resume sweep, shutdown, per-run writer, progress lock,
  ref-lock table, CPU budget, pause and stand-down logic, the state machine over `tasks.RunState`, the generic gated
  write of 05-tasks §5T.3.5, `WriteRuns`, the instruments, `export_test.go` and `legacy_mirror_test.go` (package
  `executor_test`).

Modify (see Planner notes; not in the inventory row):

- `core/tasks/` — `rc.go` (methods of `RC` and `ItemRC`), `runitems.go` (`RunItems`, `ForEach`, `RunSeq`), `await.go`
  (`rc.Await`). File names coined here.
- `core/tasks/taskstest/` — the executor-level `StoresSuite` cases named below.
- `core/kernel/`, `core/module/`, `core/app/` — the tasks wiring of 06 §6.5.1 P4c/P6/P7 and §6.5.2 (step 6 below).

## Re-verify greps

```bash
# HEAD behaviour sources (read, never import); identical at 748fd787a and the plan checkout
ls internal/operations/registry/*.go | grep -v _test.go | wc -l                     # expect 35
grep -n "opsmetrics.Default().ScheduleLag\|opsmetrics.Default().ScheduleMissed" internal/scheduler/v3_schedule.go   # expect 2 (lines ~444, ~447)
grep -n '"audiobook_organizer.ops.run.duration"\|"audiobook_organizer.ops.schedule_lag"' internal/telemetry/views.go # expect 2 (lines ~42, ~58)
grep -c 'Int64Counter("audiobook_organizer.ops.runs"\|Float64Histogram("audiobook_organizer.ops.run.duration"\|Int64Counter("audiobook_organizer.ops.items"\|ObservableGauge("audiobook_organizer.ops.inflight"\|ObservableGauge("audiobook_organizer.ops.progress_age"\|ObservableGauge("audiobook_organizer.ops.progress_timeout"\|ObservableGauge("audiobook_organizer.ops.zombies"' internal/opsmetrics/opsmetrics.go   # expect 7
grep -n "context.WithTimeout(context.Background(), 10\*time.Second)" internal/server/server_lifecycle.go | head -3   # HEAD's 10 s registry drain, expect a hit at ~:611
# Prerequisites from this wave
test -d core/tasks && test -d core/internal/taskstore && test -f core/health/ready.go && test -f core/legacy/tasks.go && echo ok   # PC-12-01; expect ok
test -d core/tasks/taskstest/parity && echo ok                                       # PC-12-03; expect ok
test ! -e core/internal/executor && echo ok                                          # expect ok
```

A zero on a target, a moved count or a missing prerequisite: STOP and report.

## Invariants (each has a test below; violating one is a STOP, not a judgement call)

1. **One commit path.** Every batch carrying a `RunStore`, `ClockStore`, `PauseStore` or `StandDownStore` write is
   built inside `WriteRuns`, which calls `storage.CommitLocked(ctx, open(), storage.Sync, stage, runLocks...)` under
   `storage.RetryOnConflict` and re-runs build-and-commit on every attempt (05-tasks §5T.3.10, §5T.7). The executor never
   imports `legacy`, never names a lock string and never calls `WriteLocks`; it receives `RunLocks []storage.Lock` from
   the kernel (COR10-5).
2. **Not re-entrant.** `WriteRuns` inside a `stage` returns `tasks.ErrWriteRunsNested` and runs nothing.
3. **Core mutexes order before the hold.** Dispatcher (rank 50), ref-lock table (60), per-run writer (70), progress
   lock (80) are taken before `WriteRuns` or after it returns, never inside `stage` (05-storage §5S.3.2 rank table).
4. **Drop rule (05-tasks §5T.7).** On a conflict a write is never re-staged over the old read: a bare progress tick
   is dropped on any state change; a chunk-ledger commit, checkpoint, Fixer row or mark, page append, item outcome or
   strike is re-staged only while the fresh read shows the run non-terminal under the same `Attempt` and `FenceEpoch`
   (`stopping` included), else refused.
5. **Run order (05-tasks §5T.4 step 2).** `PauseStore.Get` → `Recover` (when `Stores.Runs` satisfies the unexported
   mirror of `legacy.TasksRecoverer`) → `StandDownStore.List` (surviving marker: its runs stay `interrupted_quiesced`
   this boot, the marker is cleared, WARN naming the holder) → resume sweep over `Unfinished` → debounce reload →
   dispatcher (100 ms) → watchdog (30 s) → `core.tasks.workers` workers (default 8). Dispatch waits for each def's
   `Ready` signals through `health.Registry.Wait`; waiting runs hold no worker, key or clock.
6. **Shutdown (05-tasks §5T.4 step 4).** Flip `shuttingDown`, release claimed-not-started, cancel with `ErrShutdown`,
   wait `min(core.tasks.shutdown_drain (10 s), remaining budget)`, stamp the rest `interrupted`, revoke fences, return
   before storage closes; one `EXTEND_TIMEOUT_USEC` request (≤ `core.tasks.shutdown_extend_max`, 60 s) only for an
   `Uninterruptible` run under `NOTIFY_SOCKET`. Never an unbounded wait.
7. **Parallelism 1:1 (CLAUDE.md; 05-tasks §5T.8.2).** `RunItems` honours the declared `Parallelism` exactly; zero
   returns `*ItemsError{First: ErrNoParallelism, FirstIndex: -1}` before any item. No goroutine per item beyond the
   declared worker count; no unbounded fan-out. Label closures run inside workers: counters they read are atomic.
8. **Metric ownership (05-tasks §5T.11).** The executor records `ops.runs`, `ops.run.duration`, `ops.items`,
   `ops.inflight`, `ops.progress_age`, `ops.progress_timeout`, `ops.zombies` on scope `<ns>.ops` with HEAD's names,
   units, label keys and outcome values (`def_id` = the run's `StoredKind`; `plugin` = `Common.LegacyPlugin`, else the
   module ID), plus the nine new families of §5T.11's table. `ops.run.duration` and `ops.schedule_lag` declare HEAD's
   boundaries as plain numbers through `Scope.View`:
   `0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 14400, 43200, 86400` and
   `1, 5, 15, 60, 300, 900, 3600, 21600, 86400`. No ABO name enters core. (ABO's `opsmetrics` keeps emitting until
   PC-12-08 shrinks it; ABO does not run this executor before then, so no family has two emitters in any build.)
9. **Events.** After each commit publish `core.task.run-changed.v1` and `core.task.log-appended.v1`; once per terminal
   transition `core.task.finished.v1`. Call `Options.OnTerminal(ctx, b, v)` inside the terminal CAS batch (a no-op hook
   until S7). Publish nothing else; call no ABO code.
10. **Dark for ABO.** The kernel builds `tasks.Stores` and the executor only when at least one module registers a task
    kind (`Registrar.Tasks`) or supplies `legacy.Stores.Tasks`. ABO does neither before PC-12-08, so no pre-flip build
    records a `core/tasks` backend in the claim ledger's `backends` entry (05-tasks §5T.6: a recorded `native` choice
    would make the flip build fail with `ErrLegacyStoreMissing`).

## Steps

1. Write the tests below first (red).
2. `core/tasks`: implement `RC`/`ItemRC` methods of 05-tasks §5T.3.3 over an unexported per-run handle the executor
   supplies, plus `RunItems`, `ForEach`, `RunSeq` (5T.3.2) and `func (rc *RC) Await(ref RunRef) (Outcome, error)`
   (5T.3.3, written fresh; HEAD's `childop/follow.go` is the behaviour reference, not a source). No exported
   constructor may let a module forge an `RC` (coin the seam; mark it `// name coined in PC-12-02`). No package-level
   mutable state.
3. `core/internal/executor`: implement `tasks.Executor` (05-tasks §5T.3.10) with the invariants above. The state
   machine is fresh code over `tasks.RunState`; never import `internal/operations/state` or any ABO path.
4. Detect `legacy.TasksRecoverer` through an unexported interface with the identical method set; `export_test.go`
   exports a test alias; `legacy_mirror_test.go` (package `executor_test`) asserts mutual assignability both ways
   (05-tasks §5T.3.13).
5. Instruments per invariant 8; `TestNoGlobals_Tasks` covers the executor (instruments are executor fields).
6. Kernel wiring (06 §6.5.1, §6.5.2; 05-tasks §5T.3.13): at P4c build `Stores` from the supplying module's
   `legacy.Stores.Tasks` (called exactly once per App) or `NativeStores`; record the choice in the claim ledger's
   `backends` entry; assert `legacy.WriteLock` on `Stores.Runs` and, if present, pass
   `WriteLocks([]string{legacy.LockRuns})` to the executor as `RunLocks`; at P6/P7 accept `Registrar.Tasks`,
   `Registrar.Subscribe` (already from S4), `Registrar.Health`, upsert `c/tasks/def/<kind>` snapshots and run the
   op-ID ledger gate; hand modules `Deps.Tasks` (a `Client` that stamps the actor) and `Deps.Health`; start
   `Executor.Run` at the executor step, after `Bus.Open`; start PC-12-04's `core/internal/scheduler` at the next step
   and give the `Deps.Tasks` client of the module that supplies `legacy.Stores.Tasks` the `legacy.TasksScheduleTicks`
   capability over it. Apply invariant 10. Register storage's `core.storage` readiness component.
7. Boot writers (06 §6.5.1 writer table; A-8): the def-snapshot upsert, `Recover`, the stand-down clear and the resume
   sweep's transitions extend core's `TestBootCrashMatrix` in this PR.
8. Run PC-12-03's parity suites against this executor (they must turn green with no edit to the suites' assertions).
9. Acceptance, headers, `changelog.d/` fragment.

## Code contract (exact, from 05-tasks §5T.3.10)

```go
type Stager interface {
	StartIn(ctx context.Context, b *storage.Batch, kind Kind, req StartRequest) (RunRef, error)
	WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error
}
type Executor interface {
	Client
	CancelAs(ctx context.Context, id RunID, by Actor) error
	Approve(ctx context.Context, id RunID, a ApproveRequest) error
	DecideAs(ctx context.Context, id RunID, d Decision, by Actor) error
	StartIn(ctx context.Context, b *storage.Batch, kind Kind, req StartRequest) (RunRef, error)
	WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error
	PauseAs(reason string, by Actor) error
	UnpauseAs(by Actor) error
	Run(ctx context.Context) error
	Shutdown(ctx context.Context) error
}
// 05-tasks §5T.3.2 / §5T.3.3
func RunItems[T any](rc *RC, par Parallelism, items []T, fn func(ic *ItemRC, item T) error, opts ...ItemsOptions[T]) (ItemsResult, error)
func (rc *RC) Await(ref RunRef) (Outcome, error)
// 05-storage §5S.3.2
func CommitLocked(ctx context.Context, b *Batch, d Durability, stage func(ctx context.Context, b *Batch) error, locks ...Lock) error
func LocksHeld(b *Batch, l Lock) bool
```

## Tests to write first (names from the spec unless marked coined)

- `TestDispatch_WaitsForReadySignal` — a run whose def's `Ready` lists a component not yet Up is not dispatched; it
  dispatches once the component reports Up (core version over a test component; PC-12-08 re-runs it with `books.memdb`).
- Added as `taskstest.StoresSuite` cases, so they run over `NativeStores` here and over books' store through PC-12-05's
  suite instance (`synctest`): `TestLedgerCommitAfterTerminalRefused`, `TestConcurrentTransitionsOfDistinctRunsAllCommit`,
  `TestCancelCommitsUnderProgressTicks`, `TestFixerMarksLandDuringQuiesce`.
- 05-tasks §5T.9 core rows that belong to the executor: `TestRunItems_ZeroParallelismFailsBeforeFirstItem`,
  `TestRunItems_CancelStopsDispatch`, `TestRunItems_ErrorShapeSameSeqAndPar`, `TestRunItems_LabelGetsItemOnly_RaceFree`,
  `TestRunItems_CountersAtomic`, `TestRunItems_WatermarkResume`, `TestRunItems_CPUBudgetShared`,
  `TestParallelismCap_LowersDeclared`, `TestRunItems_PauseParksBeforeItemClock`, `TestResume_PolicyMatrix`,
  `TestCancel_GraceThenFenceRevoked`, `TestZombieCapBlocksModule`, `TestShutdown_DrainBoundedAndStampsInterrupted`,
  `TestShutdown_UninterruptibleExtendsOnceOnlyUnderNotify`, `TestStandDown_MarkerAtBootDefersQuiescedRun` (core
  half), `TestOnTerminal_NotificationStagedInTerminalBatch`, `TestPerRunSeq_NoReissueAfterCrash`, `TestNoGlobals_Tasks`.
- `TestWriteRuns_NestedRefused` and `TestWriteRuns_HoldSpansFirstReadToCommit` (names coined here): a nested call
  returns `ErrWriteRunsNested`; with a test `storage.Lock` passed as `RunLocks`, a second `WriteRuns` blocks from the
  first one's first read until its commit returns.
- `TestKernel_NoTasksBackendWithoutTaskKinds` (name coined here): an App with no task kind and no legacy tasks store
  boots, builds no executor and records no `core/tasks` backend.
- `TestExecutorInstruments_HEADNamesAndBuckets` (name coined here): under namespace `audiobook_organizer` the gathered
  families are `audiobook_organizer_ops_runs_total`, `_ops_run_duration_seconds` (with the 18 HEAD `le` values),
  `_ops_items_total`, `_ops_inflight`, `_ops_progress_age_seconds`, `_ops_progress_timeout_seconds`, `_ops_zombies`.
- PC-12-03's parity suites run against this executor and pass.

## Acceptance

```bash
(cd core && go build ./... && go vet ./internal/executor/... ./tasks/... ./kernel/...)     # exit 0
(cd core && go test -race ./internal/executor/... ./tasks/... ./kernel/... -count=1)       # every package "ok", no FAIL, no DATA RACE
(cd core && go test ./tasks/taskstest/parity/... -count=1)                                  # ok (parity suites on the core executor)
(cd core && go test ./internal/executor/... -run 'TestDispatch_WaitsForReadySignal|TestWriteRuns_NestedRefused' -count=1 -v | grep -c '^--- PASS')   # expect 2
grep -rn 'internal/operations\|audiobook-organizer\|"github.com/falkcorp/mediacore/legacy"' core/internal/executor --include=*.go | grep -v _test.go | wc -l   # expect 0
go test ./internal/operations/registry/... -run Parity -count=1                            # ok (v2 engine still green)
make -C core ci            # exit 0
make ci                    # exit 0 (StoresSuite over books' store includes the new cases)
git diff --stat origin/main -- internal/server internal/books/module.go cmd | tail -1        # expect empty: ABO not wired
```

## Out of scope

- Any ABO wiring: opsbridge, `ReportScheduleTick` callers, shrinking `opsmetrics`, the books subscribers (PC-12-08).
- The scheduler (PC-12-04), taskhttp (PC-12-06), books' store (PC-12-05).
- Changing any definition's parallelism (a separate PR under CLAUDE.md's rules, never T0+T1).
- Deferred-build members: `Online` data migrations, held-run cutovers, storage grants.

## STOP conditions

- A parity case passes on the v2 engine and fails here and the fix would change the case's assertion: STOP and report
  (the suite is HEAD's oracle).
- `CommitLocked`, `LocksHeld` or `RetryOnConflict` differ from 05-storage §5S.3.2's signatures at your base.
- Anything requires the executor to import `legacy`, `internal/operations/state` or an ABO path.

## Rollback

Revert the PR. ABO does not run the executor before PC-12-08 and invariant 10 keeps every pre-flip ABO build from
writing `c/tasks/` or a backend record, so a revert is a binary swap with no state.

## Repo rules

- File headers on every new or changed file; bump versions; `changelog.d/` fragment without a header.
- Commits end with `Stage-Release: S5 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; conventional messages
  (`feat(tasks): ...`); every commit builds; `gh pr merge <n> --rebase`, never squash, never `--admin`; merge only after
  OWNER-12's row is current (`check_soak.py`).
- Boot writers extend `TestBootCrashMatrix` in the same PR (06 §6.5.1).
- CLAUDE.md concurrency: bounded pools only; worker count from config; no per-item goroutines beyond `Parallelism`.

## Planner notes

- The row lists only `core/internal/executor/`. The spec puts `RC`, `RunItems`, `ForEach` and `rc.Await` in package
  `tasks` (05-tasks §5T.3.2–§5T.3.3) and makes `TestLedgerCommitAfterTerminalRefused`,
  `TestConcurrentTransitionsOfDistinctRunsAllCommit`, `TestCancelCommitsUnderProgressTicks` and
  `TestFixerMarksLandDuringQuiesce` cases of `taskstest.StoresSuite`, so this task also edits `core/tasks/` and
  `core/tasks/taskstest/` (shared with PC-12-01 and PC-12-03, which merge earlier).
- No W12 row lists `core/kernel/`, `core/module/` or `core/app/`, yet 06 §6.5.1 P4c (building `tasks.Stores`,
  asserting `legacy.WriteLock`, resolving `RunLocks`), `Registrar.Tasks`/`Registrar.Health` and `Deps.Tasks`/`Deps.Health`
  must exist before PC-12-06's `TestSecondModuleNoAudiobookEdit` can boot `hello.count`. This brief assigns that wiring
  here; the inventory should add those paths to PC-12-02.
- The row's depends-on lists PC-12-01 and PC-12-03 only. This brief also needs PC-12-04 (the kernel starts the core
  scheduler and wires `ReportScheduleTick`) and PC-12-05 (the new `StoresSuite` cases must pass over books' store in root
  `make ci`). The plan's serialized order already puts PC-12-02 after both, but the depends-on list should name them.
