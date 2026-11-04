<!-- file: docs/agent-tasks/platform-core/PC-13-05-t5b-dedup-definitions-native.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1039287e-824e-48a3-a94f-4f0701090d78 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-05 — T5b: the 32 dedup plugin definitions native

| Field | Value |
|---|---|
| ID | PC-13-05 |
| Tier | **T2 Sonnet/high** — the CLAUDE.md concurrency hotspot (`dedup.full-scan` went single-core for 3 hours on 2026-07-05); parallelism must map 1:1, and two parent ops follow child runs |
| Wave | W13 (stage S5, release `S5r3`); runs beside PC-13-04 and PC-13-06 under `/parallel-sweep` (file-disjoint) |
| Depends on | PC-13-03 |
| Trailer | `Stage-Release: S5 3` on every commit |
| Spec | 05-tasks §5T.8.2 row T5 and the T0+T1 row's 1:1 parallelism rule; §5T.8.1 childop row; §5T.6.2 "v1 checkpoints" row (`internal/dedup/drain_stale.go:324`); §5T.3.1-§5T.3.3; CLAUDE.md "Concurrency — Prefer Multi-Core Design"; digest S5 §5 rule 8 |

## Gate

Merges only after PC-13-03 has merged. No `ci/stage.txt` change.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-05 -b pc-13-05-t5b-dedup-native origin/main
cd ../audiobook-organizer-pc-13-05
git rebase origin/main
npm ci --prefix web
git worktree list
```

## Goal

Every v2 definition of the dedup plugin (32 at `748fd787a` and HEAD) becomes a native `tasks.Task` registered by
books; every loop keeps its legacy worker count exactly; `dedup.run-all` and `dedup.label-refinement` follow their
children through `rc.Enqueue`/`rc.Await` instead of `childop`; `dedup.drain-stale`'s v1 checkpoint moves to
`rc.Checkpoint`. No behaviour change beyond the one-time restart of a moved checkpoint after a rollback.

## Exact files

Modify: `internal/plugins/dedup/` (inventory), plus `internal/dedup/drain_stale.go` (the v1 checkpoint writer of
`dedup.drain-stale`; see Planner notes); one `changelog.d/` fragment.

## Re-verify greps (HEAD `f07f516be`; identical at `748fd787a`)

```bash
awk '/func \(p \*Plugin\) OperationDefs\(\)/,/^}/' internal/plugins/dedup/plugin.go | grep -cE '^\s+p\.[A-Za-z]+Def\(\),'   # 32
grep -nE '(ops)?registry\.RunItems\(' internal/plugins/dedup/*.go | grep -v _test | wc -l          # 12
grep -rn 'childop\.' internal/plugins/dedup/*.go | grep -v _test | wc -l                          # 11 (run_all.go 7, label_refinement.go 4)
grep -nE '\b[a-z]+\.(SaveCheckpoint|LoadCheckpoint|ClearState)\(' internal/dedup/drain_stale.go    # 3: :228 Load, :324 Save, :381 Clear
grep -rn '"github.com/falkcorp/audiobook-organizer/internal/operations/registry"' internal/plugins/dedup | grep -v _test | wc -l   # 17 files
```

The 12 loops at HEAD: `breakdown_backfill.go:420`, `build_candidate_index.go:131`, `cleanup_orphan_embeddings.go:217`,
`dataset_backfill.go:214`, `embed_scan.go:150`, `lsh_index_build.go:176`, `mine_gold_labels.go:102`,
`quarantine_chapter_artifacts.go:203,315`, `reembed_embeddings.go:335`, `rescore_labeled_examples.go:223`,
`window_index_build.go:135`. Several dedup ops use `runtime.NumCPU()` (raw/06 §2); record each loop's exact
`Concurrency` expression in the PR description before converting it.

The 32 definitions are the entries of `OperationDefs()` (`plugin.go:87-122` at HEAD): embedScan, embedAsync,
fullScan, llmReview, bookSignatureScan, splitBookScan, splitBookBulkMerge, purgeStale, lshIndexBuild,
windowIndexBuild, purgeLegacyFP, embReencode, bookfileSegDrop, datasetBackfill, calibrateEmbeddingThresholds,
calibrateComposite, mineGoldLabels, rebuildGoldLabels, quarantineChapterArtifacts, drainStale, checkBook,
buildISBNIndex, reembedEmbeddings, autoResolve, cleanupOrphanEmbeddings, cleanupOrphanAuthorEmbeddings,
buildCandidateStatusIndex, rescoreLabeledExamples, breakdownBackfill, rescore, runAll, labelRefinement
(`p.<name>Def()`). Read each `ID:` at your base for the table in your PR description.

Zero hits on an edit target, or a moved count, means **STOP and report**.

## The conversion recipe (apply to every definition in scope, in this order)

The reference for every field and every runtime behaviour is **what opsbridge's adapter built for the v2 def at
your base** (05-tasks §5T.8.2: T0+T1 wrapped every v2 `OperationDef` as a core task with `LegacyDefID` = its def
ID). A native definition must behave, for the same request and store, exactly as the adapted one did. Find the
adapter once and keep it open:

```bash
grep -rn 'LegacyDefID' internal/operations/registry/*.go | grep -v _test     # the adapter (PC-12-08); >= 1 hit, else STOP
grep -rn 'func RunItems\[' internal/operations/registry/*.go                  # opsbridge's RunItems over tasks.RunItems
```

1. **Kind and identity** (05-tasks §5T.3.1, §5T.8.2). Kind = the kind the adapter assigned the def at your base,
   which 05-tasks' rule makes `books.<def ID>` (e.g. `dedup.full-scan` → `books.dedup.full-scan`); if the adapter
   used another kind, **STOP**. `LegacyDefID` = the v2 def ID (runs keep storing under it, so an older build
   resumes them). `FormerKinds` = the v2 `FormerIDs`. `LegacyPlugin` = the v2 `Plugin` (keeps the `plugin` label of
   the `ops_*` series, 05-tasks §5T.11).
2. **`Common`**: copy, field for field, the `Common` the adapter built for this def (Title, Help, Effects, Perms,
   Resume, ConcurrencyKey, NotWhile, Priority, Timeout, ProgressTimeout, Dedupe, Debounce, Yields, Ready). Two
   reminders: a v2 def with `Schedule != nil` folded every enqueue into its active run
   (`internal/operations/registry/registry.go:1058-1060` at HEAD), which the adapter expressed in `Dedupe`; keep it.
   A def with no `Permissions` had the implicit `settings.manage` default, now explicit (09 S5 "Permissions").
3. **Shape**: `tasks.Task[P, R]` with `TaskSpec{Common, Run}`. `P` is the params struct the body already decodes
   (else `json.RawMessage`), minus its `DryRun`/`DryRunCamel` fields: the mode arrives in the envelope and, for a
   kind with a `LegacyDefID`, a `dry_run` inside params is lifted out before decode (05-tasks §5T.3.7 "Mode /
   dry-run contract" rules 1-3). Replace `opmode.ResolveDryRun(...)` with `rc.Mode() == tasks.ModePreview`. `R` is
   `struct{}` unless the def records a result.
4. **Body**: `Run(ctx, raw, reporter)` becomes `Run(rc *tasks.RC, p P) (R, error)` with `ctx := rc.Context()`.
   Reporter calls map as follows (05-tasks §5T.3.3):

   | v2 `registry.Reporter` | native |
   |---|---|
   | `Logger()` | `rc.Log()` |
   | `Log(level, msg, attrs...)` | `rc.Log().LogAttrs(ctx, level, msg, attrs...)` |
   | `IsCanceled()` | `rc.Canceled()` |
   | `Checkpoint(v)` / reading it back on resume | `rc.Checkpoint(v)` / `rc.Restore(&v)`, same Go value |
   | `RunPhase(ctx, name, fn)` | `end := rc.Phase(name)`; run `fn`'s body; `end()` |
   | `UpdateProgress`, `SetCurrentItem` | `rc.Progress()`, exactly as the adapter's Reporter-over-RC maps them at your base |
   | `Trigger(ctx, event, payload)` | no mapping in the spec: **STOP** and report the def ID |

5. **Loops** (05-tasks §5T.3.2; digest S5 rule 8). Each `registry.RunItems(ctx, reporter, items, fn, opts)` becomes
   `tasks.RunItems(rc, par, items, fn2, tasks.ItemsOptions[T]{...})`:
   - `par` is **1:1** with the legacy `Concurrency`: a non-zero expression `E` → `tasks.Fixed(E, "legacy
     Concurrency")` (the same expression text, evaluated where it was); absent or literal 0 →
     `tasks.Sequential("legacy zero Concurrency")`. A `// runitems:sequential <reason>` marker call is the absent
     case. Never `tasks.CPU()`, never a different number: changing a def's parallelism is a separate PR under
     CLAUDE.md's concurrency rules (05-tasks §5T.8.2 T0+T1).
   - `fn(ctx, item)` → `fn2(ic *tasks.ItemRC, item)` with `ctx := ic.Context()`.
   - `ErrMode: ErrModeCollect` → `OnError: tasks.Collect`; the zero value stays `FailFast`.
   - `PerItemTimeout` → `ItemTimeout`; `ProgressOffset`/`ProgressTotal` → `ProgressBase`/`ProgressTotal`.
   - `Label func(i, total int) string` → `Label func(T) string` keeping only the item-derived text; core renders
     `done/total` itself (5T.3.2 "Label" row). A label that only formats the index is dropped.
   - `CheckpointStateFn`/`CheckpointEvery`/`ResumeFrom`/`CheckpointFn` → `Watermark: "<def ID>"` (a second
     checkpointed loop in the same body: `"<def ID>.2"`, names coined here) with the same `CheckpointEvery`; the
     5T.3.2 table names `Watermark` as the replacement of "caller-wired `ResumeFrom`/`CheckpointStateFn`". Record
     each such def ID in the PR description: after a rollback it restarts from zero once (09 §9.2.1 row 5).
   - Callers that test the error with `errors.Is` keep working (`*tasks.ItemsError` unwraps `First`, `Errs`,
     `Cause`).
6. **Child runs.** A body that enqueues and follows a child through `internal/operations/childop` (or the server's
   `WaitForOp`) becomes `ref, err := rc.Enqueue(childKind, params)` and `out, err := rc.Await(ref)` (05-tasks
   §5T.8.1 childop row: "No v2 body is rewritten onto `rc.Await` before its batch" — this is its batch). The child
   kind is the child def's kind (rule 1).
7. **Dead v2 metadata.** A v2 cron string (`Schedule: new("...")` / `&sched`) has no native field: it never ran
   (05-tasks §5T.3.8 "What replaces v2 cron strings"; 14 Q-C default "delete the declarations"). Drop it, keep the
   enqueue fold of rule 2, and list the def ID in the PR description for T7's record. `Cancellable` is never
   consulted (`docs/plans/platform-core/raw/06-tasks-ops.md` §7 item 7) and is dropped; `Isolate` follows the adapter.
8. **Registration.** Remove the def from its plugin's `OperationDefs()`/`Register` list and hand the native
   definition to books through the seam PC-13-01 used for the maintenance jobs (`Registrar.Tasks`, 06 §6.4.3). If
   no such seam exists at your base, **STOP**.
9. **Imports.** A converted file imports neither `internal/operations/registry` nor `pkg/plugin/sdk`, unless it
   still holds an unconverted def of a later batch.

## Batch-specific rules

- **Parallelism is the whole risk here.** Every loop maps 1:1 (recipe rule 5). In particular a loop whose
  `Concurrency` is `runtime.NumCPU()` becomes `tasks.Fixed(runtime.NumCPU(), "legacy Concurrency")`, **not**
  `tasks.CPU()`: `CPU()` takes a slot from the executor-wide CPU budget (05-tasks §5T.3.2 "CPU budget"), which is a
  parallelism change. Note in the PR that `Fixed` clamps to `MaxFixed` = 64 exactly as opsbridge already did at
  T0+T1, so no new clamp is introduced.
- **Child runs.** `run_all.go` (`:216,218,510,532-540`) and `label_refinement.go` (`:153-160`) enqueue children and
  follow them with `childop`. Convert to `rc.Enqueue(<child kind>, params)` + `rc.Await(ref)` (recipe rule 6), keeping
  the order, the params and the stop-on-failure rule of each parent exactly. `dedup.run-all` is resumable: its
  resume point keeps its value through `rc.Checkpoint`/`rc.Restore`.
- **`dedup.drain-stale`'s v1 checkpoint** (`internal/dedup/drain_stale.go:228,324,381`): replace
  `LoadCheckpoint`/`SaveCheckpoint` with `rc.Restore`/`rc.Checkpoint` of the same value, passed in from the plugin's
  `Run` (the engine method receives a checkpoint function or the `*tasks.RC`; keep `internal/dedup` free of a
  `core/tasks` import if a function value suffices), and delete the `ClearState` call. Lower `TestLegacyOpsWriters`
  by the removed sites (its T5 target; 05-tasks §5T.6.2).

## Tests to write first

- `TestT5bNativeKinds` (coined): 32 native definitions (the base count), kinds `books.<def ID>`, `LegacyDefID`s equal
  to the HEAD IDs, v2 `OperationDefs()` empty.
- `TestT5bParallelismOneToOne` (coined): an AST test over `internal/plugins/dedup/*.go` that collects every
  `tasks.RunItems`/`tasks.RunSeq` call and asserts its parallelism argument is a `tasks.Fixed(<expr>, "legacy
  Concurrency")` or `tasks.Sequential("legacy zero Concurrency")` call whose `<expr>` text equals the
  `Concurrency` expression recorded for that call site in `testdata/t5b_concurrency.golden` (generated from the base
  before conversion, one line per call: file, enclosing func, expression).
- Keep `run_all` and `label_refinement` tests green with the child order asserted.

## Acceptance

```bash
grep -nE '(ops)?registry\.RunItems\(' internal/plugins/dedup/*.go | grep -v _test | wc -l              # 0
grep -nE 'tasks\.RunItems\(|tasks\.RunSeq\(' internal/plugins/dedup/*.go | grep -v _test | wc -l      # 12 (the base count)
grep -rn 'childop\.' internal/plugins/dedup/*.go | grep -v _test | wc -l                              # 0
grep -rn 'tasks\.CPU()' internal/plugins/dedup/*.go | wc -l                                           # 0
grep -nE '\b[a-z]+\.(SaveCheckpoint|LoadCheckpoint|ClearState)\(' internal/dedup/drain_stale.go | wc -l # 0
grep -rn '"github.com/falkcorp/audiobook-organizer/internal/operations/registry"\|"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"' internal/plugins/dedup | grep -v _test | wc -l   # 0
go test ./internal/plugins/dedup/... ./internal/dedup/... -count=1                                   # ok
go test ./internal/plugins/ -run . -count=1                                                          # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile' -count=1                   # ok
go test ./... -run 'TestLegacyOpsWriters' -count=1                                                   # ok at its T5 target
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden                          # no output
make ci                                                                                             # exit 0
```

## Out of scope

Any parallelism improvement, even an obvious one (CLAUDE.md: a separate PR, never inside T0-T7); the scheduled
`dedup_refresh`/`label_refinement`/`dedup_llm_review` ticker entries (T7, PC-13-14); `internal/dedup`'s engine
beyond `drain_stale.go`'s checkpoint.

## Rollback

Revert (binary swap). Runs keep their `LegacyDefID`; `dedup.drain-stale` and any loop moved to `Watermark` restart
from zero once after a rollback (09 §9.2.1 row 5; list them in the release-note draft).

## Repo rules

- File headers (`.standards/instructions/file-headers.md`) on every new or changed file, version bumped; none on the
  `changelog.d/` fragment. One fragment per PR.
- Conventional commits, each ending with:
  ```
  Stage-Release: S5 3
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- Every commit builds (rebase merges). Push, `gh pr create`, wait for the required checks of 09 §9.2.1
  (`extraction-gates`, `docker-image`, `vet-darwin`, `vet-integration`), then
  `gh pr merge <n> --rebase`. Never squash, never `--admin` past a required check (14 Q-I).
- CLAUDE.md concurrency rules: every converted loop keeps its HEAD worker count 1:1 (`Fixed(n, "legacy
  Concurrency")` or `Sequential("legacy zero Concurrency")`); any parallelisation is a separate PR.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`.

## Planner notes

- `internal/dedup/drain_stale.go` holds `dedup.drain-stale`'s v1 checkpoint writer (05-tasks §5T.6.2 lists
  `internal/dedup/drain_stale.go:324`), which moves "in the batch that makes its job native" (T5); the row's files
  list only `internal/plugins/dedup/`.
- `TestLegacyOpsWriters` has no creating row (see PC-13-01's notes).
