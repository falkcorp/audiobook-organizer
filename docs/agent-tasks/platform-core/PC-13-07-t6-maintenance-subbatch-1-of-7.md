<!-- file: docs/agent-tasks/platform-core/PC-13-07-t6-maintenance-subbatch-1-of-7.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3089d580-defb-446e-a283-c02dc504bb73 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-07 — T6 sub-batch 1 of 7: maintenance-plugin definitions native (at most 15, one file group)

| Field | Value |
|---|---|
| ID | PC-13-07 |
| Tier | **T1 Sonnet/medium** — at most 15 mechanical conversions in one file group, plus one mechanical, behaviour-neutral split of the registration list |
| Wave | W13 (stage S5, release `S5r4`); first PR of `S5r4`, serial; PC-13-08 to PC-13-13 then run beside each other under `/parallel-sweep` |
| Depends on | OWNER-15 (the `S5r3` soak row) |
| Trailer | `Stage-Release: S5 4` on every commit |
| Spec | 05-tasks §5T.8.2 row T6 ("Maintenance plugin's 104 defs, in sub-batches of ≤ 15 by file group"), the T0+T1 row's 1:1 parallelism rule and the closing paragraph (`/parallel-sweep`, partitioned so parallel PRs never touch the same file); §5T.3.1-§5T.3.3, §5T.3.8 ("What replaces v2 cron strings"), §5T.8.1 childop row; CLAUDE.md "Concurrency" and "Parallel Subagent Coordination"; digest S5 §5 |

## Gate

Merges only after OWNER-15 has merged a current `S5r3` row into `ci/soak.txt`. This PR changes `release:`, so
`check_soak.py` refuses it until then and `compat-rehearsal` runs on it (09 §9.2.1).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-07 -b pc-13-07-t6-group-1 origin/main
cd ../audiobook-organizer-pc-13-07
git rebase origin/main
npm ci --prefix web
git worktree list
```

## Goal

Fix the T6 partition and land sub-batch 1: compute the seven file-disjoint groups of the maintenance plugin's
remaining v2 definitions at S5r4's base, commit the partition, split the plugin's registration list so that each
group registers from its own file (so the six later sub-batches are file-disjoint), convert group 1 to native
`tasks.Task` definitions with 1:1 parallelism, and set `release: S5r4`. No behaviour change (05-tasks §5T.8.2 T6).

## Exact files

Modify:

- `internal/plugins/maintenance/plugin.go` (the `Register` list, once, in Step 2) — see Planner notes;
- new: `internal/plugins/maintenance/register_t6_g1.go` … `register_t6_g7.go`,
  `internal/plugins/maintenance/testdata/t6_groups.txt`, `internal/plugins/maintenance/testdata/t6_g1_concurrency.golden`,
  `internal/plugins/maintenance/t6_groups_test.go`, `internal/plugins/maintenance/t6_group1_test.go` (names coined here);
- the files of group 1 (inventory: `internal/plugins/maintenance/<group-1>`), and test files of the same names where a
  test constructs a converted def;
- `ci/stage.txt` (`release: S5r3` → `release: S5r4`);
- one `changelog.d/` fragment.

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

Group 1 does not exist until Step 1 of this brief writes `t6_groups.txt`.

Zero hits on an edit target, or a count that moved, means **STOP and report**.

## Steps

0. **Preconditions (STOP on failure).** PC-13-02 (T3) and PC-13-06 (T5c) have merged, so `repairsPlanDef`,
   `repairsApplyDef` and the four iTunes defs are gone from the maintenance plugin's `Register` list. The native
   seam PC-13-01 used exists. `tasks.RunItems`, `tasks.Fixed`, `tasks.Sequential` exist in `core/tasks`.
1. **Compute the groups** with this script (deterministic: sorted file paths, whole files only, greedy packing to at
   most 15 definitions per group, at most 7 groups):

```bash
python3 - <<'PY'
# Compute the T6 file groups at S5r4's base (run from the worktree root). Deterministic: same base, same output.
import re, pathlib, sys
root = pathlib.Path("internal/plugins/maintenance")
plugin = (root / "plugin.go").read_text()
m = re.search(r"func \(p \*Plugin\) Register\(r sdk\.Registry\) error \{(.*?)\n\}", plugin, re.S)
if not m: sys.exit("STOP: Register(r sdk.Registry) not found in plugin.go")
funcs = re.findall(r"^\s*p\.([A-Za-z0-9]+Def)\(\),", m.group(1), re.M)
where = {}
for f in sorted(root.glob("*.go")):
    if f.name.endswith("_test.go"): continue
    src = f.read_text()
    for fn in funcs:
        if re.search(r"^func \(p \*Plugin\) %s\(" % fn, src, re.M): where[fn] = f.as_posix()
missing = [fn for fn in funcs if fn not in where]
if missing: sys.exit("STOP: no file for %s" % missing)
per_file = {}
for fn in funcs: per_file.setdefault(where[fn], []).append(fn)
groups, cur, n = [], [], 0
for path in sorted(per_file):
    c = len(per_file[path])
    if c > 15: sys.exit("STOP: %s alone holds %d defs (> 15)" % (path, c))
    if cur and n + c > 15: groups.append(cur); cur, n = [], 0
    cur.append(path); n += c
if cur: groups.append(cur)
if len(groups) > 7: sys.exit("STOP: %d groups needed (> 7); the planner must add a sub-batch" % len(groups))
print("# T6 file groups: total defs %d, files %d, groups %d" % (len(funcs), len(per_file), len(groups)))
for g, paths in enumerate(groups, 1):
    for path in paths:
        for fn in per_file[path]: print(g, path, fn)
PY
```

   At HEAD the script STOPs with "8 groups needed" because the six T3/T5c definitions are still listed. At S5r4's
   base it is expected to print 7 groups (98 definitions in 85 files, sizes 14, 13, 15, 15, 15, 15, 11, if T4
   converted nothing). If it STOPs, **STOP and report** its message. Save its output, header line included, as
   `internal/plugins/maintenance/testdata/t6_groups.txt` (name coined here) in the first commit.
2. **Restructure registration, behaviour-neutral** (its own commit). Create `register_t6_g1.go` … `register_t6_g7.go`
   in `internal/plugins/maintenance/` (names coined here). Each holds
   `func (p *Plugin) t6G<k>Defs() []sdk.OperationDef` returning exactly its group's v2 definitions, in the order they
   appear in today's `Register` list, and `func (p *Plugin) t6G<k>Tasks() []tasks.Definition` returning `nil`. A group
   the script did not produce still gets its file, both functions returning `nil`. In `plugin.go`, replace the
   literal list in `Register` with the concatenation `t6G1Defs()…t6G7Defs()` (keeping any non-T6 entry the base still
   has, which should be none), and add `func (p *Plugin) TaskDefinitions() []tasks.Definition` (coined) returning
   `t6G1Tasks()…t6G7Tasks()`, handed to books through the seam. After this commit every sub-batch edits only its
   own group's files and its own `register_t6_g<k>.go`.
3. **Test first for group 1:** generate `testdata/t6_g1_concurrency.golden` (one line per `registry.RunItems` call in
   group 1's files: file, enclosing func, the `Concurrency` expression text or `<none>`) and add
   `t6_group1_test.go` (Tests section).
4. **Convert group 1** by the recipe below; move each converted def from `t6G1Defs()` to `t6G1Tasks()`.
5. **Marker:** in `ci/stage.txt` change `release: S5r3` to `release: S5r4` (first PR of `S5r4`).
6. Run the acceptance block; changelog fragment; headers.

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

- `TestT6GroupsCoverRegister` (coined), in `t6_groups_test.go`: every line of `t6_groups.txt` names a def that is in
  exactly one `t6G<k>Defs()` or (as a `LegacyDefID`) one `t6G<k>Tasks()`; no file appears in two groups; the union
  of IDs equals the set of IDs the `Register` list held at the base (the IDs are read from the defs the lines name).
  It must pass after Step 2 and stay green through PC-13-13.
- `TestT6Group1Native` (coined): `t6G1Defs()` is empty; `t6G1Tasks()` holds one definition per line of group 1 in
  `t6_groups.txt`, each with kind `books.<def ID>` and `LegacyDefID` = the def ID.
- `TestT6Group1ParallelismOneToOne` (coined): an AST check over the group's files: every `tasks.RunItems`/`tasks.RunSeq`
  call's parallelism argument is `tasks.Fixed(<expr>, "legacy Concurrency")` or `tasks.Sequential("legacy zero
  Concurrency")`, and `<expr>` equals the text recorded for that call in `t6_g1_concurrency.golden` (`<none>` ↔
  `Sequential`). The count of native loops equals the golden's line count.

## Acceptance

```bash
F=$(awk '$1==1{print $2}' internal/plugins/maintenance/testdata/t6_groups.txt | sort -u)
grep -nE '(ops)?registry\.RunItems\(' $F | grep -v _test | wc -l                                        # 0
grep -lE '"github.com/falkcorp/audiobook-organizer/(internal/operations/registry|pkg/plugin/sdk)"' $F | wc -l   # 0
grep -n 'tasks\.CPU()' $F | wc -l                                                                     # 0
grep -c . internal/plugins/maintenance/testdata/t6_g1_concurrency.golden                           # = your loop count
go test ./internal/plugins/maintenance/ -run 'TestT6Group1|TestT6GroupsCoverRegister' -count=1        # ok
go test ./internal/plugins/maintenance/... ./internal/plugins/ -count=1                                # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile' -count=1                     # ok (op-ID ledger green)
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden                            # no output
grep -n '^release:' ci/stage.txt                                                                      # release: S5r4
ls internal/plugins/maintenance/register_t6_g*.go | wc -l                                             # 7
make ci                                                                                               # exit 0
```

"Declared Parallelism equals the legacy Concurrency (1:1)" is proven by `TestT6Group1ParallelismOneToOne` and by the PR description's
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
  (`extraction-gates`, `docker-image`, `vet-darwin`, `vet-integration`, and `compat-rehearsal`, because this PR changes `release:`), then
  `gh pr merge <n> --rebase`. Never squash, never `--admin` past a required check (14 Q-I).
- CLAUDE.md concurrency rules: every converted loop keeps its HEAD worker count 1:1 (`Fixed(n, "legacy
  Concurrency")` or `Sequential("legacy zero Concurrency")`); any parallelisation is a separate PR.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`.

## Planner notes

- The inventory's T6 rows list only `internal/plugins/maintenance/<group-k>`, but every maintenance-plugin def is
  registered from one literal list in `plugin.go`'s `Register`, so seven sub-batches would all edit `plugin.go` and
  collide. This brief makes PC-13-07 (serial, first) split that list into seven `register_t6_g<k>.go` files and
  commit the partition `t6_groups.txt`; add `plugin.go`, those files and the testdata file to PC-13-07's row.
- At HEAD the 104 listed defs need 8 groups of at most 15 (whole files); the T3 and T5c conversions remove 6 first,
  leaving 98 in 7 groups. If T4 (PC-13-03, SPEC-GATED) converts generic defs out of this plugin first, the groups
  shrink and some sub-batches may be empty; the script reports the real count.
- 05-tasks says "104 defs"; that count includes `repairs.plan`/`repairs.apply` (T3) and the four iTunes defs (T5),
  so T6's real count is 98 or fewer.
