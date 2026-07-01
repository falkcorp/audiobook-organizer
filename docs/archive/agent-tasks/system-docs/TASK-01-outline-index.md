<!-- file: docs/agent-tasks/system-docs/TASK-01-outline-index.md -->
<!-- version: 1.0.0 -->
<!-- guid: a4152637-8394-4a2b-9063-98b920314283 -->
<!-- last-edited: 2026-06-28 -->

# TASK-01 — System docs index + outline

**Priority:** P3 · **Effort:** S · **Recommended subagent:** documentation
subagent · **Depends on:** none (do this first)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sd-index" -b agent/sd-index origin/main
cd "$REPO/.worktrees/sd-index"
git rebase origin/main
```

## Goal

Create `docs/system/README.md` — the index that every other system doc links
into. It lists the planned docs (architecture, pipelines, storage, api, runbooks,
components, incidents), each with a one-line summary, and includes a site-map
Mermaid flowchart. This locks the structure so TASK-02..07 can run in parallel
without colliding on naming.

## Step-by-step

1. Skim existing docs to anchor the structure: `docs/AI-REFERENCE.md`,
   `docs/database-architecture.md`, `docs/database-pebble-schema.md`, and
   `ls docs/specs/ | sort -r | head`.
2. Create `docs/system/README.md` with the standard header and:
   - a 1-paragraph "what this system is",
   - a table of the system docs with one-line summaries and relative links,
   - a **site-map Mermaid flowchart** showing how the docs relate.
3. Create empty-but-headed stub files is NOT required — the other tasks create
   their own files. Just make sure your index links to the agreed filenames:
   `architecture.md`, `pipelines.md`, `storage.md`, `api.md`, `runbooks.md`,
   `components.md`, `incidents.md`.

## How to test

Render-check the Mermaid block (any Mermaid live editor) and confirm links use
the agreed filenames. No code build needed.

## Acceptance criteria

- [ ] `docs/system/README.md` exists with header, intro, doc table, and a site-map Mermaid flowchart.
- [ ] Links use the agreed filenames for TASK-02..07.
- [ ] Mermaid renders.

## Commit message

```
docs(system): add system-docs index + site map (DOCS-1)

Index for the comprehensive system documentation set: doc table with summaries,
relative links to the planned area docs, and a site-map Mermaid flowchart.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/sd-index
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `docs/system/README.md` already exists with the index, this is done. Rollback
= delete the file.
