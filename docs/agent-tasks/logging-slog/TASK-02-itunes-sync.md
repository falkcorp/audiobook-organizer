<!-- file: docs/agent-tasks/logging-slog/TASK-02-itunes-sync.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2dac907b-f4ee-4bd3-847e-0492c429e76c -->
<!-- last-edited: 2026-07-01 -->

# TASK-02 — Wire logging.Info(ctx) into iTunes sync ops (SLOG-W13b)

## ⛔⛔⛔ THIS TASK IS BLOCKED — DO NOT IMPLEMENT NEW ITUNES SYNC LOGIC ⛔⛔⛔
**Verified 2026-07-01: there is currently no valid mechanical work for this task.** Every iTunes plugin operation entry point is an unimplemented stub, and there is zero `logging.WithOp` usage anywhere in the iTunes code. Follow the steps below to CONFIRM this and then CLOSE the task as a no-op. Do not write any iTunes sync implementation — that is an entirely different, much larger feature and is explicitly out of scope for this logging workstream.

**Priority:** P3 · **Effort:** XS (verification + close only) · **Recommended subagent:** Haiku · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sl-itunes-sync" -b agent/sl-itunes-sync origin/main
cd "$REPO/.worktrees/sl-itunes-sync"
git rebase origin/main
```

## Goal
**BLOCKED.** Confirm (via the greps below) that no iTunes sync op is currently wired into `logging.WithOp`, then close the underlying tracking item (SLOG-W13b) as a documented no-op. Do NOT open a PR with code changes for this task — there is nothing safe to change under the workstream's ground rule ("replace raw slog ONLY inside op-context flows where `logging.WithOp` is upstream").

## Background (verify before editing — this IS the task)
Run every grep below yourself. All of them are expected to confirm the blocked state; if any of them shows different results than expected, STOP and escalate to a human rather than guessing at scope.

```bash
# 1. Confirm every itunes plugin op entry point is an unimplemented stub.
grep -rn "TODO: Implement" internal/plugins/itunes/*.go
# expected: 5 hits, one each in sync.go, position_sync.go, import.go,
# path_reconcile.go, path_repair.go — each function body is just a TODO
# comment followed by `return nil`.

# 2. Confirm there is zero logging.WithOp / logging.Info / logging.Warn
#    usage anywhere in the itunes tree (i.e. no op-context flow reaches
#    any itunes code today).
grep -rn "logging.WithOp\|logging\.Info\|logging\.Warn" internal/itunes/ internal/plugins/itunes/
# expected: no output at all.

# 3. Confirm the raw-slog-containing service functions have no non-test
#    callers (i.e. they are effectively dead code today).
grep -rn "MigrateSmartPlaylists\|\.PushDirty(\|Positions\.Sync\|positionSync\.Sync" --include="*.go" . | grep -v _test
# expected: at most a comment reference inside
# internal/plugins/itunes/position_sync.go ("This should call
# p.svc.Positions.Sync().") — a TODO comment, not an actual call.
```

Why this matters: the raw `slog.*` calls that DO exist (in `internal/itunes/service/playlist_sync.go`, `position_sync.go`, `validate.go`, `importer.go`, `writeback_batcher.go`, `track_provisioner.go`, `location_normalize.go`) all live in functions with no `ctx context.Context` parameter, called (if at all) only from the stub `run*` functions above — which themselves never call into them. Per this workstream's ground rule: "Code outside ops (startup, background goroutines) can stay as raw slog." These functions are not just outside an op — they are not reachable from any op at all today. There is no ctx to thread, and threading one in would require actually implementing iTunes sync (writing the stub bodies), which is a completely different, unscoped feature task.

## Step-by-step
1. Run all 3 greps in Background above. Save their output.
2. If (and only if) all 3 greps confirm the expected "blocked" state exactly as described: do not modify any source file. Skip straight to step 3.
3. If any of the 5 stub functions (`runSync`, `runPositionSync`, `runImport`, `runPathReconcile`, `runPathRepair`) has since been implemented (i.e. grep #1 returns fewer than 5 hits), STOP — the situation has changed since this brief was written. Do not guess at what to do; report back that TASK-02's premise is stale and needs re-scoping by a human, and do not open a PR.
4. Assuming the blocked state is confirmed: remove your worktree (nothing to commit) and report the task as closed-as-no-op:
   ```bash
   cd "$REPO"
   git worktree remove "$REPO/.worktrees/sl-itunes-sync"
   git worktree prune
   ```
5. In your final report to the coordinator, state explicitly: "TASK-02 (SLOG-W13b) closed as no-op — verified 2026-07-01 that no iTunes op-context flow exists yet (all 5 plugin op entry points are TODO stubs, zero logging.WithOp usage in the itunes tree). Re-open only after a future task implements one of the iTunes plugin op stubs."

## How to test
No code changes, so no build/test is required. If you did run any greps as scratch commands, that's the extent of verification needed. Do NOT run `make ci` expecting a diff — there should be none.

## Acceptance criteria
- [ ] All 3 confirming greps from Background were run and their output matches the "expected" description.
- [ ] No source files were modified.
- [ ] No PR was opened.
- [ ] Worktree removed and pruned.
- [ ] Final report states the task is closed as no-op with the reason, per step 5.

## Commit message
N/A — no commit is created for this task. If you find yourself drafting a commit message, you have gone out of scope; stop and re-read the Goal section.

## PR + merge
N/A — do not open a PR for this task.

## Idempotency / Rollback
Idempotency: this task has no code effect, so re-running it is always safe — it just re-confirms the blocked state. Rollback: not applicable, nothing was changed.