<!-- file: docs/agent-tasks/todo-completion/dedup/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 39f28385-7af8-490a-9704-df9699529c29 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — dedup workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK040[TASK-040 make-unmergeauto-reverse-ext]
      TASK041[TASK-041 audit-remaining-we-use-the-w]
      TASK180[TASK-180 measure-whether-dedup-durati]
      TASK043[TASK-043 add-a-dry-run-parameter-to-d]
      TASK045[TASK-045 build-a-dry-run-report-only-]
    end
    subgraph Wave2
      TASK181[TASK-181 find-the-createbook-path-s-t]
      TASK047[TASK-047 narrow-collectduration-s-tag]
      TASK049[TASK-049 acoustic-confirm-signal-prom]
    end
    subgraph Wave3
      TASK042[TASK-042 forward-fix-demote-pre-exist]
      TASK044[TASK-044 apply-the-unfiltered-ref-cou]
    end
    subgraph Wave4
      TASK046[TASK-046 route-merge-asexternalidreas]
    end
    subgraph Wave5
      TASK048[TASK-048 physically-co-locate-a-combi]
    end
    subgraph Wave8
      TASK050[TASK-050 shattered-book-reassembly-ma]
    end
    TASK021 --> TASK050
    TASK040 --> TASK042
    TASK040 --> TASK046
    TASK040 --> TASK048
    TASK040 --> TASK049
    TASK041 --> TASK047
    TASK042 --> TASK046
    TASK042 --> TASK048
    TASK043 --> TASK044
    TASK046 --> TASK048
    TASK049 --> TASK050
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/dedup/... ./internal/merge/... -count=1 ; go build ./... && go vet ./... && go test ./internal/dedup/... -count=1 ; go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/maintenance/jobs/... -count=1 ; go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/itunes/service/... ./internal/scanner/... -count=1 ; go build ./... && go vet ./... && go test ./internal/merge/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1`) in each
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
