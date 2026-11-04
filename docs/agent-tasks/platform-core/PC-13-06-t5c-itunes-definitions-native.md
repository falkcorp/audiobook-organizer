<!-- file: docs/agent-tasks/platform-core/PC-13-06-t5c-itunes-definitions-native.md -->
<!-- version: 1.0.0 -->
<!-- guid: 187d7dee-393d-41e8-a2b9-86ca851a1908 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-06 — T5c: iTunes definitions native, with the four importer.go v1 checkpoint writers; pkg/plugin/sdk unused

| Field | Value |
|---|---|
| ID | PC-13-06 |
| Tier | **T1 Sonnet/medium** — six registered definitions and four checkpoint writers, mechanical |
| Wave | W13 (stage S5, release `S5r3`); runs beside PC-13-04 and PC-13-05 under `/parallel-sweep` (file-disjoint) |
| Depends on | PC-13-03 |
| Trailer | `Stage-Release: S5 3` on every commit |
| Spec | 05-tasks §5T.8.2 row T5 ("iTunes 7 (with the 4 `importer.go` v1 checkpoint writers)"; gate "`pkg/plugin/sdk` unused"); §5T.6.2 "v1 checkpoints" row; §5T.3.1-§5T.3.3; `docs/plans/platform-core/raw/06-tasks-ops.md` §6 rows 68-74 (the iTunes class); digest S5 §5 |

## Gate

Merges only after PC-13-03 has merged. No `ci/stage.txt` change.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-06 -b pc-13-06-t5c-itunes-native origin/main
cd ../audiobook-organizer-pc-13-06
git rebase origin/main
npm ci --prefix web
git worktree list
```

## Goal

The iTunes definitions become native books tasks: `itunes.import` (registered by `internal/server/itunes_ops.go`,
running `internal/itunes/service`'s importer), `itunes.position-sync` (a refusing stub in `internal/plugins/itunes`),
and the four iTunes definitions the maintenance plugin registers (`itunes.clone-into-library`, `itunes.heal`,
`itunes.playlist-import`, `itunes.regroup`). The importer's four v1 checkpoint writers move to `rc.Checkpoint`. The
excluded, unregistered stub `itunes.import` in `internal/plugins/itunes/import.go` is deleted. Afterwards no iTunes
package imports `pkg/plugin/sdk` or `internal/operations/registry`.

## Exact files

Modify (inventory: `internal/itunes/`, `internal/itunes/service/`, `pkg/plugin/sdk/`; the rest under Planner notes):

- `internal/itunes/service/importer.go` (checkpoint writers `:626,636,725,732`, `LoadCheckpoint` `:361`,
  `ClearState` `:375,413,737`)
- `internal/server/itunes_ops.go` (`RegisterITunesImportOp`, `:41`)
- `internal/plugins/itunes/` (`plugin.go` `OperationDefs` `:70`, `import.go` (deleted), `position_sync.go`,
  `adapter.go`, `register.go`)
- `internal/plugins/maintenance/itunes_clone_into_library.go` (`:181`), `reconcile.go` (`itunesHealDef`, `:21`),
  `itunes_playlist_import.go` (`:82`), `itunes_regroup.go` (`:72`), and the four matching entries of the maintenance
  plugin's `Register` list in `internal/plugins/maintenance/plugin.go`
- one `changelog.d/` fragment

`pkg/plugin/sdk/` itself is not edited unless a symbol becomes unreferenced repo-wide (see Acceptance).

## Re-verify greps (HEAD `f07f516be`; identical at `748fd787a`)

```bash
grep -n 'func (s \*Server) RegisterITunesImportOp' internal/server/itunes_ops.go                       # 1 (:41)
grep -nE '\b[a-z]+\.SaveCheckpoint\(' internal/itunes/service/importer.go | wc -l                      # 4 (:626,636,725,732)
grep -nE '\b[a-z]+\.(LoadCheckpoint|ClearState)\(' internal/itunes/service/importer.go | wc -l         # 4 (:361 Load; :375,413,737 Clear)
grep -n 'func (p \*Plugin) importDef\|func (p \*Plugin) positionSyncDef' internal/plugins/itunes/*.go   # 2
grep -n 'func (p \*Plugin) itunesCloneIntoLibraryDef\|func (p \*Plugin) itunesHealDef\|func (p \*Plugin) itunesPlaylistImportDef\|func (p \*Plugin) itunesRegroupDef' internal/plugins/maintenance/*.go   # 4
grep -rln '"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"' --include=*.go . | grep -v '^./pkg/plugin/sdk/' | sed 's|/[^/]*$||' | sort | uniq -c
#   HEAD: internal/importer 3, internal/plugins/acoustid 13, dedup 45, deluge 6, itunes 5, maintenance 129,
#         metafetch 6, internal/reconcile 1, internal/scheduler 1, internal/server 2, tools/cmd/sdkguard 1
```

| Def ID | Registered by | Notes |
|---|---|---|
| `itunes.import` | `internal/server/itunes_ops.go:43` | runs `Importer.Execute`; holds the four checkpoint writers |
| `itunes.position-sync` | `internal/plugins/itunes/position_sync.go:16` | a stub whose `Run` refuses; keep the refusal (`plugin.go:70-85` comment) |
| `itunes.clone-into-library` | `internal/plugins/maintenance/itunes_clone_into_library.go:181` | |
| `itunes.heal` | `internal/plugins/maintenance/reconcile.go:21` | restart-with-checkpoint (raw/06 row 69) |
| `itunes.playlist-import` | `internal/plugins/maintenance/itunes_playlist_import.go:82` | |
| `itunes.regroup` | `internal/plugins/maintenance/itunes_regroup.go:72` | |
| `itunes.import` (stub, unregistered) | `internal/plugins/itunes/import.go:17` | delete |

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

- **The importer's checkpoints** (05-tasks §5T.6.2): `SaveCheckpoint(imp.store, opID, "itunes_import", <phase>, ...)`
  at `:626,636,725,732` becomes `rc.Checkpoint(v)` of one value carrying the same phase and counters;
  `LoadCheckpoint` at `:361` becomes `rc.Restore(&v)`; the three `ClearState` calls are deleted. The importer
  receives a checkpoint function or the `*tasks.RC` from `itunes.import`'s `Run`. Lower `TestLegacyOpsWriters` by
  the four write sites (its T5 target). After a rollback an import in flight restarts from zero once (09 §9.2.1
  row 5).
- **`itunes.position-sync`** stays a refusing stub as a native task: "unknown op" is a worse error than a plain
  refusal (`plugin.go` comment).
- **The excluded stub** `importDef` (`internal/plugins/itunes/import.go`) is unregistered at HEAD; delete it with
  its file.

## Tests to write first

- `TestT5cNativeKinds` (coined): the six native kinds are `books.<def ID>` with `LegacyDefID` equal to the HEAD IDs,
  and neither the iTunes plugin's `OperationDefs()` nor the maintenance plugin's `Register` list holds an iTunes def.
- An importer resume test: a run interrupted after the `hash_validation` checkpoint resumes from it on the same build.

## Acceptance

```bash
grep -nE '\b[a-z]+\.(SaveCheckpoint|LoadCheckpoint|ClearState)\(' internal/itunes/service/importer.go | wc -l   # 0
grep -rn '"github.com/falkcorp/audiobook-organizer/internal/operations/registry"' internal/itunes internal/plugins/itunes | grep -v _test | wc -l   # 0
grep -rln '"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"' internal/plugins/itunes internal/itunes | wc -l   # 0
test ! -f internal/plugins/itunes/import.go && echo gone                                                # gone
go test ./internal/itunes/... ./internal/plugins/itunes/... ./internal/plugins/maintenance/ -run 'ITunes|Itunes|itunes|TestT5c' -count=1   # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile|ITunes' -count=1                # ok
go test ./... -run 'TestLegacyOpsWriters' -count=1                                                      # ok at its T5 target
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden                             # no output
grep -rln '"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"' --include=*.go . | grep -v '^./pkg/plugin/sdk/' | wc -l   # report the number; see Planner notes
make ci                                                                                                # exit 0
```

The plan's done-check "pkg/plugin/sdk has no importer" cannot hold in this PR (see Planner notes): report the
remaining importer count in the PR description instead of failing on it.

## Out of scope

The other 98-or-so maintenance-plugin definitions (T6); the `relink_missing_to_itunes`/`recompute_itunes_paths`/
`backfill_itunes_positions` maintenance jobs (already native since T2); any parallelism change.

## Rollback

Revert (binary swap). Runs keep their `LegacyDefID`; an iTunes import in flight restarts from zero once.

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

- Files missing from the row: `internal/server/itunes_ops.go` (where `itunes.import` is registered),
  `internal/plugins/itunes/`, and the four maintenance-plugin files plus `internal/plugins/maintenance/plugin.go`'s
  `Register` list. `internal/itunes/` (outside `service/`) needs no change at HEAD.
- The done-check "pkg/plugin/sdk has no importer" (05-tasks §5T.8.2 T5's gate "`pkg/plugin/sdk` unused") cannot hold
  at T5: at HEAD the maintenance plugin (129 files, T6), `internal/scheduler` (T7), `internal/server`,
  `internal/importer`, `internal/reconcile` and `tools/cmd/sdkguard` import it. Move the check to the last T6/T7
  PR (or T8), or restate it as "no T5 plugin package imports it".
- The T6 sub-batches must exclude the four iTunes defs this row converts (PC-13-07's grouping runs at S5r4's base,
  after this row, so it sees them gone).
