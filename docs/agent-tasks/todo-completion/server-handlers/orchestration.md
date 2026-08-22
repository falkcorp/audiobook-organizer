<!-- file: docs/agent-tasks/todo-completion/server-handlers/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: c66a80f9-eac9-4b5c-af65-2b219194a843 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — server-handlers workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK142[TASK-142 expose-unmergeauto-through-a]
      TASK143[TASK-143 n-3-stop-advertising-delete-]
      TASK145[TASK-145 n-6-log-metric-when-listenin]
      TASK149[TASK-149 detect-multi-file-books-whos]
      TASK150[TASK-150 audit-apply-shaped-endpoints]
      TASK152[TASK-152 bound-the-itunes-search-hand]
      TASK153[TASK-153 implement-post-api-session-l]
      TASK155[TASK-155 move-tasks-and-maintenance-w]
    end
    subgraph Wave2
      TASK144[TASK-144 n-5-search-narrators-must-om]
      TASK146[TASK-146 n-10-advertised-login-rate-l]
      TASK213[TASK-213 replace-the-single-file-orga]
      TASK151[TASK-151 document-the-hardcoded-abs-t]
      TASK154[TASK-154 implement-post-api-session-l]
      TASK156[TASK-156 phase-7-socket-io-for-absorb]
      TASK157[TASK-157 parallelize-the-per-candidat]
    end
    subgraph Wave3
      TASK147[TASK-147 align-abs-conformance-fixtur]
      TASK212[TASK-212 add-get-api-libraries-librar]
    end
    subgraph Wave4
      TASK148[TASK-148 re-capture-the-series-abs-fi]
    end
    TASK142 --> TASK157
    TASK143 --> TASK146
    TASK143 --> TASK147
    TASK144 --> TASK147
    TASK144 --> TASK212
    TASK145 --> TASK147
    TASK146 --> TASK147
    TASK147 --> TASK148
    TASK149 --> TASK151
    TASK153 --> TASK154
    TASK153 --> TASK212
    TASK154 --> TASK212
    TASK156 --> TASK212
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only' ; go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/... ./internal/server/handlers/operations/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/abs/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/dedup/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/... ./internal/server/handlers/metadata/... ./internal/server/handlers/review/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1 ; go build ./... && go vet ./... && go test ./internal/server/handlers/dedup/... -count=1`) in each
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
