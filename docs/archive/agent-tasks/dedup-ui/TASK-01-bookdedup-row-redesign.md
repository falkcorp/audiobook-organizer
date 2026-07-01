<!-- file: docs/agent-tasks/dedup-ui/TASK-01-bookdedup-row-redesign.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2c3d4e5f-0516-4243-9e85-103142539405 -->
<!-- last-edited: 2026-06-28 -->

# TASK-01 — BookDedup row redesign (CONS-4)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** frontend subagent ·
**Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dui-row-redesign" -b agent/dui-row-redesign origin/main
cd "$REPO/.worktrees/dui-row-redesign"
git rebase origin/main
```

## Goal

Apply the existing `renderBookCard` visual pattern to `renderBookSide` in the
dedup comparison so both sides of a candidate look consistent: tall left-aligned
cover, the quality chip inline after the title, and no dead whitespace below the
chip.

## Background (verify before editing)

- Find the component and the two render functions:
  ```bash
  grep -rn "renderBookSide\|renderBookCard" web/src/components/dedup/BookDedup.tsx
  ```
  (`BookDedup.tsx` is large; `renderBookSide` is roughly mid-file. Use grep, not line numbers.)
- Compare the markup/sx of `renderBookCard` (the desired look) with
  `renderBookSide` (the one to fix).

## Step-by-step

1. In `renderBookSide`, make the cover tall and left-aligned:
   `alignSelf: 'stretch'`, fixed width (~56px), `height: '100%'` — matching `renderBookCard`.
2. Move the quality/score chip to sit **inline after the title** (not on its own row below).
3. Remove the empty space under the chip (trim the trailing spacer/Box).
4. Keep all data/handlers identical — this is purely layout.
5. Bump the file header.

## How to test

```bash
cd web && npm run build && npm test
```
Manually (optional): run the dev server, open the dedup page, confirm both sides
match `renderBookCard` and there's no whitespace gap.

## Acceptance criteria

- [ ] `renderBookSide` cover is tall/left-aligned like `renderBookCard`.
- [ ] Quality chip renders inline after the title.
- [ ] No empty whitespace below the chip.
- [ ] `npm run build` + `npm test` pass; no behavior/data changes.
- [ ] File header bumped.

## Commit message

```
feat(dedup-ui): align BookDedup row layout with renderBookCard (CONS-4)

renderBookSide now uses the tall left-aligned cover, inline quality chip, and
trimmed whitespace from renderBookCard so both sides of a candidate match.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dui-row-redesign
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `renderBookSide` already mirrors `renderBookCard`, this is done. Rollback =
revert the commit.
