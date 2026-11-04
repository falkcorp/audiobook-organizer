<!-- file: docs/agent-tasks/platform-core/PC-13-12-t6-maintenance-subbatch-6-of-7.md -->
<!-- version: 1.0.0 -->
<!-- guid: 77fb1ff4-d0e0-4d7f-88ce-cad58a028b52 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-12 — T6 sub-batch 6 of 7: maintenance-plugin definitions native (at most 15, one file group)

| Field | Value |
|---|---|
| ID | PC-13-12 |
| Tier | **T1 Sonnet/medium** — at most 15 mechanical conversions in one file group, by the recipe below |
| Wave | W13 (stage S5, release `S5r4`); one of six sub-batches (PC-13-08 to PC-13-13) run beside each other under `/parallel-sweep` after PC-13-07 |
| Depends on | PC-13-07 |
| Trailer | `Stage-Release: S5 4` on every commit |
| Spec | 05-tasks §5T.8.2 row T6 ("Maintenance plugin's 104 defs, in sub-batches of ≤ 15 by file group"), the T0+T1 row's 1:1 parallelism rule and the closing paragraph (`/parallel-sweep`, partitioned so parallel PRs never touch the same file); §5T.3.1-§5T.3.3, §5T.3.8 ("What replaces v2 cron strings"), §5T.8.1 childop row; CLAUDE.md "Concurrency" and "Parallel Subagent Coordination"; digest S5 §5 |

## Gate

Merges only after PC-13-07 (the PR that set `release: S5r4` and committed the group partition) has merged;
PC-13-07 in turn waits for OWNER-15's `S5r3` row. This PR does not change `ci/stage.txt`.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-12 -b pc-13-12-t6-group-6 origin/main
cd ../audiobook-organizer-pc-13-12
git rebase origin/main
npm ci --prefix web
git worktree list
```

## Goal

Every v2 definition of T6 **file group 6** (the lines `6 <file> <def func>` of
`internal/plugins/maintenance/testdata/t6_groups.txt`, which PC-13-07 computed at S5r4's base) becomes a native
`tasks.Task` registered by books, with each loop's parallelism equal to its legacy `Concurrency` (1:1). No behaviour
change (05-tasks §5T.8.2 T6: binary swap). At most 15 definitions.

## Exact files

Modify only:

- the files of group 6: `F=$(awk '$1==6{print $2}' internal/plugins/maintenance/testdata/t6_groups.txt | sort -u)`
  (inventory: `internal/plugins/maintenance/<group-6>`), test files of the same names (`<file>_test.go`) where a
  test constructs a converted def;
- `internal/plugins/maintenance/register_t6_g6.go` (your group's two lists, created by PC-13-07);
- new: `internal/plugins/maintenance/t6_group6_test.go` and `internal/plugins/maintenance/testdata/t6_g6_concurrency.golden`
  (names coined here);
- one `changelog.d/` fragment.

Do **not** touch `plugin.go`, `deps.go`, another group's files or `register_t6_g<j>.go`, or anything outside
`internal/plugins/maintenance/`: the other sub-batches run beside you under `/parallel-sweep` and the groups are
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
awk '$1==6' internal/plugins/maintenance/testdata/t6_groups.txt                 # your group's lines: <group> <file> <def func>
awk '$1==6' internal/plugins/maintenance/testdata/t6_groups.txt | wc -l         # your def count (<= 15)
F=$(awk '$1==6{print $2}' internal/plugins/maintenance/testdata/t6_groups.txt | sort -u)   # your files
grep -n 't6G6Defs\|t6G6Tasks' internal/plugins/maintenance/register_t6_g6.go          # 2 funcs
grep -nE '(ops)?registry\.RunItems\(' $F | grep -v _test | wc -l                    # your loops: record the number
```

Zero hits on an edit target, or a count that moved from what your group's line in
`internal/plugins/maintenance/testdata/t6_groups.txt` says, means **STOP and report**.

## Steps

1. Run the re-verify greps. If your group has **no** lines (fewer than 6 groups were needed at S5r4's base),
   open no PR and report "group 6 empty".
2. **Test first.** Generate `testdata/t6_g6_concurrency.golden` from the unconverted files: one line per
   `registry.RunItems` call in your group's files (file, enclosing func, the `Concurrency` expression text or
   `<none>`). Commit it with `t6_group6_test.go` (Tests section) before converting anything.
3. Convert every def of your group by the recipe below, one def per commit if that keeps each commit building.
4. Move each converted def from `t6G6Defs()` to `t6G6Tasks()` in `register_t6_g6.go`.
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

- `TestT6Group6Native` (coined): `t6G6Defs()` is empty; `t6G6Tasks()` holds one definition per line of group 6 in
  `t6_groups.txt`, each with kind `books.<def ID>` and `LegacyDefID` = the def ID.
- `TestT6Group6ParallelismOneToOne` (coined): an AST check over the group's files: every `tasks.RunItems`/`tasks.RunSeq`
  call's parallelism argument is `tasks.Fixed(<expr>, "legacy Concurrency")` or `tasks.Sequential("legacy zero
  Concurrency")`, and `<expr>` equals the text recorded for that call in `t6_g6_concurrency.golden` (`<none>` ↔
  `Sequential`). The count of native loops equals the golden's line count.
- `TestT6GroupsCoverRegister` (created by PC-13-07) stays green.

## Acceptance

```bash
F=$(awk '$1==6{print $2}' internal/plugins/maintenance/testdata/t6_groups.txt | sort -u)
grep -nE '(ops)?registry\.RunItems\(' $F | grep -v _test | wc -l                                        # 0
grep -lE '"github.com/falkcorp/audiobook-organizer/(internal/operations/registry|pkg/plugin/sdk)"' $F | wc -l   # 0
grep -n 'tasks\.CPU()' $F | wc -l                                                                     # 0
grep -c . internal/plugins/maintenance/testdata/t6_g6_concurrency.golden                           # = your loop count
go test ./internal/plugins/maintenance/ -run 'TestT6Group6|TestT6GroupsCoverRegister' -count=1        # ok
go test ./internal/plugins/maintenance/... ./internal/plugins/ -count=1                                # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile' -count=1                     # ok (op-ID ledger green)
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden                            # no output
make ci                                                                                               # exit 0
```

"Declared Parallelism equals the legacy Concurrency (1:1)" is proven by `TestT6Group6ParallelismOneToOne` and by the PR description's
table (call site, legacy `Concurrency` expression, native `Parallelism` expression), one row per converted loop.

## Out of scope

Any parallelism change (CLAUDE.md; a separate PR, never inside T0-T7); giving a cron-declaring def a real schedule
(14 Q-C; T7 records the deleted crons); the scheduled ticker entries that enqueue these defs (T7, PC-13-14); removing
`WaitForOp` from the plugin's `ServerDeps` or from `internal/server` once its last caller is native (PC-13-15 or T8);
another group's defs.

## Rollback

Revert (binary swap). Runs keep their `LegacyDefID`, so the previous build resumes them; a def whose loop moved to
`Watermark` may restart from zero once (09 §9.2.1 row 5; list those def IDs in the release-note draft).

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

- The row's file list is a placeholder (`<group-6>`); this brief resolves it through `t6_groups.txt`, which PC-13-07
  creates, plus `register_t6_g6.go`. Both are PC-13-07 inventions to make the six parallel PRs file-disjoint:
  without them every sub-batch edits `plugin.go`'s one `Register` list (see PC-13-07's notes).
- Moving a caller-wired checkpoint to `Watermark` gives the one-time restart after a rollback that 09 §9.2.1 row 5
  lists only for T2 (see PC-13-04's notes).
