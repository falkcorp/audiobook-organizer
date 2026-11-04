<!-- file: docs/agent-tasks/platform-core/PC-13-15-t7b-server-ops-books-definitions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 215f17d0-6176-47f0-8496-2065a7ae9c4e -->
<!-- last-edited: 2026-11-04 -->

# PC-13-15 — T7b: server-package ops and scheduler.metadata-upgrade become books task definitions

| Field | Value |
|---|---|
| ID | PC-13-15 |
| Tier | **T2 Sonnet/high** — about 49 definitions, mechanical by the recipe, plus v1 result, checkpoint and cursor writers whose stored positions must survive |
| Wave | W13 (stage S5, release `S5r5`; the last PR of `S5r5`) |
| Depends on | PC-13-14 |
| Trailer | `Stage-Release: S5 5` on every commit |
| Spec | 05-tasks §5T.8.2 row T7 ("Server-package ops (`internal/server/*_op*.go`, incl. `metadata_ops.go`'s v1 checkpoint writer) and `scheduler.metadata-upgrade` (its v1 cursor) → `books`"); §5T.6.2 "v1 families with live books writers"; §5T.8.1 rows `internal/operations` (`BulkMetadataFetchParams` → `internal/server`) and `internal/operations/registry` (the v1 status bridge stays in opsbridge); 04 §8.3 registry row ("all of them, through 05-tasks T2–T7; T8 ships only at a generated count of 0"); digest S5 §5 |

## Gate

Merges only after PC-13-14 (which set `release: S5r5`) has merged. No `ci/stage.txt` change. OWNER-17's soak of
`S5r5` counts from the deploy that carries this PR (plan §(b) decision 2).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-15 -b pc-13-15-t7b-server-ops origin/main
cd ../audiobook-organizer-pc-13-15
git rebase origin/main
npm ci --prefix web
git worktree list
```

## Goal

Every v2 `OperationDef` still registered by `internal/server` (the `addOpRegistrar` entries of
`internal/server/*_op*.go` and friends) and `scheduler.metadata-upgrade` (moved into `internal/server` by
PC-13-14) becomes a native books task. Their v1 result writers move to the run's result, `metadata_ops.go`'s v1
checkpoint to `rc.Checkpoint`, and the metadata-upgrade cursor to a books-keyspace cursor. After this PR no ABO
package outside `internal/operations/registry` (opsbridge) imports the registry, which is T8's precondition
(04 §8.3).

## Exact files

Modify (inventory): `internal/server/` (non-test and test files), `internal/server/metadata_ops.go`. Also needed
(Planner notes): `internal/metabatch/upgrade.go` (the cursor), `internal/organizer/service.go:304` (a v1
`ClearState` on the organize path), and any other importer the census in Step 1 reports. One `changelog.d/`
fragment.

## Re-verify greps (HEAD `f07f516be`; `internal/server` differs from `748fd787a` only outside these lines)

```bash
# v2 registrars in internal/server (HEAD: 48 call lines; PC-13-01 removed the maintenance-job bridge (:316 of
# maintenance_job_op.go) and PC-13-06 the iTunes import (:83 of itunes_ops.go), so expect 46 at your base, plus
# the scheduler.metadata-upgrade registrar PC-13-14 moved in)
grep -rn 'addOpRegistrar(func' internal/server/*.go | grep -v _test | wc -l
# v1 result writers in internal/server (HEAD: 10: metadata_candidate_op.go:345, metadata_ops.go:391,440,1056,1105,
# metadata_batch_candidates.go:954,1029, bulk_apply_preview.go:437,483, apply_when_scanned_op.go:430)
grep -rnE '\.CreateOperationResult\(' internal/server/*.go | grep -v _test | wc -l
# v1 checkpoint in metadata_ops.go (HEAD: SaveCheckpoint :1326, ClearState :1362)
grep -nE '\b[a-z]+\.(SaveCheckpoint|ClearState)\(' internal/server/metadata_ops.go
# BulkMetadataFetchParams uses (HEAD: metadata_ops.go:196,796,885; it moved into internal/server at S5)
grep -n 'BulkMetadataFetchParams' internal/server/metadata_ops.go
# the metadata-upgrade cursor (HEAD: upgradeCursorKey :162, read :175, write :199)
grep -n 'upgradeCursorKey' internal/metabatch/upgrade.go
# legacy loops in internal/server (HEAD: 8)
grep -rnE '(ops)?registry\.RunItems\(' internal/server/*.go | grep -v _test | wc -l
# the importer census (HEAD: 38 directories; the T2-T7a rows remove most; see Step 1)
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/operations/registry"' --include=*.go . | grep -v '^./internal/operations/registry/' | sed 's|/[^/]*$||' | sort | uniq -c
```

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

## Steps

1. **Census first.** Run the last re-verify grep. Every directory it lists must be one this PR changes
   (`internal/server`, `internal/server/handlers/...` and their `mocks`, `internal/metabatch`, `internal/organizer`)
   or one whose import is only the type of a def this PR converts. If it lists a directory outside that set
   (at HEAD candidates are `.`, `cmd`, `internal/catalog`, `internal/metadata`, `internal/opscatalog`,
   `internal/reconcile`, `internal/testutil`, `pkg/ops/opstest`, `pkg/plugin/sdk`, `tools/cmd/sdkguard`), **STOP**
   and report the list: the plan gives no row for them (Planner notes).
2. **Inventory.** List every v2 def `internal/server` registers at your base (ID, file, legacy `Concurrency` of each
   loop) in the PR description. 05-tasks' "about 49" is informative.
3. **Test first.** `TestT7bNativeKinds` (coined): the server registers no v2 def; the native definitions have kinds
   `books.<def ID>` and `LegacyDefID`s equal to the inventory's IDs (count = the inventory's). Add a
   `t7b_concurrency.golden` + `TestT7bParallelismOneToOne` pair as PC-13-05 does.
4. Convert by the recipe, file by file, each commit building.
5. **v1 writers.**
   - The 10 `CreateOperationResult` sites become the run's result (the same fields; a slice where a def writes
     several rows).
   - `metadata_ops.go`'s `SaveCheckpoint(store, opID, "bulk_write_back", "writing", done, total)` becomes
     `rc.Checkpoint` of the same value; the `ClearState` at `:1362` goes. `internal/organizer/service.go:304`'s
     `ClearState` goes with the organize op's conversion.
   - **The metadata-upgrade cursor** is carried from one run to the next (`upgrade.go:156-162` comment), so it is
     not a run checkpoint: store it as a books-keyspace cursor (05-tasks §5T.6.2: "moves to `rc.Checkpoint` or a
     books-keyspace cursor"), in books' own key (name coined; say it in the PR), with the same value encoding. On the
     first run, when the new key is absent, read the old `opstate` key once and continue from it, so the sweep does
     not restart from the top. After a rollback the older build reads its own (older) cursor and re-searches from
     there; say so in the release-note draft.
   - Lower `TestLegacyOpsWriters` to its **final target**: only the two `UpdateOperationStatus` sites that stay in
     opsbridge (`legacy_op_status.go:244`, `legacy_backfill.go:218`; 05-tasks §5T.6.2), which T8 deletes.
6. **The v1 status bridge** stays in opsbridge, driven by the books subscriber on `core.task.finished.v1`, and the
   backfill keeps its server op (`internal/server/legacy_backfill_op.go`, `operations.backfill-legacy-status`),
   converted like the others (05-tasks §5T.8.1 registry row).
7. Run the acceptance block; changelog; headers.

## Acceptance

```bash
grep -rn 'addOpRegistrar(func' internal/server/*.go | grep -v _test | wc -l                                  # 0
grep -rnE '\.CreateOperationResult\(' --include=*.go internal/server | grep -v _test | wc -l                 # 0
grep -nE '\b[a-z]+\.(SaveCheckpoint|LoadCheckpoint|ClearState)\(' --include=*.go -r internal/server internal/metabatch internal/organizer | grep -v _test | wc -l   # 0
grep -n 'GetOperationState(upgradeCursorKey)\|SaveOperationState(upgradeCursorKey' internal/metabatch/upgrade.go | wc -l   # 1 (the one-time read of the old key only)
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/operations/registry"' --include=*.go . | grep -v '^./internal/operations/registry/' | wc -l   # 0
go test ./internal/server/... ./internal/metabatch/... ./internal/organizer/... -count=1                      # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile|TestT7b' -count=1                   # ok
go test ./... -run 'TestLegacyOpsWriters|TestS5_OperationsComponentStaysBooks|TestS5_OperationsTimelineLogDiscardParity' -count=1   # ok
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden                                  # no output
make ci                                                                                                     # exit 0
```

## Out of scope

Deleting `OperationDef`, opsbridge, `childop`, `opsmetrics` or v2 `RunItems` (T8, PC-14-01); any parallelism
change; schedule changes (PC-13-14 did them); the `/api/v1/operations` routes, which stay books' gin routes.

## Rollback

Revert (binary swap). Runs keep their `LegacyDefID`; the bulk write-back restarts from zero once; the metadata
upgrade continues from the older build's cursor (09 §9.2.1 row 5).

## Repo rules

- File headers (`.standards/instructions/file-headers.md`) on every new or changed file, version bumped; none on the
  `changelog.d/` fragment. One fragment per PR.
- Conventional commits, each ending with:
  ```
  Stage-Release: S5 5
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- Every commit builds (rebase merges). Push, `gh pr create`, wait for the required checks of 09 §9.2.1
  (`extraction-gates`, `docker-image`, `vet-darwin`, `vet-integration`), then
  `gh pr merge <n> --rebase`. Never squash, never `--admin` past a required check (14 Q-I).
- CLAUDE.md concurrency rules: every converted loop keeps its HEAD worker count 1:1 (`Fixed(n, "legacy
  Concurrency")` or `Sequential("legacy zero Concurrency")`); any parallelisation is a separate PR.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`.

## Planner notes

- Done-check "no `registry.` import outside opsbridge": at HEAD 38 directories import the registry, among them
  `.`, `cmd`, `internal/catalog`, `internal/metadata`, `internal/opscatalog`, `internal/reconcile`,
  `internal/testutil`, `pkg/ops/opstest`, `pkg/plugin/sdk` and `tools/cmd/sdkguard`, which no T2-T7 row lists. Unless
  PC-12-08 removed them, this row STOPs at Step 1; the plan needs a row (or this row's files extended) for them.
- Files missing from the row: `internal/metabatch/upgrade.go` (the metadata-upgrade cursor, a T7 writer per 05-tasks
  §5T.6.2) and `internal/organizer/service.go` (a v1 `ClearState` on the organize path).
- 05-tasks §5T.6.2 lists `retention_and_hygiene.go:339-374`'s v1 checkpoint deletes; they are deletes of old rows,
  not writes, and stay until `Prune` clears the family.
