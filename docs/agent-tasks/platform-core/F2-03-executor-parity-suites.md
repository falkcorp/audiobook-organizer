<!-- file: docs/agent-tasks/platform-core/F2-03-executor-parity-suites.md -->
<!-- version: 1.1.2 -->
<!-- guid: f8fddedf-5163-4b6b-a6b2-d8e1133ec889 -->
<!-- last-edited: 2026-11-05 -->

# F2-03 — `core/internal/executor` written fresh, the resume/cancel/shutdown parity suites, and the executor cell of `TestABOMetricsGatherNoDuplicate`

| Field | Value |
|---|---|
| ID | F2-03 |
| Release / flip | R2, foundation PRs (F2), **dark** |
| Wave | W27 |
| Tier | **T4** (Opus / xhigh–max): a fresh executor over every definition shape with dispatch gates, fencing, cancellation, resume, stand-down, the chunk ledger and the CPU budget; correctness is concurrency correctness |
| Depends on | F2-02 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-03-executor
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md).

## Goal

Write `github.com/falkcorp/mediacore/internal/executor` (`core/internal/executor/`) as a **re-implementation**, not a
move, of HEAD's `internal/operations/registry` engine, against `tasks.RunStore` and `tasks.RunState` (05-tasks
§5T.8.1 first row; 04 §8.6 registry row): dispatcher with its gates, workers, watchdog, resume sweep, cancel with
grace and fence revocation, pause, stand-down with persisted markers, the Batch/Fixer runner with the chunk ledger,
`rc.Await` (re-implemented `childop`), the CPU budget and ref-lock table behind `RunItems`/`ForEach`, `WriteRuns`,
and the task-run instruments (the successor of `internal/opsmetrics`, 05-tasks §5T.11). Add the parity kit under
`core/tasks/taskstest/parity/` and extend ABO's `TestABOMetricsGatherNoDuplicate`, which R1-15 created (05-metrics §12:
it exists from R1), with the executor's cell.

**Dark in ABO, and this is the invariant that can break production:** ABO's App constructs no executor and creates
no `<namespace>.ops` instrument before F5 (05-tasks §5T.11 "One owner per family"; 05-metrics §13 item 1).
`internal/opsmetrics` stays the one owner of `ops.*` in ABO. `TestABOMetricsGatherNoDuplicate` decides, and runs on
every PR from F2.

## Spec to read first

1. 05-tasks §5T.3.2 (behaviour table), §5T.3.3 (RC), §5T.3.4 (dispatch conflict rule, ref locks), §5T.3.5
   (Writer: Preview intents, fence, ChangeSink), §5T.3.6 (resume policies, checkpoints, yield, timeout, cancel,
   watchdog), §5T.3.7 (permissions at start/cancel/approve/decide, mode contract, stand-down), §5T.3.9 (lanes),
   §5T.3.10 (`Client`, `Stager`, `Executor`, `StartRequest`, actor stamping, `WriteRuns`), §5T.3.11 (held runs),
   §5T.3.12 (outbox-triggered: `StartIn` only; the relay is F6's).
2. 05-tasks §5T.4 (start sequence, run, shutdown), §5T.5 (every row), §5T.7 (the concurrency contract, all bullets),
   §5T.11 (instruments), §5T.9 (test names).
3. 05-metrics §12 (`TestABOMetricsGatherNoDuplicate` definition), §13 item 1.
4. 05-health §5H.3.1 (`Registry.Wait`; `tasks.ReadySignal` is `health.ComponentID`).
5. 04 §8.1 F2 row, §8.6 rows for `internal/operations/registry`, `internal/operations/childop`, `internal/opsmetrics`.

## Base state: re-verify before editing

```bash
grep -rn 'func NativeStores' core/tasks/                         # 1 hit (F2-02), else STOP
grep -rn 'func StoresSuite' core/tasks/taskstest/                # 1 hit, else STOP
grep -rn 'type Executor interface' core/tasks/                   # 1 hit (F2-01), else STOP
grep -rn 'func (r \*Registry) Wait' core/health/                 # 1 hit (F2-10), else STOP
test ! -e core/internal/executor && echo absent                  # "absent"
grep -rln --include='*.go' 'func TestABOMetricsGatherNoDuplicate' . | grep -v '^./core/'   # exactly 1 path (R1-15's; expected internal/books/metrics_gather_test.go), else STOP
# HEAD engine being re-implemented (still present until F5; counts at ac4368bdb)
ls internal/operations/registry/*.go | grep -vc _test            # 35 non-test files at ac4368bdb (spec cites 30 at 748fd787a; regenerate)
grep -n 'New(store, slog.Default(), 8, hub)' internal/operations/registry/register.go   # 1 hit: HEAD's 8-worker default
grep -c '^func Test' internal/operations/registry/resume_test.go internal/operations/registry/resume_shutdown_roundtrip_test.go internal/operations/registry/resume_supersede_test.go internal/operations/registry/run_items_resume_test.go internal/operations/registry/fixer_resume_real_writer_test.go
#   11, 4, 8, 9, 1 at ac4368bdb (resume oracles)
grep -c '^func Test' internal/operations/registry/cancel_persisted_test.go internal/operations/registry/cancel_requeue_race_test.go internal/operations/registry/cancel_row_cas_test.go
#   3, 1, 14 (cancel oracles)
grep -c '^func Test' internal/operations/registry/shutdown_drain_test.go internal/operations/registry/shutdown_cause_test.go internal/operations/registry/shutdown_race_test.go internal/operations/registry/shutdown_terminal_status_test.go internal/operations/registry/shutdown_stub_handle_test.go internal/operations/registry/dispatch_shutdown_race_test.go internal/operations/registry/sweeptick_shutdown_race_test.go
#   1, 1, 1, 3, 2, 7, 2 (shutdown oracles)
grep -n 'zombies' internal/opsmetrics/opsmetrics.go | head -2   # the attribute-less ops.zombies gauge (line 18 at ac4368bdb)
```

`tasks.ReadySignal` is already `health.ComponentID` (F2-01 depends on F2-10); no switch is needed here.

**Metrics test.** R1-15 created `TestABOMetricsGatherNoDuplicate` in R1 (05-metrics §12). This PR **extends** it in the
file the grep above prints (expected `internal/books/metrics_gather_test.go`); it never creates a second copy. If the
grep prints 0 paths or more than 1, STOP and report.

## Files

Create:

- `core/internal/executor/` — dispatcher, workers, watchdog, resume, cancel, pause, stand-down, batch runner and
  chunk ledger, `rc` implementation (RC, ItemRC, Writer), `WriteRuns`, CPU budget, ref-lock table, lanes, await,
  instruments, a `New(opts Options) (tasks.Executor, error)` constructor (name coined here; `Options` carries the
  `core.tasks.*` values of §5T.10, the `tasks.Stores`, the `health.Registry`, the metrics scope, `OnTerminal` and an
  `InstrumentsOff bool`, all coined here).
- `core/tasks/taskstest/parity/` — the parity kit (case tables + runner) and `TestParity_ResumeCancelShutdown`.
Modify:

- `internal/books/metrics_gather_test.go` (or the path the grep printed) — add the executor cell below.

May modify: `core/tasks/` only for unexported hooks `RunItems`/`ForEach` need to
reach the executor through an RC-derived ctx (F2-01 left an unexported interface for this). Nothing else in
`core/tasks`. Do not touch `core/kernel/`, `internal/operations/**`, `internal/opsmetrics/**`.

## Invariants (each has a test below; violating any is a STOP-and-report, not a judgement call)

1. **One build-and-commit path.** Every batch carrying a store write is built in `WriteRuns` and committed with
   `storage.Sync` under `storage.RetryOnConflict`; a `storage.ErrConflict` while `want` matches re-stages the whole
   batch; a changed `want` returns `ErrRunConflict`; `WriteRuns` inside `WriteRuns` returns `ErrWriteRunsNested`
   (§5T.3.10, §5T.7).
2. **Closed ledger.** A bare progress tick is dropped on any state change; ledger, checkpoint, row, mark, page,
   item and strike writes re-stage only while the run is non-terminal under the same `Attempt` and `FenceEpoch`
   (`stopping` included) (§5T.7).
3. **Fencing.** After grace, the fence is revoked (`ErrAbandoned`), late writes fail `ErrFenced`; zombies keep
   their `ConcurrencyKey` and ref locks until their goroutine exits; per-module zombie cap blocks dispatch (§5T.3.6).
4. **Dispatch gates.** `ConcurrencyKey`, write-set overlap (`{M,K,""}` overlaps `{M,K,*}`), `NotWhile`, `Ready`
   via `health.Registry.Wait` (waiting runs hold no worker, key or clock), module `max_concurrent`, zombie cap,
   stand-down, pause, held; `PrioritySystem` on `core.tasks.system_workers` reserved workers.
5. **Bounded everything (CLAUDE.md).** N workers (`core.tasks.workers`), CPU-slot semaphore (`core.tasks.cpu_slots`)
   taken per item by `CPU()` workers only, `core.tasks.kinds[<kind>].parallelism_cap`, lanes, one sorted all-or-wait
   ref-lock table. No goroutine per item, no unbounded fan-out.
6. **No globals (R5).** Pause gate, CPU budget, ref locks, metrics, def registry are executor fields; two executors in
   one process share nothing.
7. **Shutdown bounded.** Release claimed-not-started, cancel all with `ErrShutdown`, wait
   `min(core.tasks.shutdown_drain, remaining budget)`, stamp the rest `interrupted`, revoke fences; one
   `EXTEND_TIMEOUT_USEC` request only for an `Uninterruptible` run under systemd with `NOTIFY_SOCKET` (via
   `core/internal/sdnotify`, R1-16) (§5T.4 step 4).
8. **Instruments.** The 18 instruments of §5T.11 under `<ns>.ops` with HEAD's names and bucket lists, `module`
   replacing `plugin`; created only when `Options.InstrumentsOff` is false. ABO never constructs an executor before
   F5; `hello`/`coretest` create them from F2.

## TDD: tests to write first (names copied from 05-tasks §5T.9 unless marked)

In `core/internal/executor` (or `core/tasks` external tests driving an executor):
`TestRunItems_WatermarkResume`, `TestRunItems_CPUBudgetShared`, `TestParallelismCap_LowersDeclared` (the
config-file and JSON-env cells need core config: drive the cap through `Options` here and leave the two cells to
F3-01, noted in the test), `TestRunItems_PauseParksBeforeItemClock`, `TestForEach_JoinsRunBudgetWhenCtxFromRC`,
`TestExclusiveRefs_OverlapNeverConcurrent`, `TestLockRefs_CrossRunSerialises`, `TestLockRefs_SortedNoDeadlock`,
`TestLockRefs_CtxFromRCJoinsTable`, `TestLockRefs_PlainCtxRefused`, `TestResume_PolicyMatrix`,
`TestContinueInPlace_RequiresProgressAndCheckpoint`, `TestContinueInPlace_SameRunIDFreshTimeout`,
`TestCancel_GraceThenFenceRevoked`, `TestZombieCapBlocksModule`, `TestWriteSet_ResourceOverlapMatrix`,
`TestConcurrencyKey_NamespaceEnforced`, `TestMode_OmittedIsPreviewForWriters`, `TestPreview_WriterRecordsIntentOnly`,
`TestFixer_PlanRefAgeAndRowsOnly`, `TestShutdown_DrainBoundedAndStampsInterrupted`,
`TestShutdown_UninterruptibleExtendsOnceOnlyUnderNotify`, `TestHeld_DisabledModuleRunsUntouchedAndBootSucceeds`
(held computed from a manifest set passed in `Options`), `TestHeld_MutationsRefused409` (as `ErrHeld`-style sentinel
here; the 409 mapping is F2-05's), `TestStandDown_MarkerAtBootDefersQuiescedRun`, `TestUnknownKindRun_DroppedAtResume`,
`TestDispatch_WaitsForReadySignal`, `TestOnTerminal_NotificationStagedInTerminalBatch`, `TestModuleSystemActor_CannotStartUndeclaredKind`.
All concurrency tests use `testing/synctest` where the spec says so and run under `-race`.

Tests whose subject is not the executor stay with their owners: `TestStart_DryRunFieldRefused400`,
`TestApprove_RequiresVerifiedMethodAndPermission`, `TestStart_IdempotencyKeyHeaderReplays`,
`TestStart_ConcurrentSameIdempotencyKeyOneRun`, `TestRunView_CurrentRedactedWithoutUsersView`, `TestTimelineLogDiscard`
(F2-05, HTTP; `TestRunView_CurrentRedactedWithoutUsersView` is F6-02's); the `TestSchedule*` rows (F2-04);
`TestOutboxTriggered_*` (F6); `TestCoreRun_NeverCallsModuleWriter` (archtest over core's own kinds, F5-03);
`TestDisabledModuleWithRunsBoots` (the kernel's held-runs boot, F2-16).

**Parity kit** (`core/tasks/taskstest/parity/`): port HEAD's resume, cancel and shutdown suites (the 15 files and 68
`Test` functions listed in the base-state greps at ac4368bdb; regenerate the count) as **behaviour oracles**: for each HEAD test,
one case `{name, HEAD file:func, setup, action, expected outcome}` where the expected outcome is what HEAD's test
asserts, expressed in `tasks.RunState` and `ItemsResult` terms. `TestParity_ResumeCancelShutdown` runs every case on
core's executor with test definitions shaped like HEAD's test defs. A HEAD case that has no core equivalent (a v1
status bridge, a v2-only row shape) is listed in the kit's `excluded.go` with its reason; the list may only shrink.
F5-18 re-runs `TestParity_ResumeCancelShutdown` over the converted definitions at the flip.

**ABO metrics cell** (added to R1-15's `TestABOMetricsGatherNoDuplicate`; keep its existing cells unchanged): over the
App the test already builds (ABO's `MetricNamespace`, ABO's real recorders, one HEAD task run, one legacy HTTP request and
one core-route request, 05-metrics §12), add an executor sub-case: build a second, standalone executor on a separate
`coretest.MetricsProvider` with `InstrumentsOff: false` and assert its 18 families gather there without error, then assert
that ABO's gather still returns no error, that no family has two owners, and that no `ops` family in ABO's gather came
from `core/internal/executor` (ABO builds none before F5; 05-metrics §13 item 1).

## Steps

1. Base-state greps; plan in the PR body (this is a large re-implementation: list the files you will write and
   the HEAD files each one replaces).
2. Port the parity cases first (they are the oracle), then write the executor tests above.
3. Implement in this order: `WriteRuns` and transitions; claim and terminal CAS with fence epoch; workers and
   dispatcher gates; RC/ItemRC/Writer; resume sweep; cancel/grace/zombies; watchdog; shutdown; batch runner and
   chunk ledger; Fixer plan/apply with marks; stand-down; await; lanes; instruments.
4. `go test -race` until green; run the parity kit; run the ABO metrics test.
5. Headers, changelog fragment, acceptance.

## Acceptance

```bash
cd core
go build ./... && go vet ./internal/executor/... ./tasks/...                       # exit 0
go test ./internal/executor/... ./tasks/... -race -count=1                         # all ok
go test ./tasks/taskstest/parity/ -run TestParity_ResumeCancelShutdown -race -count=1 -v | tail -1   # ok
go test ./internal/executor/... -run 'TestDispatch_WaitsForReadySignal|TestShutdown_DrainBoundedAndStampsInterrupted|TestCancel_GraceThenFenceRevoked|TestResume_PolicyMatrix' -race -count=1 -v | grep -c '^--- PASS'   # 4
grep -rn 'internal/operations\|internal/database\|pkg/ops' internal/executor/ | wc -l   # 0
cd ..
go test ./internal/books/ -run TestABOMetricsGatherNoDuplicate -count=1             # ok (or the package where the R1 test lives)
git grep -n 'mediacore/internal/executor' -- ':!core/' | wc -l                      # 0 (ABO never imports the executor before F5)
make -C core ci                                                                    # exit 0
make ci                                                                            # exit 0
```

## STOP conditions

- `TestABOMetricsGatherNoDuplicate` fails because an `ops` family has two owners: you wired the executor into ABO.
  Undo that; do not "fix" by renaming instruments.
- A parity case's HEAD outcome cannot be expressed without HEAD's `internal/operations/state` vocabulary: list it in
  `excluded.go` with the reason, never weaken the expected outcome.
- Any need to edit `core/kernel/` or `internal/operations/**`: out of scope, report.

## Out of scope

Kernel construction and start of the executor, the start rule (F2-16); `taskhttp` (F2-05); the scheduler (F2-04);
the outbox relay (F6); converting any ABO definition; re-running the parity kit over converted definitions and deleting
HEAD's engine (F5-18).

## Idempotency / rollback

If `core/internal/executor` exists with a `WriteRuns` implementation, run the acceptance instead. Rollback = revert
the PR; nothing in ABO or the kernel imports the executor yet.

## Repo rules

File headers; conventional commit (`feat(core/executor): fresh task executor and parity kit (dark)`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; `changelog.d/` fragment; CLAUDE.md
concurrency rules (this package is where they are enforced for every app).
