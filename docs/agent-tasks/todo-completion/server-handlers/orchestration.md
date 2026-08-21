<!-- file: docs/agent-tasks/todo-completion/server-handlers/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b782bf4-92af-4e80-94c3-7fa377979501 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — server-handlers workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK152[TASK-152 expose-unmergeauto-through-a]
      TASK153[TASK-153 n-3-stop-advertising-delete-]
      TASK155[TASK-155 n-6-log-metric-when-listenin]
      TASK158[TASK-158 detect-multi-file-books-whos]
      TASK160[TASK-160 bound-the-itunes-search-hand]
      TASK161[TASK-161 implement-post-api-session-l]
      TASK163[TASK-163 move-tasks-and-maintenance-w]
    end
    subgraph Wave2
      TASK156[TASK-156 n-10-advertised-login-rate-l]
      TASK159[TASK-159 document-the-hardcoded-abs-t]
      TASK162[TASK-162 implement-post-api-session-l]
      TASK164[TASK-164 phase-7-socket-io-for-absorb]
      TASK165[TASK-165 parallelize-the-per-candidat]
    end
    subgraph Wave4
      TASK154[TASK-154 n-5-search-narrators-must-om]
    end
    subgraph Wave5
      TASK157[TASK-157 align-abs-conformance-fixtur]
    end
    TASK152 --> TASK165
    TASK153 --> TASK156
    TASK153 --> TASK157
    TASK154 --> TASK157
    TASK155 --> TASK157
    TASK156 --> TASK157
    TASK158 --> TASK159
    TASK161 --> TASK162
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
