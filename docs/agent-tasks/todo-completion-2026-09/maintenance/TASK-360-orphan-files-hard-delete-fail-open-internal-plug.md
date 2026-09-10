<!-- file: docs/agent-tasks/todo-completion-2026-09/maintenance/TASK-360-orphan-files-hard-delete-fail-open-internal-plug.md -->
<!-- version: 1.0.0 -->
<!-- guid: 27d06f1d-f63d-5dde-a6ba-f12c3de0b859 -->
<!-- last-edited: 2026-09-10 -->

# TASK-360 — ORPHAN-FILES-HARD-DELETE-FAIL-OPEN — `internal/plugins/maintenance/orphan_book_files.go` classifies  (TODO.md:5139)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “`recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics” (L4679), items at lines 5139
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE (add requireTablesComplete/ErrMemdbIncomplete guard, matching series/author getters) · class: data-loss — severe: memdb-incomplete reads feed an orphan-file HARD DELETE with no completeness guard, i.e. irreversible file loss on stale data · ⛔ standing-ban contact: none — fix adds a guard · One of the highest-severity items in the batch; prioritize.
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — orphan_book_files.go:232,256 builds its valid set from GetAllBooksCore + ListSoftDeletedBooks; neither memdb implementation (memdb_reads.go:660,790) calls requireTablesComplete, unlike memdb_reads.go:506; this path genuinely hard-deletes (DeleteBookFilesByIDs).
**Priority:** P1 · **Effort:** S · **Recommended subagent:** Opus-class · maintenance subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “`recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics” (L4679), items at lines 5139. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-360" -b agent/maintenance-360-orphan-files-hard-delete-fail-open-inter origin/main
cd "$REPO/.worktrees/maintenance-360"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “`recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics” (TODO.md line 4679; items at lines 5139 as of HEAD 42d187168):
  - L5139: 🔴 **ORPHAN-FILES-HARD-DELETE-FAIL-OPEN** `internal/plugins/maintenance/orphan_book_files.go` classifies `book_file` rows as orphans by testing membership against a map built from TWO unguarded dual-dispatch getters, then

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write.

## Background (verify before editing)

- L5139 — 🔴 **ORPHAN-FILES-HARD-DELETE-FAIL-OPEN** `internal/plugins/maintenance/orphan_book_files.go` classifies `book_file` rows as orphans by testing membership against a map built from TWO unguarded dual-dispatch getters, then **hard-deletes** them. This is worse than SERIES-MERGE-UNGUARDED-DENOMINATOR, w — evidence: internal/plugins/maintenance/orphan_book_files.go:232 `store.GetAllBooksCore(0, 0)` and :256 `store.ListSoftDeletedBooks(0, 0, nil)` are unchanged, still fold into `valid` at :236-238/:264. internal/database/pebble_store.go:599-602 GetAllBooksCore still dispatches to `p.mem().GetAllBooksCore(...)` unconditionally whenever `p.UseMemDB && p.mem() != nil`, with no requireTablesComplete/ErrMemdbIncomplete guard -- unlike the series/author getters that did get this hardening (PR #2839/#2983), this one was never touched.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "🔴 **ORPHAN-FILES-HARD-DELETE-FAIL-OPEN** `internal/plugins/m" TODO.md   # item L5139 still exists (line numbers drift; text is the anchor)
  sed -n '4679p' TODO.md   # the enclosing heading: `recoverPebbleClosed` does not cover the WAL-write leg, so teardown st
  test -e internal/plugins/maintenance/orphan_book_files.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_maintenance_360.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_maintenance_360.md`.

## Commit message

```
fix(maintenance): ORPHAN-FILES-HARD-DELETE-FAIL-OPEN — `internal/plugins/maintenance/orp (TODO.md:5139)

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
