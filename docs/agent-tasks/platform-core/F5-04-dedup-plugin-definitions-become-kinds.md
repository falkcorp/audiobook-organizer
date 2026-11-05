<!-- file: docs/agent-tasks/platform-core/F5-04-dedup-plugin-definitions-become-kinds.md -->
<!-- version: 1.0.0 -->
<!-- guid: f65363ce-a82a-4644-94e3-cbeb03867ead -->
<!-- last-edited: 2026-11-05 -->

# F5-04 — Conversion group T5, dedup plugin: its 32 definitions become books kinds, dark

| Field | Value |
|---|---|
| ID | F5-04 |
| Release / flip | R2, tasks flip F5 (dark conversion PR) |
| Wave | W26 |
| Tier | **T2** (Sonnet / high): 32 definitions converted under fixed rules, including the package's two v2 child-run waits and its one `Requires`/`Batchable` definition |
| Depends on | F2-05 (transitively F2-01..F2-04) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-04-dedup-kinds
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

Give each of the **32** v2 definitions of `internal/plugins/dedup` a native `core/tasks` definition
(kind `books.<HEAD ID>`) that lands **dark** (05-tasks §5T.8.2, T5 row: "service-registry plugin defs, per
plugin: … dedup 32"; gate "`pkg/plugin/sdk` unused by them"). HEAD's definitions keep running unchanged
until the tasks flip F5-18 registers the native kinds. The nine `dedup.*` definitions that live in
`internal/server/duplicates_ops.go` are **not** this PR's (they are T7, F5-13).

## Spec to read first

1. 05-tasks §5T.8.2 (T5 row; the three body rules; the kind-rename map), §5T.8.1 (`internal/operations/childop`
   row: "`plugins/dedup/run_all.go:532`, `label_refinement.go:153` … move to `rc.Await` in their conversion PRs").
2. 05-tasks §5T.3.1–§5T.3.7; §5T.9 (`TestParallelismCap_LowersDeclared` names `books.dedup.full-scan`).
3. 05-rbac §6 (atom table). CLAUDE.md "Concurrency" (the 2026-07-05 `dedup.full-scan` incident is this package's).

## The definitions (at `f7ac5b11e`; regenerate at your base)

| File | HEAD def IDs to convert |
|---|---|
| `internal/plugins/dedup/auto_resolve.go` | `dedup.auto-resolve` |
| `internal/plugins/dedup/book_signature_scan.go` | `dedup.book-signature-scan` |
| `internal/plugins/dedup/bookfile_seg_sweep.go` | `dedup.bookfile-seg-drop` |
| `internal/plugins/dedup/breakdown_backfill.go` | `dedup.breakdown-backfill` |
| `internal/plugins/dedup/build_candidate_index.go` | `dedup.build-candidate-status-index` |
| `internal/plugins/dedup/build_isbn_index.go` | `dedup.build-isbn-index` |
| `internal/plugins/dedup/calibrate_composite.go` | `dedup.calibrate-composite` |
| `internal/plugins/dedup/calibrate_embedding_thresholds.go` | `dedup.calibrate-embedding-thresholds` |
| `internal/plugins/dedup/check_book.go` | `dedup.check-book` |
| `internal/plugins/dedup/cleanup_orphan_author_embeddings.go` | `dedup.cleanup-orphan-author-embeddings` |
| `internal/plugins/dedup/cleanup_orphan_embeddings.go` | `dedup.cleanup-orphan-embeddings` |
| `internal/plugins/dedup/dataset_backfill.go` | `dedup.dataset-backfill` |
| `internal/plugins/dedup/drain_stale.go` | `dedup.drain-stale` |
| `internal/plugins/dedup/emb_reencode.go` | `dedup.emb-reencode` |
| `internal/plugins/dedup/embed_async.go` | `dedup.embed-async` |
| `internal/plugins/dedup/embed_scan.go` | `dedup.embed-scan` |
| `internal/plugins/dedup/full_scan.go` | `dedup.full-scan` |
| `internal/plugins/dedup/label_refinement.go` | `dedup.label-refinement` |
| `internal/plugins/dedup/llm_review.go` | `dedup.llm-review` |
| `internal/plugins/dedup/lsh_index_build.go` | `dedup.lsh-index-build` |
| `internal/plugins/dedup/mine_gold_labels.go` | `dedup.mine-gold-labels` |
| `internal/plugins/dedup/purge_legacy_fp.go` | `dedup.purge-legacy-fp-candidates` |
| `internal/plugins/dedup/purge_stale.go` | `dedup.purge-stale` |
| `internal/plugins/dedup/quarantine_chapter_artifacts.go` | `dedup.quarantine-chapter-artifacts` |
| `internal/plugins/dedup/rebuild_gold_labels.go` | `dedup.rebuild-gold-labels` |
| `internal/plugins/dedup/reembed_embeddings.go` | `dedup.reembed-embeddings` |
| `internal/plugins/dedup/rescore_labeled_examples.go` | `dedup.rescore-labeled-examples` |
| `internal/plugins/dedup/rescore_op.go` | `dedup.rescore` |
| `internal/plugins/dedup/run_all.go` | `dedup.run-all` |
| `internal/plugins/dedup/split_book_bulk_merge.go` | `dedup.split-book-bulk-merge` |
| `internal/plugins/dedup/split_book_scan.go` | `dedup.split-book-scan` |
| `internal/plugins/dedup/window_index_build.go` | `dedup.window-index-build` |

**Total: 32** (05-tasks §5T.8.2 says 32 at `748fd787a`; `internal/server/testdata/op_ids.golden` is unchanged
since then).

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
grep -rn 'OperationDef{' internal/plugins/dedup/*.go | grep -v _test.go | grep -vc 'plugin.go'     # 32, else STOP
grep -n 'childop\.\|WaitForOp' internal/plugins/dedup/run_all.go internal/plugins/dedup/label_refinement.go | wc -l   # >=2 (the v2 child waits)
grep -n 'Requires:' internal/plugins/dedup/check_book.go | wc -l                                     # 1
grep -n 'Batchable: *true' internal/plugins/dedup/check_book.go | wc -l                              # 1
grep -ln 'mediacore/tasks"' internal/plugins/dedup/*.go | wc -l                                     # 0 (else see Idempotency)
```

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

**Dedup specifics.**

- **Child runs.** `run_all.go` and `label_refinement.go` wait for v2 child ops through `childop`. In the
  native shell the env's child wait is `rc.Enqueue(kind, params)` then `rc.Await(ref)` (05-tasks §5T.3.3,
  §5T.8.1 childop row), naming the **native** child kind (`books.dedup.<child>`); the v2 shell keeps
  `childop`. Add a `Await func(kind string, params any) error` field to that definition's env (rule R2).
- **`dedup.check-book`** carries `Requires` and `Batchable`: map `Batchable`/`BatchWindow`/`BatchMaxWait` to
  `Debounce`; map `Requires` by rule R5 (STOP for this one definition if `core/tasks` has no `Requires`
  field at your base; convert the other 31).
- **`dedup.full-scan`** and the other library-scale scorers keep HEAD's exact worker counts (rule R4). Do
  not "fix" a sequential loop here; a parallelism change is a separate PR.

## Files

| Action | Path |
|---|---|
| modify | the 32 definition files above (shared core + v2 shell + native shell, rule R2) |
| create | `internal/plugins/dedup/kinds.go` (name coined here): `func (p *Plugin) Kinds(sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition`, every native kind in file order with `Schedule: sched(kind)` (rule R9); nothing calls it in production |
| create | `internal/plugins/dedup/kinds_test.go` (name coined here): rule R8 |
| create | one `changelog.d/` fragment |

Do not modify `internal/plugins/dedup/plugin.go`'s `OperationDefs()`/registration, `internal/server`,
`internal/opscatalog` or `main.go`.

## TDD: tests to write first

- `TestDedupKinds_Conformance` (name coined here): `taskstest.Conformance(t, p.Kinds(nil)...)` (05-tasks §5T.9 last row).
- `TestDedupKinds_MatchHEADDefs` (name coined here): rule R8's table over all 32.
- `TestDedupKinds_ChildWaitsUseAwait` (name coined here): the native shells of the two child-waiting
  definitions call `rc.Await` on a `books.dedup.*` kind (a fake `RC` from `taskstest`).

## Steps

1. Base checks; STOP on a mismatch.
2. Convert file by file (rule R2, then R3–R7); after each file run its existing tests.
3. `kinds.go`, the tests, the PR description (`old ==> new` list; rule R5/R7 lists with counts;
   `converted: 32/32`).

## Acceptance (commands and expected output)

```bash
go build ./...                                                                   # exit 0
go vet ./internal/plugins/dedup/                                                 # exit 0
go test ./internal/plugins/dedup/ -count=1 -short                                # ok
go test ./internal/plugins/dedup/ -run 'TestDedupKinds_' -count=1 -v | grep -c '^--- PASS'   # 3
grep -o '"books\.dedup\.[a-z0-9.-]*"' internal/plugins/dedup/*.go | sort -u | wc -l   # 32
go list -deps ./internal/plugins/dedup/ | grep -c 'pkg/plugin/sdk'               # >=1 until F5-18 (HEAD shells still use it; the T5 gate "sdk unused" is checked by F5-18)
git grep -n 'mediacore/tasks"' -- main.go internal/opscatalog/ | wc -l           # 0
make ci                                                                          # exit 0
```

If `tasklint` (F5-17) flags `writer-bypass` or `store-in-deps` on an unchanged shared core, STOP and
report (Planner notes).

## Out of scope

Registration (F5-18); schedules (`dedup_refresh`, `dedup_llm_review`, `label_refinement`, `series_*` are
F5-14's); the nine server-side `dedup.*` defs (F5-13); any parallelism or write-set change.

## Idempotency / rollback

If `internal/plugins/dedup/kinds.go` exists and its tests pass, run the acceptance instead. Rollback = revert;
HEAD's definitions are unchanged in behaviour.

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

Commit message: `refactor(dedup): native books kinds for the dedup plugin's 32 definitions (dark)`.

## Planner notes

- Rule R7 (effects for HEAD's undeclared write sets) and rule R2 (shared core) are plan rules, not spec text;
  the review loop should confirm them before W26.
- The T5 gate "`pkg/plugin/sdk` unused by them" cannot hold while the dark rule keeps HEAD's v2 shells in the
  same package; it becomes checkable at F5-18, when the v2 shells are deleted. F5-18's acceptance carries it.
- tasklint's `writer-bypass`/`store-in-deps` rules (F5-17, W24) would flag unchanged bodies; the spec does not
  say whether a dark conversion must move writes behind `tasks.Writer`.
