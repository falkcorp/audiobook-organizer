<!-- file: docs/agent-tasks/todo-completion/server/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: c3d36e10-800d-419a-9797-7bf16b74bd40 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — server workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK131[TASK-131 log-abs-api-enabled-s-actual]
      TASK134[TASK-134 reproduce-and-classify-the-p]
      TASK137[TASK-137 fix-indexedstore-updatebook-]
      TASK139[TASK-139 add-a-wiring-level-test-prov]
      TASK140[TASK-140 convert-metadata-batch-apply]
      TASK141[TASK-141 convert-reconcile-apply-from]
      TASK142[TASK-142 fix-testorganizeservice-perf]
      TASK143[TASK-143 exempt-the-abs-router-group-]
      TASK145[TASK-145 retire-the-unsafe-cleanup-me]
      TASK146[TASK-146 add-regression-tests-for-the]
    end
    subgraph Wave2
      TASK132[TASK-132 fix-enableratelimit-false-no]
      TASK133[TASK-133 fix-wipeactivity-dry-run-cou]
      TASK135[TASK-135 register-searchindexdroppedc]
      TASK138[TASK-138 regression-test-soft-deletin]
    end
    subgraph Wave3
      TASK136[TASK-136 fix-audiobook-organizer-book]
    end
    subgraph Wave4
      TASK144[TASK-144 prune-expired-abs-sess-recor]
    end
    TASK132 --> TASK136
    TASK132 --> TASK144
    TASK135 --> TASK136
    TASK136 --> TASK144
    TASK137 --> TASK138
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; make ci`) in each
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
