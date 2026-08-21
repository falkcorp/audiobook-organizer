<!-- file: docs/agent-tasks/todo-completion/web/TASK-181-retarget-diagnostics-spec-ts-ai-submit-and-expor.md -->
<!-- version: 1.0.0 -->
<!-- guid: d2cd78bc-2739-416b-a82b-e5c877c4cc98 -->
<!-- last-edited: 2026-08-21 -->

# TASK-181 — Retarget diagnostics.spec.ts AI-submit and export status mocks to v2 (TODO.md L4960)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Mechanical URL+body retarget across two mocks in one file, same pattern as part 1. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4960 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Six E2E mocks point at operation URLs that no lo" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-10.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-181-retarget-diagnostics-spec-ts-ai-submit-and-expor" -b agent/web-181-retarget-diagnostics-spec-ts-ai-submit-and-expor origin/main
cd "$REPO/.worktrees/web-181-retarget-diagnostics-spec-ts-ai-submit-and-expor"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In web/tests/e2e/diagnostics.spec.ts, change both page.route('**/api/v1/operations/op-2', ...) (~L80) and page.route('**/api/v1/operations/op-1', ...) (~L175) to '**/api/v1/operations/v2/op-2' and '**/api/v1/operations/v2/op-1' respectively, and change each fulfilled body from the flat legacy shape to the v2 envelope { data: { operation: { id, def_id, status, progress_current, progress_total, progress_message, error_message: null, queued_at }, logs: [] } }, preserving each test's existing pollCount-based status transition (running until a threshold, then completed).

## Background (verify before editing)

- web/src/pages/Diagnostics.tsx:154 calls api.getOperationStatus(opId), which (per api.ts:2044) is the same v2-only function used everywhere else in the app — there is no legacy fallback left.
- The op-2 mock backs 'diagnostics_ai' AI-results polling; the op-1 mock backs 'diagnostics_export' ZIP-export polling. Use def_id values 'diagnostics.submit-ai' and 'diagnostics.export' unless a grep of the real registered op IDs (search internal/server for the diagnostics operation defs) shows different strings — cosmetic either way since getOperationStatus does not branch on def_id.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "operations/op-2" web/tests/e2e/diagnostics.spec.ts   # 1 hit, ~L80 — AI-results poll mock still targets the legacy non-v2 op-2 URL
  grep -n "operations/op-1" web/tests/e2e/diagnostics.spec.ts   # 1 hit, ~L175 — export poll mock still targets the legacy non-v2 op-1 URL
  grep -n 'api.getOperationStatus' web/src/pages/Diagnostics.tsx   # 1 hit, ~L154 — Diagnostics.tsx polls via the same getOperationStatus used elsewhere
  ```

### Reuse — don't invent

- Use `v2 mock envelope pattern` in `web/tests/e2e/transcode-and-counting.spec.ts` (verify: `grep -n 'data: { operation' web/tests/e2e/transcode-and-counting.spec.ts`) — do NOT write a parallel helper.

## Step-by-step

1. In web/tests/e2e/diagnostics.spec.ts, locate the page.route call ~L80 for '**/api/v1/operations/op-2' inside setupAiResultsMocks.
2. Change the glob to '**/api/v1/operations/v2/op-2' and rewrite the fulfilled JSON to the v2 envelope shape (see goal), keeping the existing pollCount-driven status/progress logic (status = pollCount >= 2 ? 'completed' : 'running'; progress_current mirrors the old progress value; progress_total mirrors total).
3. Locate the second page.route call ~L175 for '**/api/v1/operations/op-1' (the export-ZIP test) and apply the identical URL+body fix, using op-1's existing single-shot 'completed' response.
4. Run: npx playwright test web/tests/e2e/diagnostics.spec.ts to confirm the AI-results and export-ZIP tests still pass.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_181.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The AI-results mock also serves a separate GET /diagnostics/ai-results/op-2 endpoint (unrelated to operation polling) — do not touch that route, only the operation-status poll route.

## Tests

- web/tests/e2e/diagnostics.spec.ts — the AI-results flow test(s) driven by setupAiResultsMocks still pass.
- web/tests/e2e/diagnostics.spec.ts — 'download ZIP flow triggers export' still passes.

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] grep -n 'operations/v2/op-2' web/tests/e2e/diagnostics.spec.ts returns 1 hit
- [ ] grep -n 'operations/v2/op-1' web/tests/e2e/diagnostics.spec.ts returns 1 hit
- [ ] npx playwright test web/tests/e2e/diagnostics.spec.ts exits 0
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_181.md`.

## Commit message

```
refactor(web): Retarget diagnostics.spec.ts AI-submit and export status moc (TODO L4960)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

(none)
