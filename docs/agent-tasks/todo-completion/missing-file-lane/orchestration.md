<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: dd8574ff-0423-4c07-9f89-e993435b5357 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — missing-file-lane workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK094[TASK-094 give-change-log-row-compare-]
      TASK095[TASK-095 remove-dead-expanded-state-i]
      TASK097[TASK-097 audit-remaining-setupmockapi]
      TASK098[TASK-098 restore-version-group-count-]
      TASK101[TASK-101 remove-the-now-redundant-rea]
      TASK104[TASK-104 validate-the-two-unvalidated]
      TASK105[TASK-105 pin-a-regression-test-the-re]
      TASK107[TASK-107 build-a-report-only-op-categ]
      TASK108[TASK-108 build-the-version-group-acou]
      TASK110[TASK-110 import-found-playlist-files-]
      TASK111[TASK-111 export-a-playlist-back-to-m3]
      TASK112[TASK-112 add-the-review-rating-half-o]
      TASK113[TASK-113 parse-deluge-torrent-release]
      TASK115[TASK-115 build-the-pre-apply-snapshot]
      TASK117[TASK-117 missing-input-triggering-enq]
      TASK118[TASK-118 never-delete-re-associate-co]
    end
    subgraph Wave2
      TASK093[TASK-093 log-a-warning-when-getallser]
      TASK096[TASK-096 delete-the-unreachable-bulk-]
      TASK099[TASK-099 instrument-sort-by-usage-to-]
      TASK100[TASK-100 require-every-mutating-opera]
      TASK103[TASK-103 fail-warn-ci-when-the-rc-ord]
      TASK106[TASK-106 typescript-6-0-3-7-0-2-migra]
      TASK109[TASK-109 build-chapters-backfill-from]
      TASK114[TASK-114 audit-book-file-grouping-aga]
      TASK116[TASK-116 build-the-first-aid-orchestr]
    end
    subgraph Wave3
      TASK102[TASK-102 echo-which-filters-the-serve]
    end
    TASK099 --> TASK102
    TASK101 --> TASK106
    TASK110 --> TASK109
    TASK111 --> TASK109
    TASK113 --> TASK114
    TASK117 --> TASK116
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
