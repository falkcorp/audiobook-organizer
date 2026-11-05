<!-- file: docs/agent-tasks/platform-core/F5-02-repairs-fixers-become-fixer-kinds.md -->
<!-- version: 1.0.0 -->
<!-- guid: bbc45161-5107-41fe-8e3e-77b95ed3dadb -->
<!-- last-edited: 2026-11-05 -->

# F5-02 — Conversion group T3: the 24 repairs fixers become `tasks.Fixer` kinds `books.repairs.<fixer-id>`, dark

| Field | Value |
|---|---|
| ID | F5-02 |
| Release / flip | R2, tasks flip F5 (dark conversion PR) |
| Wave | W26 |
| Tier | **T3** (Opus / high): Fixer semantics (plan rows, re-evaluation at apply, applied-row marks, guards) must stay exact, and M4 imports HEAD's stored rows and marks into the shape this PR defines |
| Depends on | F2-05 (transitively F2-01..F2-04) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-02-repairs-fixer-kinds
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` in the worktree (CLAUDE.md). `core/` is the nested module
`github.com/falkcorp/mediacore`; root packages import `github.com/falkcorp/mediacore/tasks` directly.

## Goal

Every one of the fixers (25 at `f7ac5b11e`; the spec says 24, see Base state) that `internal/plugins/maintenance`'s `Repairs()` registry holds becomes a native
`tasks.Fixer` kind named `books.repairs.<fixer ID>` (the fixer's `ID()` verbatim), landing **dark**: built by
one generic native shell in `internal/repairs` over the existing `repairs.Fixer` interface, exercised by the
Fixer conformance suite and by parity tests against HEAD's `RunPlan`/`RunApply`, and registered nowhere in
production (05-tasks §5T.8.2, T3 row: "24 repairs fixers → `tasks.Fixer` kinds `books.repairs.<fixer-id>`;
the 2 defs `repairs.plan`/`repairs.apply` removed"; gate "Fixer conformance suite"). HEAD's `repairs.plan` and
`repairs.apply` v2 definitions keep running until the tasks flip F5-18, which deletes them; they get **no**
native kind (map rule 2 sends their stored runs to the per-fixer kinds).

## Spec to read first (in this order)

1. 05-tasks §5T.3.1 (`FixerSpec`, `Fixer`; `Source`, `Row`, `Guard` "keep their v3 meaning
   (`pkg/ops/batch.go:197-259`, `pkg/ops/fixer.go:20-80`), with `Row.Subjects []entity.Ref`"), §5T.3.5
   (`Writer.MarkRowApplied`, `RowApplied`, `ChangeSink.Stands`, `ErrChangedSincePlan`), §5T.3.7 (approval:
   `Perms.Approve` + verified method; `Approval{PlanRequired, MaxPlanAge}`).
2. 05-tasks §5T.8.2 (T3 row; map rule 2: "`repairs.plan`/`repairs.apply` → `books.repairs.<fixer_id>` from the
   run's `fixer_id` param, a plan run as `ModePreview`, an apply run as `ModeLive` with `PlanRef` = the plan run
   HEAD's params name").
3. 05-tasks §5T.8.3 step 2 "runs" (the rows and marks paragraph: `opv2:row:`/`opv2:rowix:` → `c/tasks/row/`,
   `opv3:mark:`/`opv3:rowmark:` → `c/tasks/mark/`; "a row counts as applied only while its mark exists";
   an imported `awaiting_decision` plan run keeps its rows).
4. 05-tasks §5T.9 rows `TestFixer_PlanRefAgeAndRowsOnly`, `TestFixerMarksLandDuringQuiesce`,
   `TestCutover_RepairsRowsAndMarksImported` (the last is F5-16's and consumes this PR's row shape).
5. 05-rbac §6 (atom table). Plan §(l) item 4 (narrow writer interface in the converted package).

## Base state: re-verify before editing

```bash
# F2 outputs (0 hits on any = STOP)
grep -rn 'func Fixer\[P, C any\](kind Kind, s FixerSpec\[P, C\]) Definition' core/tasks/*.go | wc -l   # 1
grep -rn 'type FixerSpec\[P, C any\] struct' core/tasks/*.go | wc -l                                   # 1
grep -rn 'MarkRowApplied\|RowApplied' core/tasks/*.go | grep -v _test | wc -l                          # >=2
grep -rn 'ErrChangedSincePlan' core/tasks/*.go | wc -l                                                 # >=1
grep -rn 'func Conformance(' core/tasks/taskstest/*.go | wc -l                                         # 1
# HEAD's framework (counts at f7ac5b11e)
grep -n 'repairsReg.Register(' internal/plugins/maintenance/plugin.go | wc -l     # 24 call sites
grep -c 'Register(new' internal/plugins/maintenance/plugin.go                     # 23 single registrations
grep -n 'range \[\]repairs.Fixer{newVGPrimaryFixer(p), newFragmentFixer(p)}' internal/plugins/maintenance/plugin.go | wc -l   # 1 loop over 2 more fixers
grep -n 'type Fixer interface' internal/repairs/fixer.go | wc -l                  # 1
grep -n 'Plan(ctx context.Context, params json.RawMessage, reporter registry.Reporter) (\[\]Row, error)' internal/repairs/fixer.go | wc -l    # 1
grep -n 'Replan(ctx context.Context, params json.RawMessage, planned Row, reporter registry.Reporter) (Row, error)' internal/repairs/fixer.go | wc -l   # 1
grep -n 'Apply(ctx context.Context, w \*Writer, fresh Row) error' internal/repairs/fixer.go | wc -l   # 1
grep -n 'func RunPlan(\|func RunApply(\|func LoadPlan(' internal/repairs/engine.go | wc -l             # 3
grep -n 'PlanOpID  = "repairs.plan"\|ApplyOpID = "repairs.apply"' internal/repairs/engine.go | wc -l   # 2
grep -n 'const defaultApplyConcurrency = 4' internal/repairs/engine.go | wc -l    # 1 (apply parallelism, rule 4 below)
grep -n 'type Row struct' internal/repairs/wire.go | wc -l                         # 1 (HEAD's stored row shape)
grep -rln 'mediacore/tasks"' internal/repairs/ | wc -l                             # 0
```

So HEAD registers **25 fixers through 24 call sites** at `f7ac5b11e` (23 single calls plus one loop over
`newVGPrimaryFixer` and `newFragmentFixer`); 05-tasks §5T.8.2 and §5T.2 say "24", counting the call sites
(the same at `748fd787a`). The done-check is the registry's count at your base (05-tasks §5T.8.2: "Each
group's done-check uses the count generated at its own base commit"): write the first test so it builds
the maintenance plugin's `Repairs()` registry the way that package's tests do and asserts the count is 25
(or what your base registers: if it is neither 25 nor explained by a fixer added or removed since
`f7ac5b11e`, STOP and report).

## Files

| Action | Path |
|---|---|
| create | `internal/repairs/kinds.go` (name coined here): the generic native shell |
| create | `internal/repairs/kinds_test.go` (name coined here): conformance and parity tests |
| create | `internal/repairs/rcreporter.go` (name coined here): the in-package `registry.Reporter` over `*tasks.RC`/`*tasks.ItemRC` that `Fixer.Plan`/`Replan` need until F5-18 re-signs them |
| create | one `changelog.d/` fragment |

Do **not** modify any fixer (`internal/plugins/maintenance/*_fixer.go`), `internal/plugins/maintenance/plugin.go`,
`repairs_ops.go`, the existing `internal/repairs` files' exported API, `internal/server`, `internal/opscatalog`
or `main.go`. Because the shell is generic over `repairs.Fixer`, no file of the T6 siblings (F5-06 … F5-12)
in `internal/plugins/maintenance` is touched.

## Code contract

```go
// internal/repairs/kinds.go (names coined here, except the tasks API from 05-tasks §5T.3)

// KindPrefix + Fixer.ID() is the native kind (05-tasks §5T.8.2 T3 row).
const KindPrefix = "books.repairs."

// KindDeps is what the native shell needs that HEAD's repairs.plan/apply defs get from the
// maintenance plugin: the same values as PlanDeps/ApplyDeps (engine.go), minus the v2 reporter.
type KindDeps struct { /* the fields of PlanDeps and ApplyDeps that are not engine plumbing */ }

// Kinds returns one tasks.Fixer definition per fixer, in the order given. Nothing calls it in
// production; F5-18 passes plugin.Repairs()'s fixers.
func Kinds(fixers []Fixer, deps KindDeps) []tasks.Definition
```

Each kind is `tasks.Fixer[json.RawMessage, Row]` (`C` = HEAD's `repairs.Row`, so the planned row's bytes are
HEAD's) with:

| `FixerSpec` field | Built from HEAD | Source |
|---|---|---|
| `Candidates(rc, p)` | `RunPlan`'s candidate pass: `f.Plan(ctx, p, rep)` with HEAD's normalisation and flag filtering, as a `Source[Row]` in plan order (`Snapshot` order) | engine.go `RunPlan` |
| `Evaluate(ic, p, c)` | at plan (`ModePreview`): `c` itself mapped to `tasks.Row` (ID = `RowID`, `Subjects` = one `tasks.ResID("books", "books", id)` per `BookIDs` entry, payload = `c`'s JSON); at apply (`ModeLive`): `f.Replan(ctx, p, c, rep)`, and a result different from the plan row is `tasks.ErrChangedSincePlan` (HEAD's "changed" outcome, `changedWhy`) | engine.go `applyOne`, `changedWhy` |
| `Load(rc, p, rowID)` | decode the planned row from the plan run's stored row payload; must decode **HEAD's** `opv2:row:` payload bytes unchanged, because M4 imports them verbatim into `c/tasks/row/` (05-tasks §5T.8.3) | `LoadPlan`, `database.OpRowStore` |
| `Apply(ic, w, p, row)` | `f.Apply(ctx, <repairs.Writer bound to this run>, fresh)`; the `repairs.Writer` keeps HEAD's fence, lease and journal behaviour, its fence check wired to `w` (a fenced `tasks.Writer` refuses, so the repairs writer refuses: `tasks.ErrFenced`); a successful row calls `w.MarkRowApplied(ctx, row.ID)` | engine.go `applyOne`; 05-tasks §5T.3.5 |
| `Guards` | HEAD's framework guards (`guards.go`), run over every subject of the row, as `RunApply` runs them | guards.go |
| `AfterApply(rc, applied)` | `runAfterApply` | engine.go |
| `Parallelism` | `tasks.Fixed(defaultApplyConcurrency, "<HEAD's comment on defaultApplyConcurrency>")`, or `ApplyDeps.Concurrency` when HEAD's wiring sets it (rule: HEAD's count 1:1, 05-tasks §5T.8.2) | engine.go `defaultApplyConcurrency = 4` |
| `PartitionBy` | HEAD's `partitionRows` rule (no two workers write the same book); `Row.Subjects` also act as `ExclusiveRefs` at apply (05-tasks §5T.3.1) | engine.go `partitionRows` |
| `Common.Approval` | `&tasks.Approval{PlanRequired: true}` (`MaxPlanAge` 0 = 7 days, 05-tasks §5T.3.7) unless HEAD's apply enforces another age (read `RunApply`); a different value is copied | 05-tasks §5T.3.7 |
| `Common.Perms` | `Start` = the atom-table requirement of the fixer's `PermissionScoped.RequiredPermissions()`, else of `DefaultPermission` (`library.edit_metadata` → `books:edit`); `Approve` = the requirement of HEAD's `repairs.apply` def's `Permissions` (read `repairs_ops.go`) | 05-tasks §5T.3.7; 05-rbac §6 |
| `Common.Effects` | `tasks.Writes(<repairs.apply's Writes mapped: tasks.Res("books", "series")>, tasks.Res("books", "books"))`; HEAD's apply declares only `ResSeries` because its write set could not vary per fixer (`repairs_ops.go` comment), while every fixer writes books rows | 05-tasks §5T.3.4 |
| `Common.Resume`, `Timeout`, `Priority`, `ConcurrencyKey` | from `repairs.apply`'s def: `ResumeRestart` → `tasks.ResumeContinue`; `Timeout` 6 h; `PriorityNormal`; key `repairs.apply` → `tasks.Key("books.repairs.apply")` (one key for all fixers keeps HEAD's "one apply at a time") | repairs_ops.go |
| `Common.Title`, `Help` | `f.Title()`, `f.Description()` | fixer.go |

The plan half (HEAD's `repairs.plan`: `ResumeDrop`, `PriorityLow`, 2 h, key `repairs.plan`) is the same kind
run in `ModePreview` (map rule 2). Where the plan and apply values differ (resume, timeout, priority, key),
the native kind takes the **apply** value and the PR description lists the plan values that do not carry, as
05-tasks gives one `Common` per kind.

`rcreporter.go`: a `registry.Reporter` over `rc`/`ic` (progress, log, `IsCanceled` from the context,
`Checkpoint` → `rc.Checkpoint` with HEAD's `ApplyCheckpoint` struct) so `Fixer.Plan`/`Replan` can be called
unchanged. It is books code that disappears when F5-18 removes the v2 reporter from `repairs.Fixer`.

## Invariants (each proved by a test below)

1. **Plan parity.** For every fixer, the native kind's plan (Candidates + Evaluate in Preview) yields exactly
   the rows HEAD's `RunPlan` yields on the same store and params: same IDs, order, payload bytes and
   per-class tallies.
2. **Marks.** A row whose mark exists is never applied again (resume after a crash mid-apply applies only
   unmarked rows); a row's mark is written only after its apply succeeded (05-tasks §5T.8.3,
   `pebble_store_ops_v3_rowmarks.go` rule).
3. **Re-evaluation.** A row whose `Replan` differs from its planned row is not applied and is reported as
   changed (`ErrChangedSincePlan`), never silently re-planned.
4. **Guards and exclusivity.** Every guard runs over all of a row's subjects; no two concurrent apply workers
   hold rows sharing a book.
5. **Approval.** A Live start without a `PlanRef` naming a Preview run of the same kind is refused; approval
   needs `Perms.Approve` and a verified method (the executor enforces it; the test registers through
   `coretest` and asserts the refusal).
6. **Stored-row compatibility.** `Load` decodes a payload written by HEAD's `database.OpRowStore` for a
   `repairs.plan` run byte-for-byte (fixture: the bytes HEAD's own `planstore_test.go` writes).

## TDD: tests to write first (names coined here, in `kinds_test.go`)

- `TestRepairsKinds_Count`: one kind per registered fixer (25 at `f7ac5b11e`), names `books.repairs.<ID>`, all distinct, all accepted by `tasks.ParseKind`.
- `TestRepairsKinds_Conformance`: `taskstest.Conformance(t, repairs.Kinds(fixers, deps)...)` (the Fixer
  conformance suite of 05-tasks §5T.8.2's T3 gate).
- `TestRepairsKinds_PlanRowsMatchRunPlan` (invariant 1), table over every fixer on the fixtures their
  existing tests use.
- `TestRepairsKinds_ApplySkipsMarkedRows` (invariant 2: crash after row k's mark, resume, rows ≤ k untouched).
- `TestRepairsKinds_ChangedSincePlanNotApplied` (invariant 3).
- `TestRepairsKinds_GuardsAndPartitions` (invariant 4, `-race`).
- `TestRepairsKinds_ApplyNeedsPlanRef` (invariant 5).
- `TestRepairsKinds_LoadsHEADRowBytes` (invariant 6).

## Steps

1. Base checks; STOP on a mismatch. Read `engine.go` (`RunPlan`, `RunApply`, `applyOne`, `runAfterApply`,
   `partitionRows`) and `repairs_ops.go` end to end before writing.
2. Write the tests; then `rcreporter.go`; then `kinds.go`, reusing `engine.go`'s unexported helpers (same
   package) rather than copying them.
3. If an invariant cannot hold without changing a fixer or an exported `repairs` signature, STOP and report
   which fixer and why: this PR does not edit fixers.
4. PR description: one `fixer ID ==> books.repairs.<ID>` line per fixer; the plan-only `Common` values that
   do not carry; `converted: <n>/<n>` with n the registry's count.

## Acceptance (commands and expected output)

```bash
go build ./...                                                                   # exit 0
go vet ./internal/repairs/                                                       # exit 0
go test ./internal/repairs/ ./internal/plugins/maintenance/ -count=1 -short -race   # ok x2 (HEAD's fixer tests unchanged)
go test ./internal/repairs/ -run 'TestRepairsKinds_' -count=1 -race -v | grep -c '^--- PASS'   # 8
git diff --name-only origin/main | grep -v '^changelog.d/' | sort                 # exactly the three new internal/repairs files
git grep -n 'mediacore/tasks"' -- main.go internal/opscatalog/ internal/plugins/ | wc -l   # 0
make ci                                                                          # exit 0
```

## STOP conditions

- The registry's fixer count is not 25 and no fixer was added or removed since `f7ac5b11e`.
- `core/tasks` lacks `FixerSpec.Load`, `Writer.MarkRowApplied` or `ErrChangedSincePlan`.
- An invariant needs a fixer edit (step 3).
- tasklint (F5-17) flags the unchanged fixer bodies (`writer-bypass`): report; do not add allow directives.

## Out of scope

Registration and deleting `repairs.plan`/`repairs.apply` (F5-18); re-signing `repairs.Fixer` without the v2
reporter (F5-18, when `internal/operations/registry` goes); M4's import of rows and marks (F5-16); the
`ops.abandoned_calls` `repairs_resolver` source (F5-18, 05-tasks §5T.8.1 `internal/opsmetrics` row).

## Idempotency / rollback

If `internal/repairs/kinds.go` exists and its tests pass, run the acceptance instead. Rollback = revert; HEAD's
`repairs.plan`/`repairs.apply` path is untouched.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task.
- File headers on every created file (fresh `guid`); `changelog.d/` fragment without a header.
- Conventional commit `refactor(repairs): native books.repairs.* Fixer kinds over every repairs fixer (dark)`, body
  ending `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer.
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- CLAUDE.md concurrency: the apply pool keeps HEAD's bounded count and disjoint partitions; the shell adds
  no unbounded fan-out.
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- **Count:** HEAD registers 25 fixers through 24 `repairsReg.Register` call sites (one call is inside a
  loop over two fixers), at `f7ac5b11e` and at `748fd787a`; 05-tasks §5T.2 and §5T.8.2 say "24 fixers". The
  brief uses the registry's count; the spec's figure should read 25 (or "24 call sites").

- The inventory lists only `internal/repairs/`. That holds because this brief converts the fixers through one
  generic shell over `repairs.Fixer` rather than editing the `*_fixer.go` files in
  `internal/plugins/maintenance`; if the lead prefers per-fixer native code, the row must add those files and
  the F5-06 … F5-12 file groups must keep excluding them (they do).
- 05-tasks gives one `Common` per kind, but HEAD's plan and apply definitions differ in resume policy,
  timeout, priority and key; the brief takes the apply values and lists the rest. The review loop should
  confirm (the M4 state table imports plan runs as `ModePreview` runs of the same kind).
- `repairs.apply`'s `Writes` is `ResSeries` only at HEAD; the native kinds declare `books/books` too because
  every fixer writes books rows. This widens the dispatch gate once F5-18 registers them.
