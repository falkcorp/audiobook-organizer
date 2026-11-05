<!-- file: docs/agent-tasks/platform-core/R1-21-journaled-swap-crash-matrix.md -->
<!-- version: 1.1.6 -->
<!-- guid: 0d4840c7-333d-47fc-a23f-cbdf77a2bb4e -->
<!-- last-edited: 2026-11-05 -->

# R1-21 — The one journaled swap (core/internal/restoreswap, run by the kernel at P0a) and the crash-matrix kit coretest/crashmatrix with core's TestBootCrashMatrix over hello

| Field | Value |
|---|---|
| ID | `R1-21` |
| Tier | **T4** (Opus / xhigh): a durable swap over the live store; a wrong step order loses the database |
| Release / wave | R1 / W14 |
| Depends on | R1-20 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-storage §5S.3.7, §5S.3.15, §5S.3.16; 05-backup §5B.3.4; 06 §6.4.8, §6.5.1 (writer table, P0a); 09 §9.3 (boot writers extend the crash matrix); 03 §3.3 storage/restorejournal |
| Branch | `agent/platform-core-r1-21-journaled-swap-crash-matrix` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/internal/restoreswap/`
- `core/kernel/swap.go`
- `core/kernel/boot.go` (the P0a call only; this row is W14's one owner of the kernel wiring files)
- `core/coretest/crashmatrix/`
- `core/examples/hello/crash_test.go`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-21" -b agent/platform-core-r1-21-journaled-swap-crash-matrix origin/main
cd "$REPO/.worktrees/platform-core-r1-21"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `ac4368bdb`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

The **one journaled swap** and the **crash-matrix kit** (05-storage §5S.3.7 reader check, §5S.3.15, §5S.3.16;
05-backup §5B.3.4; 06 §6.4.8, §6.5.1 writer table and P0a; 09 §9.3 "Boot writers extend the crash matrix"; 03 §3.3
`storage/restorejournal`):

- `core/internal/restoreswap` (L1; imports `storage` and its L0 leaves, never the reverse): the swapper that
  takes a staged intent (`restorejournal.Intent`, the shared record; `CreateIntent`, the one atomic create both
  stagers call: temp file + fsync, `link(2)` to `pending.json`, `EEXIST` = `storage.ErrSwapPending`, directory
  fsync) and applies it at boot phase P0a through the in-flight file `inflight.json`, which is created **before the
  first rename** in every kind, with the journal records of 05-backup §5B.3.4 and 05-storage §5S.3.15 (restore,
  checkpoint, revert and reset kinds; the journal's record types declare a fifth, `KindRewrite`, with `StepID`, which
  R1-20 lands in `core/storage/restorejournal/` and F3-04's M8 applies in `core/internal/restoreswap/rewrite.go`, so
  this row's apply path handles the four kinds above and leaves a `KindRewrite` intent to F3-04). `storage.Admin` carries **no** staging or swap method; the kernel holds the
  swapper, stages through it and runs it at P0a (row notes; 04 §8.1 R1, COR7-10, COR8-5).
- `core/kernel/swap.go`: P0a wiring: the swap runs before the store opens, and the reader check of §5S.3.7 refuses an
  open while `inflight.json` exists except for the admitted recipes of 09 §9.2.1.
- `core/coretest/crashmatrix` (public kit, 05-storage §5S.3.16; 06 §6.4.8): kills a boot at every durable write,
  rename and fsync of 06 §6.5.1's boot-writer table (from fault-injection hooks on a `vfs.FS`-style seam), reboots,
  and checks the cross-mechanism invariants; an app runs the same kit over its full boot (`TestBootCrashMatrix_ABO`,
  R1-23).
- `core/examples/hello/crash_test.go`: core's `TestBootCrashMatrix` over hello, covering every kernel writer
  that exists after R1-20 and this PR (the checkpoint, the install marker, level stamps, sidecars, the swap's own
  steps).

## Invariants and tests (spec names; write first)

- `TestBootCrashMatrix` (09 §9.4 R1 Verification; 05-storage §5S.10): a kill or an error at any durable write, rename
  or fsync of a boot leaves a store the current build boots, with no row lost or written twice (09 §9.10).
- `TestBootCrashMatrixCoversEveryWriter`: every row of 06 §6.5.1's writer table that exists in this build is
  exercised (fails when a writer is added without a matrix row).
- `TestRestoreCrashAtEveryJournalStep` and `TestInflightPrecedesFirstRename` (the swap's journal crash tests): for
  every kind, a kill after the first live-item rename always finds the in-flight file and no `pending.json`, and the
  rollback guard refuses.
- `TestSwapSlotExclusive` (06 §6.10): two stagers never both succeed, with no mutex.
- `TestSwapFailureExitBeforeFirstMove`, `TestRestoreWithoutLocationFile`, `TestStoreGenerationBumpsOnSwap`
  (05-storage §5S.10) where their subject is the swap mechanism (the backup layout is F8's).
- Pre-validation (DATA-23): every target is pre-validated at staging (same filesystem, parent writable, not a mount
  point, not a single-file bind mount; `storage.ErrRenameRefused`); the set-aside stays on the store's own
  filesystem.

## Dependency check

R1-20 merged: `test -d core/storage/restorejournal && test -f core/kernel/storage.go && echo ok`.

## Steps

1. Write the four owned tests (red) and the kit's own self-test (a writer added without a matrix row fails).
2. `core/internal/restoreswap`: `CreateIntent`, the per-kind apply with `inflight.json` first, the journal records, the
   pre-validation of every target.
3. `core/kernel/swap.go`: run the swapper at P0a, called first in `core/kernel/boot.go`'s phase list (before R1-20's
   P2 open); the reader check against `inflight.json`.
4. `core/coretest/crashmatrix`: the fault-injection seam over every durable write, rename and fsync of the writer
   table; reboot-and-check loop; the cross-mechanism invariants.
5. `core/examples/hello/crash_test.go`: `TestBootCrashMatrix` over every kernel writer present.
6. Header bumps; fragment `changelog.d/<today>_journaled_swap.md` (`### Added`: "Internal: the one journaled swap and
   the boot crash-matrix kit."). Commits by package; every commit builds.

## STOP conditions (data loss)

- Any step order in which a kill can leave neither the old nor the new store bootable.
- `inflight.json` created after any live rename, in any kind.
- A design that needs `storage.Admin` to stage or swap.

## Code contract

`restorejournal.Intent`, `restorejournal.CreateIntent` and the journal record types as 05-backup §5B.3.4 step 8 and 05-storage §5S.3.15 define them; the crash-matrix kit's API as 06 §6.4.8 declares it.

## Acceptance

```bash
go -C core test ./internal/restoreswap/... ./coretest/crashmatrix/... ./kernel/ ./examples/hello/ -race -count=1   # expect ok
go -C core test ./examples/hello/ -run 'TestBootCrashMatrix$|TestBootCrashMatrixCoversEveryWriter' -count=1 -v     # expect PASS
go -C core test ./internal/restoreswap/ -run 'TestRestoreCrashAtEveryJournalStep|TestInflightPrecedesFirstRename|TestSwapSlotExclusive' -count=1 -v   # expect PASS
make ci && make -C core ci                                                                                         # expect exit 0
```

`core-ci` runs `TestBootCrashMatrix`; it never moves to nightly (09 §9.2.10).

## Out of scope

ABO's restore staging and exit 75 (R1-25); `KindReset`'s staging command and books' reset hook (R1-25); ABO's
crash-matrix instance (R1-23).

## Rollback

Revert the PR; ABO does not use the swap yet.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green, `gh pr merge <n> --rebase` (enqueues it; the merge queue re-tests it on top of `main`, D15). Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
