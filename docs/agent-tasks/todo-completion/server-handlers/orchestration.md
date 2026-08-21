<!-- file: docs/agent-tasks/todo-completion/server-handlers/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: b1dd281f-6d3b-478a-91c3-79b5d35accee -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — server-handlers workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK147[TASK-147 expose-unmergeauto-through-a]
      TASK148[TASK-148 n-3-stop-advertising-delete-]
      TASK150[TASK-150 n-6-log-metric-when-listenin]
      TASK153[TASK-153 detect-multi-file-books-whos]
      TASK155[TASK-155 bound-the-itunes-search-hand]
      TASK156[TASK-156 implement-post-api-session-l]
      TASK158[TASK-158 move-tasks-and-maintenance-w]
    end
    subgraph Wave2
      TASK151[TASK-151 n-10-advertised-login-rate-l]
      TASK154[TASK-154 document-the-hardcoded-abs-t]
      TASK157[TASK-157 implement-post-api-session-l]
      TASK159[TASK-159 phase-7-socket-io-for-absorb]
      TASK160[TASK-160 parallelize-the-per-candidat]
    end
    subgraph Wave3
      TASK149[TASK-149 n-5-search-narrators-must-om]
    end
    subgraph Wave4
      TASK152[TASK-152 align-abs-conformance-fixtur]
    end
    TASK147 --> TASK160
    TASK148 --> TASK151
    TASK148 --> TASK152
    TASK149 --> TASK152
    TASK150 --> TASK152
    TASK151 --> TASK152
    TASK153 --> TASK154
    TASK156 --> TASK157
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
