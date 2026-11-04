<!-- file: docs/agent-tasks/platform-core/PC-12-01-core-tasks-api-and-stores.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3892d45b-07bb-407c-b1cc-2edf87cecae1 -->
<!-- last-edited: 2026-11-04 -->

# PC-12-01 — core/tasks API and tasks.Stores with the native store and taskstest suite; fencing; the readiness core of health

| Field | Value |
|---|---|
| ID | PC-12-01 |
| Tier | **T3 Opus/high** — the public task contract every module uses, frozen by the SDK's API-stability gate (03 §3.4); a wrong type here is a breaking change later |
| Wave | W12 (stage S5, release `S5r1`, lands dark before the flip PC-12-08) |
| Depends on | OWNER-12 (the `S4r2` soak row; `check_soak.py` refuses the merge without it) |
| Blocks | PC-12-02, PC-12-03, PC-12-04, PC-12-05, PC-12-07 |
| Trailer | `Stage-Release: S5 1` on every commit |
| Digest | `docs/plans/platform-core/digests/S5.md` §3, §4, §5 rules 1–4 |
| Owning spec | 05-tasks §5T.3 (all subsections), §5T.6.1, §5T.6.2 (store contract only), §5T.8.1; 05-health §5H.3.1 (readiness core), §5H.10; 04 §8.1 row S5 |

## Goal

Land the public `core/tasks` package (the SDK surface of 05-tasks §5T.3), the `tasks.Stores` interfaces with core's native
implementation over `c/tasks/` (05-tasks §5T.6.1), the store conformance suite `taskstest.StoresSuite`, run fencing
(`ErrFenced`, `WithFence`, `FenceFromContext`) moved into `tasks`, the legacy-only task identifiers in package `legacy`,
and the readiness core of `health`. Nothing in ABO calls any of it yet: this PR is dark. ABO's behaviour is byte-for-byte
unchanged.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-12-01" -b feat/pc-12-01-core-tasks-api origin/main
cd "$REPO/../audiobook-organizer-pc-12-01"
git rebase origin/main
make web-deps            # PC-01-06; if the target is missing: npm ci --prefix web
git worktree list        # confirm you are in the new worktree, not the primary checkout
```

Never run `go work init` (CLAUDE.md). Verify with real `go build ./...` and `(cd core && go build ./...)`.

## Exact files

Create:

- `core/tasks/` — the public package (`github.com/falkcorp/mediacore/tasks`). Suggested split (file names coined here):
  `kind.go` (5T.3.1 kinds, shapes, `Definition`, `Common`, `TaskSpec`, `BatchSpec`, `FixerSpec`, `Task`/`Batch`/`Fixer`
  constructors and their registration-time validation), `parallelism.go` (5T.3.2 `Parallelism`, `CPU`, `Fixed`,
  `Sequential`, `FromParam`, `MaxFixed`, `Validate`, `ErrNoParallelism`, `ItemsError`, `ItemsOptions`, `ItemsResult`
  types), `effects.go` (5T.3.4), `writer.go` (5T.3.5 types), `resume.go` (5T.3.6 policies and errors), `perms.go`
  (5T.3.7), `schedule.go` (5T.3.8 types and `ScheduleFromConfig`/`Every` declarations), `lanes.go` (5T.3.9),
  `client.go` (5T.3.10 `Client`, `Stager`, `Executor`, `RunView`, `RunState`, queries, `RedactRunView`,
  `RunViewRedactor`, `ErrWriteRunsNested`, `ErrModeUnset`), `stores.go` (5T.3.13), `fence.go`, `errors.go`,
  `rc.go` (only `type RC struct{ /* unexported */ }` and `type ItemRC struct{ /* unexported */ }`, no methods; PC-12-02
  adds them).
- `core/tasks/taskstest/` — `StoresSuite(t, func(*testing.T) tasks.Stores)` (05-tasks §5T.3.13 naming rule) with the
  store-level cases listed under "Tests to write first".
- `core/internal/taskstore/` — `NativeStores` implementation over `c/tasks/` (05-tasks §5T.6.1 key table). `tasks.NativeStores`
  is a thin exported constructor in `core/tasks/stores.go` that delegates here.
- `core/health/ready.go` — the readiness core (05-health §5H.3.1): `Status`, `ComponentID`, `Result`, `Check`, `Reporter`,
  `Reporters`, `ErrNotOwned`, `Registry` with `Register`, `Ready`, `Wait`. `Snapshot`, `Live` and the HTTP probes are S8
  (05-health §5H.10); do not add them.

Modify:

- `internal/operations/opswriter/fence.go` — keep every exported name, re-declared as aliases of the `tasks` values so one
  fence identity serves both engines (step 6).
- `pkg/ops/` — `pkg/ops/errors.go`'s `ErrFenced` becomes `= tasks.ErrFenced`; nothing else in `pkg/ops` changes.
- `internal/operations/progress.go`, `internal/operations/trigger_source.go` — `WithTriggerSource`/`TriggerSourceFromContext`
  forward to the `tasks` trigger-source context helpers so one context key serves both engines; `ProgressReporter` and
  `LoggerFromReporter` stay as they are (they name ABO's `logger.Logger`; 05-tasks §5T.8.1 "only `progress.go` and
  `trigger_source.go` → `tasks`" is satisfied by the core re-declaration plus forwarding).

Also create (not in the inventory row; see Planner notes): `core/legacy/tasks.go` and `core/legacy/legacytest/tasks.go`.

## Re-verify greps (run first; at `748fd787a` and at the plan checkout these ABO paths are identical)

```bash
grep -c "^var ErrFenced\|^func WithFence\|^func FenceFromContext" internal/operations/opswriter/fence.go   # expect 3
grep -n "ErrFenced *=" pkg/ops/errors.go                                                                     # expect 1 hit (line ~17)
grep -c "^func WithTriggerSource\|^func TriggerSourceFromContext" internal/operations/trigger_source.go       # expect 2
grep -c "^type ProgressReporter interface" internal/operations/progress.go                                   # expect 1
grep -n "ReadyStore\|ReadyIndexes\|ReadyNone" pkg/ops/effects.go | head -3                                   # expect >= 3 hits (HEAD warm-up rule, effects.go:44-57)
ls pkg/ops/*.go | grep -v _test | wc -l                                                                      # expect 16
# Prerequisites from earlier waves (absent at the plan checkout, present at this wave's base):
test -f core/storage/commitlocked.go && echo ok      # PC-07-01; expect ok
test -d core/legacy && echo ok                       # PC-04-02; expect ok
test -d core/events && test -d core/rbac && test -d core/entity && echo ok   # PC-10-01, PC-03-03; expect ok
grep -n "func CommitLocked\|func LocksHeld\|ErrNotLocked *=" core/storage/*.go | wc -l   # expect >= 3
test ! -e core/tasks && test ! -e core/health/ready.go && echo ok   # expect ok (nothing landed yet)
```

A zero on an edit target, a moved count, or a missing prerequisite: STOP and report. Do not patch around it.

## Steps

1. Read 05-tasks §5T.3.1–§5T.3.13 in full, §5T.5, §5T.6.1, §5T.7 and 05-health §5H.3.1. Copy every type, field, constant,
   error value and signature exactly as the spec spells it. Where you must coin a name, mark it in a code comment
   `// name coined in PC-12-01`.
2. Write the tests of "Tests to write first" (they fail to compile).
3. Create `core/tasks` with the declarative surface: §5T.3.1 (kinds, `Common` including `LegacyDefID Kind`,
   `LegacyPlugin string`, `FormerKinds []Kind`), §5T.3.2 (types and the four constructors with clamping and the required
   reason; `MaxFixed = 64`; `Validate` returns `ErrNoParallelism` for the zero value), §5T.3.4–§5T.3.9 types,
   §5T.3.10 (`Client`, `Stager`, `Executor`, `RunView`, `RunState` constants with `Terminal()`, `RunFilter`, `RunQuery`,
   `ListQuery`, `TimelineQuery`, `StartRequest`, `RedactRunView`, `RunViewRedactor`), §5T.3.13 (`Stores`, `RunStore`,
   `ClockStore`, `PauseStore`, `StandDownStore`, `RunRecord`, `RunSeqs`, `Expect`, `Clock`, `Pause`, `StandDown`,
   `StoredKind`, the five store errors). `RC` and `ItemRC` are empty struct declarations only.
4. Implement `RedactRunView(who rbac.Checker, v RunView) RunView`: blank `Current` unless `who` holds `core:users.view`;
   nil `who` blanks unconditionally (05-tasks §5T.3.10). `RunViewRedactor()` decodes, redacts, re-encodes; an undecodable
   payload returns `{}`.
5. Implement `core/internal/taskstore` over a `storage.Keyspace` with exactly the 05-tasks §5T.6.1 key table; mark the
   guard families `Serialized` (run row, debounce bucket, clock) and stage `Expect(absent)`/`GetForUpdate` as §5T.3.13's
   comment says; `Counts` reads the `c/tasks/ix/state/<state>/` prefixes (no census row); `NewID` returns a ULID;
   `Prune` and `ReplaceRows` commit their own chunks; the native store does **not** implement `legacy.TasksRecoverer` or
   `legacy.WriteLock`. Every staging method stages into the caller's batch and commits nothing.
6. Fencing: declare in `tasks` `ErrFenced`, `type Fence` (field set copied from HEAD's `opswriter.Fence`),
   `WithFence(ctx, Fence) context.Context`, `FenceFromContext(ctx) Fence`. In ABO, `opswriter/fence.go` becomes
   `type Fence = tasks.Fence`, `var ErrFenced = tasks.ErrFenced`, and `WithFence`/`FenceFromContext` call the `tasks`
   functions; `pkg/ops/errors.go` sets `ErrFenced = tasks.ErrFenced`. `errors.Is(err, opswriter.ErrFenced)` and
   `errors.Is(err, ops.ErrFenced)` must keep holding for every HEAD caller (run `go test ./internal/operations/... ./pkg/ops/...`).
7. Trigger source: declare the context helpers in `tasks` (names coined here: `tasks.WithTriggerSource`,
   `tasks.TriggerSourceFromContext`); ABO's two functions forward to them. Same values, same key, same behaviour.
8. Legacy identifiers: in `core/legacy/tasks.go` declare exactly 05-tasks §5T.3.13's `TasksRecoverer`,
   `TasksScheduleTicks`, `ErrScheduleTickRefused`, `ErrRunsNotLocked` (wrapping `storage.ErrNotLocked`), `LockRuns =
   "tasks.runs"`, and add the `Tasks func(Handles) (tasks.Stores, error)` and `Logs func(Handles) (log.JournalStore,
   error)` fields to `legacy.Stores` (06 §6.4.11). If `log.JournalStore` does not exist yet, add the `Logs` field in
   PC-12-07 instead and say so in the PR description. Register `LockRuns` at rank 170 in storage's lock-rank table if
   PC-07-01 left a placeholder for it (05-tasks §5T.3.13; `TestLockRanksTotalOrder` must stay green).
9. `core/legacy/legacytest/tasks.go`: `TasksRunStoreSuite(t, newStores, plantStale)` with the `TasksRecoverer` and
   `WriteLock` cases of 05-tasks §5T.3.13's last paragraph. It is exercised over books' store in PC-12-05.
10. `core/health/ready.go`: the readiness core exactly as 05-health §5H.3.1 declares it. `Wait` blocks until every listed
    component is `StatusUp` or `StatusDegraded`, honours `ctx`, and holds no goroutine per waiter beyond the caller's.
    `Registry` is a per-App value, no package-level state.
11. Run the acceptance commands. Bump headers. Add one `changelog.d/` fragment (no header).

## Code contract (exact, from the spec)

```go
// 05-tasks §5T.3.13
type Stores struct { Runs RunStore; Clocks ClockStore; Pause PauseStore; StandDown StandDownStore }
func NativeStores(ks storage.Keyspace) Stores
type StoredKind string
type Expect struct { State RunState; Attempt int; FenceEpoch uint64 }
type RunSeqs struct{ Event, Intent, Log uint64 }
var ( ErrRunNotFound, ErrRunConflict, ErrClockConflict, ErrRunActive, ErrStoreUnsupported ) // messages as §5T.3.13
// RunStore, ClockStore, PauseStore, StandDownStore: method sets copied verbatim from §5T.3.13 (digest S5 §4 quotes them).

// 05-tasks §5T.3.2
func CPU() Parallelism
func Fixed(n int, reason string) Parallelism
func Sequential(reason string) Parallelism
func FromParam(field string, def, max int) Parallelism
const MaxFixed = 64

// 05-tasks §5T.3.10
var ErrWriteRunsNested = errors.New("tasks: WriteRuns called inside WriteRuns")
var ErrModeUnset = errors.New("tasks: start mode not stated")
func RedactRunView(who rbac.Checker, v RunView) RunView
func RunViewRedactor() events.RedactFunc

// core/legacy (05-tasks §5T.3.13)
type TasksRecoverer interface{ Recover(ctx context.Context) error }
type TasksScheduleTicks interface { ReportScheduleTick(ctx context.Context, kind tasks.Kind, dueAt time.Time, folded int) error }
var ErrScheduleTickRefused = errors.New("legacy: schedule tick for a kind the caller does not tick")
var ErrRunsNotLocked = fmt.Errorf("legacy: run write staged outside the run lock: %w", storage.ErrNotLocked)
const LockRuns = "tasks.runs"

// core/health (05-health §5H.3.1)
func (r *Registry) Register(c Check) (Reporter, error)
func (r *Registry) Ready() (ready bool, notReady []string)
func (r *Registry) Wait(ctx context.Context, ids ...ComponentID) error
var ErrNotOwned = errors.New("health: component not registered by this module")
```

Invariants: `tasks` never imports `legacy` (legacy imports tasks; 05-tasks §5T.3.13); `tasks.Stores` carries no
legacy-only member (SIMP5-4); no package-level mutable state in `tasks`, `taskstore` or `health` (R5,
`TestNoGlobals_Tasks`); core names no ABO key, op ID or setting (`TestCoreHoldsNoAppKeyNames` stays green).

## Tests to write first

Core (`make -C core ci`):

- `TestParallelism_ZeroInvalid`, `TestFixed_ClampAndReasonRequired` (05-tasks §5T.9).
- `TestNativeStores_RoundTrip` — every `RunStore`, `ClockStore`, `PauseStore`, `StandDownStore` method over `c/tasks/`
  (the `hello.count` half of that row lands in PC-12-06).
- `taskstest.StoresSuite` over `NativeStores`, with these store-level cases: CAS transitions over every edge of the
  §5T.3.10 state list; `Insert` refuses a second insert of one ID; per-run sequences advance only in the row's batch;
  `Prune` deletes terminal runs only; every staging method writes nothing when the batch is discarded (`Prune`,
  `ReplaceRows` excepted); `TestLegacyProgressVsCancelKeepsState` and `TestPruneUnderConcurrentCompletions` (both
  `synctest`, store-level halves). The executor-level cases (`TestLedgerCommitAfterTerminalRefused`,
  `TestConcurrentTransitionsOfDistinctRunsAllCommit`, `TestCancelCommitsUnderProgressTicks`,
  `TestFixerMarksLandDuringQuiesce`) are added by PC-12-02.
- `TestNoGlobals_Tasks` (R5 AST over `core/tasks`, `core/internal/taskstore`).
- `TestRegistry_Wait_UnblocksOnUp` (05-health §5H.9; here with a test component pushed Up, `books.memdb` comes in PC-12-08).
- `TestRedactRunView_BlanksCurrentWithoutUsersView` (name coined here): nil checker and a checker without
  `core:users.view` blank `Current`; one with it keeps it.

ABO (`make ci`):

- `TestFenceIdentityShared` (name coined here, in `internal/operations/opswriter`): `errors.Is(opswriter.ErrFenced,
  tasks.ErrFenced)`, `errors.Is(ops.ErrFenced, tasks.ErrFenced)`, and a fence set with `opswriter.WithFence` reads back
  through `tasks.FenceFromContext`.

## Acceptance

```bash
(cd core && go build ./... && go vet ./tasks/... ./internal/taskstore/... ./health/... ./legacy/...)   # exit 0, no output
(cd core && go test ./tasks/... ./internal/taskstore/... ./health/... -count=1)
#   expect: every line "ok  github.com/falkcorp/mediacore/..." ; no FAIL
(cd core && go test ./tasks/... -run 'TestNativeStores_RoundTrip|TestParallelism_ZeroInvalid|TestFixed_ClampAndReasonRequired' -count=1 -v | grep -c '^--- PASS')
#   expect: 3
go test ./internal/operations/... ./pkg/ops/... -count=1     # expect: ok for every package, no FAIL
grep -rn '"github.com/falkcorp/mediacore/legacy"' core/tasks core/internal/taskstore | wc -l   # expect 0
grep -rln 'internal/operations\|audiobook-organizer' core/tasks core/internal/taskstore core/health | wc -l   # expect 0
make -C core ci            # expect exit 0
make ci                    # expect exit 0 (extraction-gates' TestCoreImportsOnlyCore and TestCoreHoldsNoAppKeyNames green)
git diff --stat origin/main -- internal/server internal/database internal/books | tail -1   # expect empty: no ABO wiring
```

## Out of scope

- `RC`/`ItemRC` methods, `RunItems`, `ForEach`, `RunSeq`, `rc.Await`, the executor, `WriteRuns` (PC-12-02).
- The scheduler implementation (PC-12-04), books' legacy store (PC-12-05), HTTP routes (PC-12-06), the journal (PC-12-07),
  any kernel or `module.Registrar`/`Deps` wiring (PC-12-02 and PC-12-08), any ABO call site of the new API.
- Changing any def's parallelism (CLAUDE.md: a separate PR, never T0+T1).

## Rollback

Revert the PR (binary swap). Nothing in ABO calls the new packages and nothing is written to `c/tasks/` by any build,
so a revert leaves no state.

## Repo rules

- File headers on every new or changed file (`.standards/instructions/file-headers.md`); bump version and
  `last-edited`; keep existing guids. The `changelog.d/` fragment has no header.
- Conventional commits, e.g. `feat(tasks): core tasks API, native stores and StoresSuite`. Every commit ends with:
  ```
  Stage-Release: S5 1
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- Every commit builds (rebase merges). `gh pr create`, then `gh pr merge <n> --rebase` once the required checks
  (`extraction-gates`, `core-ci`, `docker-image`, `vet-darwin`, `vet-integration`) are green on an up-to-date branch.
  Never squash, never `--admin` past a required check. `check_soak.py` refuses the merge until OWNER-12's `S4r2` row
  is current and at least 14 days old; open the PR and make it green meanwhile.
- CLAUDE.md concurrency rules: no unbounded goroutine fan-out; `Wait` and the native store add no goroutine per item.

## Planner notes

- The row's file list omits `core/legacy/tasks.go` and `core/legacy/legacytest/tasks.go`. 05-tasks §5T.3.13 puts
  `TasksRecoverer`, `TasksScheduleTicks`, `ErrRunsNotLocked`, `LockRuns`, the `legacy.Stores.Tasks` field and
  `legacytest.TasksRunStoreSuite` in package `legacy`, and PC-12-04 lists `core/legacy/tasks.go` as a file it
  *modifies*, so some W12 row must create it before PC-12-04 and PC-12-05. This brief creates both; the inventory row
  should list them.
- `RC`, `RunItems`, `ForEach` and `rc.Await` are `tasks` API (05-tasks §5T.3.2–§5T.3.3) but need executor services;
  this brief declares the `RC`/`ItemRC` types only and leaves their methods to PC-12-02, whose file list therefore
  must include `core/tasks/`.
