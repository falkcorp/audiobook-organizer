<!-- file: docs/agent-tasks/platform-core/PC-07-03-crashmatrix-kit-hello-boot.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6ff6d4e3-e20b-4d61-8d0c-0e2c923fbcbb -->
<!-- last-edited: 2026-11-04 -->

# PC-07-03 — coretest/crashmatrix kit and core's TestBootCrashMatrix over hello

**Full title:** `coretest/crashmatrix` kit and core's `TestBootCrashMatrix` over hello, with hello's keyspace
and one migration.

| Field | Value |
|---|---|
| ID | PC-07-03 |
| Tier | **T3 (Opus, high) per the inventory.** A fault-injection harness across Pebble's `vfs` and the state directory. This brief also carries the kernel's storage boot phases, which no row lists; with them the work is T4 in size and risk (see Planner notes). Run it at T4 effort. |
| Wave | W07 (stage S2b, release `S2br1`) |
| Depends on | PC-07-01, PC-07-02 |
| Trailer | `Stage-Release: S2b 1` |
| Mode | Dark for ABO: only `core/` files. hello boots through the kernel's storage from this PR. |
| Normative digest | `docs/plans/platform-core/digests/S2b.md` sections 5.1, 5.2, 5.13 |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-07-03 -b feat/pc-07-03-crashmatrix origin/main
cd "$REPO/../audiobook-organizer-pc-07-03"
git rebase origin/main
git worktree list
```

## Goal

1. **Kernel storage boot for apps without legacy claims** (05-storage §5S.4 steps 1 to 14 in 06 §6.5.1's
   phases P0a, P2, P4a, P4b, P4c, P4d (the parts that exist at S2b) and Run (3b)): open at
   `core.storage.path`, guard, spec validation, the fresh-install batch, the checkpoint, the migration DAG,
   the claim ledger and owed duties, handle issue, the served-ledger commit and `c/storage/boot` with its
   mirror `<state_dir>/storage.boot.json`; P0a runs PC-07-02's swapper and a hookless restore boot reaches
   `applied` through `opened --> marker_written`.
2. **The labelled state filesystem** `core/internal/statefs`, the one seam every core non-Pebble boot write
   goes through, carrying crash-point labels.
3. **The public kit** `coretest/crashmatrix` with exactly the API of 05-storage §5S.3.16.
4. **hello** gains `Storage: storage.Spec{Keyspace: true, Migrations: [seed-names]}` and `seedNames`
   exactly as 06 §6.12.1 shows.
5. **Core's instance** `TestBootCrashMatrix` in `core/kernel/crashmatrix_test.go`, run by `core-ci`.

## Exact files

Create:

- `core/coretest/crashmatrix/` — `Injector`, `Injector.New`, `Fixture`, `Scenario`, `Run` (§5S.3.16 block).
- `core/examples/hello/storage.go` — the `storage.Spec` and `seedNames` of 06 §6.12.1.
- `core/kernel/crashmatrix_test.go` — `TestBootCrashMatrix`, `TestBootCrashMatrixCoversEveryWriter`.
- `core/kernel/storage.go` (name coined here) — the boot phases of Goal 1.
- `core/internal/statefs/` (path fixed by §5S.3.16; not in the row) — the labelled state FS.

Modify:

- `core/examples/hello/` — the manifest gains `Storage`; hello's `openapi` output is unchanged.
- `core/module/` — `Manifest.Storage storage.Spec`, `Deps.Storage module.StorageHandles{Keyspace, Legacy,
  Indexes, Aux, Derived, Maintenance}` (06 §6.4.4; `Indexes` stays a nil `index.Opener` until S7: declare the
  field with a placeholder interface only if `storage/index` does not exist, and say so in a comment).
- `core/kernel/` — call the boot phases; register the §5S.7 storage config keys as `ProtectOperator`.
- `core/app/` — `app.Options` gains nothing here except what `crashmatrix.Injector.New` needs to wire the
  injector (unexported hook).

Plus one `changelog.d/` fragment.

## Re-verify before editing

```bash
ls core/storage/db.go core/storage/commitlocked.go core/internal/pebble     # PC-07-01 merged
ls core/storage/restorejournal/restorejournal.go core/internal/restoreswap  # PC-07-02 merged
ls core/kernel core/app core/module core/examples/hello core/coretest/coretest.go   # PC-04-02, PC-03-03
grep -rn 'Storage' core/module/*.go | grep -c 'storage.Spec'                # 0 (this PR adds it)
grep -n 'phase' core/kernel/*.go | head                                     # the S2 phase list you extend
```

## Code contract

- `coretest/crashmatrix` (copy exactly, 05-storage §5S.3.16):

  ```go
  type Injector struct{ /* unexported */ }
  func (inj *Injector) New(ctx context.Context, opts app.Options, mods ...module.Module) (*app.App, error)
  type Fixture struct {
  	Name     string
  	DBDir    string
  	StateDir string
  }
  type Scenario struct {
  	Build    func(t testing.TB, inj *Injector) error
  	Prev     string
  	Fixtures []Fixture
  }
  func Run(t *testing.T, s Scenario)
  ```
  No `core/internal` type is exposed. The injector wraps Pebble's `vfs.NewCrashableMem` and `CrashClone`
  behind an injecting `vfs.FS` that stops the boot at the N-th operation, and the labelled `statefs`.
- `Run` breaks the boot at every durable write, rename and fsync between P0a and Run (3b) in two modes (a
  kill; an `ENOSPC` then `EACCES` error return with the boot continuing), reboots `cur` until readiness, runs
  `Prev` through `CheckReader` on a copy and boots it when admitted, and asserts I1 to I7 of §5S.3.16. It
  fails on any durable operation without a registered label, and on any file under the DB parent or the
  state directory that the after-boot tree diff finds without a label.
- Boot phases follow 05-storage §5S.4 and 06 §6.5.1 to the letter, including: steps 1 to 5 write nothing;
  the first store write is the fresh-install batch or the checkpoint (step 6); the fresh-install batch is
  **one** batch (install marker, epoch, every owner's stamp at the build's level, applied records below each
  owner's lowest `Seed` step, the claim ledger; on a reset boot the intent's `fresh_rows`); `Seed` without
  `Additive` fails registration; the served ledger's `last` record and the foreign-open identity are written
  at Run step (3b), never at step 9; the empty store is exempt from the floor check.
- hello's `Prev` is hello built from the PR's merge base (06 §6.10 row `TestBootCrashMatrix`).

## Invariants and scenarios

The scenario list for core's instance is §5S.3.16's, minus what needs books or hooks: a fresh install; an
upgrade boot with a pending step and its checkpoint; a level step (a synthetic test owner, since no
extraction step raises a level); a staged restore of each kind (archive, revert, checkpoint) **without
hooks**, reaching `applied` through `marker_written`; a kill between steps c and g followed by `prev` with
`ROLLBACK_IGNORE_RESTORE=1`, which `CheckReader` still refuses (`restore_inflight`); an error return on every
operation from the in-flight rename to the first journalled move (ends `swapped_back`, store untouched); a
restore on S2b with no `backup.location.json`; a boot after a foreign open (sidecar rewrite, `OPTIONS-<n>`
bump, behind lineage); a rollback to `prev` and a roll-forward. The `KindReset` scenarios are added by
PC-07-07 and the offline-open ones by PC-07-08, each in its own PR.

## TDD: tests first (spec names)

`TestBootCrashMatrix`, `TestBootCrashMatrixCoversEveryWriter`, `TestRestoreBootWithoutHooksReachesApplied`,
`TestFreshInstallStampsLevelsAtomically`, `TestFreshInstallRunsSeedSteps` (hello greets with `Seen == 1`; the
seed on a store holding its rows changes nothing; `Seed` without `Additive` fails registration),
`TestCheckpointPrecedesEveryWrite`, `TestCheckpointFreeSpaceRefused`, `TestMigrationStepAtomicWithBookkeeping`,
`TestMigrationDAGOrdersAcrossOwners`, `TestMigrationFailureHaltsBoot`, `TestUpgradeFloorRefusesBeforeAnyWrite`,
`TestAppliedSidecarMatchesBookkeeping`, `TestBootLineageVerdicts`, `TestStagedRestoreIsNotForeignOpen`,
`TestForeignOpenDetected`, `TestServedDutiesFallBack`, `TestOrphanedClaimNeedsTakeover`,
`TestBackendsRecordedInLedger`, `TestNoExtractionStepRaisesCoreLevel`, `TestFirstBootRefusesForeignKeys`.
Write each as a `crashmatrix` scenario where §5S.3.16 says it is one.

## Steps

1. Re-verify; read 06 §6.5.1 (the phase table and its "Boot writers" table) and 05-storage §5S.4 in full.
2. Write `core/internal/statefs` (WriteFile, Rename, Remove, RemoveAll, fsync, each with a label) and route
   `restoreswap`'s filesystem seam (PC-07-02) through it.
3. Write the `crashmatrix` tests first against hello fixtures (they fail: hello has no storage yet).
4. Add `module.Manifest.Storage`, `module.Deps.Storage` and `module.StorageHandles`; add hello's spec.
5. Implement `core/kernel/storage.go` phase by phase, each durable operation labelled; add every label to
   06 §6.5.1's boot-writer table **in this PR** (09 §9.3 "Boot writers extend the crash matrix"). That table
   is in `docs/specs/platform-core/06-app-sdk.md`; editing it is the one spec edit this PR makes.
6. Implement `crashmatrix.Run` and the injector.
7. Run the acceptance block.

## Acceptance

```bash
go -C core build ./...                                                     # exit 0
go -C core test -count=1 -run 'TestBootCrashMatrix' ./kernel/              # ok
go -C core test -race -count=1 ./kernel/... ./coretest/... ./examples/hello/... ./internal/statefs/...   # ok
go -C core run ./examples/hello openapi && git diff --exit-code core/openapi/core.json   # exit 0 (no route change)
make -C core ci                                                            # exit 0, within core-ci's timeout
make ci                                                                    # exit 0
git diff --name-only origin/main | grep -vE '^(core/|changelog.d/|docs/specs/platform-core/06-app-sdk.md)'   # no output
```

## STOP conditions

- 06 §6.5.1 and 05-storage §5S.4 disagree on an order you need (06 wins, §5S.4's preamble; if 06 is silent,
  STOP and report).
- A durable operation cannot be routed through `statefs` or a labelled Pebble batch.
- The matrix exceeds core-ci's 30-minute job budget (09 §9.2.10): shard it in this PR; never move it to
  nightly.

## Out of scope

Books and every ABO file; `KindReset` (PC-07-07); offline opens (PC-07-08); restore hooks (S4 r2);
`config.OverrideStore`'s native implementation (PC-07-05 per Planner notes); data migrations (S5).

## Rollback

Revert. hello's store keys live only in test fixtures; no production binary boots the kernel's storage yet.

## Repo rules

- Headers on every new or changed file; bump `06-app-sdk.md`'s header version for the writer-table rows.
- Conventional commits with `Stage-Release: S2b 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`;
  `gh pr merge <n> --rebase`; never squash; never `--admin`. Required: `core-ci`, `unit-lifecycle`
  (`core/kernel/**`), `extraction-gates`, `vet-darwin`, `vet-integration`.
- CLAUDE.md concurrency: the matrix runs scenarios in parallel subtests (`t.Parallel()`), bounded by
  `GOMAXPROCS`; the boot itself keeps 06's strict order (do not parallelise phases).

## Planner notes

- **Kernel storage boot phases have no row.** No W07 row lists `core/kernel` boot code for storage, yet
  this PR's matrix needs a boot to break and PC-07-05 needs the kernel to open the store. The brief puts
  05-storage §5S.4 and the S2b parts of 06 §6.5.1 here (the advisor pairs "kernel phases with the crash
  matrix"). Recommend re-tiering PC-07-03 to **T4**, or splitting a new row "kernel storage boot phases"
  between PC-07-02 and PC-07-03.
- **Files not in the row:** `core/kernel/storage.go`, `core/internal/statefs/` (path fixed by 05-storage
  §5S.3.16), `core/module/` (Manifest.Storage, Deps.Storage, StorageHandles), `core/app/` (injector hook),
  and the 06 §6.5.1 writer-table rows (spec doc).
- **The native `config.OverrideStore` over `c/config/`, `config/configtest`, `legacy.Stores`/`Handles`/
  `ConfigOpener`/`ConfigOpenEnv`/`StateFiles` and `legacy/legacytest`'s config suite** (06 §6.4.10 S2b row)
  are listed in no W07 row; PC-07-05's brief carries them.
