<!-- file: docs/agent-tasks/todo-completion-2026-09/maintenance/TASK-362-memdb-lossy-readers-headline-is-stale-correct-it.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8ab4e587-3818-5978-90f0-908e3ad09022 -->
<!-- last-edited: 2026-09-10 -->

# TASK-362 — MEMDB-LOSSY-READERS headline is STALE — correct it before acting on it (TODO.md:5246)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “`recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics” (L4679), items at lines 5246
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE, with a caveat · class: data-loss — legit for the two supporting defects (error-folded-to-absent in memdb read; discarded error in GetAllAuthorBookCounts) feeding a purge gate · The headline ask ('correct the STALE headline') is a TODO.md text edit the coordinator owns; only the two supporting code defects are dispatchable.
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — author_purge_empty.go:180-184 still gates deletion on the unfiltered database.AuthorRefCounts — the headline-correction claim is accurate at HEAD. Doc-only correction plus the two supporting code defects.
**Priority:** P1 · **Effort:** S · **Recommended subagent:** Opus-class · maintenance subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “`recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics” (L4679), items at lines 5246. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-362" -b agent/maintenance-362-memdb-lossy-readers-headline-is-stale-co origin/main
cd "$REPO/.worktrees/maintenance-362"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “`recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics” (TODO.md line 4679; items at lines 5246 as of HEAD 42d187168):
  - L5246: **MEMDB-LOSSY-READERS headline is STALE — correct it before acting on it.** `todo.d/20260823-memdb-lossy-projection-unguarded-readers.md` names `purge-empty-authors` (4,975 of 12,854 authors) as its worked example, gatin

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write.

## Background (verify before editing)

- L5246 — **MEMDB-LOSSY-READERS headline is STALE — correct it before acting on it.** `todo.d/20260823-memdb-lossy-projection-unguarded-readers.md` names `purge-empty-authors` (4,975 of 12,854 authors) as its worked example, gating deletion on two unguarded counters. At HEAD that is no longer true: — evidence: The headline-correction itself (purge-empty-authors now gates on the guarded database.AuthorRefCounts) is confirmed true today: internal/plugins/maintenance/author_purge_empty.go:180-184 still calls `database.AuthorRefCounts(store)` as the deletion gate. But the two supporting defects the item says "DO still stand" are both still present: internal/database/memdb_reads.go:184 `if bErr != nil || raw == nil { continue }` still folds a lookup error into "book absent"; internal/plugins/maintenance/author.go:57 is still `bookCounts, _ := store.GetAllAuthorBookCounts()`, discarding the error.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**MEMDB-LOSSY-READERS headline is STALE — correct it before " TODO.md   # item L5246 still exists (line numbers drift; text is the anchor)
  sed -n '4679p' TODO.md   # the enclosing heading: `recoverPebbleClosed` does not cover the WAL-write leg, so teardown st
  test -e internal/plugins/maintenance/author_purge_empty.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_maintenance_362.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_maintenance_362.md`.

## Commit message

```
fix(maintenance): MEMDB-LOSSY-READERS headline is STALE — correct it before acting on it (TODO.md:5246)

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
