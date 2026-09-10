<!-- file: docs/agent-tasks/todo-completion-2026-09/web/TASK-329-dashboard-count-widgets-silently-show-0-when-the.md -->
<!-- version: 1.6.0 -->
<!-- guid: 18490850-f6c4-45d5-b7d1-60ad62e16e91 -->
<!-- last-edited: 2026-09-10 -->

# TASK-329 — Dashboard count widgets silently show 0 when the count API fails -- indistinguishable from a genuinely-empty library (WEB-03)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `WEB-03` (audit_web.json)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Haiku-class · web subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `WEB-03` (audit_web.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-329" -b agent/web-329-dashboard-count-widgets-silently-show-0 origin/main
cd "$REPO/.worktrees/web-329"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Track a per-widget error flag (or reuse a shared `dashboardError` string) and render an inline warning icon/tooltip on the stat tile instead of silently coercing to 0; keep the last-known-good value rather than zeroing it.

Why it matters: This is exactly the standing 'loading/error/empty/populated must be visually distinguishable' lesson: a user opening the dashboard during a backend blip sees '0 authors, 0 series, 0 imported' -- which reads as 'the library is empty,' not 'the request failed.' Given this project's history of scans/exports being triggered off dashboard state, an operator could reasonably (and wrongly) conclude the library was wiped.

## Background (verify before editing)

- Dashboard.tsx:224-249: `loadAuthors`, `loadSeries`, `loadImportedCount` each do `try { setXCount(await api.countX()) } catch { setXCount(0) }` -- no `console.error`, no toast, no error state stored or rendered anywhere for these three. `loadStats` (113-222) at least does `console.error('Failed to load system status:', error)` before zeroing everything out, but still renders identical-looking zeroed stat tiles with no visible error banner. All four fire again every 15s while a scan is active (260-285), so a flaky count endpoint flaps the dashboard between real and fake zeros with no visible indication.
- Anchor: `web/src/pages/Dashboard.tsx:224` (audit `WEB-03`, confidence high, severity high).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e web/src/pages/Dashboard.tsx   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '218,255p' web/src/pages/Dashboard.tsx   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `web/src/pages/Dashboard.tsx` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_web_329.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_web_329.md`.

## Commit message

```
fix(web): Dashboard count widgets silently show 0 when the count API fails -- in (WEB-03)

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
