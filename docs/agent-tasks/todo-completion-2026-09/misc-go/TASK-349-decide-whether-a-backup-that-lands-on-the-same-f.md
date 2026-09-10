<!-- file: docs/agent-tasks/todo-completion-2026-09/misc-go/TASK-349-decide-whether-a-backup-that-lands-on-the-same-f.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b6397c7-d072-5763-9509-9f2b686ac8cc -->
<!-- last-edited: 2026-09-10 -->

# TASK-349 — Decide whether a backup that lands on the same filesystem should warn at startup (TODO.md:3395)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “Move database backups off the database's own filesystem” (L3384), items at lines 3395
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): HOLD-FOR-OWNER** — shape: DECISION (item literally says 'Decide whether… should warn') · class: data-loss — legit · The item's own text is a policy decision, not a spec an agent can implement.
> **Do NOT dispatch this brief to a worker.** It needs an owner decision or a prod run; it is listed in BREAKDOWN under *Held for the owner* and gated in PRIORITY-MATRIX.
**Priority:** P1 · **Effort:** S · **Recommended subagent:** Opus-class · misc-go subagent · **Depends on:** none · **Wave:** owner-gated — not a worker task · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “Move database backups off the database's own filesystem” (L3384), items at lines 3395. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-349" -b agent/misc-go-349-decide-whether-a-backup-that-lands-on-th origin/main
cd "$REPO/.worktrees/misc-go-349"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “Move database backups off the database's own filesystem” (TODO.md line 3384; items at lines 3395 as of HEAD 42d187168):
  - L3395: Decide whether a backup that lands on the same filesystem should warn at startup.

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write.

## Background (verify before editing)

- L3395 — Decide whether a backup that lands on the same filesystem should warn at startup. — evidence: No same-filesystem detection exists: `grep -rn 'st_dev|SameDevice|sameFilesystem|sameVolume|Statfs' internal/backup/*.go internal/organizer/*.go` returns nothing. No startup warning code found anywhere in internal/backup or internal/organizer referencing filesystem/volume comparison.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "Decide whether a backup that lands on the same filesystem sh" TODO.md   # item L3395 still exists (line numbers drift; text is the anchor)
  sed -n '3384p' TODO.md   # the enclosing heading: Move database backups off the database's own filesystem
  test -e internal/backup/   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_misc_go_349.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/backup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_misc_go_349.md`.

## Commit message

```
fix(misc-go): Decide whether a backup that lands on the same filesystem should warn  (TODO.md:3395)

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
