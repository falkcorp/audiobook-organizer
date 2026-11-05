<!-- file: docs/agent-tasks/platform-core/R1-24-checkpoint-sweeper-and-prune.md -->
<!-- version: 1.1.0 -->
<!-- guid: 88124076-5269-41e3-875c-b216bc9ff445 -->
<!-- last-edited: 2026-11-05 -->

# R1-24 — Checkpoint sweeper (every boot after readiness and hourly), storage prune-checkpoints, health storage_checkpoint_open, and the split of HEAD's fixed-token source with the authenticated degraded field

| Field | Value |
|---|---|
| ID | `R1-24` |
| Tier | **T3** (Opus / high): a sweeper that deletes checkpoints; the retention rule decides what a rollback can restore |
| Release / wave | R1 / W17 |
| Depends on | R1-23, R1-25 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-storage §5S.3.12, §5S.4 step 15; 05-health §5H.3.1, §5H.9, §5H.10 Landing R1 and R1 row; 09 §9.2.3 (the checkpoint and its 7 days); 06 §6.4.10 R1 (OfflineCommands) |
| Branch | `agent/platform-core-r1-24-checkpoint-sweeper-and-prune` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/storage/checkpoint_sweep.go`
- `core/kernel/`: `sweeper.go`, `boot.go`
- `core/app/`: `offline_prune.go`, `offline.go`
- `internal/server/handlers/system/`: `handler.go`, `health_tokens_test.go`
- `internal/books/module.go`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-24" -b agent/platform-core-r1-24-checkpoint-sweeper-and-prune origin/main
cd "$REPO/.worktrees/platform-core-r1-24"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

The first R1 boot on a non-empty store takes a checkpoint holding the whole database, saved passwords and
sign-in tokens included, so R1 must also ship what deletes it (09 §9.2.3; 09 §9.4 R1 "Content: storage";
05-storage §5S.3.12 "Retention", §5S.4 step 15; CONFORM-6):

1. **The checkpoint sweeper** (`core/storage/checkpoint_sweep.go`, run by a kernel lifecycle component
   `core/kernel/sweeper.go`, registered in `core/kernel/boot.go`; this row is W17's one owner of the kernel wiring
   files): at 05-storage §5S.4 step 15 of every boot (after readiness) and then **hourly** while
   serving, it scans `.mediacore-checkpoints/`, its scratch area and every `restore/<id>/failed/`, writes each record's
   `delete_after` exactly per 05-storage §5S.3.12's "Retention" row (completed: `completed_at + 7 days`; open: kept
   while it is still the rollback of the live store, then `now + 7 days`; swapped back: from the swap-back), and
   deletes every checkpoint whose `delete_after` has passed: **the record first**, then `<name>/` with `os.RemoveAll`,
   then `.migration-checkpoint` when it names it. Each deletion is logged (audited from F2). The period is fixed; no
   setting lengthens it. Scratch copies are deleted 7 days after their mtime.
2. **`storage prune-checkpoints`** as an `app.OfflineCommands` command (06 §6.4.10 R1: present from R1;
   implemented in `core/app/offline_prune.go`): deletes eligible checkpoints sooner, with the service stopped,
   through the same delete order. Add it to the `OfflineCommands` list R1-25 created in `core/app/offline.go`;
   R1-25 already exposes that list through ABO's cobra tree, so `cmd/` needs no edit here.
3. **Health tokens on the authenticated route only** (05-health §5H.3.1 "The unauthenticated token set"): while any
   checkpoint exists health reports `storage_checkpoint_retained` with its `delete_after`; an open scratch copy
   raises `storage_checkpoint_open` with its age; `<namespace>_storage_checkpoint_bytes` reports the size
   (05-storage §5S.8). Before F8 these travel the `degraded` field of HEAD's **authenticated**
   `GET /api/v1/system/status` (`settings.manage`, `wire_system_routes.go:28`), never `/health`. **R0-06 created
   that field** with its one token `auto_update_paused` (05-health §5H.10 R0 row); this row extends it with the
   posture tokens and does not create it.
4. **Split HEAD's fixed-token source** (`internal/server/handlers/system/handler.go:222-275` builds `/health`'s
   `degraded` list) into the unauthenticated set (`memdb_warming`, failed `Degraded` startup-step tokens
   `metrics_disabled`/`search_index`, `basic_gate_no_password`) and the authenticated rest, pinned by
   `TestHealth_UnauthenticatedTokenSetGolden` (05-health §5H.9) (05-health §5H.10 Landing R1: "the split of HEAD's
   fixed-token source and the authenticated `degraded` field of `GET /api/v1/system/status`"). The authenticated
   field is fed by an injected func: `internal/books/module.go` hands the kernel's token source to the handler.

## Tests this task owns (spec names)

- `TestCheckpointAutoDeletedAfterSevenDays` (09 §9.4 R1 Verification; over R1's first-boot checkpoint, with a fake
  clock: a completed checkpoint is gone after the first sweep past `completed_at + 7 days` and present before).
- `TestPruneCheckpoints` (05-storage §5S.3.12).
- Also 05-storage §5S.10: `TestOpenCheckpointKeptWhileRollback`, `TestOrphanedOpenCheckpointDeletedAfterSevenDays`,
  `TestPartialCheckpointSwept`, `TestDryRunScratchModeAndSweep` (the sweep half).
- `TestHealth_UnauthenticatedTokenSetGolden` (05-health §5H.9; created by R0-06 in
  `internal/server/handlers/system/health_tokens_test.go` with its R0 cell): add the R1 cells against HEAD's
  `/health` and `GET /api/v1/system/status`: with every token raised at once, `/health`'s `degraded` list carries only
  the fixed set; `storage_checkpoint_retained` and `storage_checkpoint_open` appear only in
  `/api/v1/system/status`'s `degraded`, beside R0's `auto_update_paused`. Do not rewrite the R0 cell.
- A crash between record delete and directory delete leaves garbage the next sweep removes (crash-matrix row for
  this writer: add it to R1-21's kit through `TestBootCrashMatrix`/`_ABO`, 09 §9.3).

## Dependency check

R1-23 merged (the kernel opens the store and takes the first-boot checkpoint):
`git grep -q 'print-storage-levels' -- main.go cmd core && echo ok`. R1-25 merged (`OfflineCommands` exists):
`git grep -q 'func OfflineCommands' -- core/app/offline.go && echo ok`. R0-06's field and golden exist:
`grep -c 'auto_update_paused' internal/server/handlers/system/handler.go internal/server/handlers/system/health_tokens_test.go`
(expect ≥ 1 each). Any of these missing: STOP.

## Re-verify (expected at `f7ac5b11e`)

```bash
sed -n 222,275p internal/server/handlers/system/handler.go | grep -c 'degraded\|Degraded'   # expect >= 1: HEAD's fixed-token list
sed -n 28p internal/server/wire_system_routes.go                                          # expect the GET /api/v1/system/status route
```

## Steps

1. Tests first (red) with a fake clock in `core/storage` and `core/kernel`; the golden for the unauthenticated set.
2. `core/storage/checkpoint_sweep.go` (record-first delete order, `delete_after` rules), `core/kernel/sweeper.go`
   (step 15 and hourly) with its registration in `core/kernel/boot.go`, `core/app/offline_prune.go` (the offline
   command) and its entry in `core/app/offline.go`'s `OfflineCommands`.
3. Split the fixed-token source in `internal/server/handlers/system/handler.go`; extend R0-06's authenticated
   `degraded` field of `GetSystemStatus` (`handler.go:278` at the plan's base) with the posture tokens, fed by an
   injected func the kernel's tokens back; wire it in `internal/books/module.go`; add the R1 cells to
   `health_tokens_test.go`.
4. Crash-matrix row for the sweeper's deletes (R1-21's kit).
5. Header bumps; fragment with the release-note line above. Commit:
   `feat(storage): checkpoint sweeper, prune-checkpoints and authenticated storage tokens`.

## STOP conditions

- A sweep order that can leave a record naming a half-deleted directory.
- Any posture token reaching the unauthenticated `/health`.
- A setting that lengthens retention.

## Code contract

`storage prune-checkpoints` as an `app.Command` in `app.OfflineCommands` (06 §6.4.5); the sweeper is unexported kernel machinery.

## Acceptance

```bash
go -C core test ./storage/ ./kernel/ ./app/ -run 'Checkpoint|Prune|Sweep' -count=1 -v      # expect PASS incl. TestCheckpointAutoDeletedAfterSevenDays, TestPruneCheckpoints
go test ./internal/server/handlers/system/ -run 'Health|Status' -count=1                  # expect ok, TestHealth_UnauthenticatedTokenSetGolden PASS (R0 and R1 cells)
make build-api && ./audiobook-organizer storage --help | grep -c prune-checkpoints          # expect 1
make ci && make -C core ci                                                                  # expect exit 0
```

Release note line (09 §9.4 R1): "The first start after this upgrade keeps a full copy of the database, including
saved passwords and sign-in tokens, beside it for 7 days; `audiobook-organizer storage prune-checkpoints` deletes it
sooner." Put it in this PR's `changelog.d/` fragment.

## Out of scope

`storage restore-offline`, `storage reset` (R1-25); the core health registry (F2); `/api/core/v1/health` (F8).

## Rollback

Revert the PR before the R1 release. After R1 ships, the sweeper is what keeps the checkpoint's 7-day promise; do
not revert it without the owner.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

