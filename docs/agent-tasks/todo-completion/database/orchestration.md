<!-- file: docs/agent-tasks/todo-completion/database/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0990d49a-02a4-43d7-83ac-750e00f9936c -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — database workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK022[TASK-022 reduce-internal-database-s-s]
      TASK024[TASK-024 finish-killing-database-stor]
      TASK025[TASK-025 investigate-then-evict-dirty]
      TASK026[TASK-026 replace-fragile-0x30-0x3a-on]
      TASK027[TASK-027 make-wipeallactivity-cancell]
      TASK029[TASK-029 build-a-diagnostic-reconcili]
      TASK030[TASK-030 guard-author-delete-paths-wi]
      TASK032[TASK-032 add-a-compare-and-swap-on-co]
      TASK033[TASK-033 lock-the-three-bare-globalst]
      TASK034[TASK-034 add-the-4-missing-compile-ti]
      TASK036[TASK-036 add-func-override-fields-to-]
      TASK037[TASK-037 add-deletenarrator-to-the-st]
      TASK040[TASK-040 filter-system-sourced-tags-o]
    end
    subgraph Wave2
      TASK023[TASK-023 database-store-40-build-the-]
      TASK028[TASK-028 triage-the-remaining-misc-co]
      TASK031[TASK-031 add-getbooksbyseriesidallver]
      TASK035[TASK-035 repoint-store-go-17-s-broken]
      TASK038[TASK-038 fix-deleteauthor-s-junction-]
    end
    subgraph Wave3
      TASK039[TASK-039 omnibus-anthology-book-type-]
    end
    subgraph Wave6
      TASK041[TASK-041 add-transcribe-status-to-the]
    end
    TASK006 --> TASK041
    TASK024 --> TASK028
    TASK028 --> TASK041
    TASK031 --> TASK041
    TASK033 --> TASK035
    TASK033 --> TASK039
    TASK033 --> TASK041
    TASK035 --> TASK039
    TASK035 --> TASK041
    TASK037 --> TASK038
    TASK039 --> TASK041
    TASK046 --> TASK031
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
> **A wave MUST NOT start** while any of: the previous wave has an unmerged PR; any
> sibling worktree is un-rebased; the gate is red on `origin/main`; or a
> `rebase_blocked` marker is unresolved.

## Run it

Dispatch each brief with the paste preamble:

> You are an autonomous coding agent. Execute this task exactly. Do not skip the
> START HERE setup. Stop and report if any acceptance criterion fails.

Run at most 4 workers concurrently on this machine (16 concurrent agents crashed the session on 2026-08-21).
