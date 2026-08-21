<!-- file: docs/agent-tasks/todo-completion/ci-tooling/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 73b4ce94-77eb-49f9-8b12-12724c28abf8 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — ci-tooling workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK007[TASK-007 add-a-scheduled-detect-only-]
      TASK008[TASK-008 wire-scripts-test-check-memo]
      TASK009[TASK-009 bump-the-ghcommon-reusable-w]
      TASK010[TASK-010 teach-the-abs-fixture-captur]
      TASK012[TASK-012 pin-sha256-checksums-for-doc]
      TASK013[TASK-013 scripts-setup-prometheus-aut]
      TASK014[TASK-014 build-a-report-only-scan-for]
      TASK015[TASK-015 remove-committed-mtls-bridge]
    end
    subgraph Wave2
      TASK011[TASK-011 add-top-level-permissions-bl]
      TASK016[TASK-016 stop-committing-series-dedup]
    end
    TASK009 --> TASK011
    TASK015 --> TASK016
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'`) in each
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
