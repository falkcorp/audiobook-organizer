<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-337-add-a-dry-run-count-mode-to-delete-operations-hi.md -->
<!-- version: 1.0.0 -->
<!-- guid: 14fecd79-df57-54c7-8432-58c61ffa50a8 -->
<!-- last-edited: 2026-09-10 -->

# TASK-337 — Add a dry-run / count mode to `DELETE /operations/history` (TODO.md:1192)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “`GET /operations/timeline` silently ignores its query filters (2026-09-07)” (L1134), items at lines 1192
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE · class: weak data-loss — missing dry-run before an irreversible DELETE of operation-history rows (audit metadata, not book/library data) · Heading and cited item are about two different endpoints (timeline filters vs DELETE history dry-run) — still a legitimate small guard to add.
**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · server-handlers subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “`GET /operations/timeline` silently ignores its query filters (2026-09-07)” (L1134), items at lines 1192. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-337" -b agent/server-handlers-337-add-a-dry-run-count-mode-to-delete-opera origin/main
cd "$REPO/.worktrees/server-handlers-337"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “`GET /operations/timeline` silently ignores its query filters (2026-09-07)” (TODO.md line 1134; items at lines 1192 as of HEAD 42d187168):
  - L1192: **Add a dry-run / count mode to `DELETE /operations/history`**, and consider a delete-by-id endpoint. Today it deletes **by status only** (`DeleteOperationsByStatus`, `internal/server/handlers/operations/handler.go:215`)

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. 

## Background (verify before editing)

- L1192 — **Add a dry-run / count mode to `DELETE /operations/history`**, and consider a delete-by-id endpoint. Today it deletes **by status only** (`DeleteOperationsByStatus`, `internal/server/handlers/operations/handler.go:215`) and `DELETE — evidence: internal/server/handlers/operations/handler.go:242-267 DeleteOperationHistory still deletes immediately via h.store.DeleteOperationsByStatus(statuses) with no dry_run/count query param and no delete-by-id endpoint. PR #3159 fixed a DIFFERENT endpoint (GET /operations/timeline's status filter, now [x] in the doc) — not this DELETE endpoint's missing dry-run mode.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**Add a dry-run / count mode to `DELETE /operations/history`" TODO.md   # item L1192 still exists (line numbers drift; text is the anchor)
  sed -n '1134p' TODO.md   # the enclosing heading: `GET /operations/timeline` silently ignores its query filters (2026-09
  test -e internal/server/handlers/operations/handler.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_337.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_337.md`.

## Commit message

```
fix(server-handlers): Add a dry-run / count mode to `DELETE /operations/history` (TODO.md:1192)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
