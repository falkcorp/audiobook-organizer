<!-- file: docs/agent-tasks/platform-core/PC-03-06-staged-restore-abo-local.md -->
<!-- version: 1.0.0 -->
<!-- guid: 88b12cba-327a-489d-8805-88cda662a026 -->
<!-- last-edited: 2026-11-04 -->

# PC-03-06 — Staged restore, ABO-local: a restore stages for the next boot and follows 05-backup §5B.3.4 exactly; exit 75 restarts the unit

| Field | Value |
|---|---|
| ID | PC-03-06 |
| Tier | **T4 Opus/xhigh–max.** A durable swap of the live store by renames across a restart; a wrong step order, a missing fsync or an in-flight file created after the first rename loses or half-moves the database, and S2b's kernel must later resume whatever this code leaves in flight. |
| Wave | W03 (S1, release `S1`) |
| Depends on | PC-03-05 (shares `cmd/root.go`, `internal/server/handlers/system/handler.go`), PC-02-06 (`scripts/tests/test_unit_lifecycle.py`) |
| Trailer | `Stage-Release: S1 1` |
| Release note | 09 §9.2.1 row 10, quoted in the S1 digest §6; this PR's `changelog.d/` fragment carries it verbatim |
| Digest | `docs/plans/platform-core/digests/S1.md` §4.2, §4.3, §5 rules 1–4, §6, §7 |
| Spec | 05-backup §5B.3.4 (steps 1–9 and the boot table a–x), "Stages" (the S1–S2 r3 ABO-local swap paragraph), §5B.9 rows `TestInflightBeforeFirstRename`, `TestS2bResumesS1Journal`; 05-storage §5S.3.15 (`package restorejournal`: copy the types exactly), §5S.3.16 machine 1 (the only legal transitions); 09 S1 rows "ABO-local fixes", "Deploy", "Rollback", "Done when"; 09 §9.2.1 row 10; 09 §9.2.2 "Rollback tooling" (restore probe, `ROLLBACK_IGNORE_RESTORE`); 09 §9.10 R-test cell (e) |

**Destructive path.** Exercise it only on stores the tests create; never on a store an agent did not create.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-03-06" -b platform-core/pc-03-06-staged-restore origin/main
cd "$REPO/../audiobook-organizer-pc-03-06"
git rebase origin/main
git worktree list
make web-deps
```

Preconditions: PC-03-05 and PC-02-06 merged (`grep -c 'test_s0_long_migration_not_killed' scripts/tests/test_unit_lifecycle.py`
prints 1; `grep -rn 'config.AppConfig.RootDir = ""' internal/server/handlers/system/handler.go` prints nothing). Else STOP.

## Exact files

Create: `internal/backup/staged_restore.go`, `internal/backup/restorejournal.go`, `internal/backup/staged_restore_test.go`.
Modify: `internal/backup/backup.go`, `internal/server/handlers/system/handler.go`, `internal/updater/updater.go`,
`cmd/root.go`, `Makefile`, `Makefile.local.example`, `scripts/tests/test_unit_lifecycle.py`.
Also needed (Planner notes): the R-test harness of `make compat-rehearsal` (cell e).

## Re-verify (expected at the planning checkout `51a2c2acd`)

```bash
grep -n 'func RestoreBackupIn(backupDir, filename, targetPath string, verify bool) error' internal/backup/backup.go   # 1 hit (line 732)
grep -n 'return extractArchive(archivePath, targetPath)' internal/backup/backup.go     # 1 hit (line 769)
grep -n 'func ensureSameDevice' internal/backup/backup.go                              # 1 hit
grep -n 'func (h \*Handler) RestoreBackup(c \*gin.Context)' internal/server/handlers/system/handler.go   # 1 hit (line 824)
grep -n 'backup.RestoreBackupIn(' internal/server/handlers/system/handler.go          # 1 hit (line 862)
grep -n 'initializeStore        = database.InitializeStore' cmd/root.go               # 1 hit (line 74)
grep -n 'func CheckReader' internal/database/storage_guard.go                          # 1 hit (PC-02-01)
grep -n 'exitProcess(75)\|exitProcess = os.Exit' internal/updater/updater.go           # 2 hits (PC-02-03)
grep -rn 'restore/pending.json' Makefile scripts/storage_format_guard.py | wc -l        # 2 or more (PC-02-01/04's probe)
grep -n 'func SecureStateDir' internal/config/state_dir.go                             # 1 hit
```

The spec cites `internal/backup/backup.go:777-870` (the in-place extract) and `:577-598` (`ensureSameDevice`) at
`748fd787a`; at `51a2c2acd` `extractArchive` starts at line 777 and `RestoreBackupIn` at 732. A miss: STOP.

## Contracts

`internal/backup/restorejournal.go` is a **copy** of 05-storage §5S.3.15's `package restorejournal` (the core package
lands at S2b, and S2b's P0a must read what this code writes): `ErrSwapPending` with the exact text
`storage: a restore or factory reset is already staged or in flight`, `Intent` (every field and JSON tag, the
`KindReset` fields included even though S1 never sets them), `Target`, `Row`, `Kind` and its four constants,
`ResetKind` and its two constants, `Record` (every field), `SidecarIdentity`, and `CreateIntent(stateDir string, in
Intent) error` with exactly that behaviour: temp file + fsync, `link(2)` to `restore/pending.json` (`EEXIST` →
`ErrSwapPending`, temp removed), remove the temp, directory fsync; also `ErrSwapPending` while any
`restore/*/inflight.json` or `inflight-checkpoint.json` exists. A comment at the top says: "Copy of 05-storage
§5S.3.15 `storage/restorejournal`; S2b deletes this file and imports the core package; keep the JSON shape identical."

Journal step names (05-backup §5B.3.4; 05-storage §5S.3.16 machine 1) used at S1: `staged`, `rechecked`,
`previous_moved`, `staged_moved`, `sidecars`, `swapped`, `applied`, `swapped_back`, `rejected`, `no_previous`.

`internal/backup/staged_restore.go` (names coined here):

```go
// StageRestore runs 05-backup §5B.3.4 steps 1–8 (the S1 subset below) and returns the intent ID.
func StageRestore(ctx context.Context, req StageRequest) (string, error)
type StageRequest struct {
	BackupDir, Filename string // resolved through os.ReadDir as RestoreBackupIn does (CodeQL go/path-injection)
	DBPath, StateDir     string // absolute
	Verify               bool   // HEAD's verify flag; false only for an archive without .sha256 (AllowWeakVerify)
	Kind                 Kind   // KindArchive (KindRevert is S2b's RevertLastRestore; refuse anything else)
	RequestedBy          string
}
// RunPendingSwap is ABO's pre-serve swap, the S1 equivalent of 06's P0a: call it in serve before the store opens.
func RunPendingSwap(ctx context.Context, stateDir, binaryID string) (SwapOutcome, error)
type SwapOutcome struct{ Applied, Rejected, SwappedBack bool; IntentID, Reason string }
```

Staging (05-backup §5B.3.4, S1 subset; each refusal writes nothing): (1) refuse with `ErrSwapPending` while the slot
is taken or a swap is in flight; (2) verify the archive against its `.sha256` (HEAD's verification); (4) compatibility
before extraction: every owner level of the archive's store ≤ the build's (`database.SupportedLevels()`), and the
build's upgrade floor (empty at S1) against the archive's applied versions; (6) pre-validate every rename: staging
dir and each target on the DB's filesystem (`ensureSameDevice`), parent writable, target not a mount point and not a
single-file bind mount (device of the file ≠ device of its directory); (7) extract into
`<db parent>/.mediacore-restore/<id>/staging/`, fsync every file, then each directory; (8) `CreateIntent` with `Kind`,
`DBPath`, `Targets` (the DB directory and its four sidecars `.storage-levels`, `.storage-applied`, `.storage-format`,
`.migration-checkpoint`, each `{From: staged, To: live}`, absolute), `StagedByBuild` = the binary ID,
`PebbleOptions` = the live DB directory's highest `OPTIONS-<n>` (0 when no store), `ExpiresAt` = now + 24h (the
default of `core.storage.swap.intent_ttl`, whose key lands at S2b), `InstallID` = the live `c/storage/install`
value, `ArchiveID`, `Staging`, `ManifestSHA256` (the archive's SHA-256), `RequestedBy`, `RequestedAt`; on
`ErrSwapPending` remove the staging directory; then journal `staged` in `restore/<id>/journal.json`. Step 3
(provenance) and step 5 (secrets) have no S1 counterpart: every S1 archive is `legacy-abo/0` and the keyring is S3's.

Boot (the table a–x, S1 subset; every rename is "rename only if the source exists and the destination does not"):
- **a.** With `restore/pending.json` present: reject when `ExpiresAt` passed, `StagedByBuild` ≠ `binaryID`, or the
  live highest `OPTIONS-<n>` ≠ `PebbleOptions` → rename it to `restore/<id>/rejected.json`, journal `rejected`, boot
  the untouched store with health `degraded` token `restore_rejected`. The rule reads only `pending.json`.
- **b.** Re-check staging against `ManifestSHA256` and entry sizes; failure → reject as in a. Success → journal
  `rechecked`, **then** rename `pending.json` → `restore/<id>/inflight.json`, before step c renames anything.
- **c.** Rename the live DB directory and its four sidecars to `<db parent>/.mediacore-restore/<id>/previous/`;
  journal `previous_moved`. (No `backup.location.json` or `storage.boot.json` exists before S2b/S4 r2; copy-if-exists.)
- **d.** Rename the staged items into place; journal `staged_moved`.
- **e.** Rewrite `.storage-levels`, `.storage-applied` and `.storage-format` from the staged store (PC-02-01's
  writers, without opening it read-write: read the stamps read-only); journal `sidecars` with the
  `.storage-format` stat identity and the staged directory's highest `OPTIONS-<n>`.
- **g.** Journal `swapped`; then journal `applied` and delete `inflight.json`. **No lineage write** (`c/storage/boot`
  exists only from S2b). The in-flight file is deleted only at `applied` or `swapped_back`.
- **x.** Any failure after the in-flight rename (inside c included) swaps back: move the restored items in place to
  `restore/<id>/failed/`, rename `previous/` back, rewrite the sidecars from the original stamps, journal
  `swapped_back`, delete `inflight.json`, and exit non-zero so the unit restarts on the original store, which boots
  `degraded` (`restore_failed`). On a host with no previous store journal `no_previous` and refuse to create a fresh
  store at that path.
- A journal whose `inflight.json` exists is **never rejected**: the next boot resumes it forward from its last step
  or swaps back, whichever build of S1+ boots it.

API: `RestoreBackup` calls `StageRestore` and answers **202** with `{"restart_required": true, "intent_id": …}`
(a refusal keeps HEAD's status mapping: 409 for `ErrSwapPending` and checksum mismatch, 400 for unverifiable), then,
after the response is written, requests the restart by exiting **75** through the updater's restart path
(`internal/updater/updater.go`: export one function, name coined here `ExitForRestart(reason string)`, used by both
the updater and the restore). `RestoreBackupIn` and the in-place extract stay only for `staging-restore` (PC-02-05)
and `StageRestore`'s extraction into the staging directory; no code path extracts over the live store any more.
`target_path` in the request is refused with 400 unless it equals the live DB's parent (a staged restore only
replaces the live store).

Serve: `cmd/root.go` calls `backup.RunPendingSwap(ctx, config.SecureStateDir(), <binary ID>)` before
`initializeStore`; an outcome with `SwappedBack` exits non-zero after logging; `Rejected`/`SwappedBack` set the health
token through a package-level boot-outcome getter that `handler.go`'s `HealthCheck` reads.

Tooling (09 S1 "Deploy"; §9.2.2): `make rollback` and the `deploy` recipe already refuse on PC-02-04's probe; make
their refusal text name the kind and carry row 10's sentence, and document `ROLLBACK_IGNORE_RESTORE=1` (passes a
staged intent only; `restore_inflight` has no override: "boot the current build once to finish the swap").
`internal/updater` refuses while either file exists (PC-02-03's `CheckReader` call already does; add a test).

## Invariants (each one a test)

1. `inflight.json` exists before the first rename of a live item and until `applied` or `swapped_back`.
2. Every state of machine 1 that this code can reach has a failure exit; no other transition is written.
3. Every rename is idempotent; a second boot after a kill repeats nothing destructive.
4. A rejected intent leaves the live store byte-identical.
5. Nothing extracts into the live DB directory.
6. The JSON written to `pending.json`, `inflight.json` and `journal.json` decodes with 05-storage §5S.3.15's types
   field for field (S2b's `TestS2bResumesS1Journal` reads it).

## TDD tests to write first (`internal/backup/staged_restore_test.go` unless noted)

- `TestInflightBeforeFirstRename` (spec name; 05-backup §5B.9): the in-flight file exists before step c's first
  rename; a kill between c and g, then a `prev`-style check (`database.CheckReader` with the probe semantics and
  `ROLLBACK_IGNORE_RESTORE=1` in the script) still refuses `restore_inflight`, and the next boot resumes or swaps back.
- `TestStagedRestore_KillAtEachRename` (coined): a rename seam (`var renameFn = os.Rename`) fails or panics at the
  k-th rename for every k of a full swap; after each, `RunPendingSwap` again ends at `applied` with the store equal
  to the archive, or at `swapped_back` with the store equal to the original; never anything else.
- `TestStagedRestore_RehearsalStoreEqualsArchive` (coined; 09 S1 "Done when": "stage, boot, store equals the
  archive"): stage, `RunPendingSwap`, open with `database.NewPebbleStore`, compare every key/value with the archive's.
- `TestStagedRestore_RejectsStaleForeignOrMovedStamp` (coined): expired, other `StagedByBuild`, moved `OPTIONS-<n>`.
- `TestStagedRestore_SlotExclusive` (coined): two concurrent `StageRestore` calls, exactly one succeeds.
- `TestRestoreBackup_Answers202_RequestsRestart` (coined, in `internal/server/handlers/system`): 202, body, and the
  restart function called once with exit code 75 (seam).
- `TestHotfixUpdater_RefusesWhileRestoreStaged` (coined, in `internal/updater`).
- `scripts/tests/test_unit_lifecycle.py::test_s1_staged_restore_brings_service_back` (09 S1 "Done when"): under the
  committed unit (`Restart=on-failure`), stage a restore through the running service's API (authenticate the way
  the test's fixture store allows, through the shims PC-02-06 added), observe the exit with status 75, the restart
  without operator action, and the restored store served; `NRestarts` rose by exactly one.
- R-test cell e (09 §9.10): an archive from `prev` restored on `cur`, one from `cur` on `prev` (prev's in-place
  restore), and a restore staged on `cur` across a `make rollback` (refused while pending; with the override the
  intent is discarded by the roll-forward boot's step a, not applied).

## Steps

1. Re-verify. Read 05-backup §5B.3.4 and 05-storage §5S.3.15–§5S.3.16 machine 1 in full.
2. `restorejournal.go` (copy) with a round-trip test against hand-written JSON of each type.
3. Tests above, failing. Then `StageRestore`, `RunPendingSwap`, the handler, the serve hook, the restart function.
4. Tooling text, `test_unit_lifecycle.py`, the R-test cell.
5. `changelog.d/` fragment with row 10's release-note text: "A restore now takes effect at the next restart, which
   the service performs itself. A container started without a restart policy must be started again. Rolling back
   while a restore is staged cancels it; after such a rollback, updates from Settings are refused until the upgraded
   release has started once, and the upgrade itself needs the same override."

## Acceptance

```bash
go test ./internal/backup/ -run 'TestInflightBeforeFirstRename|TestStagedRestore_' -count=1 -race   # ok
go test ./internal/server/handlers/system/ -run TestRestoreBackup_Answers202_RequestsRestart -count=1   # ok
go test ./internal/updater/ -run TestHotfixUpdater_RefusesWhileRestoreStaged -count=1                    # ok
python3 -m unittest scripts/test_storage_format_guard.py scripts/tests/test_make_rollback.py -v          # OK
grep -rn 'extractArchive(' --include=*.go internal/ cmd/ | grep -v _test.go                               # only the staging-dir and staging-restore callers
make ci; echo "exit=$?"                                                                                   # exit=0
```

Required checks: `unit-lifecycle` (green with the S0 and S1 cases), `compat-rehearsal` with cell e, `extraction-gates`,
`docker-image`. The restore rehearsal on a fixture copy is the test above; the staging-copy rehearsal on production
data is the owner's (OWNER-03).

## STOP conditions

- The design needs a write to the live DB directory before `inflight.json` exists, a lineage write, or a hook.
- `restorejournal.go` cannot match 05-storage §5S.3.15's JSON exactly.
- The unit-lifecycle restore case needs a change to the committed unit or to the drop-in beyond PC-02-06's three overrides.

## Out of scope

The kernel's journaled swap and `c/storage/boot` (S2b); restore hooks (S4 r2); `RevertLastRestore` and `KindRevert`
staging (S2b); `previous/` sweeping (`keep_previous`, S2b); the offline `restore --archive` subcommand.

## Rollback

Binary swap; 09 §9.2.1 row 10 applies (a pending intent makes `make rollback` refuse; with the override it is
discarded). A journal left in flight is finished by booting this build once; S2b resumes it (`TestS2bResumesS1Journal`).

## Repo rules

- Worktree only; never `go work init`. Headers on every changed file; one `changelog.d/` fragment.
- Every commit carries `Stage-Release: S1 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds.
- Concurrency (CLAUDE.md): staging and the swap are single-threaded by design (one slot, one journal, renames in a
  fixed order); say so in a comment. The extraction loop is I/O-bound and sequential, as HEAD's is.
- `gh pr merge <n> --rebase` after the required checks pass; never squash or `--admin`.

## Planner notes

- R-test cell e (a done-check of this row) lives in the `make compat-rehearsal` harness PC-01-04 created; that file is
  not in the inventory row.
- `internal/server/update_handlers.go` may need the shared restart function too (PC-02-03 moved the manual apply there).
- The unit-lifecycle restore case must authenticate to stage through the API; 09 §9.2.1 allows the drop-in to override
  only `ExecStart` and `DATABASE_PATH`, so the fixture store has to carry the credential. If that is impossible
  without a new subcommand, the spec needs a ruling.
