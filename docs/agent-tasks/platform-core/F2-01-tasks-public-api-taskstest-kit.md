<!-- file: docs/agent-tasks/platform-core/F2-01-tasks-public-api-taskstest-kit.md -->
<!-- version: 1.1.1 -->
<!-- guid: 24217568-a4f2-4955-a65e-13e6cfd7ef35 -->
<!-- last-edited: 2026-11-05 -->

# F2-01 — `core/tasks` public API and the `taskstest` kit, written fresh over `tasks.RunState`

| Field | Value |
|---|---|
| ID | F2-01 |
| Release / flip | R2, foundation PRs (F2), **dark** |
| Wave | W25 |
| Tier | **T3** (Opus / high): the public task API every later definition, the executor, the scheduler, the stores and `datamigrate` are written against; the contract is fixed by the spec, the internals are not |
| Depends on | OWNER-03 (R1 released and deployed), F2-09 (`config.Duration`), F2-10 (`health.ComponentID`) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f2-01-tasks-api
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

Do **not** run `go work init` in the worktree (CLAUDE.md: it breaks the build). `core/` is a nested Go module
(module path `github.com/falkcorp/mediacore`); run its tests from inside `core/`.

## Goal

Create the public package `github.com/falkcorp/mediacore/tasks` (directory `core/tasks/`) and its test-support
package `core/tasks/taskstest/`, written fresh (not moved from `pkg/ops`) over `tasks.RunState`, carrying amendments
A1–A7 (05-tasks §5T.0). This PR lands **the public surface only**: types, constructors, sentinels, validation, the
pure loops `RunItems`, `RunSeq` and `ForEach`, and `taskstest.Conformance`. The executor (F2-03), scheduler (F2-04),
native stores and importer (F2-02), HTTP routes (F2-05) and the kernel wiring (F2-16) are separate PRs that implement the interfaces this PR
declares. Nothing in ABO imports `core/tasks` in this PR, and nothing registers a kind (05-tasks §5T.8.2 Core group;
09 §9.4 R2 F2 row: "nothing switches").

## Spec to read first (in this order)

1. 05-tasks §5T.0 (A1–A7), §5T.3.1 to §5T.3.13 (the Go API; copy every exported name exactly), §5T.5, §5T.7.
2. 05-tasks §5T.8.1 rows `pkg/ops`, `pkg/ops/opstest`, `internal/operations/opswriter` (what this package replaces).
3. 05-tasks §5T.9 (the test names), §5T.10 (config keys the loops read: `core.tasks.cpu_slots`, `core.tasks.kinds`).
4. 03 §3.3 `tasks` row and "Store interfaces and importers"; 03 §3.6 naming.
5. 04 §8.1 F2 row (minimum surface at F2) and §6 (`pkg/ops` stays HEAD's until the F5 flip).
6. 06 §6.4.10 F2 row (no `module.Registrar` or `app` member lands at F2).

## Base state: re-verify before editing

The plan was written at commit `f7ac5b11e`, where `core/` does not exist yet; R1 creates it. Run these at **your
base** (fresh `origin/main` after OWNER-03). Any "STOP" condition: stop and report, do not improvise.

```bash
# R1 and W22 outputs this PR builds on (each must hit; created by R1-01, R1-09, R1-12, R1-13, R1-20, F2-09, F2-10)
test -f core/go.mod && grep -c '^module github.com/falkcorp/mediacore$' core/go.mod      # expect 1, else STOP
grep -rln '^package entity$' core/entity/ | wc -l                                         # expect >=1 (entity.Ref), else STOP
grep -rn 'type Ref struct' core/entity/                                                   # expect 1 hit, else STOP
grep -rn 'type Requirement ' core/rbac/                                                   # expect 1 hit (R1 leaf), else STOP
grep -rn 'type Checker interface' core/rbac/                                              # expect 1 hit, else STOP
grep -rn 'ErrShutdown' core/lifecycle/ | head -3                                          # expect >=1 (re-exported by tasks), else STOP
grep -rn 'type Batch struct\|type Keyspace interface\|type Keyspace struct' core/storage/ # expect >=2, else STOP
grep -rn 'type ComponentID ' core/health/                                                  # expect 1 hit (F2-10), else STOP
grep -rn 'type Duration ' core/config/                                                     # expect 1 hit (F2-09), else STOP
# This PR's target must not exist yet
test ! -e core/tasks && echo absent                                                       # expect "absent"; if present: see Idempotency
# HEAD evidence the amendments cite (counts at f7ac5b11e; regenerate at your base)
grep -n 'concCPU concKind = iota' pkg/ops/batch.go                                        # 1 hit (A1)
grep -n 'const MaxNetworkWorkers = 64' pkg/ops/batch.go                                   # 1 hit (MaxFixed's source)
ls pkg/ops/opstest/conformance.go                                                         # exists (Conformance's source)
grep -n 'func RunItems' internal/operations/registry/run_items.go                         # 1 hit (the loop being re-implemented)
```

`ReadySignal` is `health.ComponentID` and `ScheduleConfig.Every` is `config.Duration`, used directly
(05-tasks §5T.3.1, §5T.3.8; 05-health §5H.3.1). F2-09 and F2-10 are merged dependencies of this row, so no
fallback type is declared; a zero-hit grep above stops the task.

## Files

Create only:

- `core/tasks/` — `kind.go`, `definition.go` (Common, Task/Batch/Fixer specs and constructors, DedupePolicy,
  Priority, Debounce, Notify, ReadySignal, LaneID, Lane, LaneLease), `parallelism.go`, `items.go`
  (RunItems, RunSeq, ForEach, ItemsOptions, ItemsResult, ItemsError, ItemRC, ErrorMode), `rc.go` (RC, Mode,
  Trigger, Actor, the cancel causes, ErrContinueInPlace), `effects.go` (Resource alias, Res, ResID, Overlaps,
  FormatRef, LockRefs, Effects, SubjectsFunc, Key), `writer.go` (Writer, Write, Change, ChangeSink, write
  sentinels), `resume.go` (ResumePolicy, CancelContract and its constructors), `perms.go` (Permissions, Approval),
  `schedule.go` (Schedule, ScheduleFromConfig, ScheduleConfig, Every, MissedPolicy, Window and the builder
  methods), `client.go` (Client, Stager, Executor, StartRequest, RunView, RunState, RunQuery, RunFilter,
  TimelineQuery, RedactRunView, ErrModeUnset, ErrWriteRunsNested; **not** `RunViewRedactor`, which is F6-02's), `stores.go` (Stores, RunStore,
  ClockStore, PauseStore, StandDownStore, RunRecord, RunSeqs, Expect, Clock, Pause, StandDown and the store
  sentinels), `doc.go`. Split differently if you prefer; the exported names are fixed.
- `core/tasks/taskstest/` — `conformance.go` (`Conformance(t, defs ...tasks.Definition)`), plus the fakes the
  tests below need. `StoresSuite` is **not** this PR's (F2-02 adds `storesuite.go`).
- Tests beside them (`*_test.go`).

Modify nothing outside `core/tasks/`. In particular: no change to `core/kernel/`, `core/app/`, `core/module/`,
`pkg/ops`, `internal/**`, `go.mod`. `pkg/ops` stays HEAD's until F5-18 (04 §6).

## Code contract (copy exactly from the spec; do not rename)

Every exported identifier in 05-tasks §5T.3.1–§5T.3.13 is part of this PR, with these fixed points:

- `type Kind string`, `ParseKind`, `Kind.Module()`; grammar `<moduleID>.<name>`, segments `[a-z][a-z0-9-]*`, no
  `:` (§5T.3.1).
- `Shape` with `ShapeTask`, `ShapeBatch`, `ShapeFixer` only (A7: no Pipeline).
- `Definition` interface with the unexported `spec()` method, so only `tasks` implements it. `Task`, `Batch`,
  `Fixer` constructors **never panic**; problems are collected and reported later by registration.
- `Common` field set and comments as §5T.3.1 (no alias field, D10). `Resume ResumePolicy` zero = invalid (A2).
- `Parallelism` with unexported fields; `CPU()`, `Fixed(n, reason)`, `Sequential(reason)`,
  `FromParam(field, def, max)`; `const MaxFixed = 64`; `ErrNoParallelism` and `ErrNoRunContext` with the exact
  messages of §5T.3.2; `Validate()` returns nil or the **bare** `ErrNoParallelism`; `String()` renders `cpu`,
  `fixed(4): <reason>`, `sequential: <reason>`. Zero value invalid (A1). `Fixed(n, "")` and `Sequential("")`
  invalid.
- `RunItems`, `RunSeq`, `ForEach` signatures exactly as §5T.3.2. `ItemsError` is the **only** non-nil error type
  they return; `Unwrap() []error` yields `First`, `Errs` and `Cause`.
- `Resource = entity.Ref` is an **alias** (A4); `Key` is namespaced `<module>.<name>`.
- `Writer` interface and sentinels `ErrFenced`, `ErrPreview`, `ErrChangedSincePlan`, `ErrOutsideEffects`
  (A3: no Book type anywhere in core).
- `ResumePolicy` constants `ResumeContinue`, `ResumeFromZero`, `ResumeDrop`, `ResumeAsk` (`iota + 1`).
- `Every(d)` exists (only `core.outbox.relay` may use it; enforced at registration, not here).
- `ScheduleFromConfig(key)` checks the key **form** only (`<owner>.<subsystem>.schedules.<name>`, owner `core` or
  `modules.<id>`, segments `[a-z][a-z0-9_]{0,47}`); whether the key is declared with type `tasks.ScheduleConfig`
  is checked by the kernel at registration (§5T.3.1, §5T.3.8). Record a malformed key as a constructor problem.
- `ScheduleConfig` uses `config.Duration` for `Every` (§5T.3.8), imported from `core/config` (F2-09). `ReadySignal`
  is `type ReadySignal = health.ComponentID` with `SignalStorage ReadySignal = "core.storage"` (§5T.3.1), imported
  from `core/health` (F2-10). No local fallback types.
- `Stores`, `RunStore`, `ClockStore`, `PauseStore`, `StandDownStore` and their records (`RunRecord`, `RunSeqs`,
  `Expect`, `Clock`, `Pause`, `StandDown`, `RunEvent`, `ItemOutcome`, `LogLine`, `PageQuery`, `PageEntry`, `Chunk`,
  `DebounceBucket`, `Strike`) and the sentinels `ErrRunNotFound`, `ErrRunConflict`, `ErrClockConflict`,
  `ErrRunActive` are **declared** here exactly as §5T.3.13. `tasks.Importer`, `RunData` and `NativeStores` are
  F2-02's (its `core/tasks/importer.go`); do not declare them here.
- `RunViewRedactor() events.RedactFunc` (§5T.3.10) is **not** this PR's: its `events.RedactFunc` has its first
  consumer in the realtime filter, so F6-02 adds it beside `RedactRunView`. This row does not import `core/events`.
- No package-level mutable state (R5; `TestNoGlobals_Tasks`): the CPU budget, pause gate and ref-lock table are
  executor fields. In this PR `RunItems` reaches them through `rc` via an unexported interface the executor
  implements in F2-03; with a plain `ctx`, `ForEach` is a plain bounded pool.

## Loop semantics (`RunItems`/`RunSeq`/`ForEach`): the table in §5T.3.2 is normative

Implement every row of the behaviour table: a fixed set of `min(workers, len)` goroutines started with `wg.Go`
pulling indexes from an atomic cursor (never one goroutine per item); `ctx.Err()` checked before taking an index and
again before calling `fn`; `NotStarted` exact; FailFast stops dispatch and cancels in-flight item contexts with
`ErrItemsStopped`; Collect runs all; zero parallelism returns `*ItemsError{First: ErrNoParallelism, FirstIndex: -1}`
before any item; `ForEach` with `Watermark` returns `*ItemsError{First: ErrNoRunContext}` before any item; `Label`
receives only the item; counters only through `ItemRC.Count` (atomic). This is the CLAUDE.md concurrency rule
expressed as API: a bounded pool sized by a declared `Parallelism`, with no shared mutable state in workers.

## TDD: tests to write first (spec names, copied exactly)

In `core/tasks/` (package `tasks_test` unless noted):

- `TestParallelism_ZeroInvalid`, `TestFixed_ClampAndReasonRequired`
- `TestBatch_ZeroParallelismRefusedAtRegister`: in this PR assert the constructor records the problem
  (`Definition` carries it); the registration-time refusal is the kernel's (F2-16 wires registration). Keep the name.
- `TestRunItems_ZeroParallelismFailsBeforeFirstItem` (asserts `*ItemsError` and
  `errors.Is(err, ErrNoParallelism)`)
- `TestRunItems_CancelStopsDispatch` (`testing/synctest`: cancel while 3 of 8 in flight; no `fn` starts after
  cancel; `NotStarted` exact)
- `TestRunItems_ErrorShapeSameSeqAndPar` (table: `Sequential` vs `CPU`, FailFast/Collect; identical `*ItemsError`
  fields)
- `TestRunItems_LabelGetsItemOnly_RaceFree` (`-race`, 64 workers), `TestRunItems_CountersAtomic`
- `TestForEach_WatermarkRefused`
- `TestNoGlobals_Tasks` (AST: no package-level `var` of mutable type in `core/tasks`, except sentinel errors)
- Kind grammar: `TestParseKind_Grammar` (name coined here): valid `books.dedup.full-scan`, `core.backup.run`;
  invalid `Books.x`, `books`, `books.a:b`, `books..x`.
- `TestScheduleFromConfig_KeyForm` (name coined here): accepts `core.backup.schedules.create`,
  `modules.books.scheduler.schedules.library_scan_full`; records a problem for a hyphenated segment, a key
  without `.schedules.`, an owner other than `core`/`modules.<id>`.
- `taskstest.Conformance` self-test: `TestConformance_SelfTest` (name coined here) runs `Conformance` over one
  valid Task, Batch and Fixer definition and over three broken ones (zero `Resume`, zero `Parallelism`, Fixer
  without `Approval`), expecting failures only for the broken ones.

Tests that need the executor (`TestRunItems_WatermarkResume`, `TestRunItems_CPUBudgetShared`,
`TestRunItems_PauseParksBeforeItemClock`, `TestForEach_JoinsRunBudgetWhenCtxFromRC`, `TestExclusiveRefs_*`,
`TestLockRefs_*`, `TestParallelismCap_LowersDeclared`) belong to F2-03, which owns the run context. Do not stub
them here.

## Steps

1. Run the base-state greps. Write the plan into the PR description (CLAUDE.md "Plan before execution").
2. Write the tests above first; they fail to compile.
3. Declare the types and constructors of §5T.3.1–§5T.3.13 (copy the Go blocks; keep doc comments).
4. Implement `Parallelism`, `ItemsError`, `RunItems`, `RunSeq`, `ForEach` per the behaviour table.
5. Implement constructor validation as "collect problems" (the `Definition` holds a problem list the kernel reads
   at registration through an unexported accessor or an exported `Problems(d Definition) []error`; name coined
   here if you add it, and mark it in the doc comment).
6. Implement `taskstest.Conformance` by re-implementing the intent of `pkg/ops/opstest/conformance.go` over
   `tasks` types (04 §8.6 "re-implementation"; read the HEAD file, copy no code that names `database` or
   `internal/operations/state`).
7. File headers on every new file (4-line header, version 1.0.0, a new guid, `last-edited` today).
8. Run the acceptance commands.

## Acceptance (commands and expected output)

```bash
cd core
go build ./...                                                     # exit 0
go vet ./tasks/...                                                 # exit 0, no output
go test ./tasks/... -race -count=1                                 # ok  github.com/falkcorp/mediacore/tasks ... ; ok .../tasks/taskstest
go test ./tasks/ -run 'TestParallelism_ZeroInvalid|TestRunItems_ZeroParallelismFailsBeforeFirstItem|TestRunItems_CancelStopsDispatch|TestRunItems_ErrorShapeSameSeqAndPar|TestRunItems_LabelGetsItemOnly_RaceFree|TestForEach_WatermarkRefused|TestNoGlobals_Tasks' -race -count=1 -v | grep -c '^--- PASS'   # 7
grep -rn 'internal/operations\|pkg/ops\|internal/database' tasks/ | wc -l                     # 0 (no ABO import)
grep -rn 'Pipeline' tasks/*.go | grep -v _test | wc -l                                        # 0 (A7)
cd ..
make -C core ci                                                    # exit 0
make ci                                                            # exit 0
git grep -n 'mediacore/tasks"' -- ':!core/' | wc -l                # 0 (nothing in ABO imports tasks yet)
```

## Out of scope

The executor, run context implementation, CPU budget, pause gate and ref locks (F2-03); the scheduler (F2-04);
`NativeStores`, `Importer` implementation, `taskstest.StoresSuite` (F2-02); `taskhttp` and `hello.count`'s TaskSpec
(F2-05); kernel registration, the start rule and `coretest`'s task cells (F2-16); `RunViewRedactor` (F6-02);
`tasklint` and the 05-tasks §5T.9 guards' self-tests `archtest.ResumeContinueCheckpoints` and
`archtest.RunItemsDeclareParallelism` (F2-17); any ABO edit; deleting `pkg/ops` (F5-18).

## Idempotency / rollback

If `core/tasks/parallelism.go` exists with `ErrNoParallelism`, this task is applied; run the acceptance commands
instead. Rollback = revert the PR: nothing imports `core/tasks` yet, so the revert touches no other package.

## Repo rules

- File headers on every new file; conventional commit, e.g. `feat(core/tasks): public task API and taskstest kit (dark)`;
  end the commit body with `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr create`, then `gh pr merge <n> --rebase` (never squash). F2 PRs are outside the R2 freeze (09 §9.2.1); 09
  defines no release or flip commit trailer (§9.2.1: "Nothing records a flip").
- Add a `changelog.d/` fragment (no file header in fragments).
- CLAUDE.md concurrency rules apply to every loop you write (bounded pool, declared parallelism).
