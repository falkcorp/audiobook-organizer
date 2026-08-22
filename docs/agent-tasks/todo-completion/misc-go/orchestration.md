<!-- file: docs/agent-tasks/todo-completion/misc-go/orchestration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 14979f53-b1c7-48c8-af07-1f6dbd3b9816 -->
<!-- last-edited: 2026-08-21 -->

# Orchestration — misc-go workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
    subgraph Wave1
      TASK082[TASK-082 fix-the-go-zipslip-finding-o]
      TASK083[TASK-083 fix-or-verify-the-4-still-op]
      TASK084[TASK-084 add-codeql-specific-lgtm-sup]
      TASK085[TASK-085 add-search-index-metrics-doc]
      TASK086[TASK-086 collapse-internal-whitespace]
      TASK087[TASK-087 replace-serviceregistry-get-]
      TASK088[TASK-088 route-acoustid-lsh-backfill-]
    end
    subgraph Wave2
      TASK197[TASK-197 audit-every-registry-runitem]
    end
    subgraph Wave6
      TASK186[TASK-186 measure-the-real-double-prim]
    end
    TASK088 --> TASK197
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/database/... ./internal/organizer/... ./internal/plugins/maintenance/... ./internal/reconcile/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/backup/... -count=1 ; go build ./... && go vet ./... && go test ./internal/fileops/... ./internal/metadata/... ./internal/server/handlers/... -count=1 ; go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/server/... -count=1 ; go build ./... && go vet ./... && go test ./internal/mtls/... ./tools/cmd/merge-split-books/... ./tools/cmd/reconcile-paths/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/acoustid/... -count=1 ; go build ./... && go vet ./... && go test ./internal/plugins/acoustid/... ./internal/plugins/dedup/... ./internal/plugins/maintenance/... ./internal/plugins/metafetch/... -count=1 ; go build ./... && go vet ./... && go test ./internal/serviceregistry/... -count=1 ; go build ./... && go vet ./... && go test ./internal/util/... -count=1`) in each
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
