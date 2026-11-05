<!-- file: docs/agent-tasks/platform-core/F5-01-maintenance-jobs-become-task-kinds.md -->
<!-- version: 1.1.2 -->
<!-- guid: 4851e004-a6e4-42d7-97a5-b7407fc8b8a3 -->
<!-- last-edited: 2026-11-05 -->

# F5-01 — Conversion group T2: the 34 `MaintenanceJob`s of `internal/maintenance/jobs` become books Task kinds, dark

| Field | Value |
|---|---|
| ID | F5-01 |
| Release / flip | R2, tasks flip F5 (dark conversion PR) |
| Wave | W31 |
| Tier | **T3** (Opus / high): 34 jobs converted through one native shell, each job body rerouted to reach its writer and stores only through what the shell hands it (05-tasks conversion rule 3), with conformance tests |
| Depends on | F2-05 (transitively F2-01..F2-04), F2-16 (`Registrar.Tasks`, `coretest`'s task cells), F2-17 (`tasklint` and this row's exemption entry file) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-01-maintenance-jobs-kinds
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` in the worktree (CLAUDE.md: it breaks the build). `core/` is a nested Go module
(module path `github.com/falkcorp/mediacore`); the root module requires it, so root packages import
`github.com/falkcorp/mediacore/tasks` directly. Run core's own tests from inside `core/`.

## Goal

Give each of the 34 `maintenance.MaintenanceJob`s registered by `internal/maintenance/jobs` a native
`core/tasks` Task kind `books.maintenance.<job ID>` that lands **dark** (05-tasks §5T.8.2, T2 row:
"`Run(ctx, JobStore, ProgressReporter, dryRun)` `internal/maintenance/job.go:498` → `TaskSpec.Run(rc, p)`;
`Policy()` → `Common`"; gate "`taskstest.Conformance` per kind"). HEAD's wrapper
(`internal/server/maintenance_job_op.go`) keeps registering and running the jobs until the tasks flip F5-18.

Because every job already has the shared-core shape (`Run` takes `dryRun bool` and returns `error`), this PR
needs **one** native shell for all 34, not 34 refactors. Job files change only for the three v1 checkpointers and
for any job that reaches a store other than its `store` argument (rule R10). It deletes its own `tasklint`
exemption entry file, `internal/books/testdata/tasklint_exempt.d/F5-01.txt`, in the same PR.

## Spec to read first

1. 05-tasks §5T.8.2 (T2 row; body changes: `rc.Mode()`, `rc.Checkpoint`, return the result; conversion rules
   1–4), §5T.8.4 (`tasklint`'s `writer-bypass`/`store-capture` and the dark-conversion ratchet), §5T.8.1
   (`internal/maintenance` row: "jobs → books Task kinds (group T2); the package is deleted by the flip";
   `internal/operations` row: the v1 checkpoint helpers, 11 books call sites, "replaced by `rc.Checkpoint` in
   each caller's conversion PR").
2. 05-tasks §5T.3.7 mode contract item 2: "Maintenance jobs' per-job default (`ResolveDryRunDefault`) is
   expressed as `ReadOnly` for the read-only jobs".
3. 05-tasks §5T.3.1, §5T.3.3, §5T.3.6; 05-rbac §6; CLAUDE.md concurrency rules.
4. Plan §(l) item 4: converted bodies call a narrow interface declared in their own package; F5-18 wires
   `internal/books/writer` behind it.

## Base state: re-verify before editing

```bash
# F2 outputs this PR builds on. Each must hit at your base; 0 hits on any line = STOP and report.
test -f core/go.mod && grep -c '^module github.com/falkcorp/mediacore$' core/go.mod      # 1
grep -c 'github.com/falkcorp/mediacore' go.mod                                            # >=1 (root requires core)
grep -rn 'func Task\[P, R any\]' core/tasks/*.go | grep -v _test | wc -l                  # 1 (F2-01)
grep -rn 'func Batch\[P, T, R any\]\|func Fixer\[P, C any\]' core/tasks/*.go | grep -v _test | wc -l   # 2
grep -rn 'func Fixed(n int, reason string) Parallelism\|func Sequential(reason string) Parallelism' core/tasks/*.go | wc -l   # 2
grep -rn 'func RunItems\[T any\](rc \*RC' core/tasks/*.go | wc -l                         # 1
grep -rn 'func (rc \*RC) Mode() Mode\|func (rc \*RC) Checkpoint(v any) error\|func (rc \*RC) Restore(v any) (bool, error)' core/tasks/*.go | wc -l   # 3
grep -rn 'func Conformance(' core/tasks/taskstest/*.go | wc -l                             # 1 (F2-01)
test -f core/coretest/tasks.go && echo present                                            # present (F2-16: coretest's task cells)
grep -c 'Tasks(' core/module/registrar.go                                                 # >=1 (F2-16: Registrar.Tasks)
test -d core/archtest/tasklint && test -f internal/books/tasklint_test.go && echo tasklint   # tasklint (F2-17)
test -f internal/books/testdata/tasklint_exempt.d/F5-01.txt && echo entry                 # entry (F2-17 created it), else STOP
grep -v '^#' internal/books/testdata/tasklint_exempt.d/F5-01.txt | grep -v '^$' | grep -v '^internal/maintenance/jobs/' | wc -l   # 0 (the entry names only files under internal/maintenance/jobs/)
```

A grep that misses only because of formatting (a wrapped signature, an extra space) is not a STOP: grep the bare
symbol name (`func Task[`, `func Conformance(`) first, and STOP only if the symbol itself is absent.

```bash
# HEAD's engine is still the production engine (dark rule R1): each must still hit
grep -rn 'func RunItems\[T any\](ctx context.Context, r Reporter' internal/operations/registry/run_items.go | wc -l   # 1
grep -n 'Empty Writes = declares nothing' internal/operations/registry/types.go | wc -l                               # 1
grep -n 'ResumeRestart *ResumePolicy\|ResumeRestart  *// reload' internal/operations/registry/types.go | wc -l        # >=1
# No converted kind registered in production yet (must stay 0 after your PR too)
git grep -n 'mediacore/tasks"' -- main.go internal/opscatalog/ | wc -l                                         # 0
```

```bash
grep -rn 'maintenance.Register(' internal/maintenance/jobs/*.go | grep -v _test | wc -l        # 34, else STOP
grep -n 'Run(ctx context.Context, store JobStore, reporter ProgressReporter, dryRun bool) error' internal/maintenance/job.go | wc -l   # 1
grep -n 'type JobStore interface' internal/maintenance/job.go | wc -l                             # 1
grep -n 'func All() \[\]MaintenanceJob' internal/maintenance/registry.go | wc -l                # 1
grep -n 'func WithOperationID\|func WithRawParams\|func WithResultSetter' internal/maintenance/*.go | wc -l   # 3
grep -ln 'operations.SaveCheckpoint\|operations.LoadCheckpoint' internal/maintenance/jobs/*.go | grep -v _test   # backfill_file_hashes.go, recompute_book_aggregates.go, retention_and_hygiene.go
grep -n 'func advertisedDryRunDefault\|func maintenanceJobPreview' internal/server/*.go | wc -l  # 2 (HEAD's wrapper: read it, do not edit it)
grep -rln 'mediacore/tasks"' internal/maintenance/ | wc -l                                       # 0
```

## What HEAD's wrapper does (read `internal/server/maintenance_job_op.go`; you do not edit it)

For each job it registers a v2 def `maintenance.<job ID>` whose `Run`: decodes `dry_run`/`dryRun` with
`opmode.ResolveDryRunDefault` and the job's advertised default; sets `maintenance.WithOperationID(ctx, <run's
own v2 ID>)`, `maintenance.WithRawParams(ctx, rawParams)` and a result setter; and calls
`job.Run(ctx, store, reporter, dryRun)`. Its field values: `Preview` = `maintenanceJobPreview(job, policy)`;
`Permissions` = the job's `PermissionAware.Permission()` or `settings.manage`; `ConcurrencyKey` =
`policy.ConcurrencyKey` or the def ID; `Timeout`, `ResumePolicy`, `Liveness`, `Capabilities` from `Policy()`;
`DefaultPriority` `PriorityNormal`.

## The native shell (one file, all 34 jobs)

`internal/maintenance/jobs/kinds.go` (name coined here):

```go
// Store is the narrow interface the converted jobs call (plan §(l) item 4): the method set of
// maintenance.JobStore, declared here so F5-18 can delete internal/maintenance. Name coined here.
type Store interface{ /* exactly maintenance.JobStore's methods, copied */ }

var _ Store = maintenance.JobStore(nil)   // compile-time: same method set
var _ maintenance.JobStore = Store(nil)

// KindDeps is what the owner (books, from F5-18) declares for the kinds (rule R10; 05-tasks §5T.8.4
// store-capture). Name coined here.
type KindDeps struct {
	// NewStore returns the job's store view for one run: its write methods go through w (F5-18:
	// internal/books/writer over rc.Writer()), its read methods to the books store.
	NewStore func(w tasks.Writer) Store
}

// Kinds returns one native kind per registered job, in maintenance.All() order. Nothing calls it in
// production; F5-18 does. Name coined here.
func Kinds(deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition   // Schedule: sched(kind), rule R9

// one kind per job:
//   tasks.Task(tasks.Kind("books.maintenance."+job.ID()), tasks.TaskSpec[json.RawMessage, json.RawMessage]{
//       Common: <rules R5–R7 over job.Policy(), job.Name(), job.Description(), PermissionAware>,
//       Run: func(rc *tasks.RC, raw json.RawMessage) (json.RawMessage, error) { ... },
//   })
```

The native `Run`:

1. Refuses a `dry_run`/`dryRun` key in `raw` with an error (05-tasks §5T.3.7 item 1; the start route answers
   `400 tasks.mode_field_unsupported` before this, the shell is the backstop).
2. Builds ctx from `rc.Context()` with `maintenance.WithOperationID(ctx, string(rc.RunID()))`,
   `maintenance.WithRawParams(ctx, raw)`, a result setter that captures the value, and the checkpointer of
   step 4.
3. Calls `job.Run(ctx, deps.NewStore(rc.Writer()), progress, rc.Mode() == tasks.ModePreview)` with `progress` an in-file
   `maintenance.ProgressReporter` (and `ProgressSeeder`) over `rc.Progress()`/`rc.Log()` (name coined here:
   `rcProgress`).
4. **Checkpoints.** The three jobs that call `operations.SaveCheckpoint`/`LoadCheckpoint` switch to an
   in-package checkpointer read from ctx (`checkpointerFrom(ctx)`, name coined here): when ctx carries one
   (the native shell sets one over `rc.Checkpoint`/`rc.Restore`, HEAD's checkpoint struct unchanged so
   `Restore` decodes HEAD's bytes), use it; when not (HEAD's wrapper, unchanged), call the v1 helpers exactly
   as today. Their existing tests must stay green unchanged.
5. Returns the captured result (or `null`) and `job.Run`'s error.

`Common` by rules R5–R7: `Title`/`Help` = `job.Name()`/`job.Description()`; `Resume` from
`Policy().ResumePolicy`; `Timeout`; `ConcurrencyKey` = `tasks.Key("books." + policy.ConcurrencyKey)` when set,
else zero (`Key(Kind)`, which equals HEAD's derived key); `Priority` `tasks.PriorityNormal`; `Perms.Start`
from `Permission()` or `settings.manage` by rule R6; `Effects` by rule R7 with HEAD's `Preview` computed the way
`maintenanceJobPreview` does (non-zero iff the job advertises a dry-run default), which makes the
read-only jobs `ReadOnly` (05-tasks §5T.3.7 item 2). Jobs have no in-body `registry.RunItems` the shell
touches; a job's own internal loops are unchanged (rule R4 applies only to the loop the shell owns).

## The conversion rules (every definition this PR converts)

05-tasks §5T.8.2 fixes the three body changes (`rc.Mode()`, `rc.Checkpoint`, the returned result), conversion
rules 1–4 (here R7, R2, R10 and R9) and the naming rule (R3); R4 is its parallelism rule. R5, R6 and R8 are this
plan's mechanical mapping of HEAD's fields onto the spec's `tasks.Common` (05-tasks §5T.3.1), with the source of
each line. Apply every rule the same way to every definition; never improvise a field.

**R1, dark (05-tasks §5T.8.2).** The converted definition compiles, is exercised by
`taskstest.Conformance`, and is **not registered anywhere in production**: HEAD's v2 definition keeps its ID,
its registration and its behaviour, and the production build still runs HEAD's engine. Nothing in `main.go`,
`internal/server` wiring, `internal/opscatalog` or any `Register`/`RegisterOp` call changes in this PR. The
tasks flip F5-18 registers the native kinds and deletes HEAD's definitions.

**R2, shared core, two shells (05-tasks §5T.8.2 conversion rule 2; helper names coined here).** One body serves
both engines during the dark window, so the parity suites test the code production runs and HEAD's behaviour is
the oracle. For the jobs, the shared body is each job's existing `Run` and the two shells are HEAD's wrapper and
`kinds.go` (see "The native shell" above); rule R10 says what each shell hands it. The general form:

1. Move the body of HEAD's `Run` func into an unexported function in the **same file**, named
   `<runFuncName>Core` (name coined here), that takes `ctx context.Context`, the decoded params struct, and a
   per-definition env struct declared in the same file (`<runFuncName>Env`, name coined here) holding only
   what the body needs from its engine: `Live bool`; `Log func(level slog.Level, msg string)`;
   `Progress func(done, total int, msg string)`; and, only when the body uses them, `SaveCheckpoint func(v any) error`,
   `LoadCheckpoint func(v any) (bool, error)` and `Items func(n int, fn func(ctx context.Context, i int) error) error`
   (the in-body loop). It **returns** the result value instead of writing it (`(Result, error)`).
2. HEAD's `Run` becomes the **v2 shell**: decode params exactly as before (including the `dry_run`/`dryRun`
   reading through `opmode`), fill the env from the `Reporter` (`Live` = not dry-run; `Items` = HEAD's
   `registry.RunItems` call with its **unchanged** `RunItemsOptions`), call the core, then write the result
   exactly as before (`registry.ReporterSetResult` / `maintenance.SetResult` / the summary row). Every
   existing test of the definition must still pass **unchanged**: they are the proof the shell kept HEAD's
   behaviour.
3. The **native shell** is a `tasks.Task[P, R]` (or `tasks.Batch` where HEAD's def is already a per-item
   runner and you keep the item function verbatim) whose `Run(rc, p)` fills the env from `rc`:
   `Live` = `rc.Mode() == tasks.ModeLive`; `Log`/`Progress` = `rc.Log()`/`rc.Progress()`;
   `SaveCheckpoint`/`LoadCheckpoint` = `rc.Checkpoint`/`rc.Restore` **with HEAD's checkpoint struct type
   unchanged**, so `Restore` decodes the bytes HEAD wrote (05-tasks §5T.3.6 "An imported run carries HEAD's
   checkpoint bytes"; `TestCutover_CheckpointsResume` in the flip depends on it); `Items` = `tasks.RunItems`
   with the parallelism of rule R4. It returns the core's result. It never reads a `dry_run` param
   (05-tasks §5T.3.7 mode contract item 3: params structs carry no `DryRun` field on the native side; if
   HEAD's params struct has one, the native shell uses a copy of the struct without it, declared in the
   same file).
4. Do **not** rename, re-sign or delete any existing function, type, constant or exported symbol: other
   files (and other same-wave PRs) call them. Only add.

**R3, kind name (05-tasks §5T.8.2 "kind-rename map", rule 4).** `tasks.Kind("books." + <HEAD def ID>)`,
the ID verbatim (dots and hyphens kept; `tasks.ParseKind` must accept it). Declare it as an unexported
constant next to HEAD's ID constant. A HEAD `FormerIDs` list is **not** carried (there is no alias field,
05-tasks §5T.3.1; M4's map consumes aliases, rule 3).

**R4, parallelism 1:1 (05-tasks §5T.8.2).** Every in-body loop keeps HEAD's worker count: HEAD
`RunItemsOptions.Concurrency` = n > 0 → `tasks.Fixed(n, "<reason>")`; zero or omitted → `tasks.Sequential("<reason>")`
(zero is sequential at HEAD, `internal/operations/registry/run_items.go` clamps it to 1). `Concurrency:
runtime.NumCPU()` → `tasks.CPU()`. The reason is one sentence copied from HEAD's comment on that call, or
"HEAD declared no concurrency (sequential)". Any change of a definition's parallelism is a **separate PR**
(05-tasks §5T.8.2; CLAUDE.md), never this one.

**R5, field map** (HEAD `registry.OperationDef`, `internal/operations/registry/types.go`, → `tasks.Common`):

| HEAD field | Native | Source |
|---|---|---|
| `DisplayName`, `Description` | `Title`, `Help` (verbatim) | 05-tasks §5T.3.1 |
| `ResumePolicy` `ResumeRestart` / `ResumeRequeue` / `ResumeDrop` / `ResumeAsk` | `tasks.ResumeContinue` / `tasks.ResumeFromZero` / `tasks.ResumeDrop` / `tasks.ResumeAsk` | 05-tasks §5T.3.6 |
| `DefaultPriority` `PriorityLow` / `PriorityNormal` / `PriorityHigh` (0/1/2) | `tasks.PriorityLow` / `PriorityNormal` / `PriorityHigh` | 05-tasks §5T.3.1; §5T.8.3 ("Priority 0/1/2 → Low/Normal/High") |
| `Timeout`, `ProgressTimeout` | `Timeout`, `ProgressTimeout` (0 stays 0: the shape default) | 05-tasks §5T.3.6 |
| `ConcurrencyKey` | `""` → leave zero (`Key(Kind)`); `v` → `tasks.Key("books." + v)` (the same HEAD key maps to the same native key in every PR) | 05-tasks §5T.3.4 (keys are `<module>.<name>`) |
| `DependsOn` | `NotWhile`, each ID mapped by R3 (or by 05-tasks §5T.8.2's core table for the six T4 IDs) | 05-tasks §5T.3.1 ("v2 DependsOn") |
| `DedupeQueuedRuns`, `DedupeSameActorOnly`, `MergeQueuedParams` | `Dedupe{AnyParams, SameActorOnly, Merge}` | 05-tasks §5T.3.1 `DedupePolicy` |
| `Batchable`, `BatchWindow`, `BatchMaxWait` | `Debounce` | 05-tasks §5T.3.1 |
| `NotifyLevel` | `Notify` | 05-tasks §5T.3.1 |
| `NoWarmupWait` | `false` → `Ready` = the readiness component `books.memdb`; `true` → `Ready` nil | 05-tasks §5T.3.1 `Ready`; `TestDispatch_WaitsForReadySignal` (COR4-8) |
| `Permissions` | `Perms` by rule R6 | 05-tasks §5T.3.7 |
| `Writes`, `Reads`, `Preview`, `Capabilities` | `Effects` by rule R7 | 05-tasks §5T.3.4 |
| `Schedule` (a v2 cron string) | **none**: never evaluated at HEAD (05-tasks §5T.2); dropped and listed for the release note (05-tasks §5T.3.8; 14 Q-C). Schedules are F5-14's | 05-tasks §5T.3.8 |
| `Triggers` | none in this PR (the one trigger, `book.relocated`, becomes an outbox topic at F6) | 05-tasks §5T.2; 05-events §5E.13 |
| `Plugin`, `FormerIDs`, `Cancellable`, `ParamsSchema`, `SummarizeQueued`, `Liveness`, `MinCheckpointInterval`, `ProgressFlushInterval`, `Synchronous`, `Isolate`, `RunsAs` | not carried; list each non-zero value per kind in the PR description under "Dropped HEAD fields". `Cancellable` is never read at HEAD (05-tasks §5T.2); `Isolate: true` or `RunsAs: ActorSystem` has 0 hits at `ac4368bdb`: a hit at your base is a STOP | 05-tasks §5T.2, §5T.3.6 |
| `Requires` | the `Requires` field `core/tasks` landed (grep `Requires` in `core/tasks/*.go`); absent → STOP for that one definition and report | 05-tasks §5T.4 (`waiting_deps`) |
| `Phases` | `rc.Phase(name)` around the same code | 05-tasks §5T.3.3 |

**R6, permissions (05-tasks §5T.3.7, "start" row; 05-rbac §6 atom table).** `Perms.Start` is the native
requirement the atom table maps HEAD's `Permissions` to (the "Route requirement" column; all of HEAD's atoms
are required, so combine them with `rbac.AllOf`); HEAD's empty list means HEAD's default `settings.manage`.
A definition whose atoms map only to view-class scopes (`library.view`) converts to
`rbac.AllOf(books:scan, books:view)`, which keeps the scan floor (`TestStart_ConvertedViewDefKeepsScanFloor`).
Read the table from the books atom-table package the identity flip landed (`grep -rln 'TestAtomTable_Golden'
internal/` at your base; 0 hits = STOP). Never set `StartableByViewers` in a conversion. `Perms.Approve`
is set only where `Approval` is (Fixers, F5-02).

**R7, effects (05-tasks §5T.8.2 conversion rule 1; §5T.3.4).** HEAD's write set is table-level and optional
("Empty Writes = declares nothing = the gate skips this op", `internal/operations/registry/types.go`); core
requires `Effects` (05-tasks §5T.3.1). Map with `res(s) = tasks.Res("books", s)` for HEAD's resource strings
(`books`, `book_files`, `authors`, `series`, `review_items`, `embeddings`, `operations`, `catalog`,
`fingerprint_index`):

| HEAD | Native `Effects` | PR description list |
|---|---|---|
| `Writes` non-empty | `tasks.Writes(res(w)...)`, plus `.AlsoReads(res(r)...)` for `Reads` | — |
| `Writes` empty, the body writes | `tasks.Writes(res(x)...)` for every books resource the shared core **and every function it calls** writes, found by reading them (05-tasks conversion rule 1). For a job this is a reading of the write methods it calls on its `store` argument (rule R10), each mapped to the books resource that `JobStore` method writes | "Writes found": per definition, each resource and the call that writes it |
| `Writes` empty, the body writes nothing | `tasks.ReadOnly()`, with no declared reads | "Declared ReadOnly" |

A definition HEAD runs only in an explicit live mode (`Writes` empty, `Preview` zero, a `library.write`/
`files.write`/`db.migrate` capability; `internal/operations/registry/preview.go`, `PreviewSpec` comment) keeps that
behaviour (rule 3): its native shell returns an error when `rc.Mode() != tasks.ModeLive` ("<kind>: preview not
supported; start with mode live"). List those under "Live-only writer".

**R9, the schedule hook (05-tasks §5T.8.2 conversion rule 4; list-function names coined here).** `tasks.Definition` is opaque and `Common.Schedule` is set
at construction (05-tasks §5T.3.1, §5T.3.8), while the 38 schedules are declared later, in
`internal/books/schedules.go` (F5-14), and attached by the flip. So every list function this PR adds takes
its owner-declared dependencies (rule R10) and `sched func(tasks.Kind) *tasks.Schedule` and sets `Common.Schedule: sched(<kind>)` on every kind it builds
(a nil `sched` means no schedule; tests pass nil). No conversion declares a schedule itself.

**R8, tests (05-tasks §5T.8.2 "Gate" column; §5T.9 last row).** One new test file per PR (name in the
Files table) that builds every native kind of the PR and runs `taskstest.Conformance(t, defs...)` over
them, plus one table test that asserts each kind's name, `Title`, `Resume`, parallelism `String()` and
`Effects` against the HEAD definition it came from (built from HEAD's `xxxDef()` in the same test, so a
drift fails). HEAD's existing tests stay unchanged and green.

**R10, writer and stores only through the shell (05-tasks §5T.8.2 conversion rule 3; §5T.8.4 "The
dark-conversion ratchet"; plan §(l) items 4 and 5).** A dark conversion keeps HEAD's behaviour, and the shared
body reaches its writer and its stores **only through what its shell hands it**. For the 34 jobs the shared body
is each job's existing `Run(ctx, store, reporter, dryRun)`, and what a shell hands it is the `store` argument:

1. **The native shell** hands each run `deps.NewStore(rc.Writer())`: a `Store` whose writes go through the run's
   `tasks.Writer` and whose reads go to the store the owner declared (05-tasks §5T.8.4 `store-capture`: "through
   `tasks.Writer` (`rc.Writer()`) or the dependencies its owner declared for it"). Nothing passes `KindDeps` in
   production before F5-18, which implements `NewStore` over `internal/books/writer`; tests pass a recording fake.
2. **HEAD's shell** (`internal/server/maintenance_job_op.go`, unchanged) keeps handing HEAD's store, which is the
   adapter of the same interface over HEAD's writer (the `var _` lines above prove the method sets are equal).
3. **No other store.** A job whose body reads or writes any store other than its `store` argument (a package
   variable, a value captured at registration, a store reached through `maintenance` helpers) is rerouted to the
   argument, without changing what it writes or in which order. Find them with
   `grep -n 'Store\b\|store\.' internal/maintenance/jobs/*.go | grep -v _test` and by reading each job's `Run` and
   the functions it calls; list every rerouted job in the PR description. A helper that captures a store gets a
   new variant taking the `store` argument (name coined per helper: `<helper>Via`); the existing helper is not
   re-signed.
4. The other body changes stay the three of 05-tasks §5T.8.2 (here: `dryRun` from `rc.Mode()`, the checkpointer of
   step 4, the captured result).

STOP and report (never an allow directive, a `//nolint`, or a line added to any entry file) if a job's write cannot
reach the store argument without changing what HEAD writes or its order; if `tasklint` still reports
`writer-bypass` or `store-capture` in `internal/maintenance/jobs` after rule R10; or if it reports one in a file
this row does not own (that file is another row's; name it in the report).

**The exemption entry (05-tasks §5T.8.4; plan §(l) item 5).** F2-17 created
`internal/books/testdata/tasklint_exempt.d/F5-01.txt`, listing the books files this row converts. `tasklint`
reads the directory as one union whose ratchet only shrinks (a PR that adds a file or a line fails). This PR
deletes **that file only**, in the same PR as the conversion, and touches no other entry file: its same-wave
siblings delete their own.

## Files

| Action | Path |
|---|---|
| create | `internal/maintenance/jobs/kinds.go`, `internal/maintenance/jobs/kinds_test.go` (names coined here) |
| modify | any other job file only where rule R10 item 3 reroutes a store it reaches outside its `store` argument (each listed in the PR description) |
| delete | `internal/books/testdata/tasklint_exempt.d/F5-01.txt` (this row's exemption entry file; rule R10) |
| modify | `internal/maintenance/jobs/backfill_file_hashes.go`, `recompute_book_aggregates.go`, `retention_and_hygiene.go` (the checkpointer switch only) |
| create | one `changelog.d/` fragment |

Do not modify `internal/maintenance` (the root package), `internal/server/maintenance_job_op.go`, any job file
rule R10 does not require, any other file under `internal/books/testdata/tasklint_exempt.d/`, `internal/opscatalog`
or `main.go`.

## TDD: tests to write first (names coined here)

- `TestJobKinds_Conformance`: `taskstest.Conformance(t, jobs.Kinds(fakeKindDeps(t), nil)...)` over all 34.
- `TestJobKinds_MatchHEADWrapper`: for every `maintenance.All()` job, kind = `books.maintenance.` + ID,
  `Title`, `Resume`, `Timeout`, the `Effects` class (ReadOnly exactly for the jobs whose advertised dry-run
  default is false and that hold no write capability) and `Perms.Start` match what HEAD's wrapper declares.
- `TestJobKinds_ModeMapsToDryRun`: a recording fake job sees `dryRun == true` under `ModePreview` and `false`
  under `ModeLive`; a `dry_run` key in params is refused.
- `TestJobKinds_CheckpointerFromRC`: for each of the three checkpointing jobs, a checkpoint written by the v1
  helper's struct round-trips through the native checkpointer (HEAD's bytes decode).
- `TestJobKinds_StoreFromRCWriter`: for each kind, one `ModeLive` run on a `taskstest` fake `RC` shows
  `NewStore` called with that run's `rc.Writer()` and the job receiving exactly that store (rule R10).

## Steps

1. Base checks; STOP on a mismatch (34 jobs, the three checkpoint files).
2. Write the five tests; then `kinds.go`; then the three checkpointer switches; then rule R10 item 3's reroutes.
3. `git rm internal/books/testdata/tasklint_exempt.d/F5-01.txt`, then run the acceptance block's `tasklint` line: it
   must pass with the package no longer exempt (rule R10's STOP if not).
4. PR description: the rerouted jobs (rule R10 item 3), the deleted entry file, the `maintenance.<id> ==> books.maintenance.<id>` list (34 lines), rule R5/R7 lists with
   counts, `converted: 34/34`.

## Acceptance (commands and expected output)

```bash
go build ./...                                                                   # exit 0
go vet ./internal/maintenance/...                                                # exit 0
go test ./internal/maintenance/... -count=1 -short                               # ok
go test ./internal/maintenance/jobs/ -run 'TestJobKinds_' -count=1 -v | grep -c '^--- PASS'   # 5
go test ./internal/server/ -run 'Maintenance' -count=1 -short                    # ok (HEAD's wrapper unchanged)
grep -c 'operations.SaveCheckpoint\|operations.LoadCheckpoint' internal/maintenance/jobs/backfill_file_hashes.go internal/maintenance/jobs/recompute_book_aggregates.go internal/maintenance/jobs/retention_and_hygiene.go | awk -F: '{s+=$2} END {print (s>0)}'   # 1 (the v2 fallback stays until F5-18)
git grep -n 'mediacore/tasks"' -- main.go internal/opscatalog/ internal/server/ | wc -l   # 0
test ! -e internal/books/testdata/tasklint_exempt.d/F5-01.txt && echo deleted       # deleted
git diff --name-only --diff-filter=D origin/main -- internal/books/testdata/tasklint_exempt.d/   # internal/books/testdata/tasklint_exempt.d/F5-01.txt (only)
git diff --name-only --diff-filter=AMR origin/main -- internal/books/testdata/tasklint_exempt.d/ | wc -l   # 0
go test ./internal/books/ -count=1 -run "$(grep -o '^func Test[A-Za-z0-9_]*' internal/books/tasklint_test.go | sed 's/^func //' | paste -sd'|')"   # ok (tasklint over the union; the package no longer exempt)
make ci                                                                          # exit 0
```

## Out of scope

Registration and deleting `internal/maintenance` and the wrapper (F5-18); HEAD's wrapper's activity-summary
entry, which the native shell does not reproduce (F5-18 decides it with books' activity subscriber, 05-events
§5E.13); the jobs' schedules (F5-14); the
`books.maintenance.retention-and-hygiene` / `core.tasks.prune` split, whose core half is F5-03 (05-tasks
§5T.8.2 T4 table: the job keeps its books kind and its dead-prefix sweep; its operation-record half becomes
`core.tasks.prune`, and F5-18 drops that half from the job when `core.tasks.prune` registers).

## Idempotency / rollback

If `internal/maintenance/jobs/kinds.go` exists and its tests pass, run the acceptance instead. Rollback = revert.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task (CLAUDE.md "Worktree Discipline").
- File headers on every created or modified file, version bumped, `last-edited` set to the day you edit
  (`.standards/instructions/file-headers.md`); keep existing `guid`s; new files get a fresh `guid`
  (`python3 -c 'import uuid;print(uuid.uuid4())'`). `changelog.d/` fragments carry **no** header.
- One `changelog.d/` fragment (`scriv create`, or a hand-written Markdown file); no hand edit of `CHANGELOG.md`.
- Conventional commit; the body ends with `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no
  release or flip trailer (09 §9.2.1: nothing records a flip), so add none.
- `gh pr create`, then `gh pr merge <n> --rebase` once every required check is green on an up-to-date
  branch; never squash, never `--admin` past a required check.
- CLAUDE.md concurrency rules: a loop over a library-scale collection with per-item work uses a bounded
  worker pool (`tasks.RunItems` / `tasks.ForEach` with an explicit `tasks.Parallelism`, or `errgroup` with
  `SetLimit`); `registry.RunItems` without `Concurrency` is sequential.
- Status report at the end: exact counts, then `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`,
  `BLOCKED: <n> — <list>` (CLAUDE.md).

Commit message: `refactor(maintenance): native books Task kinds for the 34 maintenance jobs (dark)`.
