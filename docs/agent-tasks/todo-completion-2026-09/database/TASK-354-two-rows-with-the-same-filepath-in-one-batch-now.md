<!-- file: docs/agent-tasks/todo-completion-2026-09/database/TASK-354-two-rows-with-the-same-filepath-in-one-batch-now.md -->
<!-- version: 1.0.0 -->
<!-- guid: aa7fd924-4e6e-514b-83a1-422921766fce -->
<!-- last-edited: 2026-09-10 -->

# TASK-354 — 🟠 Two rows with the same FilePath in one batch now corrupt Book.Duration › Fix (TODO.md:4241)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “🟠 Two rows with the same FilePath in one batch now corrupt Book.Duration › Fix” (L4235), items at lines 4241, 4242, 4244
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: MEASURE-ONLY; MIXED — 4241/4242 are clean CODE (within-batch dedup map); 4244 is decide+measure existing duplicate book_file rows · class: data-loss — legit, real Duration-field corruption from a read-committed race · ⛔ standing-ban contact: 4244's natural follow-through (a repair pass on duplicate book_file rows) hits the book_file ban; only counting them is safe · Split in execution: dispatch 4241/4242 as code; 4244 must stop at measurement and surface the decision.; Validator (TASK-350 row): count the existing duplicate book_file rows and surface the decision — a repair pass would hit the never-delete/repoint book_file ban.
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — BatchUpsertBookFiles (pebble_store_bookfiles.go:1399+) has no FilePath-dedup map; seenBooks (:1413) tracks affected books only; GetBookFileByPath is read-committed so two same-batch rows both miss it.
**Priority:** P1 · **Effort:** S · **Recommended subagent:** Opus-class · database subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “🟠 Two rows with the same FilePath in one batch now corrupt Book.Duration › Fix” (L4235), items at lines 4241, 4242, 4244. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-354" -b agent/database-354-two-rows-with-the-same-filepath-in-one-b origin/main
cd "$REPO/.worktrees/database-354"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 3 still-open `TODO.md` item(s) under the heading “🟠 Two rows with the same FilePath in one batch now corrupt Book.Duration › Fix” (TODO.md line 4235; items at lines 4241, 4242, 4244 as of HEAD 42d187168):
  - L4241: Dedup by FilePath within a single batch, before staging
  - L4242: Same for iTunes PID — `enforceBookFilePIDUniqueness` has the identical read-committed gap
  - L4244: Decide whether existing duplicate rows need a repair pass, and measure how many exist

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write.

## Background (verify before editing)

- L4241 — Dedup by FilePath within a single batch, before staging — evidence: internal/database/pebble_store_bookfiles.go:1399-1480 BatchUpsertBookFiles has no within-batch dedup map keyed on FilePath — the only map in the function (`seenBooks`, line 1413) tracks affected books for aggregate recompute, not staged rows by path. Each row still matches via GetBookFileByPath (committed reads only), so two rows sharing a FilePath in one batch both miss and both get written.
- L4242 — Same for iTunes PID — `enforceBookFilePIDUniqueness` has the identical read-committed gap — evidence: BatchUpsertBookFiles's PID branch (pebble_store_bookfiles.go:1424, GetBookFileByPID) has the identical committed-read pattern as the FilePath branch immediately below it (line 1428) — same unfixed gap, same function.
- L4244 — Decide whether existing duplicate rows need a repair pass, and measure how many exist — evidence: No maintenance op, doc, or measurement of existing duplicate book_file rows (from this specific batch-dedup gap) found in docs/ or internal/plugins/maintenance.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "Dedup by FilePath within a single batch, before staging" TODO.md   # item L4241 still exists (line numbers drift; text is the anchor)
  grep -n -F "Same for iTunes PID — `enforceBookFilePIDUniqueness` has the" TODO.md   # item L4242 still exists (line numbers drift; text is the anchor)
  grep -n -F "Decide whether existing duplicate rows need a repair pass, a" TODO.md   # item L4244 still exists (line numbers drift; text is the anchor)
  sed -n '4235p' TODO.md   # the enclosing heading: 🟠 Two rows with the same FilePath in one batch now corrupt Book.Durati
  test -e internal/database/pebble_store_bookfiles.go   # anchor file from the reconciliation evidence
  test -e internal/plugins/maintenance.   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_database_354.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_database_354.md`.

## Commit message

```
fix(database): 🟠 Two rows with the same FilePath in one batch now corrupt Book.Durati (TODO.md:4241)

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
