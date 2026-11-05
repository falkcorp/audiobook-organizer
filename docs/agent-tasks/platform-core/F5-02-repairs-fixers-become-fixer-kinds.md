<!-- file: docs/agent-tasks/platform-core/F5-02-repairs-fixers-become-fixer-kinds.md -->
<!-- version: 1.1.0 -->
<!-- guid: bbc45161-5107-41fe-8e3e-77b95ed3dadb -->
<!-- last-edited: 2026-11-05 -->

# F5-02 — Conversion group T3: the 24 repairs fixers become `tasks.Fixer` kinds `books.repairs.<fixer-id>`, dark

| Field | Value |
|---|---|
| ID | F5-02 |
| Release / flip | R2, tasks flip F5 (dark conversion PR) |
| Wave | W29 |
| Tier | **T3** (Opus / high): Fixer semantics (plan rows, re-evaluation at apply, applied-row marks, guards) must stay exact while 25 fixers reroute their stores through repairs' shell (05-tasks conversion rule 3), and M4 imports HEAD's stored rows and marks into the shape this PR defines |
| Depends on | F2-05 (transitively F2-01..F2-04), F2-16 (`Registrar.Tasks`, `coretest`'s task cells), F2-17 (`tasklint` and this row's exemption entry file) |
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

Under 05-tasks §5T.8.2 conversion rule 3 each fixer's body reaches its writer and its stores **only through what
repairs' shell hands it**, so this PR also edits the 25 fixer files in `internal/plugins/maintenance` (the T6 groups
exclude them) to take their stores from the shell instead of capturing them (rule 3 below), and deletes its own
`tasklint` exemption entry file, `internal/books/testdata/tasklint_exempt.d/F5-02.txt`, in the same PR.

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
5. 05-rbac §6 (atom table). Plan §(l) items 4 (narrow writer interface in the converted package) and 5 (the
   exemption directory).
6. 05-tasks §5T.8.2 conversion rules 1–4 and §5T.8.4 (`tasklint`: `writer-bypass`, `store-capture`, the
   dark-conversion ratchet).

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
ls internal/plugins/maintenance/*_fixer.go | wc -l                                 # 25 (the fixer files this row edits)
grep -c 'f\.p\.deps\.' internal/plugins/maintenance/*_fixer.go | awk -F: '$2>0' | wc -l   # record: the fixers that reach the plugin's deps (rule 3)
# F2-16 / F2-17 outputs (0 hits on any = STOP)
test -f core/coretest/tasks.go && echo present                                     # present (F2-16: coretest's task cells)
test -d core/archtest/tasklint && test -f internal/books/tasklint_test.go && echo tasklint   # tasklint (F2-17)
test -f internal/books/testdata/tasklint_exempt.d/F5-02.txt && echo entry          # entry (F2-17 created it), else STOP
grep -v '^#' internal/books/testdata/tasklint_exempt.d/F5-02.txt | grep -v '^$' | grep -v '^internal/repairs/' | grep -v '^internal/plugins/maintenance/[a-z0-9_]*_fixer\.go$' | wc -l   # 0 (the entry names only this row's files)
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
| create | `internal/repairs/stores.go` (name coined here): `FixerStores`, `WithStores`, `StoresFrom` (rule 3) |
| modify | `internal/repairs/engine.go` (HEAD's `RunPlan`/`RunApply` put HEAD's stores on ctx, rule 3) and, only where rule 3 needs it, other `internal/repairs` files (no exported signature changes) |
| modify | the 25 fixer files `internal/plugins/maintenance/*_fixer.go` listed in the row (rule 3 only) |
| delete | `internal/books/testdata/tasklint_exempt.d/F5-02.txt` (this row's exemption entry file) |
| create | one `changelog.d/` fragment |

Do **not** modify `internal/plugins/maintenance/plugin.go`, `repairs_ops.go`, any non-fixer file of
`internal/plugins/maintenance` (the T6 siblings F5-06 … F5-12 own them in the same wave), any other file under
`internal/books/testdata/tasklint_exempt.d/`, the exported API of `internal/repairs` or of `repairs.Fixer`,
`internal/server`, `internal/opscatalog` or `main.go`.

## Code contract

```go
// internal/repairs/kinds.go (names coined here, except the tasks API from 05-tasks §5T.3)

// KindPrefix + Fixer.ID() is the native kind (05-tasks §5T.8.2 T3 row).
const KindPrefix = "books.repairs."

// KindDeps is what the native shell needs that HEAD's repairs.plan/apply defs get from the
// maintenance plugin: the same values as PlanDeps/ApplyDeps (engine.go), minus the v2 reporter.
type KindDeps struct {
	/* the fields of PlanDeps and ApplyDeps that are not engine plumbing */
	Stores FixerStores // rule 3: the stores the native shell hands every fixer call
}

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

**Rule 3, stores and writer only through the shell (05-tasks §5T.8.2 conversion rule 3; §5T.8.4).** At HEAD the
fixers capture their stores at construction (`f.p.deps.OpsStore()`, `f.p.deps.UserReadStateStore()` and siblings
on the maintenance plugin), which `tasklint`'s `store-capture` rejects once `F5-02.txt` is gone. So:

1. `internal/repairs/stores.go` declares `FixerStores` (name coined here): an interface with exactly the
   store-returning methods the 25 fixers call on `f.p.deps` (same names and result types; one method per store),
   and `WithStores(ctx, FixerStores) context.Context` / `StoresFrom(ctx) (FixerStores, bool)` (names coined here).
2. Both shells hand it: the native shell puts `KindDeps.Stores` on the ctx of every `Plan`, `Replan` and `Apply`
   call; HEAD's `RunPlan`/`RunApply` put an adapter over the maintenance plugin's deps on it (the adapter of the
   same interface over HEAD's stores; it returns exactly what `f.p.deps` returns today).
3. Each fixer replaces every `f.p.deps.<Store>()` call with `StoresFrom(ctx)` (a missing value is an error, never a
   fallback to `f.p.deps`). A fixer helper without a ctx gets a new variant taking the stores (name coined per
   helper: `<helper>Via`); the existing helper is not re-signed. Non-store deps (`EnqueueOp` and the like) are
   not stores and stay.
4. Writes: a fixer writes a module entity only through the `*repairs.Writer` its `Apply` is handed. In the native
   path every `repairs.Writer` write runs inside the run's `tasks.Writer` (`w.Do`, 05-tasks §5T.3.5) with a
   `Resource` inside the kind's `Effects.Writes`; in HEAD's path it is unchanged. A fixer that writes outside the
   `*repairs.Writer` is rerouted through the existing `repairs.Writer` method for that write; if none exists, STOP.
5. Nothing else in a fixer changes: plan rows, replan, guards and apply logic stay byte-for-byte; HEAD's fixer
   tests stay green unchanged.

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
7. **Stores and writer through the shell (rule 3).** No fixer reaches a store except through `StoresFrom(ctx)` or
   a module entity except through the `*repairs.Writer` it is handed; `tasklint` passes over the 25 fixer files
   and `internal/repairs` with `F5-02.txt` deleted.

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
- `TestRepairsKinds_FixersUseShellStores` (invariant 7): every fixer's native plan and apply on a fake
  `KindDeps.Stores` reads only from the fake (the plugin's deps are a value whose store methods fail the test),
  and HEAD's `RunPlan` on the same fixture still yields HEAD's rows.

## Steps

1. Base checks; STOP on a mismatch. Read `engine.go` (`RunPlan`, `RunApply`, `applyOne`, `runAfterApply`,
   `partitionRows`) and `repairs_ops.go` end to end before writing.
2. Write the tests; then `rcreporter.go`; then `kinds.go`, reusing `engine.go`'s unexported helpers (same
   package) rather than copying them.
3. `stores.go`, then rule 3 in `engine.go` and in each fixer file, one fixer at a time, running that fixer's
   existing tests after each.
4. If an invariant cannot hold without changing a fixer's behaviour or an exported `repairs` signature, STOP and
   report which fixer and why: this PR changes only where a fixer reaches its stores.
5. `git rm internal/books/testdata/tasklint_exempt.d/F5-02.txt`, then run the acceptance block's `tasklint` line.
6. PR description: one `fixer ID ==> books.repairs.<ID>` line per fixer; the plan-only `Common` values that
   do not carry; the fixers rule 3 edited, with the store calls rerouted; the deleted entry file;
   `converted: <n>/<n>` with n the registry's count.

## Acceptance (commands and expected output)

```bash
go build ./...                                                                   # exit 0
go vet ./internal/repairs/                                                       # exit 0
go test ./internal/repairs/ ./internal/plugins/maintenance/ -count=1 -short -race   # ok x2 (HEAD's fixer tests unchanged)
go test ./internal/repairs/ -run 'TestRepairsKinds_' -count=1 -race -v | grep -c '^--- PASS'   # 9
git diff --name-only origin/main | grep -v '^changelog.d/' | grep -v '^internal/repairs/' | grep -v '^internal/plugins/maintenance/[a-z0-9_]*_fixer\.go$' | grep -v '^internal/books/testdata/tasklint_exempt.d/F5-02.txt$' | wc -l   # 0
grep -c 'f\.p\.deps\.\(OpsStore\|UserReadStateStore\)()' internal/plugins/maintenance/*_fixer.go | awk -F: '{s+=$2} END {print s}'   # 0 (rule 3; extend the pattern to every store method FixerStores declares)
test ! -e internal/books/testdata/tasklint_exempt.d/F5-02.txt && echo deleted       # deleted
git diff --name-only --diff-filter=D origin/main -- internal/books/testdata/tasklint_exempt.d/   # internal/books/testdata/tasklint_exempt.d/F5-02.txt (only)
git diff --name-only --diff-filter=AMR origin/main -- internal/books/testdata/tasklint_exempt.d/ | wc -l   # 0
go test ./internal/books/ -count=1 -run "$(grep -o '^func Test[A-Za-z0-9_]*' internal/books/tasklint_test.go | sed 's/^func //' | paste -sd'|')"   # ok (tasklint over the union; this row's files no longer exempt)
git grep -n 'mediacore/tasks"' -- main.go internal/opscatalog/ internal/plugins/ | wc -l   # 0
make ci                                                                          # exit 0
```

## STOP conditions

- The registry's fixer count is not 25 and no fixer was added or removed since `f7ac5b11e`.
- `core/tasks` lacks `FixerSpec.Load`, `Writer.MarkRowApplied` or `ErrChangedSincePlan`.
- An invariant needs a fixer behaviour change (step 4).
- A fixer writes a module entity outside `*repairs.Writer` and no `repairs.Writer` method covers that write.
- `tasklint` (F2-17) still reports `writer-bypass` or `store-capture` in this row's files after rule 3, or reports
  one in a file this row does not own: report it; never an allow directive or a line added to any entry file.

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
- CLAUDE.md concurrency: the apply pool keeps HEAD's bounded count and disjoint partitions (the "must not
  double-merge" rule: `PartitionBy` keeps any two workers off one book); the shell adds no unbounded fan-out.
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- **Count:** HEAD registers 25 fixers through 24 `repairsReg.Register` call sites (one call is inside a
  loop over two fixers), at `f7ac5b11e` and at `748fd787a`; 05-tasks §5T.2 and §5T.8.2 say "24 fixers". The
  brief uses the registry's count; the spec's figure should read 25 (or "24 call sites").
