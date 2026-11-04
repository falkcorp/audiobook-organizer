<!-- file: docs/agent-tasks/platform-core/PC-17-01-core-backup-catalogue-staged-restore.md -->
<!-- version: 1.0.0 -->
<!-- guid: 72f5fdb7-3706-4954-99d9-819f997fbcca -->
<!-- last-edited: 2026-11-04 -->

# PC-17-01 — `core/backup`: plaintext archives with provenance, the catalogue, staged restore through the kernel swap, sidecar rewrite on apply

| Field | Value |
|---|---|
| ID | PC-17-01 |
| Title | core/backup: plaintext archives with provenance, the catalogue, staged restore through the kernel swap, sidecar rewrite on apply |
| Tier | **T4 — Opus / xhigh–max.** Every older build must restore every archive this build writes, the restore matrix is cross-build, and a mistake loses a production store or resurrects a revoked key. |
| Wave | W17 (stage S8, release `S8`) |
| Depends on | OWNER-20 (the `S7` soak row) |
| Trailer | `Stage-Release: S8 1` (live, state-writing PR: it writes the backup catalogue and archive manifests) |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §3 (row `backup`), §4 (05-backup 5B.3.2), §5 rules 5, 6, 11, §6 row 11, §7 S8 |
| Owning spec | 05-backup B1–B7, §5B.3 (all), §5B.4–§5B.10; 09 S8 "Backup", "Rollback", "Done when"; 09 §9.2.1 rows 10–11; 09 §9.10 R-test cell (e) |

**Gate.** Merges only after the `S7` soak row exists (`check_soak.py` check (b)). Destructive paths (restore, swap)
run on fixture stores only, never on a store you did not create.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-17-01" -b feat/pc-17-01-core-backup origin/main
cd "$REPO/../abo-pc-17-01"
git worktree list
make web-deps
```

## Goal

Land core's `backup` package (L4) for ABO: archive format `mediacore-backup/1` (a **plaintext** main archive in ABO's
legacy layout plus one root entry `mediacore-manifest.json`, and a companion `.extra` archive older builds never
list), the B5 provenance HMAC over a keyring-derived key, the directory-derived catalogue, verification, retention,
import, and staged restore through the kernel's journaled P0a swap (`core/internal/restoreswap`, since S2b). It
adopts `backup/journal` (since S4). Applying a restore rewrites the **three** sidecars (`.storage-levels`,
`.storage-applied`, `.storage-format`) from the restored store. Old `audiobooks_*` archives stay listable, verifiable
and restorable as `legacy-abo/0`.

The flip (PC-17-04) deletes `internal/backup`'s staging and mounts the native routes; this PR must be shippable alone
(09 §9.2.1: S8 lands live PR by PR). Choose how much of ABO's handler path calls core in this PR, within the rule that
every intermediate build keeps HEAD's routes working and never has two stagers of one slot.

## Files

Create: `core/backup/`, `core/backup/backuptest/`.

Modify:
- `internal/backup/` — codecs to `core/internal/backuparchive` (see Planner notes); `CreateBackupWithCheckpoint`,
  `ensureSameDevice`, `sweepStaleCheckpoints`, the space guard and retention become `backup.Service.Create`/prune
  internals; the live-walk fallback and SQLite branches are deleted; `RestoreBackupIn`/`extractArchive` survive only
  as the `legacy-abo/0` reader used by `StageRestore` into staging. The staging code itself goes in PC-17-04.
- `internal/appdirs/` — the backup directory and app-dir exclusion read from core (`core.backup.dir`).
- `internal/decisions/` — see Planner notes.
- `internal/organizer/` — the pre-organize auto-backup becomes `Deps.Backup.Ensure(ctx, 6*time.Hour, "pre:books.organize")`.
- `internal/server/handlers/system/` — backup handlers call `backup.Service`.
- `core/coretest/crashmatrix/` — rows for any boot-time durable write this PR adds.

## Re-verify before editing

Counts at `748fd787a` (unchanged at this plan's HEAD for these paths). A zero-hit edit target or a moved count: STOP.

```bash
M=github.com/falkcorp/audiobook-organizer
git grep -l "\"$M/internal/backup\"" -- '*.go' | grep -v _test.go      # 4 files at 748fd787a:
#   internal/appdirs/appdirs.go, internal/config/config.go, internal/organizer/service.go, internal/server/handlers/system/handler.go
git grep -n 'func RestoreBackupIn\|func CreateBackupWithCheckpoint\|func ListBackups\|func DeleteBackupIn' -- internal/backup/backup.go   # 4
git grep -n 'POST("/backup/create"\|GET("/backup/list"\|"/backup/restore"\|DELETE("/backup/:filename"' -- internal/server/wire_system_routes.go   # 4 (:59, :60, :65, :66)
git grep -n 'newestBackupAge' -- internal/organizer/service.go | head -2                               # >= 1

# Prerequisites from earlier stages (>= 1 each; 0 = STOP)
test -d core/internal/restoreswap && echo ok          # PC-07-02 (S2b)
test -d core/storage/restorejournal && echo ok        # PC-07-02
git grep -n 'func StorageSpec' -- core/backup/journal | head -1     # S4 (backup/journal)
git grep -n 'func CreateIntent' -- core/storage/restorejournal | head -1
git grep -ln 'legacy-abo/0' -- internal/backup cmd | head -3        # the S0 manifest sidecar writer and staging-restore
test -d core/backup && ls core/backup                                # only journal/ may exist; anything else: STOP, task may be applied
```

## Steps

1. Read 05-backup from the top through §5B.10 (all of §5B.3: what is archived, types, format, staged restore, creation
   and catalogue, revocation journal, provenance, routes, deferred-build members), 05-storage §5S.3.7 (sidecars),
   §5S.3.15 (the one swap, intent slot), §5S.3.16 (crash matrix), 05-authn §4.14 and 05-rbac §7.1 (post-restore
   hooks), and 09 S8 "Backup".
2. **Tests first**, starting with the cross-build restore matrix (below), which runs real previous binaries.
3. Build `backup` per §5B.3.2 (`Artifact`, `Manifest` with `Format: "mediacore-backup/1"`, `Ensurer`, `Runner`,
   `Service`); encryption is **not** built (B3, §5B.3.9 deferred-build). A plaintext archive never carries a
   secret-class item: the operator config file goes in redacted with `config_redacted`; `config.yaml` beside the DB is
   never archived; the keyring is never archived; the 05-config keyring export is the only export.
4. The main archive keeps ABO's legacy layout and naming (`audiobooks_<dbtype>_<ts>.tar[.gz|.zst]`, entry root
   `<db basename>/`) so every older build, HEAD included, lists and restores it and ignores the companion and
   sidecars (`TestPreviousBuildRestoresMainArchive`, `TestPreviousBuildNeverListsCompanion`). The manifest's
   `binary_id` is the writer's `buildid.Binary` (09 §9.3 `staging-restore` reads it).
5. Catalogue (§5B.3.5): derived from sidecar files in the backup directory, never hashing on list; retention never
   deletes the newest verified archive and runs **before** the space check; reconciliation removes orphans an older
   build's retention left (`<main>.manifest.json`, companion).
6. Staged restore (§5B.3.4): verify, check the build's upgrade floors at staging (refuse an archive below them,
   `TestRestoreBelowUpgradeFloorRefusedAtStaging`), pre-validate every rename, extract into staging on the DB's
   filesystem, record the staging build (`StagedByBuild` = the binary ID) and an expiry, and create the intent in the
   one slot through `restorejournal.CreateIntent`; a stale or foreign intent is rejected (moved aside, Degraded). The
   kernel applies it at P0a; any failure after the swap swaps back. Applying rewrites the three sidecars from the
   restored store; P4d re-applies the revocation journal and runs the hooks, which write through books' legacy stores
   into legacy rows.
7. A `legacy-abo/0` archive (every archive made before S8) restores as a foreign restore with nil coverage
   (09 §9.2.1 row 11): every API key reads `inactive`, every session and one-time token is deleted.
8. Tasks `core.backup.*` (create, verify, prune, stage-restore) with one exclusive `Resource`, cancellation that
   removes the partial archive, and schedules under backup's own namespace through `tasks.ScheduleFromConfig`.
   Config `backup_*` → `core.backup.*` with window aliases.
9. Boot writers: if the catalogue reconciliation, the location file or the journal adoption writes at boot, add the
   rows to 06 §6.5.1's writer table and extend `TestBootCrashMatrix_ABO` (and core's `TestBootCrashMatrix` for a kernel
   writer) in this PR (09 §9.3).
10. `make rollback`, the updater and the `deploy` recipe already refuse while a restore is pending or in flight; keep
    them refusing for core-staged intents (`restore_pending`, `restore_inflight`).

## Code contract (05-backup §5B.3.2; copy exactly, full structs in the file)

```go
package backup // github.com/falkcorp/mediacore/backup (L4)
type ArchiveID string // ULID
type Ensurer interface {
	Ensure(ctx context.Context, maxAge time.Duration, reason string) (a Archive, created bool, err error)
}
type Runner interface { Ensurer; Create(ctx context.Context, o CreateOptions) (Archive, error) }
type Service interface {
	Runner
	List(ctx context.Context) ([]Archive, error)
	Verify(ctx context.Context, id ArchiveID, l VerifyLevel) (VerifyResult, error)
	Import(ctx context.Context, name string, r io.Reader) (Archive, error)
	Download(ctx context.Context, id ArchiveID) (io.ReadCloser, error)
	StageRestore(ctx context.Context, id ArchiveID, o RestoreOptions) (PendingRestore, error)
	CancelRestore(ctx context.Context) error
	Pending(ctx context.Context) (*PendingRestore, error)
	RevertLastRestore(ctx context.Context, o RestoreOptions) (PendingRestore, error)
	Delete(ctx context.Context, id ArchiveID) error
	Rewrap(ctx context.Context, id ArchiveID) (Archive, error)
}
// Manifest{Format "mediacore-backup/1", ArchiveID, App, AppVersion, CoreVersion, BinaryID `json:"binary_id"`,
//   InstallID, JournalSeq, AuditHighWater, Levels, LegacyStorageFormat, Entries, Companion, Provenance /* MAC last */}
```

06 §6.6 at S8: Registrar `Backup` (module artifacts), Deps `Backup` (`Ensurer`).

## Tests to write first (05-backup §5B.9 and 09 S8 Done when)

Restore matrix (09 S8 Done when; mandatory): archive from S0 restored on this build; archive from this build restored
on the S7 build and on the HEAD build (real binaries, built from `PREV_REF` and the evidence tag);
`TestRestoreBelowUpgradeFloorRefusedAtStaging`; a staged intent across a rollback (staged on cur, rolled back with the
override: discarded, never applied after roll-forward, 09 §9.2.1 row 10). R-test cell (e) in `make compat-rehearsal`
(archive from `prev` onto `cur`, from `cur` onto `prev`, a staged intent across a rollback, a revocation after a
restore surviving a restart and a rollback).

Named tests: `TestRestoreNeverWritesLiveStore`, `TestStagedRestoreAppliedBeforeConfigAndStorage`,
`TestStaleOrForeignIntentRejected`, `TestStagedByBinaryID`, `TestRestoreRewritesSidecarsFromStamps`,
`TestPostRestoreReappliesRevocations`, `TestRestoreHooksWriteLegacyRows`, `TestStagedRestoreKeepsCoverage`,
`TestRevertLastRestore`, `TestOfflineRevert`, `TestRestoreOlderArchiveRunsMigrations`,
`TestRestoreRefusesTooNewArchive`, `TestStagingRestoreRefusesOtherBinary`, `TestLegacyABOArchiveRestores`,
`TestPreviousBuildRestoresMainArchive`, `TestPreviousBuildNeverListsCompanion`, `TestNoKeyMaterialInArchives`,
`TestPlainArchiveHasNoConfigSecrets`, `TestUnreadableSecretsRefused`, `TestForeignArchiveNeedsFlag`,
`TestProvenanceBindsInstallAndEntries`, `TestManifestMatchesEntries`, `TestVerifyOpenCountsKeys`,
`TestCheckpointCrossDeviceRefused`, `TestRetentionFloors`, `TestRetentionRunsBeforeSpaceCheck`,
`TestCatalogueSurvivesRestore`, `TestCatalogueDropsOrphans`, `TestJournalNamePerApp`,
`TestDerivedItemsGenerationChecked`, `TestHostLossRestore`, `TestHostLossRestorePreS3Archive`,
`TestStagingTrialDecryptsLegacySecrets`, `TestInPlaceRestoreOfLastNightsCoreArchive_Replaced`,
`TestRewrapBeforeDeriveKeyRetired`, `TestBackupTaskExclusive`, `TestCreateCancelRemovesPartial` (synctest).
The earlier-stage restore tests (`TestRestoreCrashAtEveryJournalStep`, `TestInflightBeforeFirstRename`,
`TestRestoreFailureSwapsBack`, `TestCrashBetweenSwapAndHooksResumesHooks`, `TestSwapSlotExclusive`, …) must stay
green unchanged. `TestBackupRoutesPermissions` and `TestBackupDownload_SystemManageOnly403` belong to the routes
(PC-17-04).

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                     # exit 0
(cd core && go test ./backup/... ./internal/restoreswap/... ./storage/restorejournal/... ./coretest/crashmatrix/... -race -count=1)   # all "ok"
go test ./internal/backup/... ./internal/appdirs/... ./internal/organizer/... ./internal/server/handlers/system/... ./internal/books/... -race -count=1   # all "ok"
go test ./... -run 'TestBootCrashMatrix_ABO|TestRestoreBelowUpgradeFloorRefusedAtStaging|TestPreviousBuildRestoresMainArchive' -count=1 2>&1 | grep -E '^(FAIL|--- FAIL)'   # no output
git grep -n 'filippo.io/age' -- core go.mod core/go.mod | wc -l                     # 0 (archive encryption is deferred-build, B3)
make compat-rehearsal                                                             # exit 0 (cell e)
make ci                                                                           # exit 0
make -C core ci                                                                   # exit 0
```

`unit-lifecycle` triggers on this PR (it changes `internal/backup/**`): staging a restore still brings the service
back under the committed unit.

## Out of scope

Archive encryption and every §5B.3.9 member; the native `/api/core/v1/backups` routes and their aliases (PC-17-04);
deleting `internal/backup`'s staging (PC-17-04); the updater's pre-update backup (deferred-build); the UI calls
(PC-17-05).

## Rollback

Binary swap: archives written on S8 are restorable by every older build (plaintext, ABO's legacy layout, B1); older
builds ignore the companion, the sidecars and core's `c/` records. Restores of pre-S8 archives cost what 09 §9.2.1
row 11 says (release note "If you restore a backup on this release, restore one while rolled back, or restore one made
before a rollback, everyone signs in again and every API key must be re-enabled on the keys page by its owner or an
admin."); that row was introduced at S4 r2, and PC-17-04 carries its S8 line.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on new or changed files; one headerless `changelog.d/` fragment.
- Conventional commits, each with `Stage-Release: S8 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- Fixtures only for restore and swap; never point a test at a real state directory.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- The row lists `internal/decisions/`, which does not import `internal/backup` at `748fd787a` (0 hits for
  `backup` in its non-test files), and omits `internal/config/`, which does (`internal/config/config.go:24`). Re-derive
  the importer list at the base commit; the row's files should follow it.
- 05-backup §5B.10 puts the codecs in `core/internal/backuparchive` and §5B.3.8 the routes in
  `core/internal/backuphttp`; neither directory is in any W17 row's files (`backuphttp` belongs with PC-17-04's routes).
