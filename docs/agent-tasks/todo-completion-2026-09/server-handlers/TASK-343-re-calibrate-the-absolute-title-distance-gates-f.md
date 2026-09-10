<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-343-re-calibrate-the-absolute-title-distance-gates-f.md -->
<!-- version: 1.0.0 -->
<!-- guid: bf907bab-e670-4796-aca8-3eb75c85bc01 -->
<!-- last-edited: 2026-09-10 -->

# TASK-343 — Re-calibrate the absolute title-distance gates for non-Latin scripts (TODO.md:2872)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` section “Re-calibrate the absolute title-distance gates for non-Latin scripts”, lines 2872, 2887, 2901

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · server-handlers subagent · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` section “Re-calibrate the absolute title-distance gates for non-Latin scripts”, lines 2872, 2887, 2901. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-343" -b agent/server-handlers-343-re-calibrate-the-absolute-title-distance origin/main
cd "$REPO/.worktrees/server-handlers-343"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 3 still-open `TODO.md` item(s) under section “Re-calibrate the absolute title-distance gates for non-Latin scripts” (lines 2872, 2887, 2901 as of HEAD 42d187168). Each item's own text is the spec; the reconciliation evidence below says what still shows the gap.

## Background (verify before editing)

- L2872 — - [ ] **SERIES-PHANTOM-REPAIR** Repair the series IDs that are ALREADY phantom. — evidence: internal/server/duplicates_helpers.go:235 and :446 still reference the historical measurement ('6,893 phantom series' / 'the surviving half of the hazard that left 6,893 phantom...') only in comments about the PREVENTION fix (#2908's SeriesRefCounts guard, phase-1 pruning at lines 247-467) -- no report-first op listing books.series_id values with no matching series row was found anywhere in internal/plugins/maintenance or internal/server (grep for 'PhantomSeries'/'phantom.*series' as a function/op name returns only comments and test references, not a repair implementation).
- L2887 — - [ ] **SERIES-NORMALIZE-TRASHED-GAP** `mergeSeriesGroupHelper` — evidence: internal/server/duplicates_helpers.go:726-759 mergeSeriesGroupHelper still calls `store.GetBooksBySeriesIDAllVersions(fromID)` with no call to database.SeriesRefCounts (the unfiltered reference guard) anywhere in the function body -- that guard exists only in a different function (the series-prune phase-1 code at lines 247-467). A series whose books are all soft-deleted (trashed) still enumerates empty in this loop and `store.DeleteSeries(fromID)` still fires unconditionally at line 756.
- L2901 — - [ ] **SERIES-DENUMBER-TRASHED-GAP** `internal/plugins/maintenance/series_denumber_op.go` — evidence: internal/plugins/maintenance/series_denumber_op.go:300 still has `movedAll := true`, set false only inside the per-book loop body (:305, :318), and `if movedAll { ... DeleteSeries ... }` at :327 fires unconditionally when the loop body never runs (all books trashed, enumeration empty) -- the code's own comment at :286-288 explicitly still documents 'movedAll starts true and is only ever set false inside the loop', confirming the bug is unfixed at HEAD.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**SERIES-PHANTOM-REPAIR** Repair the series IDs that a" TODO.md   # the source item still exists (line numbers drift)
  test -e internal/plugins/maintenance/series_denumber_op.go   # anchor file from the reconciliation evidence
  test -e internal/server/duplicates_helpers.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_343.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- A test proving any new guard/repair path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_343.md`.

## Commit message

```
fix(server-handlers): Re-calibrate the absolute title-distance gates for non-Latin scripts (TODO.md:2872)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
