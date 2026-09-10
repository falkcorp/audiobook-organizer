<!-- file: docs/agent-tasks/todo-completion-2026-09/web/TASK-327-series-page-fetches-the-entire-series-table-on-e.md -->
<!-- version: 1.7.0 -->
<!-- guid: a82106f3-5194-4346-8a15-1f2cf8718f50 -->
<!-- last-edited: 2026-09-10 -->

# TASK-327 — Series page fetches the entire series table on every mount, no server pagination (WEB-02)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `WEB-02` (audit_web.json)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `WEB-02` (audit_web.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-327" -b agent/web-327-series-page-fetches-the-entire-series-ta origin/main
cd "$REPO/.worktrees/web-327"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Same as WEB-01: server-side pagination for `GET /series`.

Why it matters: Same whole-table-per-visit cost as WEB-01, scaled by series count instead of author count. Frequency is the same (primary nav page, refetched after every rename/split/delete/merge).

## Background (verify before editing)

- api.ts:1827-1835 `getSeries()` calls `apiFetch(`${API_BASE}/series`)` with no limit/offset. pages/Series.tsx:437-446 calls it in a mount `useEffect`, stores the full array, and paginates/filters/sorts client-side (same shape as Authors.tsx). Same fix pattern as WEB-01; separated because it is a different endpoint/component and series count is presumably smaller than authors, so severity is lower.
- Anchor: `web/src/services/api.ts:1827` (audit `WEB-02`, confidence high, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e web/src/services/api.ts   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '1821,1841p' web/src/services/api.ts   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '431,452p' pages/Series.tsx   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `web/src/services/api.ts` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_web_327.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_web_327.md`.

## Commit message

```
fix(web): Series page fetches the entire series table on every mount, no server  (WEB-02)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — stop and report before implementing: this brief was classified as a standard-lane code change, and a new write path needs the review-critical protocol (dry-run default, undo journal, owner hold).

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
