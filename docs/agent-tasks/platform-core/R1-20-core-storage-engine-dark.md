<!-- file: docs/agent-tasks/platform-core/R1-20-core-storage-engine-dark.md -->
<!-- version: 1.1.6 -->
<!-- guid: 41b1ca40-2cc3-49e2-aa3c-359bbca50e36 -->
<!-- last-edited: 2026-11-05 -->

# R1-20 — Core storage engine, dark: storage (claims, keyspaces, Admin, sequences, Expect, level stamps, sidecars, legacy projection, install marker, checkpoint with the headroom check), internal/pebble, storage/restorejournal, coretest's store fixture, hello's keyspace and seed migration

| Field | Value |
|---|---|
| ID | `R1-20` |
| Tier | **T4** (Opus / xhigh): the store engine, level stamps and the checkpoint: a wrong write order loses data |
| Release / wave | R1 / W13 |
| Depends on | R1-13 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-storage §5S.3.3-§5S.3.7, §5S.3.12, §5S.3.15 (the `restorejournal` declarations, lines `Intent` to `Record` only), §5S.9.4 step 1; 06 §6.5.1 P2, P4a; 04 §8.1 R1, §8.5 R1 (dbtest newstore.go, compactprogress step.go); 09 §9.2.2 |
| Branch | `agent/platform-core-r1-20-core-storage-engine-dark` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/storage/`
- `core/internal/pebble/`
- `core/storage/restorejournal/`
- `core/coretest/store.go`
- `core/`: `go.mod`, `go.sum`
- `go.sum`
- `internal/database/dbtest/newstore.go`
- `core/kernel/`: `storage.go`, `boot.go`
- `core/kernel/testdata/`
- `core/archtest/thirdparty_allow.txt`
- `core/archtest/batch_by_value.go` (create: `archtest.NoBatchByValue`)
- `core/examples/hello/storage.go`
- `internal/compactprogress/`
- `internal/metafetch/openlibrary.go`
- `internal/plugins/maintenance/`: `db.go`, `deps.go`, `title_backfill_test.go`
- `internal/server/server_maintenance_deps.go`
- `internal/arch/layering_test.go`
- `scripts/ci/short_test_weights.json`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-20" -b agent/platform-core-r1-20-core-storage-engine-dark origin/main
cd "$REPO/.worktrees/platform-core-r1-20"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `ac4368bdb`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Land core's storage engine **dark** (05-storage §5S.9.4 step 1: "Core `storage` lands dark with zero modules";
04 §8.1 R1 the `storage`, `internal/pebble`, `storage/restorejournal` cell): **zero ABO behaviour change**: ABO still
opens its own store until R1-23 (row notes). What lands:

- `core/storage` (L1): `DB`, `Keyspace`, keys and values (§5S.3.1); batches, preconditions (`Expect`), multi-owner
  commits, `Serialized` keyspaces, sequences (§5S.3.2); codecs over `storage/keys` (§5S.3.3); claims, keyspace issue
  and enforcement with `ErrOutsideKeyspace`, claim modes and the same-owner nesting rule, kernel validation items
  1–3 (no claim under `c/` or `m/`, overlap by longest match) and `storage.LegacyRange` (§5S.3.4); versioned
  migrations with `LegacyMigration` and the per-owner version-source adapter interface (§5S.3.5); the store guard:
  per-owner reader **level stamps**, the sidecar written before the stamp, raise-only, the legacy projection onto
  HEAD's format stamp (`storage.LegacyGuardProjection`) (§5S.3.7); `storage.Admin` (census mechanism, checkpoint)
  with **no staging or swap method** (04 §8.1 R1; R1-21's swapper is the kernel's); the install marker before any
  other `c/` key; the **pre-migration checkpoint with the headroom check** (§5S.3.12; `TestCheckpointFreeSpaceRefused`);
  the sentinel `storage.ErrSwapPending`.
- `core/internal/pebble` (L1 internal): the engine open, format pin, sidecar, gate (`GatedDB` successor), from
  HEAD's `pebble_gate.go`/`storage_format.go`/the open path (§5S.9.3 rows 1–2), re-implemented, not moved (R1-23
  re-homes ABO's files).
- `core/storage/restorejournal` (L0): the record types of the one swap journal, declared exactly as 05-storage
  §5S.3.15's Go block gives them: `Intent` and `Record` with every field, including `StepID` on both (the staging M8's
  step ID, so each M8 reads only its own journals), `CreateIntent`, `Target`, and **all five kinds**:
  `KindArchive`, `KindRevert`, `KindCheckpoint`, `KindReset` and `KindRewrite` (M8's swap, which keeps the store
  generation), with `ResetKind`, `ResetStore` and `ResetFactory` (04 §8.1 R1). F3-04 builds M8 on `KindRewrite` and
  `StepID` and STOPs if either is missing, so they land here, not there. Read by `core/internal/restoreswap` (R1-21)
  and storage's P2.
- `core/kernel/storage.go`: P2 (open) and P4a (checkpoint) of 06 §6.5.1, for apps whose modules declare storage,
  called from the phase list in `core/kernel/boot.go` (R1-13's file; this row is W13's one owner of the kernel
  wiring files and adds only the P2 and P4a calls there);
  hello declares its keyspace and one seed migration (`core/examples/hello/storage.go`).
- `core/coretest/store.go`: the store fixture over `storage` (04 §8.5 R1 row `internal/database/dbtest: newstore.go`
  → "`coretest` store fixture over `storage`"); `internal/database/dbtest/newstore.go` is rewired onto it, its
  other files stay ABO (04 key "R1 move internal/database/dbtest -> mediacore/coretest").
- `internal/compactprogress` → `core/storage` (04 key; its `step.go` takes a `*slog.Logger` passed in, no package
  logger, 04 §8.5 R1); its five ABO importer files rewired to the new path: `internal/metafetch/openlibrary.go`,
  `internal/plugins/maintenance/db.go`, `deps.go`, `title_backfill_test.go` and
  `internal/server/server_maintenance_deps.go` (at the plan's base; re-grep at yours).

## Invariants (the data-loss ones are STOP conditions)

- Nothing is written to the store before the pre-migration checkpoint (06 §6.5.1 "Write rule";
  `TestCheckpointPrecedesEveryWrite`). The install marker is the first `c/` key; every owner's level stamp starts
  at 0 (09 §9.4 R1 "Migrations").
- A level raise writes the sidecar before the stamp and never lowers a level (`TestRaiseWritesSidecarBeforeStamp`,
  `TestRaiseNeverLowersLevel`, `TestLevelsArePerOwner`).
- Reads and writes outside the handed keyspace and claims fail (`TestKeyspaceWritesCannotEscapePrefix`,
  `TestLegacyRangeRefusesForeignKeys`, `TestSameOwnerNestedClaimsSelectMode`, `TestClaimOverlapFailsBoot`,
  `TestFirstBootRefusesForeignKeys`).
- Batches are atomic across bound keyspaces; a failed `Expect` applies nothing; a leaked batch blocks nobody
  (`TestBatchAtomicAcrossBoundKeyspaces`, `TestExpectConflictAppliesNothing`, `TestLeakedBatchBlocksNobody`,
  `TestSerializedKeyspaceRefusesBlindWrite`, `TestUnserializedBulkWriteTakesNoStripes`).
- Sequences never reuse after a crash (`TestSequenceNeverReusesAfterCrash`, `TestSequenceAdvancePast`).
- Migrations: step and bookkeeping atomic, DAG order across owners, a failure halts boot
  (`TestMigrationStepAtomicWithBookkeeping`, `TestMigrationDAGOrdersAcrossOwners`, `TestMigrationFailureHaltsBoot`);
  versions are 1..n, contiguous per owner, recorded by version number (§5S.3.5 `Migration.Version`). The runner needs
  no gap rule for R2: each R2 flip adds its level step at the owner's next contiguous version and never removes an
  earlier one (F3-04 "Flip mechanism");
  a fresh install stamps levels atomically (`TestFreshInstallStampsLevelsAtomically`, `TestFreshInstallRunsSeedSteps`).
- Guard projection: `TestLegacyProjectionRefusesPreGuardBuild`, `TestProjectionKeepsRecordID`,
  `TestUnreadableVersionFailsClosed`, `TestOlderBuildAcceptsNewerAdditiveSteps`, `TestBreakingStepRequiresRaisesLevel`,
  and the two other cells of the same-release-line row (05-storage §5S.10): `TestOlderBuildRefusesNewerBreakingStep`
  (an older build refuses a store a newer build migrated through a `Breaking` step: `ErrKeyspaceTooNew`) and
  `TestCompatlessBooksRecordIsBreaking` (a books record with no compat field counts as breaking, so the same refusal
  holds).
- Batch by value: `Batch` embeds the `noCopy` marker (05-storage §5S.3.2) so `go vet`'s copylocks flags a value copy,
  and `archtest.NoBatchByValue` (COR2-12: no core API or spec Go block takes or holds a `storage.Batch` by value)
  rejects one. Create `archtest.NoBatchByValue` in `core/archtest/batch_by_value.go` (this row's file) on R1-01's
  archtest engine and its loader, and call it from a test in `core/storage/` over core's packages.
- Deps scoping, first cell: `TestDepsScoping` (06 §6.10) starts here with the storage cell, a write outside
  `m/hello/` through hello's `Deps` is refused. F4-08, F6-03 and F7-09 each extend it with the cells of their stage
  (publish or notify outside the catalog, a secret outside `modules.hello.`, `StartRequest.Actor` overwritten,
  `library.Scope` attribution, metric names under `MetricNamespace`).
- Journal records: `TestRestoreJournalKindRewriteRoundTrip` (name coined here): an `Intent` of `KindRewrite` with a
  `StepID`, and a `Record` carrying `StepID`, survive a JSON round trip with `step_id` set and every other kind's
  fields unset (`omitempty`), and all five `Kind` constants are distinct strings (05-storage §5S.3.15).
- Checkpoint: `TestCheckpointModes` (creation half: the directory and its copies are 0700 and 0600 under a permissive
  umask; the sweep half, a mode loosened by hand raising `storage_checkpoint_mode` at the next sweep, is R1-24's),
  `TestCheckpointIncludesStateFiles`, `TestCheckpointFreeSpaceRefused`,
  `TestCheckpointSurvivesCrashLoop`, `TestPartialCheckpointSwept` (the sweeper itself is R1-24's).
- Engine: `TestReadOnlyOpenRefusesWrites`, `TestCloseWaitsForLeases`, `TestJoinOrderMatchesSegmentOrder` (exists).
- Every test name above is 05-storage §5S.10's; write each first. Rows of §5S.10 whose subject lands later (the swap,
  crash matrix, offline restore, sweeper, cutover, index) belong to R1-21 … R1-25 and R2.
- Every boot-time durable write added here gets its row in 06 §6.5.1's writer table; the crash matrix that
  exercises them lands in R1-21 (09 §9.3 "Boot writers extend the crash matrix"): list the rows in the PR for R1-21.

## Dependency check

R1-13 merged: `test -d core/kernel && test -d core/examples/hello && echo ok`.

## Re-verify (expected at `ac4368bdb`)

```bash
M=github.com/falkcorp/audiobook-organizer
ls internal/compactprogress; git grep -l "\"$M/internal/compactprogress\"" -- '*.go' | xargs -n1 dirname | sort -u   # expect metafetch, plugins/maintenance, server
grep -n 'internal/logger' internal/compactprogress/step.go                    # expect 1 (the logger cut)
ls internal/database/dbtest/newstore.go internal/database/pebble_gate.go internal/database/storage_format.go   # expect all three
grep -n 'internal/compactprogress\|internal/database' scripts/ci/short_test_weights.json   # expect only internal/database (stays)
```

## Steps

1. Write the listed tests (red) in `core/storage`, `core/internal/pebble`, `core/kernel`.
2. `core/storage/restorejournal` (types only), then `core/internal/pebble` (engine, gate, format pin, sidecar), then
   `core/storage` (DB, keyspaces, batches, claims, migrations runner, guard, Admin, checkpoint, install marker).
3. `core/kernel/storage.go` (P2, P4a) with its two calls in `core/kernel/boot.go`; hello's keyspace and seed
   migration; `core/coretest/store.go`. Extend R1-13's `TestKernelFamiliesGathered` golden with
   `<namespace>_storage_level` and `<namespace>_storage_checkpoint_bytes` (05-metrics §12) if `core/kernel/testdata/`
   holds it (it is in `core/kernel/testdata/`, which is in this row's files), and the kernel scope's
   `testdata/series.golden` that R1-13 asserts with `metricstest.AssertSeries` (R1-10): add the two storage families
   there too.
4. Move `internal/compactprogress` into `core/storage` with the logger cut; rewire its five importer files;
   rewire `internal/database/dbtest/newstore.go` onto the coretest fixture; update `layerOf` and shard weights if they
   name the moved package.
5. `core/go.mod` gains Pebble and its requirements at the root's versions; tidy both modules; allowlist the new
   third-party paths in `TestCoreImportsOnlyCore` (`core/archtest/thirdparty_allow.txt`); check `make -C core exclude-check` now applies.
6. Header bumps; fragment `changelog.d/<today>_core_storage_dark.md` (`### Added`: "Internal: the core storage engine,
   not yet used by the app."). Commits by package; every commit builds.

## STOP conditions

- Any ABO behaviour change (ABO must not open the store through core in this PR).
- A write order the invariants forbid, or a test from the list that cannot be made to pass without weakening it.
- `go build ./...` fails with `ambiguous import` once Pebble enters core: the exclude belongs in `core/go.mod` only
  (R1-01); `make -C core exclude-check` now becomes **applicable** (core's graph reaches
  `github.com/cockroachdb/errors`) and must pass "load-bearing" (03 §3.2 item 1).

## Code contract

Exactly 05-storage §5S.3.1–§5S.3.7 and §5S.3.12's declarations for the members named in Goal; no staging or swap method on `storage.Admin`.

## Acceptance

```bash
go -C core test ./storage/... ./internal/pebble/... ./kernel/ ./coretest/ ./examples/hello/... -race -count=1   # expect ok
make -C core exclude-check            # expect "present; green with it; load-bearing without it (requirer github.com/cockroachdb/errors@...)"
python3 scripts/ci/check_module_skew.py                                    # expect exit 0 (Pebble at the root's version)
git grep -n '"github.com/falkcorp/audiobook-organizer/internal/compactprogress"' -- '*.go' | wc -l   # expect 0
go test ./internal/database/... ./internal/metafetch/ ./internal/plugins/maintenance/ ./internal/server/ -short -count=1   # expect ok (ABO unchanged)
make test-e2e                                                              # expect green
make ci && make -C core ci                                                 # expect exit 0
```

## Out of scope

Core opening ABO's store, strict claims for books, re-homing ABO's storage files (R1-23); the swap and crash matrix
(R1-21); the sweeper and prune (R1-24); offline restore/reset (R1-25).

## Rollback

Revert the PR; nothing in ABO uses it.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Move window (09 §9.2.10).** This task moves or re-homes packages. Before you start, the lead
  announces a move window in the tracking issue at least one working day ahead, naming the 04 rows it
  freezes and its end (at most two working days). If no announcement exists, STOP and ask the lead. The PR
  description lists every `old ==> new` package path, one per line. Every commit of the PR builds:
  `go build ./... && go -C core build ./...` at each commit (`scripts/ci/check_commits_build.py` checks it
  in `extraction-gates`); keep the rename and the import rewrite in one commit, or make each commit build.
  Apply in the same PR the `go fix` modernizers core's `fix-check` requires in the moved files
  (`go -C core fix ./...`, 04 §2.2).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
