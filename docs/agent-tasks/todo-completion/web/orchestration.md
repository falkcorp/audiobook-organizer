<!-- file: docs/agent-tasks/todo-completion/web/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 868fc962-821f-4f28-b4bd-9d0571ad69be -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — web workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK166[TASK-166 add-a-settings-panel-section]
      TASK167[TASK-167 add-and-use-a-test-reset-hoo]
      TASK168[TASK-168 move-openai-api-key-validati]
      TASK169[TASK-169 strip-dedup-and-metadata-sou]
      TASK171[TASK-171 harden-muimenu-against-the-d]
      TASK172[TASK-172 find-the-mechanism-behind-th]
      TASK174[TASK-174 play-the-first-2-minutes-of-]
      TASK180[TASK-180 retarget-dedup-operations-sp]
      TASK181[TASK-181 retarget-diagnostics-spec-ts]
      TASK182[TASK-182 add-a-frontend-test-assertin]
      TASK183[TASK-183 add-resizable-sortable-colum]
      TASK184[TASK-184 add-resizable-sortable-colum]
      TASK185[TASK-185 add-resizable-sortable-colum]
    end
    subgraph Wave2
      TASK170[TASK-170 reformat-metadata-tags-in-br]
    end
    subgraph Wave3
      TASK176[TASK-176 make-the-book-detail-page-s-]
    end
    subgraph Wave4
      TASK173[TASK-173 let-the-owner-combine-merge-]
    end
    subgraph Wave5
      TASK175[TASK-175 review-the-17-apifetch-calle]
    end
    subgraph Wave6
      TASK177[TASK-177 make-the-book-detail-page-s-]
    end
    subgraph Wave7
      TASK178[TASK-178 make-narrator-publisher-genr]
    end
    subgraph Wave8
      TASK179[TASK-179 link-version-group-id-to-a-f]
    end
    TASK045 --> TASK173
    TASK090 --> TASK173
    TASK169 --> TASK170
    TASK169 --> TASK173
    TASK169 --> TASK175
    TASK169 --> TASK176
    TASK169 --> TASK177
    TASK169 --> TASK178
    TASK169 --> TASK179
    TASK173 --> TASK175
    TASK173 --> TASK177
    TASK173 --> TASK178
    TASK173 --> TASK179
    TASK175 --> TASK177
    TASK175 --> TASK178
    TASK175 --> TASK179
    TASK176 --> TASK173
    TASK176 --> TASK175
    TASK176 --> TASK177
    TASK176 --> TASK178
    TASK176 --> TASK179
    TASK177 --> TASK178
    TASK177 --> TASK179
    TASK178 --> TASK179
    TASK183 --> TASK175
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`make ci && npm --prefix web run lint && npm --prefix web test ; npm --prefix web run lint && npm --prefix web test`) in each
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
