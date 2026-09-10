<!-- file: docs/agent-tasks/todo-completion-2026-09/dedup/TASK-358-dedup-series-dedup-s-apply-path-writes-no-undo-l.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3e95fc39-9285-5c7b-86ae-ec4624b7df46 -->
<!-- last-edited: 2026-09-10 -->

# TASK-358 — `dedup.series-dedup`'s apply path writes no undo-ledger rows and does not check for a running scan (TODO.md:4967)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “`recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics” (L4679), items at lines 4967
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE · class: data-loss — legit (no undo-ledger journaling + no running-scan check on an apply path) · ⛔ standing-ban contact: none — only checks for a running scan · Section title is the item's own bold name (cosmetic); class and shape are sound.
**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · dedup subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “`recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics” (L4679), items at lines 4967. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-358" -b agent/dedup-358-dedup-series-dedup-s-apply-path-writes-n origin/main
cd "$REPO/.worktrees/dedup-358"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “`recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics” (TODO.md line 4679; items at lines 4967 as of HEAD 42d187168):
  - L4967: **`dedup.series-dedup`'s apply path writes no undo-ledger rows and does not check for a running scan.** TASK-043 gave the op a dry run (`dry_run` defaults to true), which covers the "read before you write" half of the de

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. 

## Background (verify before editing)

- L4967 — **`dedup.series-dedup`'s apply path writes no undo-ledger rows and does not check for a running scan.** TASK-043 gave the op a dry run (`dry_run` defaults to true), which covers the "read before you write" half of the destructive-op checklist. The other half is still missing: — evidence: internal/dedup/series_dedup.go: DedupSeries (func at :311-321) takes no opID parameter at all -- signature is `DedupSeries(_ context.Context, store Store, progress ProgressReporter, dryRun bool)`. Its UpdateBook call (:476) and DeleteSeries call (:516) have no CreateOperationChange journaling anywhere in the function body (the first CreateOperationChange call in the file is at :609, inside the separate MergeSeries function starting at :582). No `library.scan` running/queued check exists in DedupSeries either.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**`dedup.series-dedup`'s apply path writes no undo-ledger ro" TODO.md   # item L4967 still exists (line numbers drift; text is the anchor)
  sed -n '4679p' TODO.md   # the enclosing heading: `recoverPebbleClosed` does not cover the WAL-write leg, so teardown st
  test -e internal/dedup/series_dedup.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_dedup_358.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_dedup_358.md`.

## Commit message

```
fix(dedup): `dedup.series-dedup`'s apply path writes no undo-ledger rows and does  (TODO.md:4967)

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
