<!-- file: docs/agent-tasks/platform-core/PC-13-01-t2-maintenance-jobs-native-tasks.md -->
<!-- version: 1.0.0 -->
<!-- guid: 36b1695e-54e8-4de7-8133-8fb1f2e93552 -->
<!-- last-edited: 2026-11-04 -->

# PC-13-01 — T2: the 34 MaintenanceJobs become native books.maintenance tasks; internal/maintenance deleted

| Field | Value |
|---|---|
| ID | PC-13-01 |
| Tier | **T1 Sonnet/medium** — a mechanical signature conversion, one checkpoint move per checkpointing job (but see Planner notes: one job's chunk journal is not mechanical) |
| Wave | W13 (stage S5, release `S5r2`; first PR of `S5r2`) |
| Depends on | OWNER-13 (the `S5r1` soak row) |
| Trailer | `Stage-Release: S5 2` on every commit |
| Spec | 05-tasks §5T.8.2 row T2; §5T.6.2 "v1 families with live books writers"; §5T.3.1, §5T.3.3, §5T.3.7; 09 S5 "Moves and batches"; 09 §9.2.1 rollback row 5; digest `docs/plans/platform-core/digests/S5.md` §5 "Batches" |

## Gate

The PR may be opened and made green at any time, but it **merges only after** OWNER-13 has merged a current
`S5r1` row into `ci/soak.txt`. This PR changes `release:` in `ci/stage.txt`, so `check_soak.py` in
`extraction-gates` refuses it until then, and `compat-rehearsal` runs on it (09 §9.2.1).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-13-01 -b pc-13-01-t2-maintenance-native origin/main
cd ../audiobook-organizer-pc-13-01
git rebase origin/main
npm ci --prefix web
git worktree list   # confirm you are in the new worktree, not the primary checkout
```

Do **not** run `go work init` in the worktree (CLAUDE.md).

## Goal

Every one of the 34 `maintenance.MaintenanceJob`s becomes a native `tasks.Task` of kind
`books.maintenance.<job-id>`, registered by books, keeping each job's body. Their v1 checkpoint writers move to
`rc.Checkpoint`, their v1 result and summary writers move to the run's result, and the package
`internal/maintenance` (the `MaintenanceJob` interface, the global `Register/Get/All` registry, the context
helpers) is deleted. `internal/maintenance/jobs` stays at its path (04 row `internal/maintenance/jobs`:
"unchanged"). No kind, param shape or effect changes, so rollback is a binary swap; the two checkpointing jobs
resume from zero once after a rollback (09 §9.2.1 row 5).

## Exact files

Modify:

- `internal/maintenance/job.go`, `internal/maintenance/progress.go`, `internal/maintenance/registry.go`,
  `internal/maintenance/result.go`, `internal/maintenance/job_test.go`, `internal/maintenance/result_test.go`
  (all deleted at the end; their still-needed types move into `internal/maintenance/jobs`)
- `internal/maintenance/jobs/*.go` (the 34 job files and the 66 files that import `internal/maintenance`)
- `internal/server/maintenance_job_op.go`, `internal/server/maintenance_dispatcher.go`, `internal/server/server.go`
  and the test files that import `internal/maintenance` (list in re-verify grep 3, including
  `internal/server/handlers/abs/user_state_deadlock_test.go` and `internal/audiobooks/revert_merge_backfill_test.go`;
  see Planner notes)
- `ci/stage.txt` (`release: S5r1` → `release: S5r2`)
- one new `changelog.d/` fragment (no file header)

Create: none required beyond the changelog fragment. You may add test files under `internal/maintenance/jobs/`.

Do **not** touch: `internal/books/module.go` (see Step 0.4), `internal/plugins/**`, `internal/scheduler/**`,
`core/**`, `ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`.

## Re-verify greps (run at your base; counts below are at HEAD `f07f516be`, identical at `748fd787a` unless noted)

```bash
# 1. The 34 job registrations (expect 34)
grep -rn 'maintenance.Register(' --include=*.go internal/maintenance/jobs | grep -v _test | wc -l
# 2. The job Run signature (expect 1 hit, job.go:498)
grep -n 'Run(ctx context.Context, store JobStore, reporter ProgressReporter, dryRun bool) error' internal/maintenance/job.go
# 3. Importers of internal/maintenance outside its directory (expect 15 files at HEAD: 2 non-test,
#    internal/server/maintenance_job_op.go and internal/server/maintenance_dispatcher.go; 13 tests: 11 under
#    internal/server/*_test.go, internal/server/handlers/abs/user_state_deadlock_test.go and
#    internal/audiobooks/revert_merge_backfill_test.go)
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/maintenance"' --include=*.go . | grep -v '^./internal/maintenance/'
# 4. Files inside jobs/ that import internal/maintenance (expect 66)
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/maintenance"' --include=*.go internal/maintenance/jobs | wc -l
# 5. The v2 bridge that registers one OperationDef per job (expect 1 hit, maintenance_job_op.go:102)
grep -n 'func (s \*Server) RegisterMaintenanceJobOps' internal/server/maintenance_job_op.go
# 6. v1 checkpoint writers in jobs/ (HEAD: 2 SaveCheckpoint, 2 LoadCheckpoint, 2 ClearState;
#    at your base the package qualifier is whatever PC-12-05 rewired them to, e.g. contract.)
grep -rnE '\b[a-z]+\.(SaveCheckpoint|LoadCheckpoint|ClearState)\(' --include=*.go internal/maintenance/jobs | grep -v _test
# 7. v1 result and summary writers in jobs/ (HEAD: 5 CreateOperationResult, 3 SaveOperationSummaryLog)
grep -rnE '\.(CreateOperationResult|SaveOperationSummaryLog)\(' --include=*.go internal/maintenance/jobs | grep -v _test
# 8. The run-result bridge that already exists (expect 2 funcs: WithResultSetter result.go:26, SetResult result.go:36)
grep -n '^func WithResultSetter\|^func SetResult' internal/maintenance/result.go
# 9. W12 symbols this task needs (0 hits at HEAD; at your base each must be >= 1, else STOP)
grep -rn '^func Task\[' core/tasks/*.go                       # tasks.Task constructor (PC-12-01)
grep -rn 'LegacyDefID' internal/operations/registry/*.go | grep -v _test | head   # opsbridge adapter (PC-12-08)
grep -rln 'func TestLegacyOpsWriters' --include=*_test.go .   # the writer ratchet (no row creates it: see Planner notes)
```

The 34 job IDs at HEAD (file → `ID()`), each becoming kind `books.maintenance.<id>` with
`LegacyDefID: "maintenance.<id>"`:

`backfill_book_files.go` backfill-book-files · `backfill_file_hashes.go` backfill-file-hashes ·
`backfill_itunes_positions.go` backfill-itunes-positions · `backfill_merge_undo_pointers.go`
backfill-merge-undo-pointers · `backfill_metadata_source_hash.go` backfill-metadata-source-hash ·
`backfill_sync_ids.go` backfill-sync-ids · `bulk_deluge_import.go` bulk-deluge-import ·
`cleanup_empty_folders.go` cleanup-empty-folders · `cleanup_organize_mess.go` cleanup-organize-mess ·
`cleanup_series.go` cleanup-series · `dedup_books.go` dedup-books · `enrich_book_files.go` enrich-book-files ·
`fix_author_narrator_swap.go` fix-author-narrator-swap · `fix_file_modes.go` fix-file-modes ·
`fix_read_by_narrator.go` fix-read-by-narrator · `fix_version_groups.go` fix-version-groups ·
`merge_chapter_groups.go` merge-chapter-groups · `normalize_primary_flags.go` normalize-primary-flags ·
`prune_book_snapshots.go` prune-book-snapshots · `purge_ua_duplicates.go` purge-unknown-author-duplicates ·
`recompute_book_aggregates.go` recompute-book-aggregates · `recompute_itunes_paths.go` recompute-itunes-paths ·
`refetch_missing_authors.go` refetch-missing-authors · `relink_missing_to_itunes.go` relink-missing-to-itunes ·
`relink_report.go` relink-report · `repoint_version_primary.go` repoint-version-primary ·
`retention_and_hygiene.go` retention-and-hygiene · `revert_metadata_fetch.go` revert-metadata-fetch ·
`scan_chapter_groups.go` scan-chapter-groups · `scan_composer_tags.go` scan-composer-tags ·
`scan_duplicate_files.go` scan-duplicate-files · `scan_duration_mismatch.go` scan-duration-mismatch ·
`scan_metadata_hash_dups.go` scan-metadata-hash-dups · `sweep_pebble_metrics_ttl.go` sweep-pebble-metrics-ttl.

v1 writers at HEAD that this PR moves (05-tasks §5T.6.2 table):

| Writer | HEAD site | Moves to |
|---|---|---|
| checkpoint | `recompute_book_aggregates.go:230` SaveCheckpoint, `:160` LoadCheckpoint, `:245` ClearState | `rc.Checkpoint` / `rc.Restore` |
| checkpoint | `backfill_file_hashes.go:1315` SaveCheckpoint, `:1232` LoadCheckpoint, `:1412` ClearState | `rc.Checkpoint` / `rc.Restore` |
| chunk journal | `backfill_file_hashes.go:722-775` (`hbJournalStateID`, `writeJournal`, kept at `opstate:<opID>:params`) | see Step 5 (STOP rule) |
| result | `backfill_merge_undo_pointers.go:178`, `merge_chapter_groups.go:601`, `bulk_deluge_import.go:112`, `scan_composer_tags.go:172,227` CreateOperationResult | the run's result |
| summary | `backfill_book_files.go:197`, `recompute_book_aggregates.go:265`, `backfill_file_hashes.go:1440` SaveOperationSummaryLog | the run's result |

Any count that differs at your base, or a zero-hit edit target, means **STOP and report** (plan §(f) drift policy).
Counts are informative at the evidence commit; the done-check uses the value at your base (09 §9.3).

## Steps

0. **Preconditions (STOP on any failure).**
   1. Re-verify grep 9: `core/tasks` exists with `Task`, `TaskSpec`, `Common`, `RC`; opsbridge sets `LegacyDefID`.
   2. `TestLegacyOpsWriters` exists. If grep 9's last line prints nothing, **STOP** and report "TestLegacyOpsWriters
      has no creator" (Planner notes). Do not write the ratchet yourself.
   3. Read the opsbridge adapter that wraps a v2 `OperationDef` as a `tasks.Definition` (the function that sets
      `LegacyDefID`, found by grep 9). Write down, for `maintenance.<id>`, exactly which `tasks.Common` it builds
      from the v2 def `RegisterMaintenanceJobOps` registers. **That adapter output is the reference** for every
      field of the native definition.
   4. Find the seam through which books' `module.Registrar.Tasks(defs ...tasks.Definition)` (06 §6.4.3) receives
      definitions at your base: `grep -rn 'Tasks(' internal/books/module.go internal/server/*.go | grep -v _test`.
      If native `tasks.Definition`s can be handed to it without editing `internal/books/module.go`, use it. If the
      only path is v2 `OperationDef`s, **STOP** and report "no native-definition seam" (Planner notes).
1. **Test first: freeze HEAD's catalogue.** Add `internal/maintenance/jobs/catalogue_test.go` with
   `TestJobCatalogueUnchanged` (name coined here): it renders, for every job in today's `maintenance.All()` order,
   `ID, Name, Description, Category, DefaultParams (JSON), CanResume, Permission` into
   `internal/maintenance/jobs/testdata/catalogue.golden`, generated **before** any other change and committed in
   the first commit. `GET /api/v1/maintenance/jobs` (`listMaintenanceJobs`) serves exactly these fields, so the
   golden pins its body. It must still pass unchanged at the end.
2. **Move the job contract into the jobs package.** Into `internal/maintenance/jobs/contract.go` (new file name
   coined here) move, unchanged in behaviour: `ProgressReporter`, `ProgressSeeder`, `PermissionAware`,
   `ParamsValidator`, `ExecutionPolicy`, `DefaultPolicy`, `RestartPolicy`, `JobStore` and its component
   interfaces, `ErrContinueInPlace`, `WithOperationID`/`OperationIDFromCtx`, `WithRawParams`/`RawParamsFromCtx`,
   `WithResultSetter`/`SetResult`, and a `Job` interface equal to today's `MaintenanceJob`. Rewrite every
   `maintenance.X` in `internal/maintenance/jobs` to the local `X` (re-verify grep 4: 66 files).
3. **Replace the init-time registry with an explicit list.** Delete the 34 `func init() { maintenance.Register(...) }`
   calls and add `func All() []Job` (name coined here) returning a fresh slice in **exactly** the order
   `TestJobCatalogueUnchanged` recorded (HEAD's init order), plus `func Get(id string) (Job, error)` with the same
   error text as HEAD's `maintenance.Get` (`maintenance: unknown job %q`). No package-level mutable registry.
4. **Native definitions.** Add `func Definitions(deps <what the server passes today>) []tasks.Definition` (name
   coined here) building, for each job `j` in `All()` order:
   ```go
   tasks.Task[json.RawMessage, struct{}]("books.maintenance."+tasks.Kind(j.ID()), tasks.TaskSpec[json.RawMessage, struct{}]{
       Common: <the adapter's Common for "maintenance."+j.ID(), field for field (Step 0.3)>, // incl. LegacyDefID: "maintenance."+id,
                                                                                           // LegacyPlugin: "maintenance", Perms.Start as the adapter set it
       Run: func(rc *tasks.RC, p json.RawMessage) (struct{}, error) {
           ctx := WithOperationID(WithRawParams(rc.Context(), p), string(rc.RunID()))
           ctx = withRunContext(ctx, rc)        // for Step 5's checkpoint helpers (name coined here)
           ctx = WithResultSetter(ctx, <the setter that stores v as the run's result, as the adapter does>)
           return struct{}{}, j.Run(ctx, store, newRCProgress(rc), rc.Mode() == tasks.ModePreview)
       },
   })
   ```
   `newRCProgress` (name coined here) is an unexported adapter in the jobs package implementing `ProgressReporter`
   and `ProgressSeeder` over `rc.Progress()` and `rc.Log()`, with the semantics the opsbridge Reporter-over-RC gives
   a v2 maintenance def at your base (copy its mapping; 05-tasks §5T.8.2 T0+T1 "a `Reporter` implemented over
   `RC`"). `store` is the same `JobStore` value `registerMaintenanceJobOp` passes today. The kind is
   `books.maintenance.<id>` (05-tasks §5T.8.2: def IDs map to kinds by prefixing the owning module); the stored kind
   stays `maintenance.<id>` through `LegacyDefID` (5T.3.1), so an older build still resumes and shows these runs.
5. **Move the v1 writers.**
   - Checkpoints (recompute-book-aggregates, backfill-file-hashes): replace each v1 `SaveCheckpoint(store, opID,
     ...)` with `saveCheckpoint(ctx, v)` → `rc.Checkpoint(v)` and each `LoadCheckpoint` with `restoreCheckpoint(ctx,
     &v)` → `rc.Restore(&v)`, keeping the same Go value (the same `OperationState`-shaped struct) so the resume
     logic is unchanged. Delete the `ClearState` calls whose only purpose was the v1 checkpoint (a run's checkpoint
     ends with the run). After a rollback the older build finds no v1 checkpoint and starts over once (09 §9.2.1
     row 5; list both job IDs in the release-note draft).
   - The hash-backfill **chunk journal** (`backfill_file_hashes.go:722-775`) is a durability record written before
     any hash is stored. Move it into the same checkpoint value only if `rc.Checkpoint` commits synchronously at
     your base (05-tasks §5T.3.10: every run write commits through `storage.CommitLocked(..., storage.Sync, ...)`)
     **and** the journal and the checkpoint can share one value without changing the write order of
     `writeJournal`. Otherwise **STOP** and report; do not invent another store.
   - Results and summaries: replace each `CreateOperationResult` and `SaveOperationSummaryLog` call with
     `SetResult(ctx, v)` carrying the same fields, appended to one result value per run (a slice for the multi-row
     writers `scan_composer_tags.go:172,227`).
   - After this step re-verify greps 6 and 7 print **0** lines for `internal/maintenance/jobs`.
6. **Server wiring.** In `internal/server`: delete `RegisterMaintenanceJobOps`, `registerMaintenanceJobOp` and the
   `addOpRegistrar` line at `maintenance_job_op.go:316`; hand `jobs.Definitions(...)` to the seam of Step 0.4;
   rewrite `listMaintenanceJobs` and `runMaintenanceJob` (`maintenance_dispatcher.go:23,83`) onto `jobs.All()` /
   `jobs.Get()` and start runs through the tasks client (`Deps.Tasks`/`tasks.Client.Start` with kind
   `books.maintenance.<id>`), keeping HEAD's request and response bodies byte for byte. Remove the blank import
   `_ ".../internal/maintenance/jobs"` from `server.go:45` once nothing depends on `init()`.
7. **Delete `internal/maintenance`** (`git rm internal/maintenance/*.go`) and fix every remaining importer from
   re-verify grep 3 (tests included) to `internal/maintenance/jobs`.
8. **Marker.** In `ci/stage.txt` change `release: S5r1` to `release: S5r2`; leave `shipped_stage: S5`.
9. **Ratchet.** Lower `TestLegacyOpsWriters`'s expected count by the sites removed in Step 5 (its T2 target);
   it may only go down (05-tasks §5T.9).
10. Changelog fragment; file headers bumped on every changed file; run the acceptance block.

## Code contract (05-tasks §5T.3.1, §5T.3.3; quoted)

```go
func Task[P, R any](kind Kind, s TaskSpec[P, R]) Definition
type TaskSpec[P, R any] struct { Common; Run func(rc *RC, p P) (R, error) }
// Common: Title, Help, Effects, Perms, Resume, Cancel, ConcurrencyKey, NotWhile, Priority, Timeout,
// ProgressTimeout, Schedule, Approval, Ready, Lanes, Subjects, Dedupe, Debounce, Yields, Notify,
// FormerKinds []Kind, LegacyDefID Kind, LegacyPlugin string
func (rc *RC) Context() context.Context
func (rc *RC) RunID() RunID
func (rc *RC) Mode() Mode            // ModePreview | ModeLive
func (rc *RC) Log() *slog.Logger
func (rc *RC) Progress() *Progress
func (rc *RC) Checkpoint(v any) error
func (rc *RC) Restore(v any) (bool, error)
var ErrContinueInPlace = errors.New("tasks: yielded before deadline; continue in place")
```

Map `maintenance.ErrContinueInPlace` (today an alias of `opsregistry.ErrContinueInPlace`) to
`tasks.ErrContinueInPlace`; it is honoured only for `Resume == ResumeContinue` (5T.3.3), which the adapter's
`Common.Resume` already carries for the jobs that use it.

## Tests to write first

- `TestJobCatalogueUnchanged` (coined; Step 1) — committed before any production change.
- `TestNativeMaintenanceKinds` (coined): `jobs.Definitions(...)` returns 34 definitions (the count at your base),
  each `Kind()` equal to `"books.maintenance."+id`, each id unique, in `All()` order.
- Extend the existing `internal/server/maintenance_*_test.go` cases that enumerate `maintenance.All()` to
  `jobs.All()`; do not delete a case.

## Acceptance (commands and expected output)

```bash
ls internal/maintenance                                                      # prints only: jobs
grep -rn '"github.com/falkcorp/audiobook-organizer/internal/maintenance"' --include=*.go . | wc -l   # 0
grep -rn 'maintenance.Register(' --include=*.go . | wc -l                    # 0
grep -rnE '\.(CreateOperationResult|SaveOperationSummaryLog)\(' --include=*.go internal/maintenance/jobs | grep -v _test | wc -l   # 0
grep -rnE '\b[a-z]+\.(SaveCheckpoint|LoadCheckpoint|ClearState)\(' --include=*.go internal/maintenance/jobs | grep -v _test | wc -l # 0
grep -n '^release:' ci/stage.txt                                             # release: S5r2
go build ./... && (cd core && go build ./...)                                # exit 0
go test ./internal/maintenance/jobs/ -run 'TestJobCatalogueUnchanged|TestNativeMaintenanceKinds' -count=1   # ok
go test ./internal/server/ -run 'TestOpIDs_|TestOpLedgerEmbedMatchesFile|TestMaintenance' -count=1         # ok
git diff --exit-code origin/main -- internal/server/testdata/op_ids.golden  # no output (ledger unchanged)
go test ./... -run 'TestLegacyOpsWriters|TestOpsFamiliesWrittenOnlyByTaskstore|TestABOMetricsGatherNoDuplicateOrMissing|TestS5_OperationsComponentStaysBooks' -count=1   # ok
make ci                                                                      # exit 0
make -C core ci                                                              # exit 0 (no core change expected; run it anyway)
```

The op-ID ledger must not change: a native kind stores under its `LegacyDefID`, so no new op ID exists. If the
ledger test asks for a new line, **STOP** and report.

## Out of scope

- Any parallelism change (CLAUDE.md concurrency; 05-tasks §5T.8.2 T0+T1: a def's parallelism changes only in its
  own PR). The two jobs that call `RunItems` with `BackfillConcurrency()` keep it: if their bodies call the
  opsbridge `registry.RunItems`, leave that call (it goes at T8), or convert it 1:1 to
  `tasks.RunItems(rc, tasks.Fixed(<same expression>, "legacy Concurrency"), ...)`.
- Moving the four generic jobs (cleanup-empty-folders, fix-file-modes, retention-and-hygiene,
  sweep-pebble-metrics-ttl) to `core.*` kinds: that is T4 (PC-13-03).
- Any schedule change (T7). The v1 checkpoint **deletes** in `retention_and_hygiene.go:339-374` stay.

## Rollback

Revert the PR (binary swap). Runs started on this build store under `maintenance.<id>`, so the previous build
resumes them; the two checkpointing jobs restart from zero once (09 §9.2.1 row 5, release note: "Runs of new task
kinds restart after a rollback; one checkpointed job may start over.").

## Repo rules

- File headers on every new or changed file (`.standards/instructions/file-headers.md`); none on `changelog.d/`.
- Conventional commits, e.g. `refactor(maintenance): convert the 34 maintenance jobs to native books tasks`, each
  ending with:
  ```
  Stage-Release: S5 2
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- Every commit builds (rebase merge). Push, `gh pr create`, wait for the required checks of 09 §9.2.1
  (`extraction-gates`, `docker-image`, `vet-darwin`, `vet-integration`, `compat-rehearsal` because `release:`
  changes), then `gh pr merge <n> --rebase`. Never squash, never `--admin`.
- CLAUDE.md concurrency rules: T2 keeps every job's concurrency 1:1; nothing new fans out.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`.

## Planner notes

- `TestLegacyOpsWriters` (05-tasks §5T.9, "successor of `TestOpStateWriters`") has no creating row in the
  inventory: no PC-12 row lists it, and `TestOpStateWriters` never existed in code. This row (its first target)
  STOPs if it is absent. Suggest adding its creation to PC-12-05 or PC-12-08.
- The native-definition seam into books' `Registrar.Tasks` is not in this row's files (`internal/books/module.go`
  is absent). If PC-12-08 left no seam that accepts `tasks.Definition`s, this row needs `internal/books/module.go`
  added (it is also edited by PC-13-03 and PC-13-14, later in the wave).
- Tier: the hash-backfill chunk journal (`backfill_file_hashes.go:722-775`) is a durability mechanism, not a
  mechanical move; T2 Sonnet/high fits that one job better than T1.
- Deleting `internal/maintenance` also edits two test files outside this row's listed paths:
  `internal/audiobooks/revert_merge_backfill_test.go` and `internal/server/handlers/abs/user_state_deadlock_test.go`
  (import rewrite only). Add them to the row's files.
- 05-download §10 maps `maintenance.bulk-deluge-import` to a `books.download.*` task, while 05-tasks §5T.8.2 T2
  maps every job to `books.maintenance.*`. This brief follows 05-tasks (kind `books.maintenance.bulk-deluge-import`);
  a later rename rides `FormerKinds`.
