<!-- file: docs/agent-tasks/backfill-pools/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 20d17acb-06f5-471b-b41c-9c8786ad1766 -->
<!-- last-edited: 2026-07-05 -->

# Orchestration — backfill-pools workstream

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

**Execution mode:** /parallel-sweep — trigger: 4 mechanically-similar tasks (≥3 threshold), disjoint files per the collision matrix, gate = `make ci`. Invocation: TASK-01,02,03,04 all in wave 1.

## Waves (respect `Depends on:`)

```mermaid
flowchart LR
    subgraph Wave1
      TASK01[TASK-01 embed-scan-pool]
      TASK02[TASK-02 embedding-backfill-pool]
      TASK03[TASK-03 tag-backfill-pool]
      TASK04[TASK-04 gold-label-memoize-pool]
    end
```

- **Wave 1** (parallel, disjoint files): TASK-01, TASK-02, TASK-03, TASK-04.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`make ci`) in each
> finished worktree, opens the PR, merges (rebase/FF unless the repo profile says
> otherwise), and then **rebases every open sibling worktree** before dispatching
> anything else.
>
> **Per-merge sibling-rebase loop:** after EVERY merge to `origin/main`:
> for each open sibling worktree, `git fetch origin && git rebase
> origin/main`. A sibling that skips a rebase is a future conflict.
>
> **Conflict escalation ladder** (in order, never skip a rung): 1) clean rebase;
> 2) conflict-resolver subagent (Sonnet-class, only when the conflict spans 1–3 small
> files); 3) file-copy cherry-pick fallback — re-apply the task's file states onto a
> fresh branch from HEAD; 4) mark `rebase_blocked`, stop the lane, escalate to a human.
>
> **A wave MUST NOT start** while any of: the previous wave has an unmerged PR; any
> sibling worktree is un-rebased; the gate is red on `origin/main`; or a
> `rebase_blocked` marker is unresolved.

## Run it

```bash
# from docs/agent-tasks/backfill-pools/
./run.sh                 # print task list + set up worktrees
./run.sh 01 02 03 04
```

After each wave: gate each worktree with `make ci`, push/PR/merge as coordinator, then rebase remaining sibling worktrees onto `origin/main` before starting the next wave.
