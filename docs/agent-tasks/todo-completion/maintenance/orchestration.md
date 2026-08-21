<!-- file: docs/agent-tasks/todo-completion/maintenance/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: ac4db05b-18f4-4d6a-bfcf-28299865e0b2 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — maintenance workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK069[TASK-069 wire-a-durable-freshness-sta]
      TASK071[TASK-071 build-a-report-only-counter-]
      TASK072[TASK-072 give-maintenance-jobs-v1-int]
      TASK074[TASK-074 build-a-detection-only-repor]
      TASK076[TASK-076 read-through-audit-of-the-8-]
      TASK077[TASK-077 build-a-report-only-census-o]
      TASK078[TASK-078 extend-purge-empty-authors-r]
      TASK079[TASK-079 author-narrator-swap-repair-]
      TASK080[TASK-080 narrow-the-3-remaining-maint]
      TASK081[TASK-081 task-04-build-the-idempotent]
    end
    subgraph Wave2
      TASK070[TASK-070 extend-the-repoint-repair-to]
    end
    subgraph Wave3
      TASK075[TASK-075 new-maintenance-op-merge-an-]
    end
    subgraph Wave5
      TASK073[TASK-073 add-a-user-configurable-acti]
    end
    TASK069 --> TASK073
    TASK071 --> TASK070
    TASK074 --> TASK075
    TASK076 --> TASK073
    TASK090 --> TASK075
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`make ci ; make ci && npm --prefix web run lint && npm --prefix web test`) in each
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
> **A wave MUST NOT start** while any of: the previous wave has an unmerged PR that is
> NOT a held review-critical PR; any sibling worktree is un-rebased; the gate is red on
> `origin/main`; or a `rebase_blocked` marker is unresolved.
>
> **Held PRs (review-critical / prod-data path):** the coordinator opens the PR and
> STOPS — never `gh pr merge`. A held PR does not block the wave; only tasks that share a
> file with it are deferred to a `held-dependent` queue and dispatched after the owner
> merges it. The owner sees the held list in the coordinator's status report.

## Run it

Dispatch each brief with the paste preamble:

> You are an autonomous coding agent. Execute this task exactly. Do not skip the
> START HERE setup. Stop and report if any acceptance criterion fails.

Run at most 4 workers concurrently on this machine (16 concurrent agents crashed the session on 2026-08-21).
