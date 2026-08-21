<!-- file: docs/agent-tasks/todo-completion/dedup/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 75b37a41-f761-4b79-9b6f-34c260bd183a -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — dedup workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK043[TASK-043 audit-remaining-we-use-the-w]
      TASK044[TASK-044 measure-whether-dedup-durati]
      TASK046[TASK-046 add-a-dry-run-parameter-to-d]
      TASK047[TASK-047 find-the-createbook-path-s-t]
      TASK049[TASK-049 build-a-dry-run-report-only-]
      TASK053[TASK-053 acoustic-confirm-signal-prom]
    end
    subgraph Wave2
      TASK042[TASK-042 make-unmergeauto-reverse-ext]
      TASK051[TASK-051 narrow-collectduration-s-tag]
      TASK054[TASK-054 shattered-book-reassembly-ma]
    end
    subgraph Wave3
      TASK045[TASK-045 forward-fix-demote-pre-exist]
      TASK048[TASK-048 apply-the-unfiltered-ref-cou]
    end
    subgraph Wave4
      TASK050[TASK-050 route-merge-asexternalidreas]
    end
    subgraph Wave5
      TASK052[TASK-052 physically-co-locate-a-combi]
    end
    TASK042 --> TASK045
    TASK042 --> TASK050
    TASK042 --> TASK052
    TASK043 --> TASK051
    TASK045 --> TASK050
    TASK045 --> TASK052
    TASK046 --> TASK048
    TASK050 --> TASK052
    TASK053 --> TASK042
    TASK053 --> TASK054
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

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

Dispatch each brief with the paste preamble:

> You are an autonomous coding agent. Execute this task exactly. Do not skip the
> START HERE setup. Stop and report if any acceptance criterion fails.

Run at most 4 workers concurrently on this machine (16 concurrent agents crashed the session on 2026-08-21).
