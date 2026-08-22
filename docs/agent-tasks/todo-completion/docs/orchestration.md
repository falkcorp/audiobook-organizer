<!-- file: docs/agent-tasks/todo-completion/docs/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 441d5e8c-3016-4f2c-95e5-ab58cc43f4ff -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — docs workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK182[TASK-182 record-the-docs-system-vs-to]
      TASK183[TASK-183 write-file-header-for-the-35]
      TASK051[TASK-051 delete-the-34-group-relative]
      TASK052[TASK-052 triage-the-16-removed-post-m]
      TASK053[TASK-053 delete-the-torrents-group-re]
      TASK054[TASK-054 re-verify-docs-reference-abs]
      TASK055[TASK-055 document-the-todo-d-fragment]
      TASK056[TASK-056 consolidate-the-august-execu]
      TASK057[TASK-057 phase-8-write-the-abs-topolo]
      TASK058[TASK-058 update-execution-manifest-do]
      TASK059[TASK-059 close-out-the-2026-05-01-re-]
      TASK060[TASK-060 docs-truth-up-with-measured-]
    end

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
