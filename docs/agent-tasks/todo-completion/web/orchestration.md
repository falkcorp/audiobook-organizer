<!-- file: docs/agent-tasks/todo-completion/web/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: a77f49e4-964d-45bd-855f-f055b4f7a827 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — web workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK161[TASK-161 add-a-settings-panel-section]
      TASK162[TASK-162 add-and-use-a-test-reset-hoo]
      TASK163[TASK-163 move-openai-api-key-validati]
      TASK164[TASK-164 strip-dedup-and-metadata-sou]
      TASK166[TASK-166 harden-muimenu-against-the-d]
      TASK167[TASK-167 find-the-mechanism-behind-th]
      TASK169[TASK-169 play-the-first-2-minutes-of-]
      TASK175[TASK-175 retarget-dedup-operations-sp]
      TASK176[TASK-176 retarget-diagnostics-spec-ts]
      TASK177[TASK-177 add-a-frontend-test-assertin]
      TASK178[TASK-178 add-resizable-sortable-colum]
      TASK179[TASK-179 add-resizable-sortable-colum]
      TASK180[TASK-180 add-resizable-sortable-colum]
    end
    subgraph Wave2
      TASK165[TASK-165 reformat-metadata-tags-in-br]
    end
    subgraph Wave3
      TASK171[TASK-171 make-the-book-detail-page-s-]
    end
    subgraph Wave4
      TASK172[TASK-172 make-the-book-detail-page-s-]
    end
    subgraph Wave5
      TASK173[TASK-173 make-narrator-publisher-genr]
    end
    subgraph Wave6
      TASK174[TASK-174 link-version-group-id-to-a-f]
    end
    subgraph Wave7
      TASK168[TASK-168 let-the-owner-combine-merge-]
    end
    subgraph Wave8
      TASK170[TASK-170 review-the-17-apifetch-calle]
    end
    TASK043 --> TASK168
    TASK085 --> TASK168
    TASK164 --> TASK165
    TASK164 --> TASK168
    TASK164 --> TASK170
    TASK164 --> TASK171
    TASK164 --> TASK172
    TASK164 --> TASK173
    TASK164 --> TASK174
    TASK168 --> TASK170
    TASK171 --> TASK168
    TASK171 --> TASK170
    TASK171 --> TASK172
    TASK171 --> TASK173
    TASK171 --> TASK174
    TASK172 --> TASK168
    TASK172 --> TASK170
    TASK172 --> TASK173
    TASK172 --> TASK174
    TASK173 --> TASK168
    TASK173 --> TASK170
    TASK173 --> TASK174
    TASK174 --> TASK168
    TASK174 --> TASK170
    TASK178 --> TASK170
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
