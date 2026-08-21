<!-- file: docs/agent-tasks/todo-completion/web/TASK-163-find-the-mechanism-behind-the-intermittent-webki.md -->
<!-- version: 1.0.0 -->
<!-- guid: 89d31fb9-1406-49b7-8865-10146db5d575 -->
<!-- last-edited: 2026-08-21 -->

# TASK-163 — Find the mechanism behind the intermittent webkit-only flake in batch-operations.spec.ts:100 (selection persists across page navigation) (TODO.md L1744)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Opus-class · web subagent · **Why:** Root-causing a webkit-only, intermittent (not reliably reproducible) e2e timing flake requires disciplined experiment design (repeated runs, targeted instrumentation) rather than a fixed step list. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1744 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🟡 **`batch-operations.spec.ts:100` [webkit] is an " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-163-find-the-mechanism-behind-the-intermittent-webki" -b agent/web-163-find-the-mechanism-behind-the-intermittent-webki origin/main
cd "$REPO/.worktrees/web-163-find-the-mechanism-behind-the-intermittent-webki"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Determine why, on webkit only and intermittently, the row for 'Select Test Book 1' is not rendered at all (label absent, not merely unchecked) immediately after a page-navigation-and-back sequence in this test, and fix the underlying timing mechanism rather than adding a retry/wait band-aid. Per the item's own framing, the failure shape (row not yet rendered) points at navigation completing before the list re-renders.

## Background (verify before editing)

- Observed failure: `expect(locator).toBeChecked()` times out because `getByLabel('Select Test Book 1', { exact: true })` finds NO element at all — the row itself hasn't rendered yet, not just lost its checked state.
- main is confirmed green on a full run (556 passed / 0 failed / 8 skipped) — this is a genuine intermittent flake, not a persistent regression; do not chase it as if main were broken.
- This is webkit-specific, which typically points at a real timing difference (webkit's navigation/paint event ordering vs chromium/firefox) rather than a logic bug that would fail everywhere.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "selection persists across page navigation" web/tests/e2e/batch-operations.spec.ts   # 1 hit at L99 — the named test exists at approximately the cited line
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Read the full test body (web/tests/e2e/batch-operations.spec.ts, the 'selection persists across page navigation' test) to identify exactly what navigation action happens between selecting the book and re-querying its checkbox (e.g. a route change, a back-button, a page reload).
2. Run the test repeatedly under `--project=webkit --repeat-each=20` (or similar) locally to establish a reproducible failure rate before making any change — per project convention, a single failed run is not a measurement.
3. Once reproducible at some rate, add temporary diagnostic logging/tracing around the navigation completion event and the list's render/mount lifecycle (e.g. Playwright trace viewer, or a console.log keyed to a data attribute that flips when the row mounts) to determine whether the test's navigation-wait condition resolves before React has actually committed the re-rendered list.
4. Identify the specific race: likely candidates are (a) the test's wait-for-navigation helper resolving on a webkit `load`/`domcontentloaded` event that fires before the SPA's client-side route transition finishes re-rendering the list, or (b) a data-fetch promise the list depends on resolving later on webkit specifically.
5. Fix the actual mechanism — e.g. wait for a specific DOM marker that only appears once the list has re-rendered (not a generic navigation-complete signal), rather than adding a longer fixed timeout, which would only narrow the race the way exit:0 narrowed (but didn't fix) the unrelated MuiMenu issue.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_163.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the mechanism turns out to be a genuine webkit navigation-timing quirk rather than an app bug, the fix may need to live in the test's own wait-for logic rather than app code — both are legitimate outcomes, decide based on what step 4 actually finds.

## Tests

- The fixed test itself, re-run with `--repeat-each=50 --project=webkit` to confirm the flake rate drops to 0/50 (or as close as achievable) after the fix, not just 0/1.

Anti-over-suppression test: `The fix must not simply add a longer fixed wait/sleep or increase the test timeout — that reduces the failure rate without finding or fixing the mechanism, which is explicitly what this item asks for ('this still gets its mechanism found rather than being ignored as noise').` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `npx playwright test batch-operations --project=webkit --repeat-each=20` passes 20/20 after the fix, where it previously failed intermittently.
- [ ] Anti-over-suppression test: `The fix must not simply add a longer fixed wait/sleep or increase the test timeout — that reduces the failure rate without finding or fixing the mechanism, which is explicitly what this item asks for ('this still gets its mechanism found rather than being ignored as noise').` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_163.md`.

## Commit message

```
refactor(web): Find the mechanism behind the intermittent webkit-only flake (TODO L1744)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Low severity/low frequency (observed twice total, both times passed on re-run) — reasonable to timebox this investigation rather than block other work on it.
