<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: cc1b13ce-feea-4848-b000-f99c14365238 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — missing-file-lane workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK089[TASK-089 log-a-warning-when-getallser]
      TASK090[TASK-090 give-change-log-row-compare-]
      TASK091[TASK-091 remove-dead-expanded-state-i]
      TASK092[TASK-092 delete-the-unreachable-bulk-]
      TASK093[TASK-093 audit-remaining-setupmockapi]
      TASK094[TASK-094 restore-version-group-count-]
      TASK097[TASK-097 remove-the-now-redundant-rea]
      TASK100[TASK-100 validate-the-two-unvalidated]
      TASK101[TASK-101 pin-a-regression-test-the-re]
      TASK103[TASK-103 build-a-report-only-op-categ]
      TASK104[TASK-104 build-the-version-group-acou]
      TASK106[TASK-106 import-found-playlist-files-]
      TASK107[TASK-107 export-a-playlist-back-to-m3]
      TASK108[TASK-108 add-the-review-rating-half-o]
      TASK109[TASK-109 parse-deluge-torrent-release]
      TASK111[TASK-111 build-the-pre-apply-snapshot]
      TASK113[TASK-113 missing-input-triggering-enq]
      TASK114[TASK-114 never-delete-re-associate-co]
    end
    subgraph Wave2
      TASK095[TASK-095 instrument-sort-by-usage-to-]
      TASK096[TASK-096 require-every-mutating-opera]
      TASK099[TASK-099 fail-warn-ci-when-the-rc-ord]
      TASK102[TASK-102 typescript-6-0-3-7-0-2-migra]
      TASK105[TASK-105 build-chapters-backfill-from]
      TASK110[TASK-110 audit-book-file-grouping-aga]
      TASK112[TASK-112 build-the-first-aid-orchestr]
    end
    subgraph Wave3
      TASK098[TASK-098 echo-which-filters-the-serve]
    end
    TASK095 --> TASK098
    TASK097 --> TASK102
    TASK106 --> TASK105
    TASK107 --> TASK105
    TASK109 --> TASK110
    TASK113 --> TASK112
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; go build ./... && go vet ./... && go test ./internal/deluge/... -count=1 ; go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1 ; go build ./... && go vet ./... && go test ./internal/operations/registry/... ./internal/scheduler/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/playlist/... ./internal/scanner/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1 && npm --prefix web run lint && npm --prefix web test ; go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/audiobooks/... -count=1 ; npm --prefix web run lint && npm --prefix web test`) in each
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
