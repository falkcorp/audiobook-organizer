<!-- file: docs/agent-tasks/platform-core/F2-06-datamigrate-runner-crash-matrix.md -->
<!-- version: 1.0.0 -->
<!-- guid: c1a6adbc-49de-4dd5-964f-59cfaafb845a -->
<!-- last-edited: 2026-11-05 -->

# F2-06 — `storage/datamigrate` and its runner `internal/storagemigrate` over `tasks.ForEach`: `BootBlocking`, chunked, resumable, with the chunk-boundary crash-matrix harness

| Field | Value |
|---|---|
| ID | F2-06 |
| Release / flip | R2, foundation PRs (F2), **dark** |
| Wave | W23 |
| Tier | **T4** (Opus / xhigh–max): the runner every R2 cutover step (M2–M8) runs on; one batch per chunk with the shard cursor, exact resume across CPU-count changes, disjoint shards, and a crash point at every chunk boundary |
| Depends on | F2-01 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-06-datamigrate
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md).

## Goal

Create the public L4 package `github.com/falkcorp/mediacore/storage/datamigrate` (declarations: `Mode`,
`DataMigration`, `StepWriter`, `VerifyReport`, `Blocker`, `DerivedBuildSpec`, `DerivedBuildTask`) and its runner
`github.com/falkcorp/mediacore/internal/storagemigrate`, which runs every pending `BootBlocking` data migration
**in process at P4b** through `tasks.ForEach` over a persisted shard plan with persisted shard cursors, one batch per
chunk (the chunk's writes and the shard cursor commit together). Wire the runner into the kernel's P4 DAG in
`core/kernel/migrate.go` and add the chunk-boundary crash cells to `coretest/crashmatrix` (`chunks.go`). Dark: no
app declares a data migration yet; F3's M2 and F4's M3 are the first consumers (05-storage §5S.3.6 first
paragraph; 09 §9.2.3 "Order", last paragraph).

## Spec to read first

1. 05-storage §5S.3.6 in full (declarations and "Execution contract"; every `Online` clause is **deferred**, SIMP5-7:
   do not build it).
2. 05-storage §5S.3.13 (cutover steps: `Cutover`, `Residual`, `Sensitive`, `Preflight`, `Quarantine`, `Batch`,
   `Verify` rules, "When"), §5S.3.16 (state machines and the boot crash matrix: how `coretest/crashmatrix` walks the
   writer table), §5S.3.14 (deferred members), §5S.9.4 step 3, §5S.7 (`core.storage.data_migration.chunk_bytes`,
   `chunk_items`), §5S.8 (`…_data_migration_items_total`, `…_data_migration_state`), §5S.10 rows
   `TestDataMigrationResumesExactly` and the §5S.3.13 cutover tests.
3. 06 §6.4.2 (`Manifest.DataMigrations`, `Cutover`), §6.5.1 P4 (a) and (b) (where the DAG runs, the checkpoint
   before it), §6.5.1 "Boot writers", §6.9 (`kernel_boot_phase_seconds{phase,step}`), §6.4.8 `coretest/crashmatrix`.
4. 05-tasks §5T.3.2 (`ForEach`, `ItemsError`), §5T.4 step 0, §5T.8 intro (ABO's build uses only the loop surface).
5. 09 §9.2.3 ("one batch per chunk"), §9.9 (concurrency); CLAUDE.md "Concurrency" (bounded pool, disjoint shards).

## Base state: re-verify before editing

```bash
grep -rn 'func ForEach\[' core/tasks/                     # 1 hit (F2-01), else STOP
grep -rn 'type ItemsError struct' core/tasks/             # 1 hit, else STOP
grep -rn 'type MigrationCompat\|type StepRef\|type Owner ' core/storage/ | wc -l   # >=3 (R1-20 storage types the declarations name), else STOP
grep -rn 'type Range ' core/storage/ ; grep -rn 'type KV ' core/storage/           # >=1 each, else STOP
grep -rn 'func (b \*Batch) Expect' core/storage/          # 1 hit
ls core/coretest/crashmatrix/*.go | wc -l                 # >=1 (R1-21), else STOP
grep -rn 'func ExtendStart\|ExtendStart' core/app/ core/kernel/ | head -3   # >=1 (R1-16 Supervisor.ExtendStart), else STOP
test ! -e core/storage/datamigrate && test ! -e core/internal/storagemigrate && echo absent   # "absent"
ls core/kernel/migrate.go 2>/dev/null || echo "migrate.go absent"   # either: create it, or extend R1's file (see below)
```

If `core/kernel/migrate.go` already exists (R1-20/R1-23 may have put the versioned-step DAG there), extend it; do not
replace R1's versioned steps or the legacy steps 1..`LegacyCap`. If `MigrationCompat`, `StepRef` or `Range` is missing
from `core/storage`, STOP: R1's storage surface is incomplete and this PR must not invent it.

## Files

Create: `core/storage/datamigrate/` (declarations + `StepWriter` + `DerivedBuildTask`), `core/internal/storagemigrate/`
(runner, plan, cursors, state records, verify, metrics), `core/coretest/crashmatrix/chunks.go`. Create or extend:
`core/kernel/migrate.go`. Tests beside each.

Do **not** touch `core/storage/` outside `datamigrate/`: the dry run (`core/storage/dryrun.go`), the quarantine record
(`core/storage/quarantine.go`), the preflight hook (`core/storage/preflight.go`) and the level step are F3-04's
(W24), which edits `core/kernel/migrate.go` after you. Leave clean seams for them (see "Seams").

## Code contract

Copy the Go block of 05-storage §5S.3.6 exactly (names, field order, comments), including the `Cutover`-only fields
(`Sensitive`, `Residual`, `Preflight`, `FixBlocker`, `DeleteBlocker`) and `StepWriter`'s methods. Mode: only
`BootBlocking` is accepted by the runner; `Online` is declared (the constant exists) and refused at registration with
an error naming SIMP5-7 (deferred).

Runner rules (each is tested):

1. **Validation at registration:** zero `Mode` or `Compat` refused; `Breaking` requires `BootBlocking` and
   `RaisesLevel > 0` (a `Cutover` step's level is M7's, so `RaisesLevel` may be 0 there); `Parallelism.Validate()`
   must pass (`tasks.ErrNoParallelism` otherwise); `ID` is `<owner>.<name>`, `<name>` `[a-z][a-z0-9-]*`, unique and
   never reused; `Cutover` requires `Source` inside retired claims (as `storage` reports them).
2. **State** `c/storage/dm/<id>` ∈ {pending, running, verifying, done, failed, held}; `done` is never re-entered.
3. **Exact resume (DATA-12):** at first start compute the shard plan `{n, boundaries}` from `Parallelism` and the key
   distribution and persist it at `c/storage/dm/<id>/plan` before any shard runs; every resume reuses that plan,
   whatever `runtime.NumCPU()` is now. Shard cursor `c/storage/dm/<id>/shard/<n>` (last key processed) is written in
   the **same batch** as the chunk's writes.
4. **Chunks:** commit every `core.storage.data_migration.chunk_bytes` (4 MiB) or `chunk_items` (2 000), whichever
   first, with `storage.Sync`. A `Cutover` step's importer staging goes through `StepWriter.Batch()` so it commits with
   the chunk and the cursor.
5. **Own writes never revisited:** a write into the unvisited part of any shard's range is refused unless
   `RewritesInPlace`; then materialise each chunk's key list before stepping it and skip keys the migration wrote.
6. **Parallelism:** shards run through `tasks.ForEach(ctx, m.Parallelism, shards, …)`; shards are disjoint key ranges,
   so no two workers touch one key (CLAUDE.md "partition the work into disjoint sets"; say so in a comment).
7. **Verify** runs after the last shard; a failure leaves state `failed` with the report, fails the boot with the
   checkpoint open (05-storage §5S.3.13), and blocks `Barrier` versions.
8. **Hold** records and skips; held items are listed, never dropped.
9. **systemd:** call `ExtendStart` at every step start and every group of chunks (05-storage §5S.3.13 "Systemd").
10. **Metrics:** `<ns>_storage_data_migration_items_total{migration,outcome}`,
    `<ns>_storage_data_migration_state{migration,state}`, and the kernel's
    `<ns>_kernel_boot_phase_seconds{phase="P4b",step="<id>"}` per step (06 §6.9).
11. **Cancellation** stops at a chunk boundary.
12. **No globals (R5);** the runner is constructed by the kernel per App.

## Seams for F3-04 (do not implement their bodies)

`StepWriter.Quarantine(key, raw, reason)` copies raw bytes under `c/storage/quarantine/` and deletes the source key, both
**in the chunk's batch** (05-storage §5S.3.6). Build the runner half for real: `Quarantine` stages the delete of the source
key into the chunk batch and calls a runner-held writer of type `func(ctx context.Context, b *storage.Batch, step string,
key, raw []byte, reason string, sensitive bool) error` (field name coined here) that stages the quarantine record into the
same batch; `sensitive` is true when `key` is under one of the step's `Sensitive` prefixes; the outcome counts as
`quarantined`. The record format, its sequence and the sealing of sensitive rows are F3-04's (`core/storage/quarantine.go`),
which supplies that writer through the kernel. Until then the kernel passes nil, and `Quarantine` returns
`datamigrate: quarantine writer not wired` (name coined here) so a step can never drop a row silently. Prove the runner
half with a fake writer (the crash cell below shows the delete and the record commit together or not at all).
`StepWriter.Batch()` is complete in this PR. Same shape for the `Preflight` call site: a kernel function called at P4a
before the checkpoint, nil until F3-04. Document both seams in `core/kernel/migrate.go`.

## TDD: tests to write first

- `TestDataMigrationResumesExactly` (05-storage §5S.10: crash between chunks and a changed CPU count on resume; no
  item processed twice or skipped).
- `TestCutoverCrashAtEveryChunk` — the **harness** in `core/coretest/crashmatrix/chunks.go`: for any registered data
  migration, enumerate every chunk boundary of every shard and a point inside each chunk, `CrashClone` there, reboot,
  and assert no row lost or doubled and the step completes. In this PR it runs over a synthetic test migration (a
  `Cutover`-shaped step over a fixture family of a few thousand rows with `Fixed(4, "test")`), proving the harness;
  each cutover step (M2 in F3-06, M3 in F4-06, …) adds its own cells (05-storage §5S.3.13 test list).
- `TestDataMigration_ZeroParallelismRefused` (name coined here): `tasks.Parallelism{}` → registration error,
  `errors.Is(err, tasks.ErrNoParallelism)`.
- `TestDataMigration_OnlineRefused` (name coined here): `Mode: Online` → registration error (deferred-build).
- `TestDataMigration_ShardsDisjoint` (name coined here, `-race`): a migration with `CPU()` writes every item exactly
  once; the race detector sees no shared mutable state.
- `TestDataMigration_VerifyFailureFailsBoot` (name coined here).
- `TestStepWriter_QuarantineInChunkBatch` (name coined here): with a fake writer, a crash at the chunk commit leaves either
  both the source delete and the record or neither; with a nil writer the step fails and the source row stays.
- Extend `TestBootCrashMatrix` (core's, R1-21) with the new boot writers this PR adds: the plan record, the shard
  cursors, the state record (06 §6.5.1 "Boot writers"; 09 §9.3 "Boot writers extend the crash matrix"). Add their rows
  to the writer table the kit walks.

## Steps

1. Base-state greps; plan in the PR body (list the boot writers you add and the matrix rows for each).
2. Tests first (the crash harness first: it is the oracle).
3. Declarations; runner; kernel P4b wiring (the DAG already holds versioned and legacy steps: data migrations join it
   in DAG order, after the versioned steps); metrics; seams.
4. `-race` green; headers; changelog fragment; acceptance.

## Acceptance

```bash
cd core
go build ./... && go vet ./storage/datamigrate/... ./internal/storagemigrate/... ./kernel/... ./coretest/...   # exit 0
go test ./storage/datamigrate/... ./internal/storagemigrate/... -race -count=1                                 # ok
go test ./internal/storagemigrate/... -run 'TestDataMigrationResumesExactly|TestCutoverCrashAtEveryChunk' -race -count=1 -v | grep -c '^--- PASS'   # 2
go test ./... -run TestBootCrashMatrix -race -count=1                                                          # ok (with the new writer rows)
grep -rn 'Online' internal/storagemigrate/*.go | grep -v _test | grep -vc 'refus\|deferred\|SIMP5-7'            # 0 (no Online execution path)
cd ..
go test ./... -run TestBootCrashMatrix_ABO -count=1                                                            # ok (unchanged: ABO declares no data migration yet)
make -C core ci                                                                                                # exit 0
make ci                                                                                                        # exit 0
```

## STOP conditions

- A crash cell loses or doubles a row: the commit unit is wrong. Do not relax the cell.
- You need a storage primitive R1 did not land (a range type, `Expect`, sidecar level stamp): STOP and report.
- Any temptation to build `Online`, `ReadAdapters` or the synthesised `<module>.datamigrate.<name>` kind: deferred.

## Out of scope

The dry run, quarantine record, preflight and level step (F3-04); any cutover step (F3-06 onward); M8 (F3-04/F3-06);
`Online` mode; ABO wiring (books declares its first data migration in F3-06).

## Idempotency / rollback

If `core/storage/datamigrate/datamigrate.go` exists with `type DataMigration struct`, run the acceptance instead.
Rollback = revert: no app declares a data migration, so the P4b DAG returns to R1's versioned steps.

## Repo rules

File headers; conventional commit (`feat(core/storage): datamigrate and the BootBlocking runner (dark)`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; `changelog.d/` fragment. This PR adds
boot-time durable writers, so it extends the crash matrix in the same PR (09 §9.3).

## Planner notes

- F2-06 and F3-04 both edit `core/kernel/migrate.go` (W23 and W24). F3-04 depends on F2-06, so the order holds. The split
  of `StepWriter.Quarantine` is: F2-06 builds the runner half (source delete and record staging in the chunk batch through a
  writer function), F3-04 supplies the writer (record format, sequence, sealing). F3-04's brief stops if `Quarantine` is
  "missing or a stub"; the runner half here is neither, and F3-04's agent should be told the writer field is its seam.
- `TestCutoverCrashAtEveryChunk` is a cutover test (05-storage §5S.3.13) whose cells belong to every step; this row owns
  the harness only, as the inventory says.
