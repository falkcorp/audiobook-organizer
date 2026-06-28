<!-- file: docs/agent-tasks/system-docs/TASK-07-components-incidents.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0a718293-4950-407 1-9629-475869 -->
<!-- last-edited: 2026-06-28 -->

# TASK-07 — Component inventory + incident history

**Priority:** P3 · **Effort:** M · **Recommended subagent:** documentation
subagent (code-exploration subagent first) · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sd-components" -b agent/sd-components origin/main
cd "$REPO/.worktrees/sd-components"
git rebase origin/main
```

## Goal

Write **two** files:
1. `docs/system/components.md` — an inventory of the `internal/**` packages: one
   row per package with its responsibility and key types/entry points. Include a
   **package-dependency Mermaid flowchart** (grouped by layer).
2. `docs/system/incidents.md` — a curated incident/decision history distilled
   from `CHANGELOG.md` and the specs: notable bugs, root causes, and fixes
   (memdb pagination cap, double-pagination, transcription parser, dedup
   false-positives, cache warm-up memory bloat). Include a **timeline/Gantt-style
   Mermaid diagram** of major milestones.

## Gather (verify against repo)

```bash
find internal -maxdepth 1 -type d | sort
sed -n '1,120p' CHANGELOG.md
ls docs/specs/ | sort -r | head -20
```

## Required content

- `components.md`: package table + a **Mermaid flowchart** of inter-package deps (layered: handlers → services → store; plus search/AI/plugins).
- `incidents.md`: ≥6 incidents/decisions with root cause + fix + PR refs, and a **Mermaid timeline/gantt** of milestones.
- Cross-link both into `README.md` (index) and `architecture.md`.

## Acceptance criteria

- [ ] `docs/system/components.md` (package inventory + dependency flowchart).
- [ ] `docs/system/incidents.md` (≥6 incidents + a timeline/gantt diagram).
- [ ] Two distinct Mermaid diagrams total; grounded in CHANGELOG/specs/code.
- [ ] Cross-linked; Mermaid renders.

## Commit message

```
docs(system): component inventory + incident history + diagrams (DOCS-1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/sd-components && gh pr create --fill && gh pr merge <number> --rebase
```

## Idempotency / Rollback

Both files exist already → done. Rollback = delete the files.
