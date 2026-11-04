<!-- file: docs/agent-tasks/platform-core/PC-13-04-t5a-acoustid-metafetch-deluge-native.md -->
<!-- version: 1.0.0 -->
<!-- guid: 78df6b71-31b0-4696-b7fa-dc1a9ba0ae98 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-04 — T5a: acoustid, metafetch and Deluge plugin definitions native

| Field | Value |
|---|---|
| ID | PC-13-04 |
| Tier | **T1 Sonnet/medium** — 15 definitions, each a mechanical conversion by the recipe below |
| Wave | W13 (stage S5, release `S5r3`); runs beside PC-13-05 and PC-13-06 under `/parallel-sweep` (file-disjoint) |
| Depends on | PC-13-03 |
| Trailer | `Stage-Release: S5 3` on every commit |
| Spec | 05-tasks §5T.8.2 row T5 and the T0+T1 row's 1:1 parallelism rule; §5T.3.1-§5T.3.3, §5T.3.7, §5T.3.8 ("What replaces v2 cron strings"); 05-download §10 (Deluge rows); digest S5 §5 rules 8 and 22 |

## Gate

Merges only after PC-13-03 (the PR that set `release: S5r3`) has merged; that PR in turn waits for OWNER-14's
`S5r2` row. This PR does not change `ci/stage.txt`.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-04 -b pc-13-04-t5a-plugin-native origin/main
cd ../audiobook-organizer-pc-13-04
git rebase origin/main
npm ci --prefix web
git worktree list
```

## Goal

The 15 v2 definitions of the acoustid (8), metafetch (3) and Deluge (4) plugins become native `tasks.Task`
definitions registered by books, with every loop's parallelism equal to its legacy `Concurrency` (1:1). No
behaviour change (05-tasks §5T.8.2 T5: binary swap).

## Exact files

Modify: `internal/plugins/acoustid/`, `internal/plugins/metafetch/`, `internal/plugins/deluge/` (non-test and test
files); one `changelog.d/` fragment. Touch nothing else (PC-13-05 and PC-13-06 run beside you).

## Re-verify greps (HEAD `f07f516be`; identical at `748fd787a`)

```bash
# Definitions per plugin (expect 8, 3, 4 entries in each OperationDefs() list)
awk '/func \(p \*Plugin\) OperationDefs\(\)/,/^}/' internal/plugins/acoustid/plugin.go  | grep -cE '^\s+p\.[A-Za-z]+Def\(\),'   # 8
awk '/func \(p \*Plugin\) OperationDefs\(\)/,/^}/' internal/plugins/metafetch/plugin.go | grep -cE '^\s+p\.[A-Za-z]+Def\(\),'   # 3
awk '/func \(p \*Plugin\) OperationDefs\(\)/,/^}/' internal/plugins/deluge/plugin.go    | grep -cE '^\s+p\.[A-Za-z]+Def\(\),'   # 4
# Legacy loops to convert (expect 8 + 3 + 5 = 16 call sites)
grep -nE '(ops)?registry\.RunItems\(' internal/plugins/{acoustid,metafetch,deluge}/*.go | grep -v _test | wc -l   # 16
# Sequential markers (expect 2: acoustid/reset_all.go and deluge/path_update.go)
grep -rn 'runitems:sequential' internal/plugins/acoustid internal/plugins/deluge | grep -v _test | wc -l   # 2
# The one v2 cron in scope (expect 1: deluge/protected_paths.go:31)
grep -n 'Schedule:\s*new(' internal/plugins/deluge/protected_paths.go
# The one v2 event trigger in scope and its publishers (expect 1 declaration; 0 publishers outside the plugin)
grep -n 'EventName: "book.relocated"' internal/plugins/deluge/path_update.go
grep -rn '"book.relocated"' --include=*.go internal pkg | grep -v _test | grep -v '^internal/plugins/deluge/' | wc -l   # 0
```

The 15 definitions at HEAD (ID — file):

| Plugin | Def ID | File |
|---|---|---|
| acoustid | `acoustid.scan` | `scan.go:20` |
| acoustid | `acoustid.backfill` | `backfill.go:270` |
| acoustid | `acoustid.fingerprint-rescan` | `fingerprint_rescan.go:52` |
| acoustid | `acoustid.reset-all` | `reset_all.go:35` |
| acoustid | `acoustid.lsh-backfill` | `lsh_backfill.go:60` |
| acoustid | `acoustid.lookup-online` | `online_lookup.go:51` |
| acoustid | `acoustid.fingerprint-duration-repair` | `duration_backfill.go:39` |
| acoustid | `acoustid.window-backfill` | `window_backfill.go:105` (const `windowBackfillDefID`) |
| metafetch | `metafetch.calibrate-scoring` | `calibrate_scoring.go:121` |
| metafetch | `metafetch.asin-backfill` | `asin_backfill.go:77` (const `asinBackfillOpID`) |
| metafetch | `metafetch.pcache-sweep` | `pcache_sweep.go:54` (const `pcacheSweepOpID`) |
| deluge | `deluge.protected-paths-sync` | `protected_paths.go:18` |
| deluge | `deluge.centralize` | `centralization.go:30` |
| deluge | `deluge.path-update` | `path_update.go:25` |
| deluge | `deluge.link-backfill` | `link_backfill.go:35` (const `linkBackfillOpID`) |

The 16 loops at HEAD: `acoustid/backfill.go:391`, `lsh_backfill.go:121`, `reset_all.go:132` (`Concurrency:
runtime.NumCPU()`), `reset_all.go:227` (sequential marker), `window_backfill.go:582,591,720,1138`;
`metafetch/asin_backfill.go:573,604` (options from `r.options(...)`), `calibrate_scoring.go:1044` (`Concurrency:
runtime.NumCPU()`); `deluge/centralization.go:270` (`Concurrency: centralizeConcurrency`), `link_backfill.go:899,1128,1558`,
`path_update.go:148` (sequential marker). Read each call's options at your base and record its `Concurrency`
expression in the PR description before converting it.

Zero hits on an edit target, or a count that moved, means **STOP and report**.

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

- **Deluge kinds** follow 05-download §10 ("`deluge.path-update`, `deluge.centralize`, `deluge.link-backfill`,
  `deluge.protected-paths-sync` … → books tasks `books.download.*` calling `Pool`"): kind
  `books.download.<name after "deluge.">` (e.g. `books.download.centralize`; the suffix is coined here, the prefix
  is 05-download's), `LegacyDefID` the HEAD ID, and `FormerKinds` adding the kind the adapter used at your base if it
  differs. Their bodies keep calling HEAD's Deluge client: `Pool` (`module.Deps.Downloads`) lands at S7, after this
  release (Planner notes).
- **`deluge.protected-paths-sync`'s cron** `*/30 * * * *` is dropped (recipe rule 7). 05-download's "gets a real
  schedule" is a behaviour change outside T5 (14 Q-C: re-add individually as a real `Schedule` by owner decision).
- **`deluge.path-update`'s trigger** on `book.relocated` is dropped: nothing in the repository publishes that event
  (re-verify grep, 0 publishers) and its handler is a placeholder (`path_update.go:73-90`). If the publisher count is
  not 0 at your base, **STOP**.
- **`acoustid.backfill`'s resume** (`reporter.Checkpoint(BackfillParams{AfterBookID: cursor})`, `backfill.go:400`)
  keeps its value through `rc.Checkpoint`/`rc.Restore` (recipe rule 4). The resume rehearsal of 09 S5 ("kill -9 mid
  `books.acoustid.backfill`, restart, checkpoint resumes") must still pass on this build.

## Tests to write first

- `TestT5aNativeKinds` (name coined here), one per plugin package (`internal/plugins/<p>/native_kinds_test.go`): the
  plugin's native definitions have exactly the kinds and `LegacyDefID`s of the table above, and the plugin's v2
  `OperationDefs()` list is empty.
- Keep every existing test of the three packages green; adapt only their construction of the def.

## Acceptance

```bash
grep -nE '(ops)?registry\.RunItems\(' internal/plugins/{acoustid,metafetch,deluge}/*.go | grep -v _test | wc -l      # 0
grep -nE 'tasks\.RunItems\(|tasks\.RunSeq\(' internal/plugins/{acoustid,metafetch,deluge}/*.go | grep -v _test | wc -l # 16 (the base count)
grep -rn '"github.com/falkcorp/audiobook-organizer/internal/operations/registry"' internal/plugins/{acoustid,metafetch,deluge} | grep -v _test | wc -l   # 0
grep -rn '"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"' internal/plugins/{acoustid,metafetch,deluge} | grep -v _test | wc -l              # 0
grep -rhoE 'tasks\.(Fixed\([^;]*"legacy Concurrency"\)|Sequential\("legacy zero Concurrency"\))' internal/plugins/{acoustid,metafetch,deluge}/*.go | wc -l   # 16
go test ./internal/plugins/acoustid/... ./internal/plugins/metafetch/... ./internal/plugins/deluge/... -count=1     # ok
go test ./internal/plugins/ -run . -count=1                                                    # runitems lint still green
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile' -count=1             # ok (op-ID ledger green)
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden                    # no output
make ci                                                                                       # exit 0
```

"Declared Parallelism equals the legacy Concurrency (1:1)" is proven by the PR description's table (call site,
legacy `Concurrency` expression, native `Parallelism` expression), one row per loop, 16 rows, and the two greps above.

## Out of scope

Any parallelism change; 05-download's `Pool` rewrite (S7); giving `protected-paths-sync` a schedule; the dedup
(PC-13-05) and iTunes (PC-13-06) plugins; `pkg/plugin/sdk` itself.

## Rollback

Revert (binary swap). Runs keep their `LegacyDefID`, so the previous build resumes them; a def whose loop moved to
`Watermark` may restart from zero once (list them in the release-note draft; 09 §9.2.1 row 5).

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

- 05-download §10 maps the Deluge defs to `books.download.*` tasks "calling `Pool`", but `Pool` is S7 and T5 ships in
  `S5r3`; the kind prefix is taken now and the bodies keep HEAD's client until S7. 05-tasks §5T.8.2's default rule
  would give `books.deluge.*`; this brief follows 05-download.
- 05-download §10 also says `protected-paths-sync` "gets a real schedule", which contradicts T5's "no behaviour
  change" and 14 Q-C's default; this brief drops the cron and leaves the schedule to an owner decision.
- Moving a caller-wired checkpoint to `ItemsOptions.Watermark` makes the one-time restart after a rollback that
  09 §9.2.1 row 5 lists only for T2; 05-tasks §5T.8.2 says T5 has no behaviour change. Suggest adding T5/T6 to
  row 5's text.
