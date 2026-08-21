<!-- file: docs/agent-tasks/todo-completion/audiobooks/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: e6b2fdeb-6695-44a2-a4b7-0bc99a2cf924 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — audiobooks workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK002[TASK-002 fix-the-3-way-disagreement-i]
      TASK003[TASK-003 build-a-read-only-census-too]
    end
    subgraph Wave2
      TASK001[TASK-001 add-a-short-ttl-cache-to-the]
    end
    subgraph Wave3
      TASK004[TASK-004 fix-the-author-path-post-fil]
    end
    subgraph Wave4
      TASK005[TASK-005 add-a-conformance-test-asser]
      TASK006[TASK-006 wire-onlyparsedtranscription]
    end
    TASK001 --> TASK004
    TASK001 --> TASK006
    TASK002 --> TASK001
    TASK002 --> TASK004
    TASK002 --> TASK006
    TASK004 --> TASK005
    TASK004 --> TASK006
    TASK025 --> TASK001
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
