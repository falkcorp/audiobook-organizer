<!-- file: docs/agent-tasks/todo-completion-2026-09/web/TASK-332-no-vitest-or-playwright-coverage-exists-for-the.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0abc956e-6a28-417e-a731-0eb3edbf326d -->
<!-- last-edited: 2026-09-10 -->

# TASK-332 — No Vitest or Playwright coverage exists for the Authors or Series pages (WEB-06)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `WEB-06` (audit_web.json)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Depends on:** none · **Wave:** 1 

Source: Wave 3 audit finding `WEB-06` (audit_web.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-332" -b agent/web-332-no-vitest-or-playwright-coverage-exists origin/main
cd "$REPO/.worktrees/web-332"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

At minimum add a Vitest test for the loading/error/empty/populated states of `loadAuthors`/`loadSeries` (mirroring the pattern already covered for Library.tsx), and one Playwright spec per page covering select-all + bulk delete with a confirm-dialog cancel path.

Why it matters: These are the two primary-nav pages with zero automated coverage of any kind, and they carry irreversible bulk-delete/merge actions plus the perf issue above -- a regression in either (e.g. a broken confirm dialog, a merge sending the wrong IDs) would ship undetected.

## Background (verify before editing)

- `ls web/src/pages/__tests__/` contains only `BookDedup.validation.test.tsx` and `DedupLabels.test.tsx` -- nothing for Authors.tsx or Series.tsx. `ls web/tests/e2e/` has `dashboard.spec.ts`, four `library-*.spec.ts` files, and `review-dupes-lane.spec.ts`, but no `authors*.spec.ts` or `series*.spec.ts`. Both pages contain destructive bulk actions (`api.bulkDeleteAuthors` at Authors.tsx:604, `api.mergeAuthors` at Authors.tsx:570, `api.bulkDeleteSeries` at Series.tsx:671) and the whole-table-fetch behavior in WEB-01/WEB-02.
- Anchor: `web/src/pages/__tests__:0` (audit `WEB-06`, confidence high, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f web/src/pages/__tests__   # the file the finding is anchored to still exists
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `web/src/pages/__tests__` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_web_332.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
cd web && npm ci && npm run build && npm test -- --run
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_web_332.md`.

## Commit message

```
fix(web): No Vitest or Playwright coverage exists for the Authors or Series page (WEB-06)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Pure code change: rollback = `git revert` the commit. If the re-verify greps show the fix already present, run acceptance instead of re-implementing.

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
