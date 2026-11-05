<!-- file: docs/agent-tasks/platform-core/F5-13-server-definitions-become-books-kinds.md -->
<!-- version: 1.1.3 -->
<!-- guid: 5ddc5a9e-9799-4f31-ba15-61c981edd016 -->
<!-- last-edited: 2026-11-05 -->

# F5-13 — Conversion group T7, server definitions: the `internal/server` `*_op*.go` definitions and `scheduler.metadata-upgrade` become books kinds, dark

| Field | Value |
|---|---|
| ID | F5-13 |
| Release / flip | R2, tasks flip F5 (dark conversion PR) |
| Wave | W31 |
| Tier | **T3** (Opus / high): 45 server definitions (44 server-package ones plus `scheduler.metadata-upgrade`), each shared body rerouted through its shell (05-tasks conversion rule 3), plus the new kind `books.library.scan-full` |
| Depends on | F2-05 (transitively F2-01..F2-04), F2-16 (`Registrar.Tasks`, `coretest`'s task cells), F2-17 (`tasklint` and this row's exemption entry file) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-13-server-kinds
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

Give each v2 definition registered from `internal/server`'s op files, and `scheduler.metadata-upgrade`
(`internal/scheduler/extra_ops.go`), a native `core/tasks` definition (kind `books.<HEAD ID>`) that lands
**dark** (05-tasks §5T.8.2, T7 row: "the server-package defs not in T4 (`internal/server/*_op*.go`) and
`scheduler.metadata-upgrade`"), and define the one books kind with no HEAD definition of its own,
`books.library.scan-full` (05-tasks §5T.8.2 kind-rename map rule 4 and the scheduler-name rule: HEAD's full sweep
becomes its own kind with its own schedule). HEAD's definitions keep running until the tasks flip F5-18. The 38
`TaskDefinition` schedule conversions of the same T7 row are F5-14's, not this PR's.

## Spec to read first

1. 05-tasks §5T.8.2 (T7 row; the three body changes; conversion rules 1–4; the kind-rename map rules 1, 4 and 5
   and the scheduler-name rule after them (`books.library.scan-full`); the T4 table, which takes
   `diagnostics.export` and `maintenance.window` out of this group), §5T.8.4 (`tasklint` and the ratchet).
2. 05-tasks §5T.8.1 (`internal/operations` row: `BulkMetadataFetchParams` → `internal/server`, its only user;
   the v1 checkpoint helpers replaced by `rc.Checkpoint` "in each caller's conversion PR"; the `childop` row:
   `internal/server/server_maintenance_deps.go` moves to `rc.Await`).
3. 05-tasks §5T.3.1–§5T.3.7; 05-rbac §6.

## The definitions (at `ac4368bdb`; regenerate at your base)

| File | HEAD def IDs to convert |
|---|---|
| `internal/server/ai_ops.go` | `ai.author-review`, `ai.author-merge-apply` |
| `internal/server/ai_scan_apply_op.go` | `ai.author-scan-apply` |
| `internal/server/aiscan_op.go` | `ai.author-scan` |
| `internal/server/apply_when_scanned_op.go` | `metadata.apply-when-scanned`, `library.organize-when-scanned` |
| `internal/server/author_reclassify_op.go` | `entities.author-reclassify` |
| `internal/server/batch_apply_op.go` | `metadata.batch-apply-cached` |
| `internal/server/batch_apply_refusal_reset_op.go` | `metadata.batch-apply-refusals-reset` |
| `internal/server/batch_save_op.go` | `metadata.batch-save` |
| `internal/server/bulk_apply_preview.go` | `metadata.bulk-apply-preview` |
| `internal/server/catalog_coverage_op.go` | `catalog.coverage-report` |
| `internal/server/catalog_harvest_op.go` | `catalog.harvest-authors` |
| `internal/server/collection_refresh_op.go` | `collections.refresh-relative-dynamic` |
| `internal/server/diagnostics_ai_ops.go` | `diagnostics.ai-analyze` |
| `internal/server/duplicates_ops.go` | `dedup.book-scan`, `dedup.book-merge`, `dedup.book-combine`, `dedup.author-scan`, `dedup.series-scan`, `dedup.series-dedup`, `dedup.series-prune`, `dedup.series-merge`, `dedup.series-normalize` |
| `internal/server/entities_ops.go` | `entities.author-merge`, `entities.resolve-production-author` |
| `internal/server/folder_autoscan_op.go` | `library.folder-auto-scan` |
| `internal/server/identification_advance_op.go` | `identification.advance` |
| `internal/server/itunes_ops.go` | `itunes.import` |
| `internal/server/library_ai_parse_op.go` | `library.ai-parse` |
| `internal/server/library_core_ops.go` | `library.scan`, `library.import`, `library.organize`, `library.transcode` |
| `internal/server/library_size_refresh_op.go` | `library.size-refresh` |
| `internal/server/library_writeback_op.go` | `library.bulk-write-back` |
| `internal/server/metadata_candidate_op.go` | `metadata.candidate-fetch` |
| `internal/server/metadata_ops.go` | `library.bulk-metadata-fetch` |
| `internal/server/openlibrary_ops.go` | `openlibrary.download`, `openlibrary.import` |
| `internal/server/purge_soft_deleted_op.go` | `library.purge-soft-deleted` |
| `internal/server/reconcile_ops.go` | `reconcile.scan`, `reconcile.apply` |
| `internal/server/series_rename_ops.go` | `entities.series-rename` |
| `internal/scheduler/extra_ops.go` | `scheduler.metadata-upgrade` |

**Total: 44 server definitions + 1 = 45**, plus the new kind `books.library.scan-full` (46 native kinds). Not converted here: `diagnostics.export`
(`diagnostics_ops.go`; core kind `core.diagnostics.export` from F2's minimal owner registration, 05-tasks
§5T.8.2), `maintenance.window` (`scheduler_maintenance_window_op.go`; core kind `core.window.nightly`,
declared at F2 with the scheduler, 05-tasks §5T.8.2), `operations.backfill-legacy-status` (`legacy_backfill_op.go`; deleted by the flip, map rule 5: `books.retired.<id>`),
and the 34 wrapped `maintenance.<job>` definitions of `maintenance_job_op.go` (T2, F5-01).

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
test -f internal/books/testdata/tasklint_exempt.d/F5-13.txt && echo entry                 # entry (F2-17 created it), else STOP
grep -v '^#' internal/books/testdata/tasklint_exempt.d/F5-13.txt | grep -v '^$' | grep -vxF -e internal/server/ai_ops.go -e internal/server/ai_scan_apply_op.go -e internal/server/aiscan_op.go -e internal/server/apply_when_scanned_op.go -e internal/server/author_reclassify_op.go -e internal/server/batch_apply_op.go -e internal/server/batch_apply_refusal_reset_op.go -e internal/server/batch_save_op.go -e internal/server/bulk_apply_preview.go -e internal/server/catalog_coverage_op.go -e internal/server/catalog_harvest_op.go -e internal/server/collection_refresh_op.go -e internal/server/diagnostics_ai_ops.go -e internal/server/duplicates_ops.go -e internal/server/entities_ops.go -e internal/server/folder_autoscan_op.go -e internal/server/identification_advance_op.go -e internal/server/itunes_ops.go -e internal/server/library_ai_parse_op.go -e internal/server/library_core_ops.go -e internal/server/library_size_refresh_op.go -e internal/server/library_writeback_op.go -e internal/server/metadata_candidate_op.go -e internal/server/metadata_ops.go -e internal/server/openlibrary_ops.go -e internal/server/purge_soft_deleted_op.go -e internal/server/reconcile_ops.go -e internal/server/series_rename_ops.go -e internal/scheduler/extra_ops.go | wc -l   # 0 (the entry names only this row's 29 files)
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
grep -rn 'OperationDef{' internal/server/*.go | grep -v _test.go | wc -l          # 47 at ac4368bdb (informative; the python below is the count that decides)
python3 - <<'EOF'                                                                  # the authoritative count at your base: prints 45
import re,glob
skip={'diagnostics_ops.go','scheduler_maintenance_window_op.go','legacy_backfill_op.go','maintenance_job_op.go'}
n=0
for f in glob.glob('internal/server/*.go'):
    if f.endswith('_test.go') or f.split('/')[-1] in skip: continue
    n+=len(re.findall(r'OperationDef\{',open(f).read()))
n+=1   # apply_when_scanned_op.go builds two defs from one literal (whenScannedDef)
n+=len(re.findall(r'OperationDef\{',open('internal/scheduler/extra_ops.go').read()))
print(n)
EOF
grep -n 'BulkMetadataFetchParams' internal/operations/state.go | wc -l             # >=1 (moves into internal/server in this PR's native shell, 05-tasks §5T.8.1)
grep -n 'operations.SaveCheckpoint\|operations.LoadCheckpoint' internal/server/metadata_ops.go | wc -l   # >=1 (v1 checkpoints -> rc.Checkpoint)
grep -rln 'mediacore/tasks"' internal/server/*.go internal/scheduler/*.go | wc -l   # 0
```

If the python count is not 45, STOP and report the list difference: the T7 set moved.

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
the oracle. Rule R10 adds two fields to the env below (`Writer`, `Store`) and is part of this rule:

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
| `Schedule` (a v2 cron string) | **none**: never evaluated at HEAD (05-tasks §5T.2); deleted and listed for the release note (05-tasks §5T.3.8; 14 §14.3.2 Q-C, D15); a def that a `TaskDefinition` fires keeps that cadence through F5-14. Schedules are F5-14's | 05-tasks §5T.3.8 |
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
| `Writes` empty, the body writes | `tasks.Writes(res(x)...)` for every books resource the shared core **and every function it calls** writes, found by reading them (05-tasks conversion rule 1). Rule R10 makes this a reading of the core's `t7Writer` calls: `ModifyBook` → `books`; `ModifyBookFile`, `RepointBookFile` → `book_files`; `ModifyBookAuthors` → `authors`; any other method → the resource of the HEAD store method it wraps | "Writes found": per definition, each resource and the call that writes it |
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

**Server specifics.**

- The native shells live in the **same file** as their HEAD definition (methods on `*Server`); the list
  function is `func (s *Server) t7Kinds(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition` in
  `internal/server/kinds_t7.go` (name coined here; rule R9). `scheduler.metadata-upgrade`'s native shell lives in `internal/scheduler/extra_ops.go` with its list
  in `internal/scheduler/kinds_t7.go` (name coined here).
- `apply_when_scanned_op.go` builds two definitions from one constructor (`whenScannedDef`): add one native
  constructor taking the same arguments, and call it twice.
- **v1 checkpoints** (`operations.SaveCheckpoint`/`LoadCheckpoint` in `metadata_ops.go`): the native shell
  uses `rc.Checkpoint`/`rc.Restore` with HEAD's checkpoint struct; the v2 shell keeps the v1 helpers
  (05-tasks §5T.8.1: "replaced by `rc.Checkpoint` in each caller's conversion PR and deleted by the flip").
- **Child waits** through `childop` (`server_maintenance_deps.go`): native side `rc.Enqueue` + `rc.Await` on
  the native child kind; v2 side unchanged.
- `library.scan` and `library.organize` are the most-scheduled definitions; their schedules are F5-14's.
- **`books.library.scan-full`** (05-tasks §5T.8.2, rename map rule 4 and the paragraph after rule 6): a second
  native kind built in `library_core_ops.go` beside `books.library.scan`, over the **same shared core** with the
  full sweep's params fixed (`force_update` and `include_root_dir` both true, the params HEAD's `library_scan_full`
  task enqueues, `internal/scheduler/full_sweep.go`). The two share one body, one `ConcurrencyKey`
  (`tasks.Key("books.library.scan")`, HEAD's key) and the scan stand-down key, "so they never run at once". It has no
  HEAD v2 shell (there is no HEAD definition to shadow) and no imported runs of its own except the ones M4 routes to
  it (F5-16). Its schedule (`modules.books.scheduler.schedules.library_scan_full`) is F5-14's.

**R10, writer and stores only through the shell (05-tasks §5T.8.2 conversion rule 3; §5T.8.4 "The
dark-conversion ratchet"; plan §(l) items 4 and 5).** A dark conversion keeps HEAD's behaviour, and the shared
core reaches its writer and its stores **only through what its shell hands it**. That is what lets `tasklint`'s
`writer-bypass` and `store-capture` pass over the converted files once this PR deletes its exemption entry file.
For every core this PR creates:

1. **No captured store.** The core reads no store, database handle, HEAD writer or service from a receiver field,
   a package variable or a closure it captures (`p.db`, `p.store`, `s.store`, a global store, a dependency reached
   through the receiver). Everything it reads or writes arrives through its arguments: the params, and two env
   fields added to rule R2's env struct, `Writer t7Writer` and `Store t7Store`. A helper the core calls that
   touches a store gets a new variant taking the env's `Writer`/`Store` (name coined per helper: `<helper>Via`);
   the existing helper is not re-signed (rule R2 item 4).
2. **The typed-writer interface** `t7Writer` (name coined here; plan §(l) item 4: a consumer interface declared in
   the converted package, with a fake in its tests) holds exactly the write methods this PR's cores call. Where
   HEAD's body already wrote through `pkg/ops.Writer`, copy those methods with their signatures (`ModifyBook`,
   `ModifyBookFile`, `RepointBookFile`, `ModifyBookAuthors`, `Effect`); a write HEAD's body made directly on a store
   becomes a method with the name and signature of the HEAD store method it calls. One interface per package for
   this PR; methods are only added, never re-signed.
3. **The store interface** `t7Store` (name coined here) holds exactly the **read** methods the cores call, with
   HEAD's signatures. A method that writes is never on it.
4. **The native shell** sets `env.Writer = deps.NewWriter(rc.Writer())` and `env.Store = deps.Store`, where
   `deps` is the owner-declared dependency value this PR's list function takes (rule R9; 05-tasks §5T.8.4
   `store-capture`: "through `tasks.Writer` (`rc.Writer()`) or the dependencies its owner declared for it"), with
   fields `Store t7Store` and `NewWriter func(tasks.Writer) t7Writer`. Nothing passes it in production before
   F5-18, which wires `internal/books/writer` behind `NewWriter` (plan §(l) item 4); tests pass a recording fake.
5. **HEAD's shell** sets `env.Writer` to an adapter of the same interface over HEAD's writer (name coined here:
   `head` + the interface name): each method calls exactly what HEAD's body called, in the same order with the same
   arguments (the reporter's v3 writer where HEAD used one, else the HEAD store method), and `env.Store` to the
   store HEAD's body read. HEAD's existing tests, unchanged and green, prove the adapter kept HEAD's behaviour.
6. The other body changes stay the three of 05-tasks §5T.8.2. Rule R10 changes where a write is reached from,
   never what is written or in which order.

STOP and report (never an allow directive, a `//nolint`, or a line added to any entry file) if a HEAD write cannot
be expressed as an interface method without changing what HEAD writes or its order; if `tasklint` still reports
`writer-bypass` or `store-capture` in a file this PR converts; or if it reports one in a file this row does not
own (that file is another row's; name it in the report).

**The exemption entry (05-tasks §5T.8.4; plan §(l) item 5).** F2-17 created
`internal/books/testdata/tasklint_exempt.d/F5-13.txt`, listing the books files this row converts. `tasklint`
reads the directory as one union whose ratchet only shrinks (a PR that adds a file or a line fails). This PR
deletes **that file only**, in the same PR as the conversion, and touches no other entry file: its same-wave
siblings delete their own.

## Files

| Action | Path |
|---|---|
| modify | the 28 files of the table above, and `internal/scheduler/extra_ops.go` |
| create | `internal/server/kinds_t7.go`, `internal/server/kinds_t7_test.go`, `internal/scheduler/kinds_t7.go`, `internal/scheduler/kinds_t7_test.go` (names coined here); each `kinds_t7.go` declares its package's `t7Writer`, `t7Store` and `t7Deps{Store t7Store; NewWriter func(tasks.Writer) t7Writer}` (rule R10), and each list function takes `deps t7Deps` before `sched` |
| delete | `internal/books/testdata/tasklint_exempt.d/F5-13.txt` (this row's exemption entry file; rule R10) |
| create | one `changelog.d/` fragment |

Do not modify `internal/server`'s registration (`RegisterOp` calls, `addOpRegistrar`, `wire_*.go`),
`maintenance_job_op.go`, `diagnostics_ops.go`, `scheduler_maintenance_window_op.go`, `legacy_backfill_op.go`,
`internal/scheduler/tasks.go`/`scheduler.go`, any other file under `internal/books/testdata/tasklint_exempt.d/`,
`internal/opscatalog` or `main.go`.

## TDD: tests to write first

`TestT7ServerKinds_Conformance`, `TestT7ServerKinds_MatchHEADDefs`, `TestT7ServerKinds_WritesThroughShell`,
`TestT7SchedulerKinds_Conformance`, `TestT7SchedulerKinds_MatchHEADDefs`, `TestT7SchedulerKinds_WritesThroughShell`
(names coined here; rules R8 and R10: for every kind whose `Effects` declare writes, one `ModeLive` run on a
`taskstest` fake `RC` shows `NewWriter` called with that run's `rc.Writer()` and every write reaching the recording
`t7Writer`), and `TestT7ServerKinds_ScanFullBesideScan` (name coined here): `books.library.scan-full` and
`books.library.scan` both exist, pass `taskstest.Conformance`, share the `ConcurrencyKey` and the stand-down key,
and a scan-full run hands the shared core `force_update` and `include_root_dir` true. The server tests build
`*Server` the way `internal/server/op_id_aliases_test.go`'s `bootServerForOpIDs` does.

## Steps

1. Base checks; STOP on a mismatch.
2. Convert file by file (rule R2, then R3–R7); run that file's existing tests after each.
3. `books.library.scan-full` beside `books.library.scan` in `library_core_ops.go`.
4. The two list files and seven tests.
5. `git rm internal/books/testdata/tasklint_exempt.d/F5-13.txt`, then run the acceptance block's `tasklint` line: it
   must pass with this row's files no longer exempt (rule R10's STOP if not).
6. PR description (`old ==> new`, plus `(new) ==> books.library.scan-full`; rule R5/R7 lists; the deleted entry
   file; `converted: 45/45`, `native kinds: 46`).

## Acceptance (commands and expected output)

```bash
go build ./...                                                                   # exit 0
go vet ./internal/server/ ./internal/scheduler/                                  # exit 0
go test ./internal/server/ ./internal/scheduler/ -count=1 -short                 # ok x2
go test ./internal/server/ ./internal/scheduler/ -run 'TestT7' -count=1 -v | grep -c '^--- PASS'   # 7
go test ./internal/server/ -run 'TestOpIDs_' -count=1                            # ok (HEAD's ledger unchanged)
grep -c '"books.library.scan-full"' internal/server/library_core_ops.go            # 1
test ! -e internal/books/testdata/tasklint_exempt.d/F5-13.txt && echo deleted       # deleted
git diff --name-only --diff-filter=D origin/main -- internal/books/testdata/tasklint_exempt.d/   # internal/books/testdata/tasklint_exempt.d/F5-13.txt (only)
git diff --name-only --diff-filter=AMR origin/main -- internal/books/testdata/tasklint_exempt.d/ | wc -l   # 0
go test ./internal/books/ -count=1 -run "$(grep -o '^func Test[A-Za-z0-9_]*' internal/books/tasklint_test.go | sed 's/^func //' | paste -sd'|')"   # ok (tasklint over the union; this row's files no longer exempt)
git grep -n 'mediacore/tasks"' -- main.go internal/opscatalog/ | wc -l           # 0
make ci                                                                          # exit 0
```

`make ci` runs `tasklint` (F2-17). With this row's entry file deleted, a `writer-bypass` or `store-capture` finding
in one of this row's files is a failure: fix it by rule R10, never with an allow directive or an entry line. A
finding in another row's file is rule R10's STOP.

## Out of scope

The 38 schedules (F5-14); the T4 core kinds (F5-03); registration and the deletion of HEAD's definitions,
routes and `legacy_backfill_op.go` (F5-18); any parallelism or write-set change.

## Idempotency / rollback

If `internal/server/kinds_t7.go` exists and its tests pass, run the acceptance instead. Rollback = revert.

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

Commit message: `refactor(server): native books kinds for the server op definitions (dark)`.
