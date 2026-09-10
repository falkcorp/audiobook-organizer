<!-- file: docs/agent-tasks/todo-completion-2026-09/misc-go/TASK-335-reauth-passkey-reverify-gate-future.md -->
<!-- version: 1.0.0 -->
<!-- guid: 78e369ae-af69-5ce8-b430-730397980ab4 -->
<!-- last-edited: 2026-09-10 -->

# TASK-335 — Reauth / passkey reverify gate (future) (TODO.md:1045)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “Activity-log reset feature + reauth gate (2026-09-07)” (L1031), items at lines 1045
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE · class: security (step-up reauth) + data-loss-prevention (gated full-DB wipe) — both legit · Section and lines verify clean; item 1049 explicitly forbids building the wipe before 1045's gate exists, which the brief's ordering respects. Already flagged review-critical, appropriately.
**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · misc-go subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “Activity-log reset feature + reauth gate (2026-09-07)” (L1031), items at lines 1045. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-335" -b agent/misc-go-335-reauth-passkey-reverify-gate-future origin/main
cd "$REPO/.worktrees/misc-go-335"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “Activity-log reset feature + reauth gate (2026-09-07)” (TODO.md line 1031; items at lines 1045 as of HEAD 42d187168):
  - L1045: **Reauth / passkey reverify gate (future).** Require a step-up reauth (passkey reverify or other 2FA) before (a) pulling the **unredacted** activity export and (b) any destructive reset. Not built yet; the reset feature 

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. 

## Background (verify before editing)

- L1045 — **Reauth / passkey reverify gate (future).** Require a step-up reauth (passkey reverify or other 2FA) before (a) pulling the **unredacted** activity export and (b) any destructive reset. Not built yet; the reset feature ships with masking + audit first, this hardens it. — evidence: No step-up reauth/passkey-reverify gate code found in internal/auth/; explicitly labeled '(future)' in the item and still unbuilt.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**Reauth / passkey reverify gate (future).** Require a step-" TODO.md   # item L1045 still exists (line numbers drift; text is the anchor)
  sed -n '1031p' TODO.md   # the enclosing heading: Activity-log reset feature + reauth gate (2026-09-07)
  test -e internal/auth/   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_misc_go_335.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/auth/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_misc_go_335.md`.

## Commit message

```
fix(misc-go): Reauth / passkey reverify gate (future) (TODO.md:1045)

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
