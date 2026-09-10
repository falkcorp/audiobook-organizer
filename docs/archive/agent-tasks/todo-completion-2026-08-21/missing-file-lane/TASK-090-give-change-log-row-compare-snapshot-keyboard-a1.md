<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-090-give-change-log-row-compare-snapshot-keyboard-a1.md -->
<!-- version: 1.1.0 -->
<!-- guid: f601ccdd-1a8b-4c1e-8201-fe980a38a983 -->
<!-- last-edited: 2026-09-02 -->

# TASK-090 — Give Change Log row 'Compare snapshot' keyboard/a11y affordance (TODO.md L5722)

> **Status 2026-09-02:** ✅ DONE — PR #2807 merged 2026-08-23 (7bac4e6de).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** Needs careful keyboard-event handling that doesn't double-fire with the nested Revert button's own stopPropagation. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 5722 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Change Log rows lost their visible \"Compare snap" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-11.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-090-give-change-log-row-compare-snapshot-keyboard-a1" -b agent/missing-file-lane-090-give-change-log-row-compare-snapshot-keyboard-a1 origin/main
cd "$REPO/.worktrees/missing-file-lane-090-give-change-log-row-compare-snapshot-keyboard-a1"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Following the item's own second (conservative, no product decision needed) option: keep the row click behavior but make it a real interactive element — add role="button", tabIndex={0}, an aria-label, and an onKeyDown handler for Enter/Space — on entries where onCompareSnapshot applies (type === 'metadata_apply' || 'tag_write'). Non-actionable row types keep no role/tabIndex.

## Background (verify before editing)

- The Revert <Button> inside the same row already calls e.stopPropagation() on its own click handler (`grep -n 'stopPropagation()' web/src/components/ChangeLog.tsx` -> 1 hit ~L222), so a new onKeyDown on the row must not fire when focus/activation originates from that button.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'onClick={() => {' web/src/components/ChangeLog.tsx   # 1 hit ~L163 — the row Box has onClick with no role/tabIndex/aria-label
  grep -c 'role="button"' web/src/components/ChangeLog.tsx   # 0 hits — role="button" does not appear anywhere in the file today (grep -c prints "0" and exits 1 on no match) — no role=button exists anywhere in the file today
  grep -n 'onCompareSnapshot?:' web/src/components/ChangeLog.tsx   # 1 hit ~L17 — onCompareSnapshot prop signature
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In web/src/components/ChangeLog.tsx, in the entries.map(...) block (~L139), compute `const clickable = entry.type === 'metadata_apply' || entry.type === 'tag_write';` once per entry (this condition is currently duplicated inline for cursor and onClick).
2. On the row <Box> (~L139-166), add: `role={clickable ? 'button' : undefined}`, `tabIndex={clickable ? 0 : undefined}`, `aria-label={clickable ? 'Compare snapshot' : undefined}`.
3. Add `onKeyDown={(e) => { if (clickable && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); onCompareSnapshot?.(entry.timestamp); } }}` on the same Box.
4. Verify the Revert Button's existing stopPropagation (~L222) still stops the click bubbling to the row's onClick; it does not need changes since key events on the nested button do not re-target the row's onKeyDown.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_090.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Row with onCompareSnapshot undefined (prop optional) — clickable should still gate on entry.type only per current cursor logic, so role stays present but the callback call is a no-op via `?.`.

## Tests

- Add a Vitest test in web/src/components/ChangeLog.test.tsx (create if absent) asserting: (a) a tag_write row has role=button and responds to Enter/Space by calling onCompareSnapshot with the entry timestamp; (b) a non-actionable entry type has no role/tabIndex; (c) pressing Enter on the nested Revert button does not also trigger onCompareSnapshot (anti-double-fire).

Anti-over-suppression test: `test: 'pressing Enter on the Revert button does not double-fire onCompareSnapshot'` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] npm --prefix web run lint passes
- [ ] npm --prefix web test -- ChangeLog passes
- [ ] web/tests/e2e/files-history.spec.ts's existing snapshot-comparison-banner assertion (L334) still passes unmodified
- [ ] Anti-over-suppression test: `test: 'pressing Enter on the Revert button does not double-fire onCompareSnapshot'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_090.md`.

## Commit message

```
refactor(missing-file-lane): Give Change Log row 'Compare snapshot' keyboard/a11y afforda (TODO L5722)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

The item leaves 'restore a visible link/button' as an alternative; this brief picks the row-becomes-keyboard-accessible option since it needs no additional product decision and preserves current visual design. If the owner prefers a visible link/button instead, that changes step 2-3 into adding a small IconButton/Link inside the row (also fine, similar test shape).
