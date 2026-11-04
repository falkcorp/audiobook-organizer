<!-- file: docs/agent-tasks/platform-core/PC-13-13-t6-subbatch-7-abandoned-calls-owner.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3855f164-01ee-4b95-b7a7-73a0bfe12719 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-13 — T6 sub-batch 7 of 7: maintenance-plugin definitions native (at most 15, one file group); books' observable takes over ops.abandoned_calls

| Field | Value |
|---|---|
| ID | PC-13-13 |
| Tier | **T2 Sonnet/high** — the group conversion is mechanical, but the PR also moves one metric family's owner, which must happen in exactly one PR with no build holding two instruments |
| Wave | W13 (stage S5, release `S5r4`); one of six sub-batches (PC-13-08 to PC-13-13) run beside each other under `/parallel-sweep` after PC-13-07 |
| Depends on | PC-13-07, PC-13-02 |
| Trailer | `Stage-Release: S5 4` on every commit |
| Spec | 05-tasks §5T.8.2 row T6 ("Maintenance plugin's 104 defs, in sub-batches of ≤ 15 by file group"), the T0+T1 row's 1:1 parallelism rule and the closing paragraph (`/parallel-sweep`, partitioned so parallel PRs never touch the same file); §5T.3.1-§5T.3.3, §5T.3.8 ("What replaces v2 cron strings"), §5T.8.1 childop row; §5T.8.1 `internal/opsmetrics` row; §5T.11 "One owner per family"; 05-metrics §6 and §13 (the `ops_abandoned_calls` row); 06 §6.4.4 (`Deps.Metrics`); CLAUDE.md "Concurrency" and "Parallel Subagent Coordination"; digest S5 §5 |

## Gate

Merges only after PC-13-07 (which set `release: S5r4` and committed the group partition) and PC-13-02 (which kept the
repairs source on `opsmetrics.RegisterAbandoned`) have merged. This PR does not change `ci/stage.txt`.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-13 -b pc-13-13-t6-group-7-abandoned-calls origin/main
cd ../audiobook-organizer-pc-13-13
git rebase origin/main
npm ci --prefix web
git worktree list
```

## Goal

Two things in one PR. (1) Every v2 definition of T6 **file group 7** (`7 <file> <def func>` lines of
`internal/plugins/maintenance/testdata/t6_groups.txt`) becomes a native `tasks.Task` with 1:1 parallelism, no behaviour
change. (2) `ops.abandoned_calls` changes owner: books' own observable on books' metrics scope takes over **both**
sources (`repairs_resolver`, `retired_versions_stat`), both callers are repointed, and `internal/opsmetrics`' instrument
is deleted, so the served series keep HEAD's name, label key and label values and no scrape ever sees two owners.

## Exact files

Modify only:

- the files of group 7: `F=$(awk '$1==7{print $2}' internal/plugins/maintenance/testdata/t6_groups.txt | sort -u)`
  (inventory: `internal/plugins/maintenance/<group-7>`), test files of the same names (`<file>_test.go`) where a
  test constructs a converted def;
- `internal/plugins/maintenance/register_t6_g7.go` (your group's two lists, created by PC-13-07);
- new: `internal/plugins/maintenance/t6_group7_test.go` and `internal/plugins/maintenance/testdata/t6_g7_concurrency.golden`
  (names coined here);
- **the metric move** (inventory): `internal/opsmetrics/opsmetrics.go` (and its tests), `internal/repairs/guards.go`
  (`:209`), `internal/plugins/maintenance/retired_versions_fixer.go` (`:240`); plus (Planner notes) a new
  `internal/repairs/abandoned_calls.go` and its test, the two `TestAbandonedGaugeIsRegisteredLazily` files
  (`internal/repairs/guards_gauge_test.go:61`, `internal/plugins/maintenance/retired_versions_gauge_test.go:61`) and
  `internal/books/metrics_gather_test.go` (the `TestABOMetricsGatherNoDuplicateOrMissing` case);
- one `changelog.d/` fragment.

Do **not** touch `plugin.go`, `deps.go`, another group's files or `register_t6_g<j>.go`, or anything outside
the paths above: the other sub-batches run beside you under `/parallel-sweep` and the groups are
file-disjoint by construction.

## Re-verify greps (HEAD `f07f516be`; the maintenance plugin differs from `748fd787a` only in `folder_books_fixer.go`, a repairs fixer, and its tests)

```bash
# v2 definitions the maintenance plugin registers (HEAD: 104 in 90 files; at S5r4's base expect 104 minus the 2 repairs ops
# (PC-13-02), the 4 iTunes defs (PC-13-06) and any T4 conversion (PC-13-03): 98 if T4 converted none)
awk '/^func \(p \*Plugin\) Register\(r sdk.Registry\) error/,/^}/' internal/plugins/maintenance/plugin.go | grep -cE '^\s*p\.[A-Za-z0-9]+Def\(\),'
# legacy loops in the plugin (HEAD: 93 call lines matching the pattern; convert the ones in your group's files)
grep -nE '(ops)?registry\.RunItems\(' internal/plugins/maintenance/*.go | grep -v _test | wc -l
# caller-wired checkpoints in the plugin (HEAD: CheckpointStateFn 3, CheckpointFn 4, ResumeFrom 11 lines)
grep -nE 'CheckpointStateFn|CheckpointFn|ResumeFrom' internal/plugins/maintenance/*.go | grep -v _test
# v2 cron strings in the plugin (HEAD: 14 defs in cleanup.go, author.go, db.go, nightly_compact_activity_log.go,
# integrity_check.go, dedup_ops.go, orphan_book_files.go, metadata.go)
grep -nE 'Schedule:\s*&' internal/plugins/maintenance/*.go | grep -v _test | wc -l
# child-run followers (HEAD: optimize.go through WaitForOp, internal/server/server_maintenance_deps.go:1185-1198)
grep -rn 'childop\.\|WaitForOp(' internal/plugins/maintenance/*.go | grep -v _test
```

Your group (created by PC-13-07; absent at HEAD):

```bash
test -f internal/plugins/maintenance/testdata/t6_groups.txt || echo "STOP: no t6_groups.txt (PC-13-07 not merged)"
awk '$1==7' internal/plugins/maintenance/testdata/t6_groups.txt                 # your group's lines: <group> <file> <def func>
awk '$1==7' internal/plugins/maintenance/testdata/t6_groups.txt | wc -l         # your def count (<= 15)
F=$(awk '$1==7{print $2}' internal/plugins/maintenance/testdata/t6_groups.txt | sort -u)   # your files
grep -n 't6G7Defs\|t6G7Tasks' internal/plugins/maintenance/register_t6_g7.go          # 2 funcs
grep -nE '(ops)?registry\.RunItems\(' $F | grep -v _test | wc -l                    # your loops: record the number
```

The metric move (HEAD; must be unchanged at your base apart from PC-12-08's shrink of `opsmetrics`):

```bash
grep -n 'RegisterAbandoned(opsmetrics.SourceRepairsResolver, &resolverAbandoned)' internal/repairs/guards.go                      # 1 (:209)
grep -n 'RegisterAbandoned(opsmetrics.SourceRetiredVersionsStat, &retiredStatsAbandoned)' internal/plugins/maintenance/retired_versions_fixer.go   # 1 (:240)
grep -n 'SourceRepairsResolver *= "repairs_resolver"\|SourceRetiredVersionsStat *= "retired_versions_stat"' internal/opsmetrics/opsmetrics.go     # 2 (:370-371)
grep -n '"audiobook_organizer.ops.abandoned_calls"' internal/opsmetrics/opsmetrics.go                                             # 1 (:240, unit "{call}")
grep -rn 'func TestAbandonedGaugeIsRegisteredLazily' internal                                                                    # 2
grep -n 'func TestABOMetricsGatherNoDuplicateOrMissing' internal/books/*_test.go                                                 # 1 (PC-12-08), else STOP
grep -rn 'RegisterAbandoned(' --include=*.go . | grep -v _test | wc -l                                                           # 3 (2 callers + the method)
```

Zero hits on an edit target, or a count that moved from what your group's line in
`internal/plugins/maintenance/testdata/t6_groups.txt` says, means **STOP and report**.

## Steps

1. Run the re-verify greps. If your group has **no** lines (fewer than 7 groups were needed at S5r4's base),
   open no PR and report "group 7 empty".
2. **Test first.** Generate `testdata/t6_g7_concurrency.golden` from the unconverted files: one line per
   `registry.RunItems` call in your group's files (file, enclosing func, the `Concurrency` expression text or
   `<none>`). Commit it with `t6_group7_test.go` (Tests section) before converting anything.
3. Convert every def of your group by the recipe below, one def per commit if that keeps each commit building.
4. Move each converted def from `t6G7Defs()` to `t6G7Tasks()` in `register_t6_g7.go`.
5. **The owner change of `ops_abandoned_calls`, in this PR and no other** (05-tasks §5T.8.1 opsmetrics row; 05-metrics
   §13 table; digest S5 rule 10). Do it after the group conversion, in commits that each build:
   1. *Test first.* Extend `TestABOMetricsGatherNoDuplicateOrMissing` with a case that bumps both counters and asserts
      the gather returns no error and exactly two series of `audiobook_organizer_ops_abandoned_calls`, one per
      `source` value, `repairs_resolver` and `retired_versions_stat`, with the bumped values. It passes before the move
      (opsmetrics is the owner) and must pass after.
   2. *Create books' observable* in `internal/repairs/abandoned_calls.go` (file name coined here): one
      `Int64ObservableGauge` made from books' `*metrics.Scope` (`Deps.Metrics`, 06 §6.4.4; `Scope.Meter()`, 05-metrics
      §3), named so the served family is `audiobook_organizer_ops_abandoned_calls` (HEAD's instrument name
      `audiobook_organizer.ops.abandoned_calls`, unit `{call}`, HEAD's description text), with one callback observing
      every registered source under the attribute key HEAD uses (`telemetry.Source`). Move the closed source set with
      its exact values: `SourceRepairsResolver = "repairs_resolver"`, `SourceRetiredVersionsStat =
      "retired_versions_stat"`. Register it once, lazily (not from `init()`, for HEAD's reason in `guards.go`'s
      comment). Find how books code reaches `Deps.Metrics` at your base
      (`grep -rn 'Deps.Metrics\|\*metrics.Scope' internal/books internal/server | grep -v _test`); if no path reaches
      `internal/repairs` without editing `internal/books/module.go`, **STOP** and report.
   3. *Repoint both callers in the same commit*: `guards.go:209` and `retired_versions_fixer.go:240` register their
      counters with the new observable.
   4. *Delete `opsmetrics`' instrument in the same commit*: the `abandoned` gauge, `abandonedSrc`/`abandonedMu`/
      `abandonedNext`, `observeAbandoned`, `RegisterAbandoned` and the two `Source*` constants. So no build ever has two
      instruments for the family and `ErrFamilyTaken` never fires (05-metrics §13). The rest of `internal/opsmetrics`
      stays until T8.
   5. Re-point both `TestAbandonedGaugeIsRegisteredLazily` tests at the new owner.
5. Run the acceptance block; changelog fragment; headers.

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

## Tests to write first

- `TestT6Group7Native` (coined): `t6G7Defs()` is empty; `t6G7Tasks()` holds one definition per line of group 7 in
  `t6_groups.txt`, each with kind `books.<def ID>` and `LegacyDefID` = the def ID.
- `TestT6Group7ParallelismOneToOne` (coined): an AST check over the group's files: every `tasks.RunItems`/`tasks.RunSeq`
  call's parallelism argument is `tasks.Fixed(<expr>, "legacy Concurrency")` or `tasks.Sequential("legacy zero
  Concurrency")`, and `<expr>` equals the text recorded for that call in `t6_g7_concurrency.golden` (`<none>` ↔
  `Sequential`). The count of native loops equals the golden's line count.
- `TestT6GroupsCoverRegister` (created by PC-13-07) stays green.
- The extended `TestABOMetricsGatherNoDuplicateOrMissing` case (Step 5.1), named by the spec: "with both source values
  of `ops_abandoned_calls`".
- `TestAbandonedGaugeIsRegisteredLazily` (both copies, existing names) against the new owner.

## Acceptance

```bash
F=$(awk '$1==7{print $2}' internal/plugins/maintenance/testdata/t6_groups.txt | sort -u)
grep -nE '(ops)?registry\.RunItems\(' $F | grep -v _test | wc -l                                        # 0
grep -lE '"github.com/falkcorp/audiobook-organizer/(internal/operations/registry|pkg/plugin/sdk)"' $F | wc -l   # 0
grep -n 'tasks\.CPU()' $F | wc -l                                                                     # 0
grep -c . internal/plugins/maintenance/testdata/t6_g7_concurrency.golden                           # = your loop count
go test ./internal/plugins/maintenance/ -run 'TestT6Group7|TestT6GroupsCoverRegister' -count=1        # ok
go test ./internal/plugins/maintenance/... ./internal/plugins/ -count=1                                # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile' -count=1                     # ok (op-ID ledger green)
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden                            # no output
grep -rn 'RegisterAbandoned(' --include=*.go internal/opsmetrics | wc -l                              # 0
grep -n 'abandoned' internal/opsmetrics/opsmetrics.go | wc -l                                         # 0
grep -rn '"audiobook_organizer.ops.abandoned_calls"\|ops.abandoned_calls' --include=*.go internal/repairs | grep -v _test | wc -l   # >= 1 (the one new owner)
go test ./internal/repairs/... ./internal/opsmetrics/... -count=1                                       # ok
go test ./internal/books/ -run TestABOMetricsGatherNoDuplicateOrMissing -count=1 -v | grep -c 'abandoned'   # >= 1 (the new case ran)
go test ./internal/telemetrypolicy/... -count=1                                                       # ok (seeded series contract test, 05-metrics §6)
make ci                                                                                               # exit 0
```

"Declared Parallelism equals the legacy Concurrency (1:1)" is proven by `TestT6Group7ParallelismOneToOne` and by the PR description's
table (call site, legacy `Concurrency` expression, native `Parallelism` expression), one row per converted loop.

## Out of scope

Any parallelism change (CLAUDE.md; a separate PR, never inside T0-T7); giving a cron-declaring def a real schedule
(14 Q-C; T7 records the deleted crons); the scheduled ticker entries that enqueue these defs (T7, PC-13-14); removing
`WaitForOp` from the plugin's `ServerDeps` or from `internal/server` once its last caller is native (PC-13-15 or T8);
another group's defs.
The other `internal/opsmetrics` content (deleted at T8, PC-14-01).

## Rollback

Revert (binary swap). Runs keep their `LegacyDefID`, so the previous build resumes them; a def whose loop moved to
`Watermark` may restart from zero once (09 §9.2.1 row 5; list those def IDs in the release-note draft).
The previous build serves `ops_abandoned_calls` from its own `opsmetrics`, with the same series, so a rollback
changes no series.

## Repo rules

- File headers (`.standards/instructions/file-headers.md`) on every new or changed file, version bumped; none on the
  `changelog.d/` fragment. One fragment per PR.
- Conventional commits, each ending with:
  ```
  Stage-Release: S5 4
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- Every commit builds (rebase merges). Push, `gh pr create`, wait for the required checks of 09 §9.2.1
  (`extraction-gates`, `docker-image`, `vet-darwin`, `vet-integration`), then
  `gh pr merge <n> --rebase`. Never squash, never `--admin` past a required check (14 Q-I).
- CLAUDE.md concurrency rules: every converted loop keeps its HEAD worker count 1:1 (`Fixed(n, "legacy
  Concurrency")` or `Sequential("legacy zero Concurrency")`); any parallelisation is a separate PR.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`.

## Planner notes

- Files beyond the row's list: a new file for books' observable (this brief puts it in `internal/repairs`, which
  outlives T8; `internal/opsmetrics` is deleted at T8), the two `TestAbandonedGaugeIsRegisteredLazily` test files and
  `internal/books/metrics_gather_test.go`. If reaching `Deps.Metrics` needs `internal/books/module.go`, that file
  must be added too (it is also edited by PC-13-03 and PC-13-14, in other releases).
- 05-tasks §5T.8.1 and 05-metrics §13 tie the move to "the T6 sub-batch PR that converts `retired_versions_fixer.go`".
  At HEAD that file holds a Repairs-lane fixer converted at T3 (PC-13-02), not a v2 definition, so no T6 group
  contains it; the plan's choice (this row) still moves the family's owner in exactly one PR.
- If fewer than 7 groups exist at S5r4's base, group 7 is empty: do the metric move alone and say so in the PR.
