<!-- file: docs/agent-tasks/todo-completion-2026-09/web/TASK-330-operations-timeline-fetch-swallows-both-network.md -->
<!-- version: 1.6.0 -->
<!-- guid: 8a276b78-34d3-41ca-8e8d-44bbbd70e1f9 -->
<!-- last-edited: 2026-09-10 -->

# TASK-330 — Operations timeline fetch swallows both network errors and non-2xx into an empty array (WEB-05)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `WEB-05` (audit_web.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · web subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `WEB-05` (audit_web.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-330" -b agent/web-330-operations-timeline-fetch-swallows-both origin/main
cd "$REPO/.worktrees/web-330"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Let `getOperationTimeline` throw on `!response.ok` and genuine fetch failure (matching the pattern used everywhere else in api.ts via `buildApiError`), and have the store-level caller keep last-known operations plus a visible 'couldn't refresh' indicator instead of clearing to empty.

Why it matters: Everything reading this feed (the operations bell / recent-activity panels via useOperationsStore.loadFromServer) will render 'no recent operations' when the timeline endpoint is actually down, rather than an error state -- the same error/empty conflation pattern as WEB-03, in a component users check specifically to confirm a long-running scan/organize job is progressing.

## Background (verify before editing)

- api.ts:589-608: `getOperationTimeline` wraps the whole fetch+parse in try/catch and both the `!response.ok` branch (596) and the catch block (605-607) `return []`. The function's own doc comment above it (585-587) explicitly calls out that a previous version of this endpoint silently truncated results and that was treated as a bug worth fixing (`truncated` is now logged) -- but the sibling failure mode (request fails entirely) still returns the same empty array as 'no operations happened in the window,' with only a `console.warn` for the truncation case, nothing for outright failure.
- Anchor: `web/src/services/api.ts:589` (audit `WEB-05`, confidence medium, severity medium).
- Related tracking: possibly adjacent to the already-tracked OperationActivityPanel duplicate-SSE-lines bug, but this is a distinct swallowed-error path, not duplicate lines

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e web/src/services/api.ts   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '583,614p' web/src/services/api.ts   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `web/src/services/api.ts` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_web_330.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_web_330.md`.

## Commit message

```
fix(web): Operations timeline fetch swallows both network errors and non-2xx int (WEB-05)

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
