<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0c28a953-f228-4a8c-bce8-37618ca62238 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — missing-file-lane workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK099[TASK-099 give-change-log-row-compare-]
      TASK100[TASK-100 remove-dead-expanded-state-i]
      TASK102[TASK-102 audit-remaining-setupmockapi]
      TASK103[TASK-103 restore-version-group-count-]
      TASK104[TASK-104 instrument-sort-by-usage-to-]
      TASK105[TASK-105 require-every-mutating-opera]
      TASK106[TASK-106 remove-the-now-redundant-rea]
      TASK109[TASK-109 validate-the-two-unvalidated]
      TASK110[TASK-110 pin-a-regression-test-the-re]
      TASK112[TASK-112 build-a-report-only-op-categ]
      TASK113[TASK-113 build-the-version-group-acou]
      TASK116[TASK-116 export-a-playlist-back-to-m3]
      TASK117[TASK-117 add-the-review-rating-half-o]
      TASK118[TASK-118 parse-deluge-torrent-release]
      TASK120[TASK-120 build-the-pre-apply-snapshot]
      TASK122[TASK-122 missing-input-triggering-enq]
      TASK123[TASK-123 never-delete-re-associate-co]
    end
    subgraph Wave2
      TASK101[TASK-101 delete-the-unreachable-bulk-]
      TASK107[TASK-107 echo-which-filters-the-serve]
      TASK108[TASK-108 fail-warn-ci-when-the-rc-ord]
      TASK111[TASK-111 typescript-6-0-3-7-0-2-migra]
      TASK115[TASK-115 import-found-playlist-files-]
      TASK119[TASK-119 audit-book-file-grouping-aga]
      TASK121[TASK-121 build-the-first-aid-orchestr]
    end
    subgraph Wave3
      TASK098[TASK-098 log-a-warning-when-getallser]
      TASK114[TASK-114 build-chapters-backfill-from]
    end
    TASK104 --> TASK107
    TASK106 --> TASK111
    TASK115 --> TASK114
    TASK116 --> TASK114
    TASK118 --> TASK119
    TASK122 --> TASK121
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; make ci ; make ci && npm --prefix web run lint && npm --prefix web test ; npm --prefix web run lint && npm --prefix web test`) in each
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
