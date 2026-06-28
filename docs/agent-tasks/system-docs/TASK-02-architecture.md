<!-- file: docs/agent-tasks/system-docs/TASK-02-architecture.md -->
<!-- version: 1.0.0 -->
<!-- guid: b5263748-9405-4b3c-9174-09203142539a -->
<!-- last-edited: 2026-06-28 -->

# TASK-02 — Architecture overview doc

**Priority:** P3 · **Effort:** M · **Recommended subagent:** documentation
subagent (code-exploration subagent first) · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sd-architecture" -b agent/sd-architecture origin/main
cd "$REPO/.worktrees/sd-architecture"
git rebase origin/main
```

## Goal

Write `docs/system/architecture.md`: the high-level architecture — Go backend
(Gin) + embedded React/TS frontend, the service registry/container, the store
layers (PebbleDB primary, NutsDB activity log, memdb query layer), search
(Bleve), embeddings/HNSW, the operations/plugin system. Include a **component
graph Mermaid flowchart**.

## Gather (verify against code)

- `docs/AI-REFERENCE.md` (architecture overview, package map) — primary source.
- `internal/server/` (server lifecycle, registry wiring), `internal/serviceregistry/`,
  `internal/database/` (PebbleStore, MemStore), `internal/search/`, `internal/metafetch/`,
  `internal/dedup/`, `internal/plugins/`.
  ```bash
  sed -n '1,80p' docs/AI-REFERENCE.md
  grep -rn "IncludeGroup\|ServiceDef\|OperationDef" internal/server/ internal/serviceregistry/ | head
  ```

## Required content

- One paragraph per major subsystem (backend, frontend, store, search, AI/embeddings, ops/plugins).
- A **Mermaid flowchart** of the component graph (HTTP → handlers → services → store; bg workers; plugins).
- A short "request lifecycle" note (how a library list request flows).
- Links back to `README.md` (index) and forward to `storage.md`, `pipelines.md`, `api.md`.

## Acceptance criteria

- [ ] `docs/system/architecture.md` exists with header, subsystem sections, and a component-graph Mermaid flowchart.
- [ ] Claims are grounded in `docs/AI-REFERENCE.md` + code (no invented components).
- [ ] Cross-links to sibling docs; Mermaid renders.

## Commit message

```
docs(system): architecture overview + component graph (DOCS-1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/sd-architecture && gh pr create --fill && gh pr merge <number> --rebase
```

## Idempotency / Rollback

Exists already → done. Rollback = delete the file.
