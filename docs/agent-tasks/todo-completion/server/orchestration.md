<!-- file: docs/agent-tasks/todo-completion/server/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3b2ff5b8-0e26-4afd-ae7b-768f2d8b106b -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — server workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK127[TASK-127 log-abs-api-enabled-s-actual]
      TASK132[TASK-132 fix-indexedstore-updatebook-]
      TASK134[TASK-134 add-a-wiring-level-test-prov]
      TASK135[TASK-135 convert-metadata-batch-apply]
      TASK136[TASK-136 convert-reconcile-apply-from]
      TASK137[TASK-137 fix-testorganizeservice-perf]
      TASK138[TASK-138 exempt-the-abs-router-group-]
      TASK140[TASK-140 retire-the-unsafe-cleanup-me]
      TASK141[TASK-141 add-regression-tests-for-the]
    end
    subgraph Wave2
      TASK128[TASK-128 fix-enableratelimit-false-no]
      TASK129[TASK-129 fix-wipeactivity-dry-run-cou]
      TASK130[TASK-130 register-searchindexdroppedc]
      TASK133[TASK-133 regression-test-soft-deletin]
    end
    subgraph Wave3
      TASK131[TASK-131 fix-audiobook-organizer-book]
    end
    subgraph Wave4
      TASK139[TASK-139 prune-expired-abs-sess-recor]
    end
    TASK128 --> TASK131
    TASK128 --> TASK139
    TASK130 --> TASK131
    TASK131 --> TASK139
    TASK132 --> TASK133
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/maintenance/jobs/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/middleware/... -count=1`) in each
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
