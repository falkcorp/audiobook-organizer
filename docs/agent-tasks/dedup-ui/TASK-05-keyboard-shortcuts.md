<!-- file: docs/agent-tasks/dedup-ui/TASK-05-keyboard-shortcuts.md -->
<!-- version: 1.0.0 -->
<!-- guid: 60718293-4950-4687-9c29-547586970849 -->
<!-- last-edited: 2026-06-28 -->

# TASK-05 — Keyboard shortcuts for the dedup page (DEDUP-KB-1)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** frontend subagent ·
**Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dui-kbd-shortcuts" -b agent/dui-kbd-shortcuts origin/main
cd "$REPO/.worktrees/dui-kbd-shortcuts"
git rebase origin/main
```

## Goal

Add keyboard navigation to the dedup page so a reviewer can work without the
mouse, with a `?` help overlay listing the shortcuts.

Shortcuts:
- `j` / `k` — move to next / previous row
- `m` — merge the focused candidate
- `d` — dismiss the focused candidate
- `s` — select / deselect the focused row
- `Enter` — open the compare drawer for the focused row
- `Esc` — close the drawer
- `Shift+A` — select all on the current page
- `?` — toggle a help overlay listing all shortcuts

## Background (verify before editing)

- Dedup page + the handlers the shortcuts trigger (merge/dismiss/select/open):
  ```bash
  grep -rn "onMerge\|onDismiss\|onSelect\|openDrawer\|CompareDrawer\|BookDedup" web/src/components/dedup/ | head
  ```
  Wire each shortcut to the **existing** handler — do not reimplement merge/dismiss.

## Step-by-step

1. Add a global `keydown` listener (effect with cleanup) active only when no
   input/textarea/dialog is focused (guard with
   `document.activeElement` tag check) so typing in fields isn't hijacked.
2. Track the "focused row" index in component state; `j`/`k` move it (clamp to
   range); scroll the focused row into view.
3. Map each key to the existing handler for the focused candidate.
4. Add a `?`-toggled help overlay (a Dialog/Popover) listing the shortcuts.
5. Bump file headers.

## How to test

```bash
cd web && npm run build && npm test
```
Manual smoke: open dedup page, use j/k to move, m/d/s, Enter/Esc, Shift+A, ? for
help. Confirm typing in a text field does NOT trigger shortcuts.

## Acceptance criteria

- [ ] All listed shortcuts work and call the existing handlers.
- [ ] Shortcuts are suppressed while an input/textarea/dialog is focused.
- [ ] `?` shows a help overlay listing the shortcuts.
- [ ] Listener is cleaned up on unmount (no leak).
- [ ] `npm run build` + `npm test` pass; file headers bumped.

## Commit message

```
feat(dedup-ui): keyboard shortcuts + help overlay for dedup page (DEDUP-KB-1)

Adds j/k navigation, m/d/s actions, Enter/Esc drawer control, Shift+A select-all,
and a ?-toggled help overlay, wired to existing handlers and suppressed while
inputs are focused.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dui-kbd-shortcuts
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If the dedup page already has a keydown listener with these shortcuts, this is
done. Rollback = revert the commit.
