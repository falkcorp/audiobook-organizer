<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-336-full-application-database-reset-future-gated.md -->
<!-- version: 1.0.0 -->
<!-- guid: 81be55b0-75f6-598f-bd1c-de9fabae937d -->
<!-- last-edited: 2026-09-10 -->

# TASK-336 — Full-application-database reset (future, GATED) (TODO.md:1049)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “Activity-log reset feature + reauth gate (2026-09-07)” (L1031), items at lines 1049
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE · class: security (step-up reauth) + data-loss-prevention (gated full-DB wipe) — both legit · Section and lines verify clean; item 1049 explicitly forbids building the wipe before 1045's gate exists, which the brief's ordering respects. Already flagged review-critical, appropriately.
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): DEFER** — The item's own text makes the DB reset valid only after the reverify gate (TASK-335) exists; that gate is unbuilt and itself needs reshaping.
> **Needs first:** TASK-335 (reshaped) must ship first.
> **Do NOT dispatch this brief to a worker** until the condition above changes; it is gated in PRIORITY-MATRIX.
**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · server-handlers subagent · **Depends on:** none · **Wave:** owner-gated — not a worker task · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “Activity-log reset feature + reauth gate (2026-09-07)” (L1031), items at lines 1049. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-336" -b agent/server-handlers-336-full-application-database-reset-future-g origin/main
cd "$REPO/.worktrees/server-handlers-336"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “Activity-log reset feature + reauth gate (2026-09-07)” (TODO.md line 1031; items at lines 1049 as of HEAD 42d187168):
  - L1049: **Full-application-database reset (future, GATED).** A "reset everything" (books, metadata, authors, versions — the whole Pebble store) reset. **ONLY valid after the passkey/2FA reverify gate above exists** — do not buil

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. 

## Background (verify before editing)

- L1049 — **Full-application-database reset (future, GATED).** A "reset everything" (books, metadata, authors, versions — the whole Pebble store) reset. **ONLY valid after the passkey/2FA reverify gate above exists** — do not build the full-DB wipe without step-up reauth guarding it. Blast radius is the entir — evidence: No full-database-reset admin feature found in internal/server/handlers/; explicitly gated on item 1045's reauth gate, which is also not built.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**Full-application-database reset (future, GATED).** A rese" TODO.md   # item L1049 still exists (line numbers drift; text is the anchor)
  sed -n '1031p' TODO.md   # the enclosing heading: Activity-log reset feature + reauth gate (2026-09-07)
  test -e internal/server/handlers/   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_336.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_336.md`.

## Commit message

```
fix(server-handlers): Full-application-database reset (future, GATED) (TODO.md:1049)

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
