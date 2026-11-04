<!-- file: docs/agent-tasks/platform-core/PC-07-07-reset-staging-legacy-routes.md -->
<!-- version: 1.0.0 -->
<!-- guid: cde989a4-18b7-42db-a937-585ea355d575 -->
<!-- last-edited: 2026-11-04 -->

# PC-07-07 — Reset staging through legacy.Maintenance

**Full title:** Reset staging: both legacy reset routes stage `KindReset` of the one swap through
`legacy.Maintenance`; the post-reset hook; the `core.storage.swap` keys.

| Field | Value |
|---|---|
| ID | PC-07-07 |
| Tier | **T4 (Opus, xhigh to max).** Destructive, restart-bound and cross-filesystem; a reset deletes library files on the reset boot, and every refusal and recovery path (expiry, another binary, a failed set-aside, a read-only root, a crash mid-hook) must leave the store whole or finish. |
| Wave | W07 (stage S2b, release `S2br1`); the next PR after the flip, replacing its 503 |
| Depends on | PC-07-05 |
| Trailer | `Stage-Release: S2b 1` |
| Kind | Live, state-writing PR of S2b r1 (per-PR prev-build tests; no full rehearsal, 09 §9.2.1 A-13) |
| Release note | §9.2.1 row 1 (S2b text, quoted under "Release note" below) |
| Normative digest | `docs/plans/platform-core/digests/S2b.md` sections 4.2, 5.6, 5.7, 5.8, 7 |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-07-07 -b feat/pc-07-07-reset-staging origin/main
cd "$REPO/../audiobook-organizer-pc-07-07"
git rebase origin/main
git worktree list
```

**Safety:** every destructive test runs on directories the test created under `t.TempDir()`. Never point a
reset, a set-aside or the hook at a store or library you did not create.

## Goal

Both legacy reset routes stop deleting anything in the request. Behind HEAD's chain and HEAD's confirm
interlock (400 without it, `handler.go:465-476,540-547` at HEAD), `POST /api/v1/system/factory-reset` stages
`ResetFactory` and `POST /api/v1/system/reset` stages `ResetStore` through
`module.StorageHandles.Maintenance.StageFactoryReset(ctx, requestedBy, req)`, which is `kernel.stageReset`. The
next boot sets every target aside on its own filesystem and boots a fresh install whose batch writes HEAD's
factory-default blob; for `ResetFactory` books' post-reset hook then runs HEAD's file side effects.

## Exact files

Create:

- `core/kernel/reset.go` — `kernel.stageReset(ctx, requestedBy string, req storage.ResetRequest)
  (storage.ResetStaged, error)` (05-storage §5S.3.15 "Who stages", four ordered steps), the reset-boot
  completion (`applied` after P4d step 1 at S2b; `hook_done` by the kernel when no app hook), the P4d sweep
  of expired `previous/` directories after readiness.
- `core/legacy/maintenance.go` — `Maintenance`, `ConfigResetArgs`, `ConfigResetPlan` (with `Paths`),
  `ConfigResetRun` (with `Done`), and the `ConfigOpenEnv.Reset` field if PC-07-05 did not add it.
- `core/internal/restoreswap/reset.go` — `Swapper.StageReset` (target resolution, rename pre-validation,
  `restorejournal.CreateIntent`, journal `staged`) and the P0a `KindReset` branch (in-flight rename before
  `set_aside_started`, journaled `moved` renames, `set_aside`, move-back on failure, `rejected.json`).
- `internal/books/post_reset_hook.go` — the books start hook (Run step 2) for `ResetFactory`.
- `internal/books/reset_test.go`.

Modify:

- `internal/books/module.go` — register the post-reset hook (`Hook.ExtendsStart`, exempt from the 60 s start
  limit, bounded by `core.storage.swap.reset_hook_timeout`).
- `internal/books/store/pebble_store.go` — nothing left to delete if PC-07-05 already removed `Reset`
  (verify with the grep below); otherwise delete it here.
- `internal/server/handlers/system/handler.go` — `ResetSystem` and `FactoryReset` call `Maintenance` with
  `{Kind, Confirm: "RESET", LegacyConfirm: true, DeleteLibraryFiles: Kind == ResetFactory}`; map errors
  (400 dangerous root, 422 confirm or invalid, 409 `ErrSwapPending`); keep HEAD's `{data: {message}}` shape;
  write no setting.
- `core/coretest/crashmatrix/` and `internal/books/crashmatrix_abo_test.go` — the `KindReset` scenarios.
- Not in the row (see Planner notes): `core/storage/reset.go` (`ResetRequest`, `Validate`, `ResetStaged`,
  `LibraryConfirmError`, the reset sentinels, `ErrSwapPending` re-export, `ErrRenameRefused`,
  `ErrResetRefused`); `internal/books/configstore/reset.go` (name coined here: books' `ResetArgs` with the
  preflight, `IsDangerousRoot` refusal, `factoryDefaultConfig` as `fresh_rows`, hook arguments and `Paths`);
  `internal/backup/` (ABO's pre-S8 restore staging calls `restorejournal.CreateIntent` and uses
  `restorejournal.Intent`, so one slot excludes a reset and a restore across processes); the §5S.7 keys
  `core.storage.swap.intent_ttl` (24h), `core.storage.swap.keep_previous` (168h),
  `core.storage.swap.reset_hook_timeout` (30m, not hot-reloadable), all `ProtectOperator`, registered by the
  kernel; 06 §6.5.1's writer table rows for every new label.

Plus a `changelog.d/` fragment carrying the release note.

## Re-verify before editing

```bash
grep -E '^(shipped_stage|release):' ci/stage.txt                        # shipped_stage: S2b / release: S2br1
grep -n 'reset_unavailable' internal/server/handlers/system/handler.go  # 2 hits (PC-07-05's 503)
grep -n 'func (h \*Handler) ResetSystem\|func (h \*Handler) FactoryReset' internal/server/handlers/system/handler.go   # 2 hits (:463, :538 at HEAD)
grep -n 'IsDangerousRoot' -r internal --include=*.go | grep -v _test | head -3    # HEAD's guard you call
grep -n 'deleteOLDataLocked' -r internal --include=*.go | head -3                  # HEAD's OL wipe the hook reuses
grep -n 'func factoryDefaultConfig\|factoryDefaultConfig()' -r internal --include=*.go | head -3
grep -n 'func (p \*PebbleStore) Reset()' internal/books/store/pebble_store.go     # expect 0 (deleted by PC-07-05)
ls core/storage/restorejournal/restorejournal.go core/internal/restoreswap/swap.go # PC-07-02
grep -n 'CreateIntent\|link(' internal/backup/*.go | head                         # how ABO stages restores now
```

## Code contract (copy exactly)

From 05-storage §5S.3.15: `storage.ResetRequest{Kind restorejournal.ResetKind "kind"; Confirm string
"confirm"; DeleteLibraryFiles bool "delete_library_files,omitempty"; LibraryRoot string
"library_root,omitempty"; LibraryFileCount int "library_file_count,omitempty"; LegacyConfirm bool "-"}`,
`(ResetRequest) Validate() error`, `ResetStaged{ID "reset_id"; LibraryRoot; LibraryFileCount}`,
`*LibraryConfirmError{Root string; Files int}` with `Error` and `Is`, and the sentinels
`ErrResetConfirmRequired`, `ErrResetRequestInvalid`, `ErrLibraryConfirmRequired`, `ErrSwapPending =
restorejournal.ErrSwapPending`, `ErrRenameRefused`, `ErrResetRefused` with their exact messages.

From 06 §6.4.11 and 05-storage §5S.3.15 (package `legacy`):

```go
type Maintenance interface {
	StageFactoryReset(ctx context.Context, requestedBy string, req storage.ResetRequest) (string, error)
}
type ConfigResetArgs interface {
	ResetArgs(ctx context.Context, req storage.ResetRequest) (ConfigResetPlan, error)
}
type ConfigResetPlan struct { Hook []byte; Rows []storage.KV; LibraryRoot string; LibraryFiles int; Paths []string }
```
`ConfigResetRun{ID string; Args []byte; Delete []string; DeleteLibraryFiles bool; Done func(ctx
context.Context, failed []string) error}` (06 §6.4.11); `Done` journals `hook_done` (failed entries in
`Record.Failed`) and deletes `inflight.json`. 06 §6.4.11 says 05-storage §5S.3.11 wins on member spelling, and
06's copy of `ConfigResetPlan` omits `Paths`: copy the 05-storage §5S.3.11 block (it has `Paths []string`,
recorded as the intent's `hook_paths`).

## Invariants (05-storage §5S.3.15; 09 S2b Data/keys)

1. Nothing destructive runs in the request; the handler writes no setting; staging requests a restart through
   the lifecycle supervisor (exit 75).
2. One slot: a second staging of any kind, in any process, gets `storage.ErrSwapPending`.
3. Staging pre-validates every rename (`ErrRenameRefused`, nothing staged) and refuses a dangerous `RootDir`
   with 400 when the reset will wipe it (books' error wrapping `ErrResetRefused`).
4. Each target is set aside beside itself on its own filesystem by §5S.3.15's one rule; a `Recreatable`
   auxiliary DB on another filesystem is listed in `delete`, not moved. The operator config file and the
   gap-fill file `<db parent>/config.yaml` stay.
5. Step a's rule rejects only `pending.json`; once renamed to `inflight.json` (before `set_aside_started`) the
   journal is never rejected and any core build resumes it to `set_aside`; a failed rename moves back every
   `moved` entry in reverse order before journalling `rejected`, then boots the untouched store `degraded`
   (`factory_reset_failed`).
6. The reset boot is a fresh install with a new install ID whose batch writes `fresh_rows` (HEAD's
   `factoryDefaultConfig`: `root_dir` empty, `setup_complete` false), so a stale `ENABLE_AUTH=false` loses.
7. The journal reads `applied` only after P4d step 1 has run (S2b: no core keyring yet; from S3
   `secrets.Open` with `ResetBoot`). `CheckReader` reports `restore_inflight` until `hook_done`.
8. The hook never fails boot: per-entry errors are logged, audited and reported as
   `factory_reset_cleanup_incomplete`; it always records `hook_done` (only a crash reruns it); it skips kept
   paths (the DB parent, the state directory, the set-aside destinations); its deletions go through
   `ConfigOpenEnv.Files`, bounded by the state directory, `<db parent>`, `delete` and `hook_paths`.
9. With no app hook, or for `ResetStore`, the kernel removes the `delete` entries and records `hook_done`.

## TDD: tests first (spec names, the row's full list)

`TestFactoryResetUnderClaims` (shadow and strict), `TestFactoryResetPostFloorBoots`,
`TestFactoryResetFromLegacyRoute_S2b`, `TestSystemResetStagesStoreOnlyReset_S2b`,
`TestFactoryResetRejectedLeavesStoreAndSettings`, `TestFactoryResetIntent_RejectedAfterInterveningOpen`,
`TestCheckReaderRefusesPendingReset`, `TestSwapSlotExclusive` (in one process and in two; stagers of any
kinds), `TestStageFactoryReset_DangerousRootRefused`, `TestFactoryResetCrossDeviceTargets` (two filesystems:
use a tmpfs or loop mount on the CI runner; skip with a reason when none is available locally, never in CI),
`TestFactoryResetSetAsideFailureKeepsStore`, `TestPostResetHook_RemoveErrorsStillDone`,
`TestFactoryResetReadOnlyRootDirCompletes`, `TestPostResetHookSkipsKeptPaths`,
`TestFactoryResetNativeAppCompletes` (hello: kernel records `hook_done`), `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`,
`TestHotfixUpdater_RefusesWhileResetPending`, `TestRestartRequest_ExitsRestartCode` (in
`scripts/tests/test_unit_lifecycle.py`, run by `unit-lifecycle`: staging a reset brings the service back without
operator action). Also extend `TestInflightPrecedesFirstRename` with the `KindReset` case and both
crash-matrix instances with: a `KindReset` of both kinds, a reboot after `expires_at` and by another binary
mid-set-aside, and an error return on every operation from the in-flight rename to the first `moved` record
(ends `rejected`, store untouched).

## Steps

1. Re-verify; read 05-storage §5S.3.15 in full, 05-backup §5B.3.4 steps a and 8, and 06 §6.4.11.
2. `core/storage/reset.go` and its unit tests (`Validate` table).
3. `restoreswap/reset.go`: `StageReset` and the P0a branch; crash-matrix scenarios first.
4. `kernel/reset.go`: `stageReset`, completion rules, `previous/` sweep; register the three keys.
5. ABO `internal/backup`: restore staging through `restorejournal.CreateIntent` with `restorejournal.Intent`
   (same JSON shape as S1's, so a rollback reads it).
6. Books: `configstore/reset.go` (`ResetArgs`), `post_reset_hook.go`, module wiring.
7. Handlers: replace the 503 with the staging call.
8. Writer-table rows in 06 §6.5.1; acceptance.

## Acceptance

```bash
go build ./... && go -C core build ./...                                            # exit 0
go test -race -count=1 ./internal/books/... ./internal/server/handlers/system/... ./internal/backup/...   # ok
go -C core test -race -count=1 ./kernel/... ./internal/restoreswap/... ./storage/...                     # ok
go test -count=1 -run 'TestFactoryReset|TestSystemResetStagesStoreOnlyReset_S2b|TestSwapSlotExclusive|TestPostResetHook|TestCheckReaderRefusesPendingReset' ./...   # ok
go test -count=1 -run TestBootCrashMatrix_ABO ./internal/books/ && go -C core test -count=1 -run TestBootCrashMatrix ./kernel/   # ok
grep -n 'reset_unavailable' internal/server/handlers/system/handler.go               # no output
grep -rn 'func (p \*PebbleStore) Reset()' internal/books/store                        # no output
make -C core ci && make ci                                                           # exit 0
```

`unit-lifecycle` green including the reset case.

## Release note (09 §9.2.1 row 1, S2b; put verbatim in the `changelog.d/` fragment)

"A factory reset or a reset of the database on this release takes effect at the next restart; undoing a
factory reset restores the database but not the library files or Open Library data it deleted, and files it
could not delete are listed in the log and in health."

## STOP conditions

- 05-storage §5S.3.11 and 05-config §1.3.3 disagree on a `legacy` member's fields (06 defers to 05-storage).
- A test would delete outside `t.TempDir()`.
- The cross-device test cannot get a second filesystem in CI.

## Out of scope

The core route `POST /api/core/v1/system/factory-reset` and `internal/storagehttp` (S4 r2, PC-11-03); the
native `delete_library_files` confirmation flow beyond what `kernel.stageReset` needs; restore hooks (S4 r2).

## Rollback

Binary swap, with row 1's cost: `make rollback` refuses while a reset is staged or in flight; an older build's
factory reset (`PebbleStore.Reset`) wipes the whole range including `c/`. A revert of this PR is admitted as an
exact inverse; the 503 of PC-07-05 returns.

## Repo rules

- Headers on every new or changed file; bump `06-app-sdk.md` for the writer-table rows.
- Every commit `Stage-Release: S2b 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n>
  --rebase`; never squash; never `--admin`. Required: `extraction-gates` (incl. the prev-build one-format tests),
  `unit-lifecycle`, `core-ci`, `docker-image`, `vet-darwin`, `vet-integration`.
- CLAUDE.md concurrency: the hook's `RootDir` wipe is one directory read and a per-entry `RemoveAll`; keep it
  sequential and bounded by the timeout (strict accounting of failed entries); do not fan out over a NAS.

## Planner notes

- **Files missing from the row:** `core/storage/reset.go` (the request type and errors 05-storage owns),
  `internal/books/configstore/reset.go` (books' `legacy.ConfigResetArgs` must be on the built legacy config
  store), `internal/backup/` (ABO's restore staging onto `restorejournal.CreateIntent`, needed by
  `TestSwapSlotExclusive`), and the 06 §6.5.1 writer-table rows.
- **`internal/books/store/pebble_store.go`** may need no change: PC-07-05's brief deletes `PebbleStore.Reset`
  there because its 503 leaves it dead.
- **06 §6.4.11's `ConfigResetPlan` lacks `Paths`** that 05-storage §5S.3.11 and 05-config §1.3.3 carry; 06 defers
  to 05-storage, so the brief follows it. A spec cleanup could add the field to 06's copy.
- **`TestRestartRequest_ExitsRestartCode`** lives in `scripts/tests/test_unit_lifecycle.py`, which is not in the
  row's file list.
