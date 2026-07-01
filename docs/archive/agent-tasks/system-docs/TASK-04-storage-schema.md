<!-- file: docs/agent-tasks/system-docs/TASK-04-storage-schema.md -->
<!-- version: 1.0.0 -->
<!-- guid: d7485960-1627-4d5e-9396-2142536071bc -->
<!-- last-edited: 2026-06-28 -->

# TASK-04 — Storage & schema doc

**Priority:** P3 · **Effort:** M · **Recommended subagent:** documentation
subagent (code-exploration subagent first) · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sd-storage" -b agent/sd-storage origin/main
cd "$REPO/.worktrees/sd-storage"
git rebase origin/main
```

## Goal

Write `docs/system/storage.md`: the storage architecture — PebbleDB (primary
k/v), the memdb in-memory query layer (go-memdb indexes), NutsDB activity log,
optional SQLite tier, key-format conventions, and the cached-aggregate +
dirty-flag pattern. Include an **ER-style or flowchart Mermaid diagram** of the
core entities (Book, BookFile, Author, Series, Narrator) and the index layers.

## Gather (verify against code/docs)

- `docs/database-architecture.md`, `docs/database-pebble-schema.md` (primary).
- `internal/database/store.go` (models), `pebble_store.go`, `memdb_schema.go`,
  `memdb_summaries.go`.
  ```bash
  sed -n '1,60p' docs/database-pebble-schema.md
  grep -rn "memIdx\|memTable\|counter:\|book:work:\|external_id_map" internal/database/ | head
  ```

## Required content

- PebbleDB key conventions (with real prefixes), the memdb index list and what
  each accelerates, NutsDB activity buckets, SQLite opt-in.
- The cached-aggregate + dirty-flag pattern (e.g. `stats:library`).
- A **Mermaid diagram** of core entities + index layers.
- Cross-links to `architecture.md`, `pipelines.md`.

## Acceptance criteria

- [ ] `docs/system/storage.md` with header, key-format tables, memdb index list, a Mermaid entity/index diagram.
- [ ] Key prefixes match the code/`database-pebble-schema.md`.
- [ ] Cross-linked; Mermaid renders.

## Commit message

```
docs(system): storage + schema (Pebble/memdb/NutsDB) + diagram (DOCS-1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/sd-storage && gh pr create --fill && gh pr merge <number> --rebase
```

## Idempotency / Rollback

Exists already → done. Rollback = delete the file.
