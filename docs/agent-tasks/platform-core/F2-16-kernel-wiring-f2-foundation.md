<!-- file: docs/agent-tasks/platform-core/F2-16-kernel-wiring-f2-foundation.md -->
<!-- version: 1.0.0 -->
<!-- guid: ee4d77ac-3711-47b9-9be9-7b78297d36f2 -->
<!-- last-edited: 2026-11-05 -->

# F2-16 — Kernel wiring of the F2 foundation: the tasks start rule, `Registrar.Tasks` and `Resources`, `Manifest.TaskLedger` and `DataMigrations`, the boot calls of every F2 subsystem, `hello.count` registered, and `coretest`'s task cells and held runs

| Field | Value |
|---|---|
| ID | F2-16 |
| Release / flip | R2, foundation PRs (F2), **dark in ABO** (ABO registers no kind and holds no `c/tasks/` run, so nothing of `tasks` starts in ABO); `hello` runs `hello.count` from this PR |
| Wave | W28 |
| Tier | **T3** (Opus / high): the kernel's start rule for tasks and the boot calls of every F2 subsystem; a wrong rule starts ABO's executor before F5 |
| Depends on | F2-03 (executor), F2-04 (scheduler, schedule keys), F2-05 (`taskhttp`, `countTask`), F2-06 (data-migration runner), F2-07 (audit), F2-08 (scope catalog), F2-09 (core sections), F2-10 (health registry) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f2-16-kernel-wiring
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged (the client regeneration needs it)
```

Do **not** run `go work init` (CLAUDE.md). `core/` is the nested module `github.com/falkcorp/mediacore`; run its tests
from inside `core/`. This row is **the one owner of the kernel and module wiring files in W28** (`core/kernel/boot.go`,
`core/kernel/kernel.go`, `core/module/manifest.go`, `core/module/registrar.go`), of `core/coretest/tasks.go` and
`harness.go`, and of the regenerated `core/openapi/core.json` and client; no other W28 PR touches them.

## Goal

The F2 dark rows each built their package and a kernel-side file (`core/kernel/audit.go`, `catalog.go`,
`core_sections.go`, `health.go`, `migrate.go`) and left the **boot calls** to this row, because F2 has no flip to carry
them (plan §(d) "Shared wiring files have one owner per wave"). This PR:

1. **The SDK members of F2** (06 §6.4.10 F2 row): `Manifest.TaskLedger []tasks.Kind` and
   `Manifest.DataMigrations []datamigrate.DataMigration` (06 §6.4.2, copy the doc comments), and
   `Registrar.Tasks(defs ...tasks.Definition)` and `Registrar.Resources(kinds ...string)` (06 §6.4.3). Not `Lanes`,
   `ChangeSink`, `TaskStarts` or `Importers.Tasks` (F5, 06 §6.4.10 F5 row).
2. **The tasks start rule** (05-tasks §5T.4 step 1, normative; 06 §6.4.10 F2 row; 03 §3.8 F2 row) in
   `core/kernel/tasks.go`: the kernel builds the native stores at P4c, collects definitions at P6, validates and upserts
   the definition snapshots `c/tasks/def/<kind>` at P7, and **builds and starts the executor, the scheduler and
   `taskhttp`'s routes only when any kind is registered (a module's, through `Registrar.Tasks`, or one of core's own) or
   `c/tasks/` holds runs**; otherwise it starts none of them, mounts no task route and creates no `<ns>.ops` instrument.
   The kernel registers **none** of core's own kinds in this PR (each from its owner's flip, F5 or later).
3. **The boot calls** of audit (F2-07: built before P4b, writer started at Run step 7, stopped on shutdown), the scope
   catalog (F2-08), the core sections (F2-09 and F2-04's schedule keys: on the kernel's core-section list), the health
   registry with `core.storage` and `core.secrets` (F2-10), and the data-migration runner at P4b over every manifest's
   `DataMigrations` (F2-06).
4. **`hello` registers `hello.count`** (06 §6.12.2–§6.12.3): `TaskLedger: []tasks.Kind{"hello.count"}` in its
   manifest, `r.Resources("name")` and `r.Tasks(countTask(svc))` in `Register`; F2-05 wrote `countTask`.
5. **`coretest`'s task cells** (06 §6.4.8): `WithRuns`, `Harness.RunTask`, `RunResult` in `core/coretest/tasks.go`, and
   `AssertModuleInert`'s held-runs cell in `core/coretest/harness.go` ("its persisted runs (see WithRuns) are listed as
   held and none was dispatched").
6. **Regenerates** `core/openapi/core.json` and the generated client for the task routes `hello`'s App now mounts.

In ABO before F5 neither start condition holds (ABO registers no kind, the kernel registers none of core's own kinds,
and HEAD's runs stay in HEAD's families until M4), so ABO starts nothing of `tasks`, creates no ops instrument and
mounts no task route (05-tasks §5T.4 step 1, §5T.8.2 Core group row; 05-metrics §13 item 1).

## Spec to read first (in this order)

1. 05-tasks §5T.4 steps 0–4 (step 1 is the start rule; step 4 the stop order: scheduler before the executor drains),
   §5T.3.1 (registration checks), §5T.3.8 (schedule-key checks at registration), §5T.3.11 (held runs from every
   manifest's `TaskLedger`), §5T.3.13 (stores built at P4c), §5T.8.2 Core group row.
2. 06 §6.4.2 (`TaskLedger`, `DataMigrations`, `Cutover` doc comments), §6.4.3 (`Tasks`, `Resources`), §6.4.8
   (`WithRuns`, `RunTask`, `RunResult`, `AssertModuleInert`), §6.4.10 F2 row, §6.5.1 P1, P4 (a)/(b)/(c), P6, P7,
   "Boot writers", §6.5.2 Run steps (5) executor and (6) scheduler, §6.6 (`ProblemTaskLedger`), §6.10
   (`TestDisabledModuleWithRunsBoots`, `TestDisabledModuleInert`), §6.12.2–§6.12.3 (hello's manifest and `Register`).
3. 03 §3.8 F2 row; 05-rbac §5 items 2, 3 and 7 (audit before P4b, catalog, writer start); 05-config §1.12.1;
   05-health §5H.10 F2 row; 05-storage §5S.3.6 (where the runner runs).

## Base state: re-verify before editing

```bash
cd core
# every dependency's output must exist (each else STOP)
grep -rn 'func NativeStores' tasks/ | wc -l                                  # 1 (F2-02)
ls internal/executor/*.go | grep -vc _test                                    # >=1 (F2-03)
ls internal/scheduler/*.go | grep -vc _test                                   # >=1 (F2-04)
grep -c 'tasks.ScheduleConfig' kernel/core_sections.go                        # 13 (F2-04's schedule keys)
ls internal/taskhttp/*.go | grep -vc _test                                    # >=1 (F2-05)
grep -n 'func countTask' examples/hello/count.go | wc -l                     # 1 (F2-05)
ls storage/datamigrate/*.go internal/storagemigrate/*.go | grep -vc _test     # >=2 (F2-06)
ls kernel/audit.go kernel/catalog.go kernel/core_sections.go kernel/health.go kernel/migrate.go   # all 5 exist (F2-07, F2-08, F2-09, F2-10, F2-06)
# this row's targets must not be applied yet
grep -n 'TaskLedger' module/manifest.go | wc -l                               # 0 (1+ = applied, see Idempotency)
grep -n 'func (r \*Registrar) Tasks' module/registrar.go | wc -l              # 0
test ! -e kernel/tasks.go && echo absent                                      # absent
grep -n 'countTask' examples/hello/module.go | wc -l                          # 0
grep -rn 'func WithRuns' coretest/ | wc -l                                    # 0
grep -n 'Problem' module/*.go | grep -c 'ProblemTaskLedger'                   # 0 (this row adds it)
cd ..
# ABO side: still no kind and no tasks import
git grep -n 'mediacore/internal/executor\|mediacore/internal/scheduler\|mediacore/internal/taskhttp' -- ':!core/' | wc -l   # 0
grep -rln 'func TestABOMetricsGatherNoDuplicate' internal/ | wc -l            # 1 (R1-15, extended by F2-03)
```

Read each dependency's kernel-side file and note the function names it exposes for this row (they were coined by
those rows, e.g. a build/start/stop trio in `audit.go`, a declaration function in `catalog.go`, `coreSections()` in
`core_sections.go`, a build function returning the registry and two reporters in `health.go`, a data-migration entry
point in `migrate.go`). Call those; do not re-implement them. If one is missing or does not match its row's brief,
STOP and report.

## Files

Modify: `core/kernel/boot.go` (the boot calls, in phase order), `core/kernel/kernel.go` (per-module registration
consumption, the task-route mount, the executor and scheduler in the Run and stop order), `core/module/manifest.go`
(`TaskLedger`, `DataMigrations`, `ProblemTaskLedger` if the `Problem…` constants live there), `core/module/registrar.go`
(`Tasks`, `Resources`), `core/examples/hello/module.go`, `core/coretest/harness.go`.
Create: `core/kernel/tasks.go`, `core/kernel/tasks_test.go`, `core/examples/hello/held_runs_test.go`,
`core/coretest/tasks.go`.
Regenerate: `core/openapi/core.json` and `core/web/packages/api-client/src/generated/` with the generators R1-13 and
R1-30 wired (find the command in `core/Makefile` or `core/web/package.json`; do not hand-edit either output).

If the `Problem…` constants live in a `core/module/` file other than `manifest.go` and `registrar.go`, add
`ProblemTaskLedger` there and say so in the PR body (planner note). Touch nothing in ABO.

## Code contract

- `Manifest.TaskLedger` and `Manifest.DataMigrations`, `Registrar.Tasks` and `Registrar.Resources`: exactly 06's
  signatures and doc comments. `Tasks` and `Resources` only record (06 §6.5.1 P6: "Registrations are only recorded").
- P1 (static, every manifest, enabled or not): every `TaskLedger` entry parses as a `tasks.Kind` whose module segment
  is the manifest's ID; collisions across manifests fail.
- P7 (enabled modules): a registered kind missing from its manifest's `TaskLedger` → `ProblemTaskLedger`; the
  constructor problems F2-01 collects; `ValidateSchedules` (F2-04's) over the registered definitions against the
  declared schedule keys; a registered kind whose module segment is not the registering module's ID fails. Problems
  are joined (06 §6.5.1 P6/P7).
- Start rule (`core/kernel/tasks.go`, one function the boot calls, name coined here, e.g.
  `tasksNeeded(registered int, store tasks.RunStore) (bool, error)`): true iff `registered > 0` or the run store holds
  at least one run (a bounded read: one key under `c/tasks/run/`, never a full scan). When false: no executor, no
  scheduler, no `taskhttp` mount, no `<ns>.ops` instrument, no definition snapshot write. When true: build the executor
  (`InstrumentsOff: false`), the scheduler and the routes; the held set is every kind in a **disabled** manifest's
  `TaskLedger` (05-tasks §5T.3.11); the default `ReadySignal` waits on `core.storage` through F2-10's registry.
- Order (06 §6.5.1, §6.5.2): native stores at P4c; audit built after the store opens and before P4b; the data-migration
  runner at P4b in DAG order after the versioned steps (no app declares one yet: an empty DAG step); core sections on
  the list at P0; the catalog at P5 with the core services (or where R1-13's P5 builds services); the health registry
  before any reader of `Ready`; executor `Run` at Run step (5), scheduler at (6), audit writer at (7); shutdown stops
  the scheduler before the executor drains (05-tasks §5T.4 step 4) and the audit writer after the executor.
- Concurrency (CLAUDE.md; 05-tasks §5T.7): no goroutine is started when the rule is false; everything started is
  stopped on `New`'s failure path and on shutdown, in reverse order; no package-level state (R5).

## TDD: tests to write first

Spec names copied exactly; coined names marked.

In `core/examples/hello/held_runs_test.go` (package `hello_test`):

- `TestDisabledModuleWithRunsBoots` (06 §6.10; 05-tasks §5T.4 step 1): hello disabled with
  `coretest.WithRuns("hello.count", 3)` and an op-ledger line, resolved through hello's `TaskLedger`; boot succeeds,
  the three runs are listed held over `GET /api/core/v1/tasks/runs`, none is dispatched; re-enabling hello resumes
  them. The run store holding runs is what starts `tasks` here (no kind is registered while hello is disabled).
- `AssertModuleInert` with the held-runs cell, called from this test for the disabled hello (06 §6.4.8).
- `TestHelloCount_RunsOnCoretest` (name coined here): `coretest.New(t, coretest.WithModules(hello.New()))`;
  `h.RunTask(t, "hello.count", hello.CountParams{})` succeeds (the line 06 §6.12.6's `TestHello` holds; see planner
  note), and the `hello.count` half of `TestNativeStores_RoundTrip` (05-tasks §5T.9: "`hello.count` on a `coretest`
  App whose only module is `hello`"), as a subtest named `TestNativeStores_RoundTrip_HelloCount` (name coined here).

In `core/kernel/tasks_test.go` (names coined here):

- `TestKernelTasks_NothingStartsWithoutKindsOrRuns`: a zero-module boot (G7) and a boot whose only module registers no
  kind, over an empty store: no executor, no scheduler goroutine, no route under `/api/core/v1/tasks` (404), no family
  named `<ns>_ops_*` in `coretest.MetricsProvider`'s gather, no `c/tasks/def/` key written. This is ABO's shape before
  F5.
- `TestKernelTasks_StartsWhenAKindIsRegistered` and `TestKernelTasks_StartsWhenRunsAreHeld`.
- `TestKernelTasks_LedgerMissingKindRefused` (`ProblemTaskLedger`) and `TestKernelTasks_ForeignKindRefused`.
- `TestBatch_ZeroParallelismRefusedAtRegister` and `TestStart_ViewOnlyDefRefusedAtRegister` (05-tasks §5T.9): the
  registration-time halves (F2-01 owns the constructor half of the first under the same name in package `tasks`).
- `TestKernelBoot_F2CallsInOrder`: a recorded boot shows audit built before P4b, the catalog declared, the core
  sections listed, the health registry holding `core.storage` (Up) and `core.secrets` (Up), and the data-migration
  runner invoked at P4b (the boot-path halves of F2-07's, F2-08's and F2-10's kernel tests).

Re-run, unchanged: `TestBootCrashMatrix` (core's, over hello: add the rows for the definition-snapshot upsert and any
other durable boot write this PR adds, 06 §6.5.1 "Boot writers"; 09 §9.3) and, in the root module,
`TestABOMetricsGatherNoDuplicate` (ABO builds no executor).

## Steps

1. Base-state greps; read the five kernel-side files; write the plan into the PR description (each boot call, its
   phase, the function it calls).
2. Tests first (they fail).
3. `core/module`: the two manifest fields, the two registrar methods, `ProblemTaskLedger`.
4. `core/kernel/tasks.go`: stores at P4c, P1/P7 checks, snapshot upsert, the start rule, construction and the
   Run/stop order; then the other boot calls in `boot.go` and `kernel.go`.
5. `hello`: manifest `TaskLedger` and `Register`'s two calls. `coretest`: `tasks.go` and the held-runs cell.
6. Regenerate `core/openapi/core.json` and the client; commit the generated output.
7. `-race` green; crash-matrix rows; headers; changelog fragment; acceptance.

## Acceptance (commands and expected output)

```bash
cd core
go build ./... && go vet ./kernel/... ./module/... ./coretest/... ./examples/hello/...          # exit 0
go test ./kernel/... ./module/... ./coretest/... ./examples/hello/... -race -count=1             # all ok
go test ./examples/hello/ -run 'TestDisabledModuleWithRunsBoots|TestHelloCount_RunsOnCoretest' -race -count=1 -v | grep -c '^--- PASS'   # 2
go test ./kernel/ -run 'TestKernelTasks_|TestBatch_ZeroParallelismRefusedAtRegister|TestStart_ViewOnlyDefRefusedAtRegister|TestKernelBoot_F2CallsInOrder' -race -count=1   # ok
go test ./... -run 'TestBootCrashMatrix|TestDisabledModuleInert' -race -count=1                  # ok
cd ..
git status --porcelain core/openapi/core.json core/web/packages/api-client/src/generated/ | wc -l  # 0 after commit (regeneration committed)
git grep -n 'Registrar.Tasks\|\.Tasks(' -- 'internal/books/*.go' | wc -l                         # 0 (ABO makes no Tasks call before F5)
go test ./internal/books/ -run TestABOMetricsGatherNoDuplicate -count=1                          # ok
make -C core ci                                                                                  # exit 0
make ci                                                                                          # exit 0
```

## STOP conditions

- `TestKernelTasks_NothingStartsWithoutKindsOrRuns` or `TestABOMetricsGatherNoDuplicate` fails: the rule starts
  `tasks` in ABO's shape. Fix the rule; never special-case ABO or hello by name.
- A dependency's kernel-side file has no function to call, or needs a change to its own package: report it.
- Any need to touch an ABO file, or to register a core kind: out of scope.

## Out of scope

ABO's first `Registrar.Tasks` call, `TaskStarts`, `Lanes`, `ChangeSink`, `Importers.Tasks`, core's own kinds and the
three late-owner registrations (F5-18); `module.Registrar.Health`/`Deps.Health` (F5); mounting any core route other
than `taskhttp`; the config loader (F3-01); the secrets keyring (`core.secrets` stays push-only, F3-03); any cutover
step (F3-06 onward).

## Idempotency / rollback

If `core/kernel/tasks.go` exists and `Manifest.TaskLedger` is declared, run the acceptance instead. Rollback = revert
the PR: the F2 packages return to unwired, hello registers no kind, and ABO is unaffected (it never met the start rule).

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task (CLAUDE.md).
- File headers on every created or modified file, version bumped (the generated client and `core.json` follow their
  generators' rules); `changelog.d/` fragment without a header.
- Conventional commit, e.g. `feat(core/kernel): wire the F2 foundation and the tasks start rule`, body ending
  `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer (§9.2.1).
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- This PR adds boot-time durable writes (the definition snapshots), so it extends the crash matrix in the same PR
  (09 §9.3 "Boot writers extend the crash matrix").
- CLAUDE.md concurrency rules: bounded executor and scheduler, everything stopped on failure and shutdown.
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- 06 §6.12.6's `TestHello` (in `core/examples/hello/hello_test.go`) holds the `h.RunTask(t, "hello.count", …)` line,
  but that file is not in the row; the brief puts the cell in `held_runs_test.go` as `TestHelloCount_RunsOnCoretest`.
  Add `hello_test.go` to the row if the spec's exact test should carry it.
- `ProblemTaskLedger` belongs with R1-13's `Problem…` constants, whose file the row does not name; if they are not in
  `manifest.go` or `registrar.go`, that file needs adding to the row.
- The `hello.count` half of `TestNativeStores_RoundTrip` needs hello's registration and `coretest`'s task cells, so it
  lands here, not in F2-02 (which owns the gate); no row named it.
