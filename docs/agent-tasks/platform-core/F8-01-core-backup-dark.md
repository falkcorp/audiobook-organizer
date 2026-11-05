<!-- file: docs/agent-tasks/platform-core/F8-01-core-backup-dark.md -->
<!-- version: 1.1.2 -->
<!-- guid: 4815a9a9-aae1-479b-9e95-a1d9a80564de -->
<!-- last-edited: 2026-11-05 -->

# F8-01 — Core backup, dark: mediacore-backup/1 with provenance, the catalogue, Service and Ensurer, core/internal/backuparchive codecs, the legacy-abo/0 reader for staging and `restore-offline --archive` with `--gap-fill`

| Field | Value |
|---|---|
| ID | F8-01 |
| Release | R2, flip label F8 (dark PR) |
| Tier | **T4** (Opus / xhigh–max): backup and restore of every core store; a wrong reader or a wrong staging check loses or exposes the whole install |
| Wave | W30 (beside F2-16, F4-05, F5-15, F6-01, F6-02, F7-02, F8-03; file-disjoint; this row is W26's one owner of `core/go.mod`, `core/go.sum` and `go.sum`) |
| Depends on | F4-04 (`backup/journal` types), F3-03 (core secrets: the provenance HMAC is keyring-derived), F4-11 |
| Spec | 05-backup §5B.0 (B1–B8), §5B.3 (all: §5B.3.1 contents, §5B.3.2 types, §5B.3.3 `mediacore-backup/1`, §5B.3.4 staged restore, §5B.3.5 creation/verification/retention/catalogue, §5B.3.6 revocation journal, §5B.3.7 provenance, §5B.3.8 routes, §5B.3.9 deferred-build), §5B.5–§5B.8, §5B.9 (tests), §5B.10; 05-config §1.12.2 and §2.4 (`--gap-fill`); 05-storage §5S.3.12 (`storage restore-offline`); 04 §8.1 F8 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's `internal/backup` keeps
creating `legacy-abo/0` archives and keeps its `/api/v1/backup/*` routes; no core backup task is scheduled in ABO
and no core route is mounted for ABO until F8-04. The core package is wired on `hello` and `coretest`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f8-01-backup" -b agent/platform-core-f8-01-backup origin/main
cd "$REPO/.worktrees/pc-f8-01-backup"
git rebase origin/main
```

## Goal

Build core `backup` (05-backup), dark:

- the one archive format `mediacore-backup/1` (B1): one tar per archive, manifest first, the Pebble checkpoint under
  `db/`, config file and boot record, module artifacts, optional derived items, with `.sha256` and manifest
  sidecars; **no archive ever holds the keyring**; plaintext only (encryption is deferred-build, B3, §5B.3.9);
- provenance (B5, §5B.3.7): a keyring-derived HMAC bound to the install; an archive without a valid MAC is foreign;
- creation with verification (B4) — `Create`'s journal writes included: `JournalSeq` and the `c/backup/served_boot` pair
  read into the manifest before the checkpoint, and the fresh pair written after it (§5B.3.5 step 4) — retention with its floors, the catalogue derived from sidecars in the backup
  directory (B6, §5B.3.5), `Service`, `Runner` and `Ensurer` (§5B.3.2);
- staged restore (B2, §5B.3.4, steps 1–9 and the kernel-side steps that R1's `core/internal/restoreswap` already
  runs), including step 4's upgrade-floor check from the module manifests;
- the `legacy-abo/0` reader (B7) for staging and for `storage restore-offline --archive`, with `--gap-fill <file>`
  (05-config §1.12.2): HEAD's `audiobooks_<dbtype>_<ts>.tar[.gz|.zst]` archives listable, verifiable and restorable
  as foreign restores, migrated forward by the next boot;
- the tasks `core.backup.*` (B8) and the routes of §5B.3.8 in `core/internal/backuphttp`, built but unmounted in ABO;
- `core/internal/backuparchive` codecs (gzip, zstd, none, magic sniffing, level validation) from HEAD's
  `internal/backup/codec.go`.

## Invariants (each is a §5B.9 test)

- **I1.** The API phase of a restore never writes the live store (`TestRestoreNeverWritesLiveStore`).
- **I2.** Every failure after the swap swaps back and boots the original data (`TestRestoreFailureSwapsBack`).
- **I3.** No archive contains key material or a secret-class item in plaintext (`TestNoKeyMaterialInArchives`,
  `TestPlainArchiveHasNoConfigSecrets`).
- **I4.** A foreign or planted archive needs `Foreign`, `core:admin` and an interactive owner proof
  (`TestForeignArchiveNeedsFlag`, `TestProvenanceBindsInstallAndEntries`).
- **I5.** An archive below the build's upgrade floor is refused at staging before extraction
  (`TestRestoreBelowUpgradeFloorRefusedAtStaging`; R3-02 adds R3's floor cell).
- **I6.** A `legacy-abo/0` archive restored on R2, staged and offline, is staged, extracted and checkpointed behind its
  own pre-migration checkpoint before the next boot migrates it (the staging half of
  `TestLegacyArchiveRestoreThenMigrate`; F8-04 owns the test and adds the migration over every registered step).
- **I7.** Retention never deletes the newest verified archive and runs before the space check
  (`TestRetentionFloors`, `TestRetentionRunsBeforeSpaceCheck`).
- **I8. Dark.** ABO's backup behaviour and routes are unchanged in this PR.

## Files

| Action | Path |
|---|---|
| create | `core/backup/` (types of §5B.3.2, `Service`, `Runner`, `Ensurer`, format writer/reader, catalogue, retention, provenance, staging, tasks; tests) |
| create | `core/internal/backuparchive/` (codecs; the `legacy-abo/0` reader) |
| create | `core/internal/backuphttp/` (§5B.3.8 routes, unmounted for ABO) |
| modify | `core/go.mod`, `core/go.sum`, `go.sum` (zstd and tar dependencies if new to core; `make -C core replace-check` stays green) |
| create | one `changelog.d/` fragment (headerless) |

`core/backup/journal/` exists (F4-04's types and append, F4-11's authenticated format, `backup.location.json` writer
and boot re-apply) and is not edited here except to adopt `journal.StorageSpec()` if 05-backup
says `backup` adopts it at its own flip (that adoption is F8-04's wiring; leave the kernel's registration as is).

## Re-verify before editing (expected results at your base)

```bash
# F4-04's journal leaf. Expected: package journal with Revocation, Head, RestoreHook
grep -n 'type Revocation struct\|type Head struct\|type RestoreHook func' core/backup/journal/*.go
# F4-11's boot re-apply and hook runner. Expected: both files present
ls core/kernel/restore_reapply.go core/kernel/restore_hooks.go
# The R1 swap. Expected: restoreswap and restorejournal present
ls core/internal/restoreswap/ core/storage/restorejournal/
# Core secrets (provenance key derivation). Expected: a derive API in core/secrets
grep -rn 'func .*Derive' core/secrets/*.go | head -3
# HEAD's backup code still serving ABO. Expected: codec.go and backup.go present; /api/v1/backup routes registered
ls internal/backup/codec.go internal/backup/backup.go
grep -rn '"/backup' --include='*.go' internal/server/ | head -5
# Offline restore command from R1. Expected: storage restore-offline registered in core/app
grep -rn 'restore-offline' core/app/*.go | head -3
```

STOP if the journal leaf, F4-11's two kernel files, `restoreswap` or `restore-offline` is absent (earlier rows own them).

## Steps

1. Read 05-backup §5B.0–§5B.10 in full. Copy the types of §5B.3.2 exactly.
2. Write the tests (below) first, starting with the ones marked as this PR's gates.
3. `core/internal/backuparchive`: port `internal/backup/codec.go` (gzip/zstd/none, magic sniffing, level validation);
   write the `legacy-abo/0` reader from `RestoreBackupIn`/`extractArchive`'s format (entry root
   `audiobooks.pebble/`, optional `.sha256` sidecar), extracting **only into staging** (HEAD's live extraction is
   not ported, §5B.10).
4. `core/backup`: format `mediacore-backup/1` (§5B.3.3), creation over `storage.Admin`'s checkpoint (same-device
   rule, `TestCheckpointCrossDeviceRefused`), verification levels, catalogue from sidecars, retention with floors,
   provenance (§5B.3.7), `Create`'s step 4 (read `JournalSeq`, `AuditHighWater` and the `c/backup/served_boot` pair P
   before the checkpoint; right after it write a fresh pair P′ into the live store with `GetForUpdate` and hand it to
   F4-11's journal component, the one writer of `backup.location.json`), `Import`, `Download`, `StageRestore` (steps 1–9 of §5B.3.4, the upgrade-floor check from
   `Manifest.Storage.UpgradeFloor` of each module), `CancelRestore`, `Pending`, `RevertLastRestore`, `Delete`,
   `Ensurer.Ensure`.
   The §5B.8 families add the core `backup` metrics scope: add its `testdata/series.golden` under `core/backup/`,
   asserted by `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5). If F4-11 already created the scope for
   its re-apply counter, extend that golden instead.
5. The tasks `core.backup.create`, `.verify`, `.prune`, `.stage-restore` with one exclusive `Resource` and schedules
   declared through `tasks.ScheduleFromConfig` under `core.backup.*` (B8, `TestBackupTaskExclusive`).
6. `storage restore-offline --archive <file> [--keyring <file>] [--gap-fill <file>] [--accept-loss]` (05-storage
   §5S.3.12): route `--archive` through this package's offline extractor; `--gap-fill` installs the off-host
   `config.yaml` beside the DB (05-config §1.12.2).
7. `core/internal/backuphttp`: the §5B.3.8 table (paths `/api/core/v1/backups[/{id}[/verify|/restore]]`, their
   permissions, guards and audit actions).
8. Wire on `hello`/`coretest`; nothing in ABO. Headers, fragment, acceptance.

## Code contract (05-backup §5B.3.2, verbatim excerpts; copy the rest from the spec)

```go
package backup
type ArchiveID string // ULID
type Ensurer interface {
	Ensure(ctx context.Context, maxAge time.Duration, reason string) (a Archive, created bool, err error)
}
type Runner interface {
	Create(ctx context.Context, o CreateOptions) (Archive, error)
}
type Service interface {
	List(ctx context.Context) ([]Archive, error)
	Verify(ctx context.Context, id ArchiveID, l VerifyLevel) (VerifyResult, error)
	Import(ctx context.Context, name string, r io.Reader) (Archive, error)
	Download(ctx context.Context, id ArchiveID) (io.ReadCloser, error)
	StageRestore(ctx context.Context, id ArchiveID, o RestoreOptions) (PendingRestore, error)
	CancelRestore(ctx context.Context) error
	Pending(ctx context.Context) (*PendingRestore, error)
	RevertLastRestore(ctx context.Context, o RestoreOptions) (PendingRestore, error)
	Delete(ctx context.Context, id ArchiveID) error
	// … the remaining members exactly as §5B.3.2 lists them (Rewrap is deferred-build with encryption only if
	// §5B.3.9 says so; check before adding it)
}
```

`Class`, `Consistency`, `Artifact`, `ArtifactWriter`, `ArtifactReader`, `Manifest`, `KSInfo`, `Entry`, `DerivedInfo`,
`Provenance`, `ProvenanceClass`, `Archive`, `VerifyLevel`, `VerifyResult`, `CreateOptions`, `RestoreOptions` and
`PendingRestore` are defined field by field in §5B.3.2.

## Tests (write first; spec names copied exactly, 05-backup §5B.9)

The staging half of **`TestLegacyArchiveRestoreThenMigrate`** (05-backup §5B.9; F8-04 owns the test): a `legacy-abo/0`
archive of the R0 corpus, staged and offline, passes staging, is extracted, and the pre-migration checkpoint is taken.
No cutover step is registered at this row's wave (F3-07 registers M2, M7 and M8 in a later wave; M3–M6 follow at F4-08, F5-18
and F7-09), so this PR asserts nothing about migrated families. Write it under the spec name in ABO's root module;
F8-04 extends it to the migrated store with the corpus' per-family counts.

Also in this PR: `TestRestoreNeverWritesLiveStore`, `TestStagedRestoreAppliedBeforeConfigAndStorage`,
`TestRestoreRejectedKeepsLiveData`, `TestStaleOrForeignIntentRejected`, `TestRestoreRejectsMismatchedDBPath`,
`TestRenamePrecheckRefusesBindMountedConfig`, `TestRestoreFailureSwapsBack`, `TestLocationFileWritersSerialized`,
`TestRestoreOfflineBringsBackKeyringAndStateFiles`, `TestStagedLegacyRestoreWithBlockerSwapsBack`,
`TestSwapSlotExclusive`, `TestRevertLastRestore`, `TestOfflineRevert`, `TestRestoreOlderArchiveRunsMigrations`,
`TestRestoreRefusesTooNewArchive`, `TestRestoreRewritesSidecarsFromStamps`, `TestRestoreBelowUpgradeFloorRefusedAtStaging`
(its R2 cells; R3-02 adds R3's), `TestHostLossRestoreLegacyArchive`, `TestForeignArchiveNeedsFlag`,
`TestProvenanceBindsInstallAndEntries`, `TestNoKeyMaterialInArchives`, `TestPlainArchiveHasNoConfigSecrets`,
`TestUnreadableSecretsRefused`, `TestCatalogueSurvivesRestore`, `TestCatalogueDropsOrphans`, `TestJournalNamePerApp`,
`TestBackupRoutesPermissions`, `TestBackupDownload_SystemManageOnly403`, `TestManifestMatchesEntries`,
`TestVerifyOpenCountsKeys`, `TestCheckpointCrossDeviceRefused`, `TestRetentionFloors`,
`TestRetentionRunsBeforeSpaceCheck`, `TestLegacyABOArchiveRestores`, `TestDerivedItemsGenerationChecked`,
`TestHostLossRestore`, `TestBackupTaskExclusive`, `TestCreateCancelRemovesPartial`,
`TestInPlaceRestoreOfLastNightsCoreArchive_Replaced` (`Create` writes P′ after its checkpoint, the archive carries P,
an in-place restore of it reads replaced, and a crash between the store write and the file write reads the same
store), and `metricstest.AssertSeries` over the `backup` scope's series golden (05-metrics §12).

The journal format, the P4d step 2 re-apply, the step 3 hook runner and their §5B.9 tests are F4-11's; F4-08 calls
them from boot and owns `TestRestoreHooksWriteNativeRows` and `TestCrashBetweenSwapAndHooksResumesHooks`. Do not
re-implement or re-own any of them. Of the rest of §5B.9, add here any archive-format test (`mediacore-backup/1`,
`legacy-abo/0`, catalogue, provenance, verification, retention, staging) that is not green at your base, and list in
the PR which ones already existed.

## Acceptance (commands and expected output)

```bash
# Core backup. Expected: ok, no FAIL
(cd core && go test -count=1 -race ./backup/... ./internal/backuparchive/... ./internal/backuphttp/...)

# The staging half of the gate test (F8-04 completes it). Expected: ok
go test -count=1 -run TestLegacyArchiveRestoreThenMigrate ./...

# Dark: ABO's backup unchanged. Expected: ok
go test -count=1 ./internal/backup/... ./internal/server/handlers/system/...

# No keyring in any archive written by the tests (spot check). Expected: 0
(cd core && go test -count=1 -run 'TestNoKeyMaterialInArchives' -v ./backup/... | grep -c -- '--- FAIL')

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

The crash matrix: staging writes nothing at boot, but the restore-boot path is a boot writer; if you touch any P0a or
P4d durable write, extend `TestBootCrashMatrix` and `TestBootCrashMatrix_ABO` in this PR (09 §9.3).

## Out of scope

- Mounting routes in ABO, deleting `/api/v1/backup/*`, `internal/backup` and the organizer's auto-backup, the first
  `core.backup.create` writing `mediacore-backup/1` in production (F8-04).
- Archive encryption, `Rewrap` beyond what §5B.3.9 keeps deferred (deferred-build).
- The revocation journal's writers (F4-01, F4-02), its authenticated format, the boot re-apply and the restore-hook
  runner (F4-11), and the R3 floor (R3-02).

## Rollback

Revert the PR; nothing in ABO uses it and no stored data is written (dark).

## Repo rules

- Worktree off fresh `origin/main`; headers on every file.
- Conventional commit, for example `feat(backup): core backup format, catalogue and staged restore (dark)`, ending
  with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin`.
- `core/go.mod` changes run `make -C core replace-check toolchain-check`; no `go work init` in the worktree
  (CLAUDE.md).
- Hashing and verification of large archives stream; any per-entry loop over a library-scale collection follows
  CLAUDE.md's concurrency rule.

