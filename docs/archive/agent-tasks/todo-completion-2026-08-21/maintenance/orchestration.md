<!-- file: docs/agent-tasks/todo-completion/maintenance/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4525bc82-4fca-4cc8-a8f9-f3a9bc385e04 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — maintenance workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK066[TASK-066 wire-a-durable-freshness-sta]
      TASK068[TASK-068 build-a-report-only-counter-]
      TASK071[TASK-071 build-a-detection-only-repor]
      TASK073[TASK-073 read-through-audit-of-the-8-]
      TASK074[TASK-074 build-a-report-only-census-o]
      TASK075[TASK-075 extend-purge-empty-authors-r]
      TASK077[TASK-077 narrow-the-3-remaining-maint]
      TASK078[TASK-078 task-04-build-the-idempotent]
      TASK195[TASK-195 add-a-zero-size-bucket-to-ma]
    end
    subgraph Wave2
      TASK072[TASK-072 new-maintenance-op-merge-an-]
      TASK076[TASK-076 author-narrator-swap-repair-]
    end
    subgraph Wave3
      TASK067[TASK-067 extend-the-repoint-repair-to]
      TASK220[TASK-220 journal-every-duplicate-row-]
    end
    subgraph Wave4
      TASK219[TASK-219 add-a-per-book-tsv-report-ar]
    end
    subgraph Wave6
      TASK070[TASK-070 add-a-user-configurable-acti]
    end
    TASK066 --> TASK070
    TASK066 --> TASK076
    TASK066 --> TASK220
    TASK068 --> TASK067
    TASK073 --> TASK070
    TASK076 --> TASK070
    TASK076 --> TASK220
    TASK086 --> TASK072
    TASK220 --> TASK070
    TASK220 --> TASK219
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`go build ./... && go vet ./... && go test ./internal/config/... ./internal/plugins/maintenance/... ./internal/server/... -count=1 && npm --prefix web run lint && npm --prefix web test ; go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/plugins/maintenance/... -count=1 ; go build ./... && go vet ./... && go test ./internal/maintenance/jobs/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... ./internal/server/... -count=1`) in each
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
