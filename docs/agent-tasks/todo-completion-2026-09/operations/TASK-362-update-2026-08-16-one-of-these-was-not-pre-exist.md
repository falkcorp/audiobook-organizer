<!-- file: docs/agent-tasks/todo-completion-2026-09/operations/TASK-362-update-2026-08-16-one-of-these-was-not-pre-exist.md -->
<!-- version: 1.0.0 -->
<!-- guid: 55693af5-56a3-45ed-a940-b69e7d2179cd -->
<!-- last-edited: 2026-09-10 -->

# TASK-362 — Update 2026-08-16: one of these was NOT pre-existing (TODO.md:11964)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` section “Update 2026-08-16: one of these was NOT pre-existing”, lines 11964, 15004

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · operations subagent · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` section “Update 2026-08-16: one of these was NOT pre-existing”, lines 11964, 15004. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/operations-362" -b agent/operations-362-update-2026-08-16-one-of-these-was-not-p origin/main
cd "$REPO/.worktrees/operations-362"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 2 still-open `TODO.md` item(s) under section “Update 2026-08-16: one of these was NOT pre-existing” (lines 11964, 15004 as of HEAD 42d187168). Each item's own text is the spec; the reconciliation evidence below says what still shows the gap.

## Background (verify before editing)

- L11964 — - [ ] **`OperationDef.Permissions` is enforced by nothing — and PR-3 is about to delete the code that *is* doing the enf — evidence: internal/operations/registry/registry.go:570 still only json.Marshal(def.Permissions) into the DB column; grep for def.Permissions/.Permissions across internal/server/handlers/operations*/*.go and internal/server/*.go shows no enforcement site, and internal/server/maintenance_job_op.go:94 explicitly comments that nothing enforces OperationDef.Permissions.
- L15004 — - [ ] **react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable, do not — evidence: web/package.json shows react now at ^19.2.8 (up from 18.3.1 at filing) while react-router-dom is still ^7.18.3 — the item's own 'revisit when the app moves to React 19' trigger has now been met, so the react-router v8.3.0 upgrade (closing GHSA-qwww-vcr4-c8h2) is unblocked and worth re-evaluating.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**`OperationDef.Permissions` is enforced by nothing — " TODO.md   # the source item still exists (line numbers drift)
  test -e internal/operations/registry/registry.go   # anchor file from the reconciliation evidence
  test -e web/package.json   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_operations_362.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- A test proving any new guard/repair path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_operations_362.md`.

## Commit message

```
fix(operations): Update 2026-08-16: one of these was NOT pre-existing (TODO.md:11964)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
