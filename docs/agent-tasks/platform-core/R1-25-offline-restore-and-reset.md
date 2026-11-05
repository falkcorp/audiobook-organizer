<!-- file: docs/agent-tasks/platform-core/R1-25-offline-restore-and-reset.md -->
<!-- version: 1.1.4 -->
<!-- guid: 5c692ae4-468d-4905-b950-4341dd0ae790 -->
<!-- last-edited: 2026-11-05 -->

# R1-25 — Offline restore and reset: app.OpenOffline and OfflineCommands (storage restore-offline --checkpoint/--archive/--into, storage reset as KindReset), ABO's restore staged through the swap with exit 75, HEAD's two gin reset routes deleted and the SPA's reset control hidden, the updater refusing while a swap is in flight

| Field | Value |
|---|---|
| ID | `R1-25` |
| Tier | **T4** (Opus / xhigh): destructive offline restore and reset paths; every target pre-validated |
| Release / wave | R1 / W18 |
| Depends on | R1-23, R1-21 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-storage §5S.3.12 (restore-offline), §5S.3.15 (KindReset, offline only in R1); 05-backup §5B.3.4; 05-updater §5U.12 (swap-slot refusal from R1); 06 §6.4.10 R1 (OpenOffline, OfflineCommands); 09 §9.4 R1 Content: storage, release note |
| Branch | `agent/platform-core-r1-25-offline-restore-and-reset` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/app/offline.go`
- `core/storage/reset.go`
- `core/internal/restoreswap/reset.go`
- `cmd/`
- `internal/backup/`
- `internal/server/handlers/system/`
- `internal/server/wire_system_routes.go`
- `internal/database/pebble_store.go`
- `internal/books/reset_hook.go`
- `internal/updater/updater.go`
- `web/src/pages/Settings.tsx`
- `web/src/services/api.ts`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-25" -b agent/platform-core-r1-25-offline-restore-and-reset origin/main
cd "$REPO/.worktrees/platform-core-r1-25"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Destructive offline paths, every target pre-validated (09 §9.4 R1 "Content: storage"; 05-storage §5S.3.12
`restore-offline`, §5S.3.15 `KindReset` "offline only in R1"; 05-backup §5B.3.4; 05-updater §5U.12 swap-slot
refusal from R1; 06 §6.4.10 R1 `OpenOffline`, `OfflineCommands`):

1. **`app.OpenOffline`** returning `app.Offline` (`core/app/offline.go`; 03 §3.3 `app` row): the app-side wrapper over
   `kernel.OpenOffline`, the read-write open for offline commands; it runs the verdicts a serve boot runs (the staged
   swap, the per-owner level guard, the upgrade floor) and refuses every store a serve boot would refuse (06 §6.5.1).
   **Every ABO CLI write subcommand opens through it** (`TestCLISubcommandsUseOfflineOpen`, which also covers the
   `bench` tag, 05-storage §5S.4).
2. **`app.OfflineCommands`** wired into ABO's cobra tree (`cmd/`): `storage restore-offline` (`--checkpoint <name>`,
   `--archive <file>`, `--into <empty dir>`, and `--accept-loss`: restoring a completed checkpoint or an archive is
   refused without it, because everything written after the source was taken is lost, 05-backup §5B.3.4 and
   05-storage §5S.3.12; a restore of the latest attempt's open checkpoint needs no flag. `--keyring` and `--gap-fill`
   of the archive form are F8-01's, not this row's. R1-27's `make rollback-restore` calls this flag;
   restores with the service stopped, in place through the journaled swap
   or into an empty directory inside the scratch area; never boots the app; 05-storage §5S.3.12), `storage reset`
   (stages `KindReset` — `ResetStore` or `ResetFactory` — with the service stopped, applied at the next start;
   `core/storage/reset.go`, `core/internal/restoreswap/reset.go`). Wrap every entry of `app.OfflineCommands`
   generically (06 §6.4.5: "it wraps each entry of `app.OfflineCommands` as a cobra command"), so R1-24, which follows
   this row and adds `storage prune-checkpoints` to the list in `core/app/offline.go`, needs no `cmd/` edit.
3. **ABO's restore staged through the swap**: HEAD's live restore (`internal/backup/backup.go:777-870`, which
   extracts over the running store, 09 §9.1) becomes a stage of the one journaled swap (`restorejournal.CreateIntent`,
   R1-21) that takes effect at the next restart, which the service requests by exiting **75** (05-backup §5B.3.4;
   06 §6.5.2 "Restart request"; `Lifecycle.RequestRestart`).
4. **HEAD's two gin reset routes deleted** (`POST /api/v1/system/reset` and `/system/factory-reset`,
   `internal/server/wire_system_routes.go:45-48`, handlers `ResetSystem` `handler.go:463` and `FactoryReset`
   `:538`), with HEAD's whole-store wipe `PebbleStore.Reset` (`pebble_store.go:6310-6360` at `748fd787a`); the SPA hides
   its reset control (`web/src/pages/Settings.tsx`, `web/src/services/api.ts`) until the core route lands in F4
   (05-storage §5S.3.15; 07 §7.19 step 3).
5. **Books' reset hook** (`internal/books/reset_hook.go`, `ResetFactory` only): HEAD's file effects; it never fails
   boot (05-storage §5S.3.15 rule (1)); the set-aside stays on the store's own filesystem.
6. **The updater refuses while a swap is in flight** (`internal/updater/updater.go`; 05-updater §5U.12): the
   reader check covers the swap slot (`restore_pending`/`restore_inflight`).

Every reset target is pre-validated at staging (DATA-23: same filesystem, parent writable, not a mount point, not a
single-file bind mount; `storage.ErrRenameRefused`).

## Tests this task owns (spec names)

- `TestInflightBeforeFirstRename` (05-backup §5B.9 table): the in-flight file exists before the first rename; a
  kill between the renames makes the in-flight probe refuse `.prev` and the next boot resume or swap back.
- `TestOfflineResetStagesIntent` (05-storage §5S.3.15) and its R1 done-check twin `TestFactoryResetOffline_R1`
  (06 §6.10): with the service stopped, `storage reset --kind factory --confirm RESET` stages a `ResetFactory`
  (intent in the one slot with `hook_args`, live store and settings blob untouched); a dangerous library root with
  `--delete-library-files` is refused (06 §6.10 row text).
- `TestSwapSlotExclusive` (06 §6.10; it replaces `TestRestoreAndResetBothStaged`, 05-backup §5B.3.4) with the ABO
  stagers: R1-21 lands its core half; this row extends it to ABO's restore stager and `storage reset`.
- `TestCLISubcommandsUseOfflineOpen` (06 §6.4.6; 05-storage §5S.4).
- Also: `TestRestoreOffline` (with a cell for `--accept-loss`: a completed checkpoint and an archive are refused
  without it and restored with it; the flag does not change the open checkpoint's behaviour), `TestRestoreOfflineIntoEmptyPath`,
  `TestFactoryResetUnderClaims`, `TestCheckpointEscapeKeepsOwedHooks` (05-storage §5S.10) and the updater refusal
  (a test in `internal/updater`).
- `TestOfflineOpenRules` (05-storage §5S.10; 06 §6.10): `app.OpenOffline` read-write commits and closes cleanly; a
  read-only open writes nothing; an open with a pending migration or cutover step, an empty store, or a pending or
  in-flight swap of any kind is refused; an offline revoking write is journaled once a journal exists.
- The rest of the factory-reset table of 05-storage §5S.3.15 that has an R1 shape (every name is the spec's; each
  proves what its row says; the three that do not belong here are listed after). Write each first:
  - `TestStageFactoryReset_DangerousRootRefused`: a dangerous library root with the wipe requested is refused (400)
    and nothing is staged.
  - `TestFactoryResetSetAsideFailureKeepsStore`: a rename failure before `set_aside_started` leaves the live store
    in place and moves any completed target back (DATA8-1, DATA10-4).
  - `TestFactoryResetCrossDeviceTargets`: the state directory and the Open Library store on other filesystems, or an
    injected `EXDEV`, are each set aside beside themselves or deleted by the hook (COR8-3).
  - `TestFactoryResetResumeAfterExpiryCompletes`: a crash after some targets moved, then a boot after `expires_at`
    and, separately, by another binary, resumes the set-aside to `set_aside` and completes; a rename failure on resume
    moves back before `rejected` (DATA9-4).
  - `TestFactoryResetRejectedLeavesStoreAndSettings`: an expired intent, or one staged by another binary, leaves the
    store and every setting as they were (SEC7-5, DATA7-6).
  - `TestFactoryResetIntentRejectedAfterRollback`: an older build rejects a staged reset intent at P0a.
  - `TestRollbackRefusesInflightReset`: the rollback guard refuses an in-flight `KindReset` and passes a pending one,
    which the older build rejects (OPS8-7).
  - `TestPostResetHookRerunsUntilDone`: a crash before `Done` reruns the books reset hook at the next boot,
    idempotently.
  - `TestPostResetHook_RemoveErrorsStillDone` and `TestFactoryResetReadOnlyRootDirCompletes`: per-entry failures are
    recorded, `hook_done` is written and `factory_reset_cleanup_incomplete` is reported, so the hook never fails boot
    (OPS8-5).
  - `TestPostResetHookSkipsKeptPaths`: an entry that is, or contains, `<db parent>`, the state directory or a
    set-aside destination is never removed.
  - `TestFactoryResetWithoutLocationFile`: with no `backup.location.json`, as on R1, the set-aside completes (R10-6).
  - `TestFactoryResetNativeAppCompletes`: hello leaves no `restore_inflight` after the reset boot; `storage reset
    --delete-library-files` on an app without a reset hook is refused (422 `ErrResetRequestInvalid`) with nothing
    staged (COR8-4, COR10-9 (a)).
  - `TestResetRequestFlowsUnchanged`, the offline half: the one `storage.ResetRequest` reaches `Plan` unchanged from the
    offline command. The route half is F4-05's.
  Not this row's: `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn` (F3-07) and `TestFactoryResetPostFloorBoots`
  (R3-02).

## Dependency check

R1-23 and R1-21 merged: `test -d core/internal/restoreswap && git grep -q 'print-storage-levels' -- main.go cmd core && echo ok`.

## Re-verify (expected at `f7ac5b11e`)

```bash
sed -n 45,48p internal/server/wire_system_routes.go       # expect the two reset routes
grep -n '^func (h \*Handler) ResetSystem\|^func (h \*Handler) FactoryReset' internal/server/handlers/system/handler.go   # expect 463, 538
grep -n 'func (p \*PebbleStore) Reset(' internal/database/*.go   # HEAD's wipe (pebble_store.go at 748fd787a); R1-23 may have moved it: find it by name at your base and delete it there
grep -n 'factoryReset\|factory-reset\|resetSystem' web/src/services/api.ts web/src/pages/Settings.tsx | head   # the SPA control and client calls to hide/remove
grep -n 'os.Exit' internal/backup/*.go internal/updater/updater.go | head   # restore and updater exits (exit 75 rule)
```

## Steps

1. Tests first (red).
2. `core/app/offline.go` (`OpenOffline`, `Offline`, `OfflineCommands`, `Command`), `core/storage/reset.go`,
   `core/internal/restoreswap/reset.go` (`KindReset` staging with pre-validation and the set-aside).
3. `cmd/`: expose `app.OfflineCommands`; route every write subcommand through `app.OpenOffline`.
4. `internal/backup`: stage restores through `restorejournal.CreateIntent` and request the restart (exit 75).
5. Delete the two gin reset routes, their handlers and `PebbleStore.Reset`; hide the SPA's reset control and remove
   its client call; `internal/books/reset_hook.go`; the updater's swap-slot refusal.
6. Header bumps; fragment with the release-note lines below. Commits by step; every commit builds.

## STOP conditions (data loss)

- A reset or restore that touches the live store while the service runs.
- A reset target not pre-validated at staging, or a set-aside on another filesystem.
- A post-reset hook path that can fail boot.

## Code contract

`app.OpenOffline`, `app.Offline`, `app.OfflineCommands`, `app.Command` exactly as 06 §6.4.5 declares them; the `storage restore-offline` and `storage reset` flags as 05-storage §5S.3.12 and §5S.3.15 state.

## Acceptance

```bash
go -C core test ./app/ ./storage/ ./internal/restoreswap/ -run 'Offline|Reset|Swap|Restore' -count=1   # expect ok
go test ./cmd/ ./internal/backup/ ./internal/updater/ ./internal/books/ -run 'TestFactoryResetOffline_R1|TestCLISubcommandsUseOfflineOpen|TestSwapSlotExclusive|Inflight|Restore' -count=1 -v   # expect PASS
git grep -n '"/system/reset"\|"/system/factory-reset"' -- internal/server | wc -l     # expect 0
make build-api && ./audiobook-organizer storage reset --help >/dev/null && ./audiobook-organizer storage restore-offline --help >/dev/null && ./audiobook-organizer storage prune-checkpoints --help >/dev/null && echo CLI   # expect CLI
npm test --prefix web -- --run Settings                                              # expect pass (reset control hidden)
make test-e2e && make ci && make -C core ci                                           # expect exit 0
```

Release-note lines (09 §9.4 R1), in this PR's `changelog.d/` fragment: "A restore now takes effect at the next
restart, which the service performs itself; a container started without a restart policy must be started again."
and "In this release a factory reset or a database reset runs only from the command line, with the service stopped
(`audiobook-organizer storage reset`); the reset button in Settings returns with <R2 release>. Undoing a factory reset
restores the database but not the library files or Open Library data it deleted. A reset now leaves `config.yaml`
beside the database unchanged; delete it by hand for a full reset."

## Out of scope

The core reset route (F4); backup's core layout (F8); `make rollback-restore` (R1-27).

## Rollback

Revert the PR before release. After release, an in-flight swap must finish (boot the current build once) before any
rollback (09 §9.2.1).

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

