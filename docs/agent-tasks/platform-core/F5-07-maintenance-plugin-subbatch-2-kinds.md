<!-- file: docs/agent-tasks/platform-core/F5-07-maintenance-plugin-subbatch-2-kinds.md -->
<!-- version: 1.0.0 -->
<!-- guid: 03bc2cfa-33d7-4275-bb2a-96baefde7560 -->
<!-- last-edited: 2026-11-05 -->

# F5-07 — Conversion group T6, sub-batch 2 of 7: the maintenance-plugin definitions of file group 2 become books kinds, dark

| Field | Value |
|---|---|
| ID | F5-07 |
| Release / flip | R2, tasks flip F5 (dark conversion PR) |
| Wave | W26 |
| Tier | **T1** (Sonnet / medium): up to 15 definitions of one file group, converted under fixed rules with conformance tests |
| Depends on | F2-05 (`taskhttp`, `hello.count`, `coretest` on tasks; transitively F2-01..F2-04) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-07-maint-t6-g2
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

Give each of the **13** HEAD v2 definitions listed below a native `core/tasks` definition (kind
`books.<HEAD ID>`) that lands **dark**: it compiles, passes `taskstest.Conformance`, and is registered
nowhere in production. HEAD's v2 definitions keep running unchanged until the tasks flip F5-18 registers
the native kinds and deletes HEAD's engine (05-tasks §5T.8.2: "Each group is one or more PRs that land
dark"; T6 row: "the maintenance plugin's defs not in T3 or T4, in sub-batches of ≤ 15 by file group").

This is one of seven sibling PRs (F5-06 … F5-12) that run **in parallel in the same wave** over disjoint
file groups of `internal/plugins/maintenance` (CLAUDE.md "Parallel Subagent Coordination"; a
`/parallel-sweep` candidate). You may touch **only** the files in the Files table. Anything else in the
package, `plugin.go` above all, belongs to a sibling or to the flip.

## Spec to read first

1. 05-tasks §5T.8.2 (the T6 row, the opening paragraph's three body rules, the kind-rename map rules 1–6).
2. 05-tasks §5T.3.1 (`Common`, `TaskSpec`, `Task`), §5T.3.2 (`Parallelism`, `RunItems`), §5T.3.3 (`RC`),
   §5T.3.4 (`Effects`, `Key`), §5T.3.6 (`ResumePolicy`, checkpoints), §5T.3.7 (permissions, mode contract).
3. 05-rbac §6 (the atom table, "Route requirement" column).
4. CLAUDE.md "Concurrency" and "Parallel Subagent Coordination".

## Your file group (computed at `f7ac5b11e`; regenerate at your base)

The seven groups partition the **98** T6 definitions of 05-tasks §5T.8.2 (every `sdk.OperationDef{`
literal in `internal/plugins/maintenance/*.go` except `plugin.go`'s list literal, `repairs_ops.go`'s two
`repairs.plan`/`repairs.apply` defs (T3 removes them), and the four T4 defs in `db.go`, `db_census_exact.go`,
`throttle_reset.go` and `cleanup.go`'s `maintenance.purge-old-logs`) into contiguous alphabetical runs of
whole files, each ≤ 15 definitions. The 24 repairs fixers (`*_fixer.go` and their helpers) are F5-02's and
appear in no group.

| File | `sdk.OperationDef{` literals | HEAD def IDs to convert |
|---|---|---|
| `internal/plugins/maintenance/backfill.go` | 4 | `maintenance.external-id-backfill`, `maintenance.movement-atom-cleanup`, `maintenance.malformed-m4b-remux`, `maintenance.malformed-m4b-transcode` |
| `internal/plugins/maintenance/book_atpath_index.go` | 2 | `maintenance.book-atpath-index-verify`, `maintenance.book-atpath-index-backfill` |
| `internal/plugins/maintenance/book_hash_index_repair.go` | 1 | `maintenance.book-hash-index-repair` |
| `internal/plugins/maintenance/book_shape_report.go` | 1 | `maintenance.book-shape-report` |
| `internal/plugins/maintenance/bookfile_acoustid_index_repair.go` | 1 | `maintenance.bookfile-acoustid-index-repair` |
| `internal/plugins/maintenance/booksig_recovery_audit.go` | 1 | `maintenance.booksig-recovery-audit` |
| `internal/plugins/maintenance/booksig_sidecar_migrate.go` | 1 | `maintenance.booksig-sidecar-migrate` |
| `internal/plugins/maintenance/build_folder_book_files.go` | 1 | `maintenance.build-folder-book-files` |
| `internal/plugins/maintenance/chapters_backfill.go` | 1 | `maintenance.chapters-backfill` |

**Group total: 13 definitions to convert** (13 literals in these files).

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
test -f core/coretest/tasks.go && echo present                                            # present (F2-05)
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
cd internal/plugins/maintenance
# Your group, at your base: the literal count must equal 13; any other number = STOP (the partition moved)
grep -c 'sdk.OperationDef{' backfill.go book_atpath_index.go book_hash_index_repair.go book_shape_report.go bookfile_acoustid_index_repair.go booksig_recovery_audit.go booksig_sidecar_migrate.go build_folder_book_files.go chapters_backfill.go | awk -F: '{s+=$2} END {print s}'     # 13
# Each file exists (a missing file = STOP)
ls backfill.go book_atpath_index.go book_hash_index_repair.go book_shape_report.go bookfile_acoustid_index_repair.go booksig_recovery_audit.go booksig_sidecar_migrate.go build_folder_book_files.go chapters_backfill.go | wc -l                                                            # 9
# The whole T6 set is still 98 (if not, STOP: the coordinator recomputes all seven groups)
for f in $(ls *.go | grep -v _test.go); do n=$(grep -c 'sdk.OperationDef{' $f); [ "$n" -gt 0 ] && echo "$f $n"; done \
  | grep -v '^plugin.go \|^repairs_ops.go \|^db.go \|^db_census_exact.go \|^throttle_reset.go ' \
  | awk '{s+=$2} END {print s-1}'                                            # 98 (cleanup.go's purge-old-logs subtracted)
# None of your files is already converted
grep -ln 'mediacore/tasks"' backfill.go book_atpath_index.go book_hash_index_repair.go book_shape_report.go bookfile_acoustid_index_repair.go booksig_recovery_audit.go booksig_sidecar_migrate.go build_folder_book_files.go chapters_backfill.go | wc -l                                    # 0 (else see Idempotency)
cd -
```

If the 98 total or your group's count differs at your base, STOP and report: the lead recomputes the seven
groups with the same rule (contiguous alphabetical runs of whole files, ≤ 15 definitions, pairwise
disjoint) before any sibling starts.

## The conversion rules (every definition this PR converts)

05-tasks §5T.8.2 fixes three body rules and the naming rule; the rest of this section is this plan's
mechanical mapping of HEAD's fields onto the spec's `tasks.Common` (05-tasks §5T.3.1), with the source of
each line. Apply it the same way to every definition; never improvise a field.

**R1, dark (05-tasks §5T.8.2).** The converted definition compiles, is exercised by
`taskstest.Conformance`, and is **not registered anywhere in production**: HEAD's v2 definition keeps its ID,
its registration and its behaviour, and the production build still runs HEAD's engine. Nothing in `main.go`,
`internal/server` wiring, `internal/opscatalog` or any `Register`/`RegisterOp` call changes in this PR. The
tasks flip F5-18 registers the native kinds and deletes HEAD's definitions.

**R2, shared core, two shells (plan rule, name coined here).** So that one body serves both engines during
the dark window and HEAD's behaviour is the oracle:

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
| `Plugin`, `FormerIDs`, `Cancellable`, `ParamsSchema`, `SummarizeQueued`, `Liveness`, `MinCheckpointInterval`, `ProgressFlushInterval`, `Synchronous`, `Isolate`, `RunsAs` | not carried; list each non-zero value per kind in the PR description under "Dropped HEAD fields". `Cancellable` is never read at HEAD (05-tasks §5T.2); `Isolate: true` or `RunsAs: ActorSystem` has 0 hits at `f7ac5b11e`: a hit at your base is a STOP | 05-tasks §5T.2, §5T.3.6 |
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

**R7, effects (plan rule, coined here; 05-tasks gives no rule for HEAD's empty `Writes`).** HEAD's write
set is table-level and optional ("Empty Writes = declares nothing = the gate skips this op",
`internal/operations/registry/types.go`); core requires `Effects` (05-tasks §5T.3.1). Map with
`res(s) = tasks.Res("books", s)` for HEAD's resource strings (`books`, `book_files`, `authors`, `series`,
`review_items`, `embeddings`, `operations`, `catalog`, `fingerprint_index`):

| HEAD | Native `Effects` | PR description list |
|---|---|---|
| `Writes` non-empty | `tasks.Writes(res(w)...)`, plus `.AlsoReads(res(r)...)` for `Reads` | — |
| `Writes` empty, `Preview` non-zero | `tasks.Writes(res("books"))` (+ `AlsoReads`) | "Write set widened from HEAD's undeclared" |
| `Writes` empty, `Preview` zero, no `library.write`/`files.write`/`db.migrate` capability | `tasks.ReadOnly(res(r)...)` | "Declared ReadOnly" |
| `Writes` empty, `Preview` zero, a write capability | `tasks.Writes(res("books"))`, and the native shell returns an error when `rc.Mode() != tasks.ModeLive` ("<kind>: preview not supported; start with mode live"), because HEAD requires an explicit live mode for such an op (`internal/operations/registry/preview.go`, `PreviewSpec` comment) | "Live-only writer" |

**R9, the schedule hook (plan rule, coined here).** `tasks.Definition` is opaque and `Common.Schedule` is set
at construction (05-tasks §5T.3.1, §5T.3.8), while the 38 schedules are declared later, in
`internal/books/schedules.go` (F5-14), and attached by the flip. So every list function this PR adds takes
`sched func(tasks.Kind) *tasks.Schedule` and sets `Common.Schedule: sched(<kind>)` on every kind it builds
(a nil `sched` means no schedule; tests pass nil). No conversion declares a schedule itself.

**R8, tests (05-tasks §5T.8.2 "Gate" column; §5T.9 last row).** One new test file per PR (name in the
Files table) that builds every native kind of the PR and runs `taskstest.Conformance(t, defs...)` over
them, plus one table test that asserts each kind's name, `Title`, `Resume`, parallelism `String()` and
`Effects` against the HEAD definition it came from (built from HEAD's `xxxDef()` in the same test, so a
drift fails). HEAD's existing tests stay unchanged and green.

## Files

| Action | Path |
|---|---|
| modify | each file of your group (shared core + v2 shell + native shell per definition, rule R2) |
| create | `internal/plugins/maintenance/kinds_t6_g2.go` (name coined here): `func (p *Plugin) t6g2Kinds(sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition` returning every native kind of this group, in file order, each with `Schedule: sched(kind)` (rule R9). Nothing calls it in production; F5-18 does |
| create | `internal/plugins/maintenance/kinds_t6_g2_test.go` (name coined here): rule R8's conformance and field-parity tests |
| create | one `changelog.d/` fragment |

Do **not** modify `plugin.go`, any `*_fixer.go`, `repairs_ops.go`, `db.go`, `db_census_exact.go`,
`throttle_reset.go`, any other group's file, `internal/server`, `internal/opscatalog` or `main.go`.
Package-level helper types shared across groups are forbidden (they would collide with siblings): every new
identifier carries this group's file or `t6g2` prefix.

## Code contract

```go
// in each converted file, per definition (names coined here except the tasks API)
const <name>Kind tasks.Kind = "books.<HEAD ID>"

type <runFunc>Env struct {                 // only the fields the body uses (rule R2)
	Live           bool
	Log            func(level slog.Level, msg string)
	Progress       func(done, total int, msg string)
	SaveCheckpoint func(v any) error         // only if HEAD checkpoints
	LoadCheckpoint func(v any) (bool, error) // only if HEAD restores
	Items          func(n int, fn func(ctx context.Context, i int) error) error // only if HEAD loops via RunItems
}

func (p *Plugin) <runFunc>Core(ctx context.Context, params <Params>, env <runFunc>Env) (<Result>, error)

func (p *Plugin) <name>Kind(sched *tasks.Schedule) tasks.Definition {   // Common.Schedule = sched (rule R9)
	return tasks.Task(<name>Kind, tasks.TaskSpec[<NativeParams>, <Result>]{
		Common: tasks.Common{ /* rules R5–R7 */ },
		Run: func(rc *tasks.RC, in <NativeParams>) (<Result>, error) { /* rule R2 item 3 */ },
	})
}
```

`<Result>` is HEAD's result struct when the def writes one, else a small struct with the counts HEAD logs
(`struct{}` when there is nothing). The `core/tasks` API (`Task`, `TaskSpec`, `Common`, `RC`, `Res`,
`Writes`, `ReadOnly`, `Fixed`, `Sequential`, `CPU`, `RunItems`, `Key`) is copied from 05-tasks §5T.3; do not
wrap or rename it.

## TDD: tests to write first

In `kinds_t6_g2_test.go` (both names coined here):

- `TestT6G2Kinds_Conformance`: `taskstest.Conformance(t, (&Plugin{}).t6g2Kinds(nil)...)` (05-tasks §5T.9
  last row; build the `Plugin` the way the package's existing tests build one).
- `TestT6G2Kinds_MatchHEADDefs`: a table over every converted definition: the native kind equals
  `"books." + HEAD's ID`, `Title` equals HEAD's `DisplayName`, the resume policy maps by rule R5, the
  parallelism string is `fixed(n): …` / `sequential: …` / `cpu` per rule R4, and the `Effects` class is
  rule R7's row. Build HEAD's def with its existing `xxxDef()` method in the same test.

Write them first, watch them fail to compile, then convert.

## Steps

1. Run the base checks; STOP on any mismatch.
2. For each definition, in file order: apply rule R2 (core + v2 shell, HEAD tests still green), then add the
   native shell with rules R3–R7. Run `go test ./internal/plugins/maintenance/ -run '<that def's existing
   tests>' -count=1` after each file.
3. Create `kinds_t6_g2.go` and the two tests.
4. Fill the PR description: the `old ID ==> books.<ID>` list, and rule R5/R7's lists ("Dropped HEAD
   fields", "Write set widened from HEAD's undeclared", "Declared ReadOnly", "Live-only writer"), each with
   counts. Exact counts: `converted: 13/13`.
5. Run the acceptance block; open the PR.

## Acceptance (commands and expected output)

```bash
go build ./...                                                                  # exit 0
go vet ./internal/plugins/maintenance/                                          # exit 0
go test ./internal/plugins/maintenance/ -count=1 -short                         # ok
go test ./internal/plugins/maintenance/ -run 'TestT6G2Kinds_' -count=1 -v | grep -c '^--- PASS'   # 2
grep -c 'books\.maintenance\.' internal/plugins/maintenance/kinds_t6_g2.go internal/plugins/maintenance/backfill.go internal/plugins/maintenance/book_atpath_index.go internal/plugins/maintenance/book_hash_index_repair.go internal/plugins/maintenance/book_shape_report.go internal/plugins/maintenance/bookfile_acoustid_index_repair.go internal/plugins/maintenance/booksig_recovery_audit.go internal/plugins/maintenance/booksig_sidecar_migrate.go internal/plugins/maintenance/build_folder_book_files.go internal/plugins/maintenance/chapters_backfill.go | awk -F: '{s+=$2} END {print (s>=13)}'   # 1
git diff --name-only origin/main | grep -v '^changelog.d/' | sort                 # exactly your group's files + the two new files
git diff --name-only origin/main | grep -c 'plugin.go\|_fixer.go\|repairs_ops.go\|^main.go\|internal/server/\|internal/opscatalog/'   # 0
git grep -n 'mediacore/tasks"' -- main.go internal/opscatalog/ | wc -l          # 0 (still dark)
make ci                                                                         # exit 0
```

`make ci` includes the oplint/tasklint gate. If `tasklint` (landed by F5-17) reports a finding inside a
native shell you wrote, fix the shell; if it reports `writer-bypass` or `store-in-deps` on the **shared core**
(HEAD's body, unchanged), STOP and report: whether converted bodies must move behind `tasks.Writer` in the
dark PRs is not settled by the spec (Planner notes).

## Out of scope

Registering anything (F5-18); schedules (F5-14); changing any definition's parallelism, write set or
permissions beyond the mapping rules (separate PRs, 05-tasks §5T.8.2); the 24 fixers (F5-02); the four T4
defs (F5-03); deleting HEAD's def (F5-18).

## Idempotency / rollback

If `kinds_t6_g2.go` exists and its tests pass, run the acceptance block instead of re-applying. Rollback =
revert the PR: HEAD's definitions are unchanged in behaviour (their tests prove it), and nothing in
production reads the native kinds.

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

Commit message: `refactor(maintenance): T6 group 2 definitions get native books kinds (dark)`.

## Planner notes

- **Effects for HEAD's undeclared write sets (rule R7) is a plan rule, not spec text.** 05-tasks requires
  `Effects` but gives no mapping for HEAD's empty `Writes`, which most maintenance definitions have. The rule
  keeps HEAD's preview semantics; its "widened" rows serialize those kinds against other `books/books`
  writers once registered (a dispatch change F5-18 ships). The review loop should confirm or replace it
  before W26 dispatches.
- **The shared-core / two-shells pattern (rule R2) is a plan rule.** The spec says the converted
  definitions run "in the parity suites against HEAD's behaviour, while the production build still runs
  HEAD's engine" but not how one body serves both; R2 avoids a second copy of each body during the dark
  window.
- **tasklint vs. dark conversions.** F5-17 lands tasklint (W24) before this wave; its `writer-bypass` and
  `store-in-deps` rules, re-rooted on `tasks`, would flag every converted body that writes through
  `internal/database` directly. 05-tasks §5T.8.2 lists only three body changes, so the brief STOPs rather
  than adding allow directives.
- **Group boundaries are this brief's computation at `f7ac5b11e`** (inventory placeholder `<file-group-2>`);
  the row's files should read: `internal/plugins/maintenance/backfill.go`, `internal/plugins/maintenance/book_atpath_index.go`, `internal/plugins/maintenance/book_hash_index_repair.go`, `internal/plugins/maintenance/book_shape_report.go`, `internal/plugins/maintenance/bookfile_acoustid_index_repair.go`, `internal/plugins/maintenance/booksig_recovery_audit.go`, `internal/plugins/maintenance/booksig_sidecar_migrate.go`, `internal/plugins/maintenance/build_folder_book_files.go`, `internal/plugins/maintenance/chapters_backfill.go`, plus
  the two new files.
