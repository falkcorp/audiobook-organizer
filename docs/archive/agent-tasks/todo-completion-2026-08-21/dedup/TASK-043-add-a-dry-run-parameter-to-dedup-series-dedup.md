<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-043-add-a-dry-run-parameter-to-dedup-series-dedup.md -->
<!-- version: 1.1.0 -->
<!-- guid: c2cd094b-8ba2-422f-8fe6-be62dc535a08 -->
<!-- last-edited: 2026-09-02 -->

# TASK-043 — Add a dry-run parameter to dedup.series-dedup (TODO.md L3966)

> **Status 2026-09-02:** ✅ DONE — PR #2773 merged 2026-08-23 (5ffefdd85).

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · dedup subagent · **Why:** Threading a new param through DedupSeries and its call site (internal/server/duplicates_ops.go's dedup.series-dedup op) plus a dry-run-preserves-state test. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3966 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`dedup.series-dedup` still has no dry-run parame" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-06.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-043-add-a-dry-run-parameter-to-dedup-series-dedup" -b agent/dedup-043-add-a-dry-run-parameter-to-dedup-series-dedup origin/main
cd "$REPO/.worktrees/dedup-043-add-a-dry-run-parameter-to-dedup-series-dedup"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a DryRun bool parameter to DedupSeries (default true at the call site until explicitly opted out) so a preview run reports what WOULD merge without writing, matching the pattern already used by author_conjunction_repair.go and the sibling dedup ops in duplicates_ops.go.

## Background (verify before editing)

- TODO.md:3966-3969 states this op 'has never run in production (0 of 10,161 operations), so there is no existing damage; it is a latent hazard only' -- meaning this is safe to change without a data-repair companion, unlike L3795/L3921 which touch already-written rows.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func DedupSeries" internal/dedup/series_dedup.go   # 1 hit L266, signature has no DryRun/params arg — DedupSeries has no dry-run parameter
  grep -n "DryRun\|dry_run" internal/dedup/series_dedup.go   # 0 hits — No DryRun/dry_run token exists anywhere in the file
  grep -n "p.LegacyOpID" internal/server/duplicates_ops.go   # hits at L513/L522 for the series-prune op, a nearby sibling to wire similarly — Series-prune (a sibling op) has the DryRun pattern to model this on
  ```

### Reuse — don't invent

- Use `authorConjunctionRepairParams.DryRun *bool default-true pattern` in `internal/plugins/maintenance/author_conjunction_repair.go` (verify: `grep -n "DryRun \*bool" internal/plugins/maintenance/author_conjunction_repair.go`) — do NOT write a parallel helper.

## Step-by-step

1. Change DedupSeries's signature in internal/dedup/series_dedup.go:266 to accept a `dryRun bool` parameter (or a small params struct if the package's other dedup entry points use one -- check dedup.MergeSeries's signature for the local convention).
2. In the merge loop (the branches around lines 344-366, 492-536, 565+ that call store.GetBooksBySeriesIDCore / store.DeleteSeries), gate every mutating call (book reassignment, DeleteSeries) behind `if !dryRun`.
3. When dryRun is true, still compute and report SeriesDedupResult.TotalMerged as the count that WOULD merge, so the preview is informative.
4. Update the call site in internal/server/duplicates_ops.go (the dedup.series-dedup op, around where DedupSeries is invoked -- grep for its call) to thread a DryRun param from the op's request body, defaulting to true.
5. Add a doc comment on DedupSeries noting the default and citing this TODO.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_043.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- dryRun=true with zero duplicate groups -- TotalMerged=0, no error.

## Tests

- internal/dedup/series_dedup_test.go: TestDedupSeries_DryRunMakesNoChanges -- seed 2 duplicate series with books, call DedupSeries(ctx, store, progress, true), assert store still has both series rows and no books were reassigned.
- TestDedupSeries_RealRunMerges -- same fixture, dryRun=false, assert the merge actually happens (existing coverage, if any, should already cover this).

Anti-over-suppression test: `TestDedupSeries_RealRunMerges -- proves dryRun=false still does the real work (the dry-run flag doesn't silently become the only path).` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/dedup/... -run TestDedupSeries -v exits 0.
- [ ] Anti-over-suppression test: `TestDedupSeries_RealRunMerges -- proves dryRun=false still does the real work (the dry-run flag doesn't silently become the only path).` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_043.md`.

## Commit message

```
feat(dedup): Add a dry-run parameter to dedup.series-dedup (TODO L3966)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n "func DedupSeries" internal/dedup/series_dedup.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Pair with part 2 of this same TODO line (switch to an all-versions series getter) before this op is wired to any production trigger -- both are prerequisites the TODO explicitly lists together.
