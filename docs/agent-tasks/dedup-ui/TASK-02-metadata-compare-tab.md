<!-- file: docs/agent-tasks/dedup-ui/TASK-02-metadata-compare-tab.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d4e5f60-1627-4354-9f96-214253649516 -->
<!-- last-edited: 2026-06-28 -->

# TASK-02 — Metadata-compare tab in the candidate compare drawer (CONS-6)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** frontend subagent
(code-exploration subagent first to confirm the API shape) · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dui-metadata-tab" -b agent/dui-metadata-tab origin/main
cd "$REPO/.worktrees/dui-metadata-tab"
git rebase origin/main
```

## Goal

Add a **Metadata** tab to the dedup candidate compare drawer, alongside the
existing Fingerprint tab, showing a side-by-side compare of series / narrator /
parts / duration / file size and **which signal fired** for the candidate. Data
comes from `GET /api/v1/dedup/candidates/:id/breakdown`.

## Background (verify before editing)

- Component: `web/src/components/dedup/CandidateCompareDrawer.tsx` (has the
  Fingerprint tab today). Confirm:
  ```bash
  grep -rn "Tab\|Fingerprint\|breakdown\|/dedup/candidates" web/src/components/dedup/CandidateCompareDrawer.tsx
  ```
- Confirm the breakdown endpoint exists and its response shape:
  ```bash
  grep -rn "dedup/candidates/.*breakdown\|Breakdown" internal/server/ | head
  ```
  If the endpoint does NOT exist, STOP and report — this task assumes it does
  (TODO.md CONS-6 says "wire from GET …/breakdown").

## Step-by-step

1. Add a new tab next to Fingerprint. Reuse the existing tab/panel pattern in the
   drawer.
2. Fetch the breakdown for the current candidate via the existing API helper
   (e.g. `apiFetch`), with loading + error states matching sibling components.
3. Render a two-column compare: series, narrator, parts/segment count, duration,
   file size — left book vs right book — plus a row/badge showing which signal
   fired (exact / fuzzy / embedding / etc., from the breakdown payload).
4. Highlight differing fields (e.g. subtle background) so mismatches are obvious.
5. Bump the file header.

## How to test

```bash
cd web && npm run build && npm test
```
Add/extend a component test if the drawer has tests; otherwise verify build +
manual smoke (open a candidate, switch to the Metadata tab, see the compare).

## Acceptance criteria

- [ ] New Metadata tab present alongside Fingerprint.
- [ ] Fetches `/dedup/candidates/:id/breakdown` via the standard API helper, with loading/error states.
- [ ] Side-by-side series/narrator/parts/duration/size + which-signal-fired.
- [ ] Differing fields visually distinguished.
- [ ] `npm run build` + `npm test` pass; file header bumped.

## Commit message

```
feat(dedup-ui): metadata-compare tab in candidate drawer (CONS-6)

Adds a Metadata tab beside Fingerprint showing side-by-side series/narrator/
parts/duration/size and which dedup signal fired, sourced from
GET /api/v1/dedup/candidates/:id/breakdown.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dui-metadata-tab
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If a Metadata tab already exists in the drawer, this is done. Rollback = revert
the commit.
