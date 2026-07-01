<!-- file: docs/agent-tasks/dedup-ui/TASK-03-manual-import-button.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e5f6071-2738-4465-9a07-325364759627 -->
<!-- last-edited: 2026-06-28 -->

# TASK-03 — Manual-import button on the Library page (CONS-11)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** frontend subagent ·
**Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dui-manual-import" -b agent/dui-manual-import origin/main
cd "$REPO/.worktrees/dui-manual-import"
git rebase origin/main
```

## Goal

Add a button + small dialog on the Library page that lets the operator import a
single path on demand: it POSTs a `library.import` operation and polls it to
completion, showing progress.

## Background (verify before editing)

- Library page + toolbar: `web/src/pages/Library.tsx`,
  `web/src/components/library/LibraryToolbar.tsx`.
- Operation trigger API: `POST /api/v1/operations/v2` with body
  `{ "def_id": "library.import", "params": { "path": "<abs path>" } }`; poll
  `GET /api/v1/operations/v2/:id`. Confirm the def id + params:
  ```bash
  grep -rn "library.import\|def_id\|operations/v2" internal/server/ internal/plugins/ | head
  ```
  If the def id differs, use the real one and note it in the PR.
- Reuse any existing operation-trigger/poll hook or the `apiFetch` helper. Look
  for a sibling that already launches ops from the UI:
  ```bash
  grep -rn "operations/v2\|def_id" web/src | head
  ```

## Step-by-step

1. Add a "Manual import" button to the Library toolbar.
2. On click, open a dialog with a single text field for an absolute path (+ basic
   validation that it's non-empty).
3. On submit, POST the `library.import` op with `{ path }`, then poll the op id;
   show progress (spinner/percent) and a final success/error toast.
4. Disable the submit while in flight; close the dialog on success.
5. Bump the file header on every touched file.

## How to test

```bash
cd web && npm run build && npm test
```
Manual smoke: open Library, click Manual import, submit a path, see progress →
done.

## Acceptance criteria

- [ ] Manual-import button + dialog on the Library page.
- [ ] Submits `POST /api/v1/operations/v2 {def_id:"library.import", params:{path}}` and polls to completion.
- [ ] Progress + success/error feedback; submit disabled while running.
- [ ] Reuses existing API helper/op hook (no raw fetch if a wrapper exists).
- [ ] `npm run build` + `npm test` pass; file headers bumped.

## Commit message

```
feat(library-ui): manual single-path import button + dialog (CONS-11)

Adds a Library toolbar button that POSTs a library.import operation for an
operator-entered path and polls it to completion with progress feedback.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dui-manual-import
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If a manual-import control already exists on Library, this is done. Rollback =
revert the commit.
