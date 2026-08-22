<!-- file: docs/agent-tasks/todo-completion/web/TASK-188-harden-muimenu-against-the-documented-react-sets.md -->
<!-- version: 1.0.0 -->
<!-- guid: f217c292-a3f2-4c28-ae9a-55120f9ce2cf -->
<!-- last-edited: 2026-08-21 -->

# TASK-188 — Harden MuiMenu against the documented React setState-drop defect (exit:0 -> exit:false), given the root-cause research is already extensively recorded at HEAD and remains unresolved (TODO.md L1727)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · web subagent · **Why:** The fix pattern is already proven and documented in the same file for Drawer -- this is applying that same, already-validated mitigation to MuiMenu and re-running the existing stress test, not open-ended research. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1727 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🟡 **Why does React silently drop a `setState` issu" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-188-harden-muimenu-against-the-documented-react-sets" -b agent/web-188-harden-muimenu-against-the-documented-react-sets origin/main
cd "$REPO/.worktrees/web-188-harden-muimenu-against-the-documented-react-sets"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Apply the same slotProps.transition.exit:false mitigation already shipped for MuiDrawer (theme.ts:285) to MuiMenu (theme.ts:347-350), since theme.ts's own comment (L220) already identifies them as the same underlying defect and only Drawer received the stronger fix. Re-run the existing 'clears all filters' e2e test at --repeat-each=20 --workers=12 before and after to confirm the stall rate drops to 0/20, matching the standard this repo already holds Drawer to.

## Background (verify before editing)

- theme.ts:307-317's comment on MuiMenu explicitly documents the SAME underlying defect (a Modal root staying position:fixed/pointer-events:auto because RTG's exit transition silently stalls) and states 'Applied to MuiMenu alone rather than MuiPopover, to avoid changing every Autocomplete and picker popper on the strength of a defect only observed on Select menus' -- i.e. the exit:0 mitigation was a deliberately narrow, lower-confidence choice at the time, not a considered rejection of exit:false.
- The deeper WHY (why React drops the setState at all) remains genuinely open per theme.ts's own 2026-08-20 addendum -- this item does not attempt to answer that; it only closes the gap between MuiMenu's weaker mitigation and MuiDrawer's stronger, already-proven one.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '347,351p' web/src/theme.ts   # MuiMenu: { defaultProps: { transitionDuration: { enter: 225, exit: 0 } } } — MuiMenu currently ships the exit:0 mitigation (the narrower one), not exit:false
  grep -n 'THE DEFECT (same one as MuiMenu below)' web/src/theme.ts   # 1 hit ~L220 — MuiDrawer already carries the exit:false fix for the identical defect, with theme.ts's own comment naming MuiMenu as sharing the same defect
  grep -n 'STILL UNEXPLAINED' web/src/theme.ts   # 1 hit ~L273 — theme.ts's own current comment explicitly states the root cause remains unexplained, as of 2026-08-20
  grep -n "clears all filters" web/tests/e2e/library-browser.spec.ts   # ≥1 hit — an existing e2e stress test already exercises the MuiMenu stall scenario at n=20, usable as the acceptance check
  ```

### Reuse — don't invent

- Use `the exit:false + slotProps.transition pattern already proven on MuiDrawer for the identical defect` in `web/src/theme.ts` (verify: `grep -n 'slotProps: { transition: { exit: false } }' web/src/theme.ts`) — do NOT write a parallel helper.
- Use `the existing e2e repro command/test to verify the fix at n=20 before/after` in `web/tests/e2e/library-browser.spec.ts` (verify: `grep -n "clears all filters" web/tests/e2e/library-browser.spec.ts`) — do NOT write a parallel helper.

## Step-by-step

1. In web/src/theme.ts, change MuiMenu's defaultProps (currently `transitionDuration: { enter: 225, exit: 0 }` at L349) to also include `slotProps: { transition: { exit: false } }`, mirroring MuiDrawer's defaultProps shape at L284-303 (keep `enter: 225` for the opening animation, which theme.ts's own comment notes was never implicated).
2. Copy the relevant portion of MuiDrawer's investigation comment (or a condensed pointer to it) onto MuiMenu's block, so a future reader does not have to rediscover that exit:false is a stronger fix than exit:0 for this same class of defect.
3. Run the existing e2e stress test before and after: `CI=true npx playwright test --config=tests/e2e/playwright.config.ts --project=chromium -g "clears all filters" --repeat-each=20 --workers=12` from web/, confirming a 0/20 stall rate after the change (theme.ts's own history records exit:0 alone as 20/20 PASS already at this duration setting, so this is a regression check more than a rescue).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_188.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If MuiMenu's own comment's rationale for staying narrower ('to avoid changing every Autocomplete and picker popper') still holds, confirm the change is scoped to MuiMenu's theme override only and does not cascade to MuiPopover or other consumers via inheritance.

## Tests

- The existing web/tests/e2e/library-browser.spec.ts 'clears all filters' test, run at --repeat-each=20 --workers=12, is the regression check -- no new test file needed unless the MuiMenu-specific Escape-close path isn't already covered by that test's flow.

Anti-over-suppression test: `N/A -- this is a hardening change, not a filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] The e2e stress run above passes 20/20 both before and after (exit:0 already measured 20/20 per theme.ts's own history) -- the acceptance bar for THIS item is that the change is a strict hardening (adds the stronger, already-proven exit:false guarantee) without regressing the existing enter animation or e2e suite.
- [ ] Anti-over-suppression test: `N/A -- this is a hardening change, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_188.md`.

## Commit message

```
refactor(web): Harden MuiMenu against the documented React setState-drop de (TODO L1727)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

The deeper root-cause investigation this TODO item's title names is NOT resolved by this item and is not further advanced by this rescope -- theme.ts's own 2026-08-20 comment already represents the most current, most rigorous investigation on record in this repo, explicitly declining to guess further without new evidence. Re-opening that investigation (rather than the concrete MuiMenu hardening scoped here) would need new tooling/instrumentation access this rescope does not have, and should be treated as a separate, genuinely open-ended spike if pursued.
