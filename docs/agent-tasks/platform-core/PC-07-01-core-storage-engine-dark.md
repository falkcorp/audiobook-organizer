<!-- file: docs/agent-tasks/platform-core/PC-07-01-core-storage-engine-dark.md -->
<!-- version: 1.0.0 -->
<!-- guid: af2d0c29-c46d-4233-bbc9-94209eb5b0d8 -->
<!-- last-edited: 2026-11-04 -->

# PC-07-01 — core/storage engine (dark)

**Full title:** core/storage engine (dark): DB, keyspaces, batches, claims with the persisted violation record,
LegacyRange, CommitLocked with lock ranks, the guard, OpenAuxiliary, CheckReader, internal/pebble.

| Field | Value |
|---|---|
| ID | PC-07-01 |
| Tier | **T4 (Opus, xhigh to max).** The keystone of the extraction: every later lock rule (`CommitLocked`), boot rule (the guard, sidecars, served ledger) and claim rule rests on this package, and a wrong commit-stripe or lock-rank decision is a silent lost update in production. |
| Wave | W07 (stage S2b, release `S2br1`) |
| Depends on | OWNER-06 (the current `S2r3` soak row; merge gate only) |
| Trailer | `Stage-Release: S2b 1` on every commit |
| Mode | Dark: nothing outside `core/` imports the new packages in this PR. No ABO file changes. |
| Normative digest | `docs/plans/platform-core/digests/S2b.md` sections 3, 4.1, 5.2, 5.5, 6 |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-07-01 -b feat/pc-07-01-core-storage-engine origin/main
cd "$REPO/../audiobook-organizer-pc-07-01"
git rebase origin/main
git worktree list          # confirm you are in the new worktree, not the primary checkout
```

Do not run `go work init` (CLAUDE.md). Verify with real `go build` and `go -C core build`.

## Goal

Land core's `storage` package (05-storage §5S.3.1 to §5S.3.7, §5S.3.10 and the storage side of §5S.3.11), its
engine `core/internal/pebble`, and the L0 leaf `storage/codec`, unused by ABO. The kernel wiring that opens the
store at boot is **not** in this PR (see Planner notes; PC-07-03 owns it). After this PR, `coretest` can open
an in-memory `storage.DB` and the engine's own tests prove the contracts below.

## Exact files

Create (all under the nested module `core/`, module path from `core/go.mod`):

- `core/storage/db.go` — `Owner`, `DB`, `Handle`, `Reader`, `Durability`, the error sentinels (§5S.3.1).
- `core/storage/keyspace.go` — `Keyspace`, `ScanOptions`, `Range`, `KV`, `Iterator`, `Snapshot`, `Sequence` (§5S.3.1, §5S.3.2).
- `core/storage/batch.go` — `Batch`, `BatchWriter`, `RetryOnConflict`, the 4 096-stripe commit table (§5S.3.2).
- `core/storage/commitlocked.go` — `Lock`, `CommitLocked`, `LocksHeld`, `CommitStep`, `OnCommitted`, `ErrLockOrder`, `ErrNotLocked` (§5S.3.2).
- `core/storage/claims.go` — `Claim`, `ClaimMode`, claim validation, longest-match resolution, shadow/strict enforcement, the coalescing persisted-violation writer (§5S.3.4, §5S.9.4 item 2).
- `core/storage/legacyrange.go` — `LegacyRange` (§5S.3.4).
- `core/storage/guard.go` — `Spec`, `Migration`, `LegacyMigration`, `UpgradeFloor`, `MigrationTx`, `Levels`, `LevelTooNewError`, `LegacyGuardProjection`, `StagingMarker`, `Duty`, `Owed`, `Duties`, the sidecar formats (§5S.3.5, §5S.3.7).
- `core/storage/checkreader.go` — the exported reader-check entry the kernel binds for ABO's updater; it delegates to `core/internal/pebble.CheckReader` (§5S.3.7 "Reader check").
- `core/storage/auxiliary.go` — `AuxOpener`, `AuxOptions`, `Aux`, `Admin.OpenReadOnly`, `ReadOnlyDB`, `ErrReadOnly` (§5S.3.10).
- `core/storage/storage_test.go` — the tests listed under "TDD" (split into more `_test.go` files if large).
- `core/storage/codec/` — `Codec[T]`, `JSON`, `Raw`, `Zstd`, `LegacyJSON`, `ErrUnknownVersion`, and `storage.Typed` stays in `storage` (§5S.3.3).
- `core/internal/pebble/` — the engine: open without a format version, the lease gate (successor of ABO's `GatedDB`), the format pin, `CheckReader`, the Pebble collector with HEAD's series names under the app's namespace.

Modify:

- `core/Makefile` — wire the coverage ratchet floor `core/.ci/coverage-floor.txt` into `make -C core ci` (12 SC5: "ratchet floor", never lowered by a PR). Create `core/.ci/coverage-floor.txt` with the measured total after this PR, rounded down to one decimal.

Plus one `changelog.d/` fragment (no header).

## Re-verify before editing (expected results)

Run from the worktree root. Rows marked "base" are created by earlier waves; at this plan's evidence HEAD
they are absent because W01 to W06 have not run yet. A missing "base" path means a predecessor has not
merged: STOP and report.

```bash
# base: nested module and the L0 leaves from PC-01-02 (expect all four paths listed)
ls core/go.mod core/Makefile core/buildid/buildid.go core/storage/keys/keys.go
# base: the module path (expect one line, github.com/falkcorp/mediacore unless OWNER-01 chose another)
head -1 core/go.mod
# base: nothing creates core/storage/*.go other than keys yet (expect: keys only)
ls core/storage
# base: marker (expect shipped_stage: S2r3 and release: S2r3 or UI0)
grep -E '^(shipped_stage|release):' ci/stage.txt
# HEAD anchor: ABO engine you replace, not move (expect 1 hit each)
grep -n 'type GatedDB struct' internal/database/pebble_gate.go          # :330 at the plan's HEAD
grep -n 'const SupportedStorageFormat = 1' internal/database/storage_format.go   # :50
grep -n 'FormatMajorVersion: pebble.FormatNewest' internal/openlibrary/store.go  # :44, the aux-DB defect OpenAuxiliary fixes
grep -n 'BuildFQName("audiobook_organizer"' internal/metrics/pebble_collector.go # :119, series names to keep
# HEAD anchor: Pebble version (expect github.com/cockroachdb/pebble/v2 v2.1.7)
grep -n 'cockroachdb/pebble' go.mod
```

The spec anchors at `748fd787a`; the plan's HEAD matches every line above.

## Code contract

The contract is the Go blocks of 05-storage, copied **exactly** (names, field order, comments' rules):

- §5S.3.1 `package storage`: `Owner`, `DB`, `Handle`, `Reader`, `Keyspace` (with `Prefix`, `Owner`, `Sub`, `Get`,
  `Has`, `Set`, `Delete`, `DeleteRange`, `Scan`, `ScanPrefix`, `NewIterator`, `NewBatch`, `NewIndexedBatch`,
  `Snapshot`, `Sequence`, `EstimateBytes`), `ScanOptions`, `Range`, `KV`, `Iterator`, `Durability`
  (`DurabilityInvalid`, `Sync`, `NoSync`) and the ten sentinels `ErrNotFound` … `ErrReadOnly`.
- §5S.3.2: `Batch` (with `noCopy`), `Bind`, `In`, `Set`, `Delete`, `DeleteRange`, `Get`, `Expect`,
  `GetForUpdate`, `Participants`, `Len`, `Count`, `Commit`, `Close`; `BatchWriter`; `RetryOnConflict`;
  `Snapshot`; `Sequence` (`Next`, `NextIn`, `AdvancePast`); and, verbatim:

  ```go
  type Lock interface {
  	sync.Locker
  	LockRank() int
  }
  func CommitLocked(ctx context.Context, b *Batch, d Durability,
  	stage func(ctx context.Context, b *Batch) error, locks ...Lock) error
  func LocksHeld(b *Batch, l Lock) bool
  type CommitStep struct {
  	Key string
  	Run func(ctx context.Context) error
  }
  func OnCommitted(b *Batch, step CommitStep) error
  var (
  	ErrLockOrder = errors.New("storage: a lock is out of the one lock-rank order")
  	ErrNotLocked = errors.New("storage: the batch carries no CommitLocked hold of the lock its write needs")
  )
  ```
- §5S.3.3: `storage/codec` (`Codec[T]`, `JSON[T](version uint8)`, `Raw()`, `Zstd`, `LegacyJSON[T]()`,
  `ErrUnknownVersion`) and `storage.Typed[T]`. `storage/keys` exists already (PC-01-02); do not change it.
- §5S.3.4: `Claim`, `ClaimMode` (`ClaimInvalid`, `ClaimReadWrite`, `ClaimReadOnly`, `ClaimAgingOut`,
  `ClaimRetired`), `LegacyRange` (ten methods incl. `Barrier`), `MigrationCompat`, `ReadAdapter` (declared,
  deferred-build), `Spec` (all fields as listed).
- §5S.3.5 and §5S.3.7: `StepRef`, `Migration`, `UpgradeFloor`, `LegacyMigration`, `MigrationTx`,
  `ErrUpgradeFloor`, `ErrMigrationFailed`, `Levels`, `LevelTooNewError`, `LegacyGuardProjection`,
  `StagingMarker`, `Duty`, `DutyJournal`, `GenDuty`, `Owed`, `Duties`, and the sidecar files
  `<db>.storage-levels`, `<db>.storage-applied`, `<db>.staging` with the formats §5S.3.7 fixes.
- §5S.3.7 "Reader check": `core/internal/pebble.CheckReader(ctx, stateDir, dbPath, exe string, opt
  ReaderCheckOptions) error` with every token of its fixture table (`restore_pending`, `restore_inflight`,
  `level_newer`, `upgrade_floor`, `cannot_determine`, `sidecar_unreadable`, …). The swap-journal files it
  reads are named by `storage/restorejournal` (PC-07-02); until that leaf exists, read the paths
  `restore/pending.json`, `restore/<id>/inflight.json` and `restore/<id>/inflight-checkpoint.json` as
  literals in one file so PC-07-02 replaces them with the leaf's constants.
- §5S.3.10: `AuxOpener.OpenAuxiliary`, `AuxOptions{LegacyPath string; CacheBytes int64}`, `Aux` (`Root`,
  `Close`), pinned format (no `FormatNewest`); `Admin.OpenReadOnly`, `ReadOnlyDB` (`Legacy`, `Keyspace`,
  `Close`).
- §5S.7 keys this PR reads (defaults exactly as the table): `core.storage.cache_bytes`,
  `core.storage.memtable_bytes`, `core.storage.max_batch_bytes`, `core.storage.close_timeout`,
  `core.storage.snapshot_max_age`, `core.storage.sequence.lease`, `core.storage.claims.mode` (default
  `strict`), `core.storage.claims.violation_flush` (1m). Take them as constructor options; do not register
  config sections here (the kernel does at PC-07-03).
- §5S.8 metrics, with the namespace taken as an option (never hard-coded `mediacore`):
  `<ns>_storage_ops_total`, `_op_duration_seconds`, `_batch_bytes`, `_keyspace_bytes`,
  `_claim_violations_total{owner,op,mode}`, `_conflicts_total`, `_snapshots_open`,
  `_snapshot_oldest_seconds`, `_level{owner,kind}`, `_checkpoint_bytes{state}` and `<ns>_pebble_*` with
  HEAD's family names, help strings and types (`internal/metrics/pebble_collector.go:119-146`).

The one lock-rank table of §5S.3.2 is data in this package's tests only; storage never names a `legacy`
lock (it must not import `legacy`, L5). `storage` imports neither `storage/index` nor `tasks` (§5S.3).

## Invariants (each one has a test below)

1. A `Keyspace` key never lands outside `Prefix()`; `Sub` names pass `keys.ValidName`.
2. A `LegacyRange` read **and** write is claim-checked by longest match; scans are clamped; `c/` and `m/`
   are unreachable from it.
3. Claim validation fails boot on cross-owner overlap without `Except`, duplicate prefixes, and claims under
   `m/` or `c/`; all conflicts are listed together (`ErrClaimConflict`).
4. Shadow mode (books' legacy ranges only) records and allows; strict refuses with `ErrOutsideKeyspace`;
   both count `<ns>_storage_claim_violations_total`. The persisted record
   `c/storage/claimviol/<owner>/<op>/<family>` = `{first_at, last_at, count, mode, build}` is written by one
   coalescing writer at most once per `violation_flush` per entry, **never inside the caller's batch**, and
   never holds a value; `<family>` is the longest registered family prefix, or the first 16 key bytes as hex.
5. A legacy claim never refuses a blind write and leaves no violation record for it (A-3); `Expect` on a
   non-`Serialized` claim is `ErrNotSerialized`; in a core or module keyspace a `Set`/`Delete` in a
   `Serialized` sub-prefix without `Expect` is `ErrBlindWrite`, and every `DeleteRange` there is refused.
6. Commit stripes are taken in ascending order, only inside `Commit`; a `DeleteRange` in a `Serialized`
   range takes every stripe; an unclosed batch holds nothing.
7. `CommitLocked` takes each distinct lock once in ascending `LockRank`, joins locks an enclosing hold has
   (found through `ctx`), marks `b` before `stage`, runs `OnCommitted` steps once per `Key` after a
   successful `Commit` and before release, never after a failed one, and a step's error never changes the
   result. A nested call needing a lower rank, or two distinct locks with one rank, is `ErrLockOrder`.
8. `CheckReader` agrees with `scripts/storage_format_guard.py` on one fixture table.
9. An auxiliary DB opens at the pinned format; a read-only open writes no `OPTIONS-<n>`.

## TDD: write these tests first (names from 05-storage §5S.10)

`TestKeyspaceWritesCannotEscapePrefix`, `TestLegacyRangeRefusesForeignKeys`,
`TestSameOwnerNestedClaimsSelectMode`, `TestClaimOverlapFailsBoot`, `TestClaimCarveOutAccepted`,
`TestLegacyClaimsNeverRefuseBlindWrites`, `TestUnserializedBulkWriteTakesNoStripes` (core variant over
synthetic families: a 100 000-key non-`Serialized` commit does not delay a concurrent `Serialized` commit),
`TestSerializedKeyspaceRefusesBlindWrite`, `TestReadOnlyOpenRefusesWrites` (storage half: no `OPTIONS-<n>`,
`ErrReadOnly` on every write), `TestCommitLockedHoldsThroughCommit` (`synctest`, `-race`),
`TestCommitLockedTakesEachLockOnceInOrder`, `TestBatchAtomicAcrossBoundKeyspaces`,
`TestExpectConflictAppliesNothing` (`-race`), `TestLeakedBatchBlocksNobody`,
`TestSequenceNeverReusesAfterCrash`, `TestSequenceAdvancePast`, `TestJoinOrderMatchesSegmentOrder` (exists
for `storage/keys`; keep it green), `TestRaiseWritesSidecarBeforeStamp`, `TestRaiseCommitErrorRewritesSidecar`,
`TestRaiseNeverLowersLevel`, `TestLevelsArePerOwner`, `TestReaderCheckMatchesRollbackScript` (the core
half: the fixture table of §5S.10's row, run against `CheckReader`), `TestSnapshotAgeMetricAndRefresh`
(`synctest`), `TestCloseWaitsForLeases` (`synctest`), `TestAuxiliaryDBUsesPinnedFormat`,
`archtest.NoBatchByValue` (add the check to core's archtest).

Two tests the spec states but does not name (names coined here): `TestClaimViolationPersistedCoalesced`
(one entry per `<owner>/<op>/<family>`, at most one write per flush interval, outside the caller's batch,
no value bytes) and `TestStorageMetricsUseNamespace` (every series carries the namespace option; the
`<ns>_pebble_*` families keep HEAD's names and help strings).

Use Pebble's `vfs.NewMem` / `vfs.NewCrashableMem` and `CrashClone` for crash cases.

## Steps

1. Run the re-verify block. STOP on any mismatch.
2. Write the TDD tests above against the empty package (they fail to compile; that is the red state).
3. Implement `core/internal/pebble` (open without a format version, lease gate with drain and seal on
   `Close`, format pin, Pebble collector under a namespace option). Re-implement; do not copy ABO files
   (04 §2.2: these files are a re-implementation per 05-storage, and `core` must import nothing from ABO).
4. Implement `storage` in the order db → keyspace → batch (stripes, `Expect`, `GetForUpdate`) →
   commitlocked → claims and legacyrange → guard (levels, sidecars, projection, staging marker, duties
   types) → auxiliary → checkreader. Every exported identifier and comment rule of the code contract.
5. Implement the coalescing violation writer as a single goroutine owned by `DB`, flushing on
   `violation_flush` and on `Close`; it commits its own `NoSync` batch to `c/storage/claimviol/`.
6. Wire `coverage-floor` into `core/Makefile`'s `ci` target (12 §12.3 `ci` row lists it) and commit the floor
   file.
7. Run the acceptance commands. Fix until green.

## Acceptance (commands and expected output)

```bash
go -C core build ./...                                   # exit 0
go -C core vet ./...                                     # exit 0
go -C core test -race -count=1 ./storage/... ./internal/pebble/...   # ok for every package
go -C core test -count=1 -run 'TestCommitLocked' -race ./storage/    # ok
make -C core ci                                          # exit 0; the coverage-floor step prints the floor it checked
make ci                                                  # exit 0 (root unchanged except no new imports)
grep -rn 'audiobook-organizer' core/storage core/internal/pebble     # no output (R1)
grep -rn 'FormatNewest' core/storage core/internal/pebble            # no output
git diff --name-only origin/main | grep -v '^core/' | grep -v '^changelog.d/'   # no output (dark, core-only)
```

`TestCoreImportsOnlyCore` and `TestCoreHoldsNoAppKeyNames` (PC-01-03) stay green: no ABO key name may appear
in a core string literal.

## STOP conditions

- A predecessor path in the re-verify block is missing.
- A contract in the spec block cannot be implemented as written (for example a signature that cannot
  compile): STOP and report the exact block; do not invent a variant.
- Any test needs an ABO import or an ABO key name in core.
- The lock-rank or stripe design would need storage to hold a lock for an uncommitted batch.

## Out of scope

- Kernel boot phases (P0a to Run (3b)), the fresh-install batch, the served ledger commit, the claim ledger
  write and handle issue: PC-07-03 (see Planner notes).
- `storage/restorejournal` and `core/internal/restoreswap`: PC-07-02.
- `storage/index`, `storage/datamigrate` (§5S.3.6, lands at S5 with PC-12-06), the census mechanism
  (`storage.Admin.Census`; 04 §4.5 lists it with this package; it has no S2b consumer in W07's rows).
- `storage.ResetRequest` and the reset errors (PC-07-07).
- Any ABO file.

## Rollback

Revert the PR. Nothing imports the packages and nothing is written to any store.

## Repo rules

- File headers on every new Go file (`// file:`, `// version: 1.0.0`, `// guid:`, `// last-edited:`), and a
  bumped header on `core/Makefile` (`.standards/instructions/file-headers.md`). The `changelog.d/` fragment
  has no header.
- Conventional commits, each with `Stage-Release: S2b 1` and ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. `git commit` uses the hook and `.gitmessage` from PC-01-07.
- `gh pr create`, then `gh pr merge <n> --rebase`. Never squash; never `--admin` past a required check
  (`core-ci`, `extraction-gates`, `vet-darwin`, `vet-integration`, `docker-image`). The merge waits for
  `check_soak.py` to admit the trailer (current `S2r3` row).
- CLAUDE.md concurrency: the engine adds no library-scale loop; the violation writer is one goroutine, not
  a pool. Any loop over all keys (census, estimate) declares its parallelism.

## Planner notes

- **Kernel boot wiring has no row.** 05-storage §5S.4 steps 1 to 14 and 06 §6.5.1 P2 to Run (3b) (open,
  guard, fresh-install batch, migration DAG run, claim ledger, served ledger, handle issue, `c/storage/boot`)
  are kernel code that no W07 row lists. This brief keeps them out and PC-07-03's brief carries them (the
  advisor's split pairs "kernel phases with the crash matrix"). PC-07-03 should then be re-tiered T4.
- **Spec-named tests the row calls "(new tests)".** The row's "CommitLocked holds every named lock from
  staging through commit and runs the post-commit step inside the hold (new tests)" are the spec's
  `TestCommitLockedHoldsThroughCommit` and `TestCommitLockedTakesEachLockOnceInOrder`; the brief uses those
  names. "storage conformance suite" is not a spec name; the brief lists the §5S.10 tests instead.
- **§5S.3.6 is in the row's spec range but `storage/datamigrate` lands at S5 (PC-12-06).** No S2b step is a
  data migration, so the brief leaves §5S.3.6 out.
