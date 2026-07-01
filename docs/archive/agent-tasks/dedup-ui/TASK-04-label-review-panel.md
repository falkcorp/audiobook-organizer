<!-- file: docs/agent-tasks/dedup-ui/TASK-04-label-review-panel.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5f607182-3849-4576-9b18-436475869738 -->
<!-- last-edited: 2026-06-28 -->

# TASK-04 — Dedup label review panel (C6)

**Priority:** P3 · **Effort:** M · **Recommended subagent:** frontend subagent
(code-exploration subagent first to find/confirm the labels API) · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dui-label-review" -b agent/dui-label-review origin/main
cd "$REPO/.worktrees/dui-label-review"
git rebase origin/main
```

## Goal

A web panel listing dedup label examples (the `dedup:label:*` store), filterable
by `label`, `label_source`, and `band`, where a human can **override** a label
and set `label_source=human`.

## Background (verify before editing)

- Find the dedup-labels API (list + update) and its fields:
  ```bash
  grep -rn "dedup:label\|DedupLabel\|label_source\|/dedup/labels\|GetDedupLabels" internal/server/ internal/database/ | head
  ```
  There may already be a `DedupLabels` page/component:
  ```bash
  grep -rln "DedupLabels\|dedup.*label" web/src | head
  ```
  If a labels list/update endpoint does NOT exist, STOP and report — note that the
  backend endpoint is a prerequisite (capture it as a backend follow-up task).
- Reuse an existing table component if present (the repo has a
  `ConfigurableTable`); reuse the `apiFetch` helper.

## Step-by-step

1. Add (or extend) a panel/page that lists dedup label examples in a table.
2. Add filter controls for `label`, `label_source`, and `band` (dropdowns/inputs
   driving query params).
3. Per-row action: override the label; on save, PUT/PATCH the label with
   `label_source=human`. Reflect the change in the table.
4. Loading/empty/error states matching sibling pages.
5. Bump file headers.

## How to test

```bash
cd web && npm run build && npm test
```
Manual smoke: open the panel, filter, override a label, confirm it persists +
shows `label_source=human`.

## Acceptance criteria

- [ ] Panel lists `dedup:label:*` examples in a table.
- [ ] Filter by label / label_source / band works.
- [ ] Human override saves with `label_source=human` and updates the row.
- [ ] Reuses existing table + API helper; loading/error states present.
- [ ] `npm run build` + `npm test` pass; file headers bumped.

## Commit message

```
feat(dedup-ui): dedup label review + human-override panel (C6)

Lists dedup:label:* examples filterable by label/label_source/band and lets a
human override a label (sets label_source=human).

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dui-label-review
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If a label-review panel with override already exists, this is done. Rollback =
revert the commit.
