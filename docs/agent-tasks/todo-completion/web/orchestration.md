<!-- file: docs/agent-tasks/todo-completion/web/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 51b4dc99-64a5-4594-af38-c710fec8c345 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — web workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK158[TASK-158 add-a-settings-panel-section]
      TASK159[TASK-159 add-and-use-a-test-reset-hoo]
      TASK160[TASK-160 move-openai-api-key-validati]
      TASK162[TASK-162 reformat-metadata-tags-in-br]
      TASK188[TASK-188 harden-muimenu-against-the-d]
      TASK189[TASK-189 play-the-first-2-minutes-of-]
      TASK170[TASK-170 retarget-dedup-operations-sp]
      TASK171[TASK-171 retarget-diagnostics-spec-ts]
      TASK172[TASK-172 add-a-frontend-test-assertin]
      TASK173[TASK-173 add-resizable-sortable-colum]
      TASK174[TASK-174 add-resizable-sortable-colum]
      TASK175[TASK-175 add-resizable-sortable-colum]
      TASK218[TASK-218 operationactivitypanel-stop-]
    end
    subgraph Wave2
      TASK161[TASK-161 strip-dedup-and-metadata-sou]
      TASK215[TASK-215 never-send-batchfetchcandida]
      TASK216[TASK-216 show-a-loading-skeleton-not-]
      TASK217[TASK-217 evidence-panel-explain-a-mis]
    end
    subgraph Wave3
      TASK166[TASK-166 make-the-book-detail-page-s-]
    end
    subgraph Wave4
      TASK167[TASK-167 make-the-book-detail-page-s-]
    end
    subgraph Wave5
      TASK168[TASK-168 make-narrator-publisher-genr]
    end
    subgraph Wave6
      TASK169[TASK-169 link-version-group-id-to-a-f]
    end
    subgraph Wave7
      TASK165[TASK-165 review-the-17-apifetch-calle]
    end
    TASK159 --> TASK215
    TASK159 --> TASK217
    TASK161 --> TASK165
    TASK161 --> TASK166
    TASK161 --> TASK167
    TASK161 --> TASK168
    TASK161 --> TASK169
    TASK162 --> TASK161
    TASK166 --> TASK165
    TASK166 --> TASK167
    TASK166 --> TASK168
    TASK166 --> TASK169
    TASK167 --> TASK165
    TASK167 --> TASK168
    TASK167 --> TASK169
    TASK168 --> TASK165
    TASK168 --> TASK169
    TASK169 --> TASK165
    TASK173 --> TASK165
    TASK189 --> TASK216
    TASK217 --> TASK165
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`go build ./... && go vet ./... && go test ./internal/audio/... ./internal/server/... -count=1 && npm --prefix web run lint && npm --prefix web test ; go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1 && npm --prefix web run lint && npm --prefix web test ; npm --prefix web run lint && npm --prefix web test`) in each
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
