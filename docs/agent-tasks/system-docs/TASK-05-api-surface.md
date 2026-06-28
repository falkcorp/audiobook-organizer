<!-- file: docs/agent-tasks/system-docs/TASK-05-api-surface.md -->
<!-- version: 1.0.0 -->
<!-- guid: e8596071-2738-4e6f-9407-25364750 -->
<!-- last-edited: 2026-06-28 -->

# TASK-05 — API surface doc

**Priority:** P3 · **Effort:** M · **Recommended subagent:** documentation
subagent (code-exploration subagent first) · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sd-api" -b agent/sd-api origin/main
cd "$REPO/.worktrees/sd-api"
git rebase origin/main
```

## Goal

Write `docs/system/api.md`: the HTTP API surface — auth model
(`Authorization: Bearer abk_…`), the main resource endpoints
(`/api/v1/audiobooks`, authors, series, dedup, metadata), and the **operations v2**
async pattern (`POST /api/v1/operations/v2` → poll `GET …/:id`). Include a
**sequence diagram** of launching + polling an operation.

## Gather (verify against code)

- Route wiring: `internal/server/wire_handlers.go`, the handler sub-packages
  under `internal/server/handlers/`, `internal/server/operations_v2_handlers.go`.
  ```bash
  grep -rn "router\.\(GET\|POST\|PUT\|DELETE\)\|/api/v1" internal/server/ | head -60
  grep -rn "operations/v2\|def_id" internal/server/operations_v2_handlers.go
  ```
- Auth: search for the bearer/api-key middleware.

## Required content

- Auth section (header format; never `X-API-Key`).
- A table of key endpoints grouped by resource (method, path, purpose, key params).
- The operations-v2 lifecycle + a **Mermaid sequence diagram** (client → POST op → poll → result).
- Note common query params for the library list (limit/offset cap 1000, sort_by, is_primary_version, show_quarantined).
- Cross-links to `architecture.md`, `pipelines.md`.

## Acceptance criteria

- [ ] `docs/system/api.md` with header, auth, endpoint table, operations-v2 sequence diagram.
- [ ] Endpoints/paths verified against route wiring.
- [ ] Cross-linked; Mermaid renders.

## Commit message

```
docs(system): API surface + operations-v2 sequence (DOCS-1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/sd-api && gh pr create --fill && gh pr merge <number> --rebase
```

## Idempotency / Rollback

Exists already → done. Rollback = delete the file.
