<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-198-diagnose-and-fix-scan-import-organize-spec-ts-s-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 38c9ebab-7dd4-4b9b-bdf8-0f2863d86e8c -->
<!-- last-edited: 2026-08-21 -->

# TASK-198 — Diagnose and fix scan-import-organize.spec.ts's 7 stuck-on-'Add Import Path' failures via DOM snapshot (TODO.md L6394)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** requires actually running Playwright and reading a DOM snapshot to diagnose a still-unknown root cause, then applying whichever fix the snapshot points to -- more than mechanical but the investigation is already narrowed to 3 named candidates · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 6394 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`scan-import-organize.spec.ts` (7 failures) — Se" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-198-diagnose-and-fix-scan-import-organize-spec-ts-s-" -b agent/missing-file-lane-198-diagnose-and-fix-scan-import-organize-spec-ts-s- origin/main
cd "$REPO/.worktrees/missing-file-lane-198-diagnose-and-fix-scan-import-organize-spec-ts-s-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Follow the TODO's own prescribed next step before writing any code: run scan-import-organize.spec.ts in isolation, open the resulting test-results/<dir>/error-context.md (or equivalent Playwright trace/snapshot artifact) for one of the 7 failing tests, and read the actual DOM state at the moment the test times out waiting for getByRole('button', { name: 'Add Import Path' }). Then check the three candidates the TODO already names, in order: (1) does Settings render an error boundary because of an unmocked endpoint the test's fixture setup doesn't stub; (2) does the app redirect away from /settings#paths due to an auth check the test doesn't satisfy; (3) is the tab panel lazily mounted such that the hash-selected tab index is applied AFTER the test's click/wait attempt, i.e. a race between hash-based tab selection and Playwright's assertion timing. Fix whichever candidate the snapshot confirms.

## Background (verify before editing)

- Investigated 2026-08-09: navigating tests to '/settings#paths' (the app's own supported deep link, via tabFromHash at Settings.tsx) was applied and is correct, but did NOT reduce the failure count -- still 7 of 7, all timing out on the same button query. The fix is kept because it is more correct than the old '/settings' navigation, not because it resolved anything.
- The investigation's own meta-lesson, stated explicitly: 'test-results/ was dominated by other tests' directories, so the Settings page snapshot was never actually read -- which, given that reading the snapshot has found every real cause in this effort, is the obvious gap.' This item's first step exists specifically to close that gap.
- This spec is one of the smaller offenders in the broader e2e triage (3 failures here vs 12 in library-browser, 11 in transcode-and-counting per the 2026-08-09 measurement of 66/288 chromium tests failing) -- fixing it does not require touching those other specs.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  find ./web/tests/e2e/scan-import-organize.spec.ts   # 1 hit — the spec file still exists and is presumably still the one with 7 failures
  grep -n 'Add Import Path' web/src/components/settings/PathsSettingsTab.tsx   # 1 hit, L211 — the 'Add Import Path' button all 7 failing tests wait on still exists at this location
  grep -n 'tabFromHash' web/src/pages/Settings.tsx   # 2 hits: function definition and its call site seeding initial tab state — the hash-based Settings tab deep-link mechanism the prior fix relies on is still present
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Run the isolated spec: use the project's Playwright invocation for a single file (check package.json/playwright.config.ts in web/ for the exact npx playwright test command pattern this repo uses) targeting scan-import-organize.spec.ts only, so test-results/ is not dominated by other specs' output.
2. Open the resulting test-results/<dir>/error-context.md (or the equivalent trace viewer output) for one of the 7 failures and read the captured DOM/console state at the failure point.
3. Check candidate 1 (error boundary from an unmocked endpoint): look for an error boundary's fallback UI or a console error in the captured snapshot indicating a failed fetch the test's mock/fixture setup does not stub.
4. Check candidate 2 (auth redirect): look for whether the captured page URL at failure time is still /settings#paths or has redirected elsewhere (e.g. a login page), indicating the test's auth setup doesn't satisfy whatever guard Settings now has.
5. Check candidate 3 (lazy-mount race): look for whether the Paths tab panel is present in the DOM but empty/unmounted at the snapshot point, vs the hash correctly selecting tab index but the panel's content mounting on a later render pass Playwright's default wait doesn't account for.
6. Apply the fix indicated by whichever candidate the snapshot confirms -- this may be a test-fixture fix (mock the unstubbed endpoint, satisfy the auth precondition) or a product-code fix (fix a genuine race in tab-panel mounting) depending on what's actually found; do not guess without the snapshot in hand, per the TODO's own explicit instruction.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_198.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- if the snapshot reveals a FOURTH cause not among the 3 named candidates, do not force-fit it into one of them -- document the actual finding, since the TODO's own history shows guessing without the snapshot has already failed once (the Settings deep-link fix)

## Tests

- web/tests/e2e/scan-import-organize.spec.ts -- all 7 currently-failing tests, run via the project's e2e test command (make test-e2e or the equivalent npx playwright invocation), must pass after the fix.

Anti-over-suppression test: `N/A -- this is an e2e test failure diagnosis, not a filter/guard/skip addition` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] make test-e2e (or the project's scoped single-spec Playwright command) run against scan-import-organize.spec.ts shows 0 failures where there were previously 7
- [ ] the fix's root cause is documented in the PR description or a code comment at the fix site, referencing what the DOM snapshot actually showed, so this investigation is not repeated from scratch again
- [ ] Anti-over-suppression test: `N/A -- this is an e2e test failure diagnosis, not a filter/guard/skip addition` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_198.md`.

## Commit message

```
fix(missing-file-lane): Diagnose and fix scan-import-organize.spec.ts's 7 stuck-on-' (TODO L6394)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Small blast radius (3-7 tests in one spec file) compared to the wider e2e triage (66/288 chromium tests failing as of 2026-08-09) -- safe to work independently of other e2e-repair efforts as long as the fix doesn't touch shared fixture/setup code other specs also rely on (check global-setup.ts and any shared page-object helpers before changing them).
