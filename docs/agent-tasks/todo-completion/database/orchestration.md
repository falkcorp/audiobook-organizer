<!-- file: docs/agent-tasks/todo-completion/database/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6c6bf86c-5ee4-45a7-8989-3807f9212bc2 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — database workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK177[TASK-177 add-a-per-test-deadline-cont]
      TASK024[TASK-024 replace-fragile-0x30-0x3a-on]
      TASK025[TASK-025 make-wipeallactivity-cancell]
      TASK026[TASK-026 triage-the-remaining-misc-co]
      TASK027[TASK-027 build-a-diagnostic-reconcili]
      TASK028[TASK-028 guard-author-delete-paths-wi]
      TASK030[TASK-030 add-a-compare-and-swap-on-co]
      TASK031[TASK-031 lock-the-three-bare-globalst]
      TASK032[TASK-032 add-the-4-missing-compile-ti]
      TASK034[TASK-034 add-func-override-fields-to-]
      TASK035[TASK-035 add-deletenarrator-to-the-st]
      TASK038[TASK-038 filter-system-sourced-tags-o]
    end
    subgraph Wave2
      TASK178[TASK-178 reduce-internal-database-s-s]
      TASK179[TASK-179 database-store-40-build-the-]
      TASK023[TASK-023 investigate-then-evict-dirty]
      TASK029[TASK-029 add-getbooksbyseriesidallver]
      TASK033[TASK-033 repoint-store-go-17-s-broken]
      TASK036[TASK-036 fix-deleteauthor-s-junction-]
    end
    subgraph Wave3
      TASK039[TASK-039 add-transcribe-status-to-the]
    end
    subgraph Wave6
      TASK037[TASK-037 omnibus-anthology-book-type-]
    end
    TASK005 --> TASK039
    TASK026 --> TASK039
    TASK029 --> TASK039
    TASK031 --> TASK033
    TASK031 --> TASK037
    TASK031 --> TASK039
    TASK033 --> TASK037
    TASK033 --> TASK039
    TASK035 --> TASK036
    TASK039 --> TASK037
    TASK043 --> TASK029
    TASK177 --> TASK178
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/database/... ./internal/database/mocks/... -count=1 ; go build ./... && go vet ./... && go test ./internal/database/... -count=1 ; go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... -count=1 ; go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/dedup/... -count=1 ; go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/server/handlers/audiobooks/... -count=1 && npm --prefix web run lint && npm --prefix web test ; go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/server/handlers/entities/... -count=1 ; go build ./... && go vet ./... && go test ./internal/database/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/database/... ./internal/server/... ./internal/server/handlers/system/... -count=1 ; go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/merge/... ./internal/search/... -count=1 ; go build ./... && go vet ./... && go test ./tools/cmd/reconcile-book-counts/... -count=1 ; go build ./... && go vet ./... && go test ./tools/cmd/storewidthgate/... -count=1`) in each
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
