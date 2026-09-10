<!-- file: docs/agent-tasks/todo-completion-2026-09/itunes/TASK-340-internal-itunes-service-writeback-batcher-go-sto.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0d4bf396-06c2-58cb-8af4-874290cb7f88 -->
<!-- last-edited: 2026-09-10 -->

# TASK-340 — `internal/itunes/service/writeback_batcher.go` — `Stop()` (`:814`) sets a flag and calls `flush()` o (TODO.md:1461)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “Terminal ops never get `completed_at`, so they linger as zombies (2026-09-07)” (L1335), items at lines 1461
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE · class: mixed — 1366 (no delete-one-op endpoint) is a hygiene/feature gap; 1461 (writeback_batcher.Stop() doesn't join goroutines) is a real concurrent-write hazard to iTunes library files · Neither item is literally about completed_at/zombie ops — both are follow-ons bundled under that heading. 1366 should be reclassified down; 1461 is the genuine risk.
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — writeback_batcher.go Stop() (:814-825) sets b.stopped, stops the timer, flushes once, no WaitGroup join for the three goroutines, stopCh never closed. Independent of the iTunes 2-way-sync design work.
**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · itunes subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “Terminal ops never get `completed_at`, so they linger as zombies (2026-09-07)” (L1335), items at lines 1461. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-340" -b agent/itunes-340-internal-itunes-service-writeback-batche origin/main
cd "$REPO/.worktrees/itunes-340"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “Terminal ops never get `completed_at`, so they linger as zombies (2026-09-07)” (TODO.md line 1335; items at lines 1461 as of HEAD 42d187168):
  - L1461: `internal/itunes/service/writeback_batcher.go` — `Stop()` (`:814`) sets a flag and calls `flush()` once but **waits for nothing**; three goroutines (`:235`, `:256`, `:262`) are unjoined, `flush()` never checks `b.stopped

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. 

## Background (verify before editing)

- L1461 — `internal/itunes/service/writeback_batcher.go` — `Stop()` (`:814`) sets a flag and calls `flush()` once but **waits for nothing**; three goroutines (`:235`, `:256`, `:262`) are unjoined, `flush()` never checks `b.stopped`, and the `stopCh` field (`:93`, `:137`) is dead. Separately, `b.mu` is release — evidence: internal/itunes/service/writeback_batcher.go Stop() (~:814) still only sets b.stopped=true, stops the timer, and calls b.flush() once — no WaitGroup join for the three goroutines the item names; b.mu is still released before SafeWriteITL per the described concurrent-flush hazard.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "`internal/itunes/service/writeback_batcher.go` — `Stop()` (`" TODO.md   # item L1461 still exists (line numbers drift; text is the anchor)
  sed -n '1335p' TODO.md   # the enclosing heading: Terminal ops never get `completed_at`, so they linger as zombies (2026
  test -e internal/itunes/service/writeback_batcher.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_itunes_340.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/itunes/service/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_itunes_340.md`.

## Commit message

```
fix(itunes): `internal/itunes/service/writeback_batcher.go` — `Stop()` (`:814`) set (TODO.md:1461)

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
