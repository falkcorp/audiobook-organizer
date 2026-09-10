<!-- file: docs/agent-tasks/todo-completion-2026-09/web/TASK-324-authors-page-fetches-the-entire-authors-table-on.md -->
<!-- version: 1.6.0 -->
<!-- guid: 56f64ec2-0b86-409f-a529-cf481c9b1768 -->
<!-- last-edited: 2026-09-10 -->

# TASK-324 — Authors page fetches the entire authors table on every mount, no server pagination (WEB-01)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `WEB-01` (audit_web.json)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `WEB-01` (audit_web.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-324" -b agent/web-324-authors-page-fetches-the-entire-authors origin/main
cd "$REPO/.worktrees/web-324"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add `page`/`limit`/`search`/`sort` query params to `GET /authors` (backend work) and switch `getAuthorsWithCounts` + Authors.tsx to server-side pagination like Library.tsx already does via `getBooks(itemsPerPage, offset, ...)`.

Why it matters: Every visit to /authors (and every `loadAuthors()` re-run after merge/delete/undo) pulls ~17k+ author rows including nested alias arrays over the wire and holds them all in React state, just to show a paginated table. This is the same whole-library-fetch shape the standing lesson set (`feedback_measure_frequency_not_just_cost.md`) warns about, except this one has a real, frequent caller: Authors is a primary nav page.

## Background (verify before editing)

- api.ts:1893-1901 `getAuthorsWithCounts()` calls `apiFetch(`${API_BASE}/authors`)` with no limit/offset/page params at all -- it is a bare GET. pages/Authors.tsx:424-439 `loadAuthors` calls `api.getAuthorsWithCounts()` in a `useEffect` that runs once on mount, stores the full array in `authors` state, then pages/Authors.tsx:441-462 does search/filter/sort and `.slice(page*rowsPerPage, ...)` entirely client-side. Per project memory the prod author table is ~17,477 rows post strip-merge (project_author_data_state.md).
- Anchor: `web/src/services/api.ts:1893` (audit `WEB-01`, confidence high, severity high).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e web/src/services/api.ts   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '1887,1907p' web/src/services/api.ts   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '418,468p' pages/Authors.tsx   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `web/src/services/api.ts` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_web_324.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_web_324.md`.

## Commit message

```
fix(web): Authors page fetches the entire authors table on every mount, no serve (WEB-01)

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
