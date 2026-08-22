<!-- file: docs/agent-tasks/todo-completion/metadata/TASK-079-route-scoreoneresultwithbreakdown-s-base-0-path-.md -->
<!-- version: 1.0.0 -->
<!-- guid: f8e5f851-ac69-4fc7-8ec7-7288b47ae38d -->
<!-- last-edited: 2026-08-21 -->

# TASK-079 — Route ScoreOneResultWithBreakdown's base==0 path through scoreRecorder instead of a hand-built ScoreStep (SCORE-REC)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · metadata subagent · **Why:** Tiny diff but the golden-fixture/mutation-testing requirement means the change must be verified by breaking a value on purpose, which needs care from whoever executes it. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1338 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SCORE-REC**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-079-route-scoreoneresultwithbreakdown-s-base-0-path-" -b agent/metadata-079-route-scoreoneresultwithbreakdown-s-base-0-path- origin/main
cd "$REPO/.worktrees/metadata-079-route-scoreoneresultwithbreakdown-s-base-0-path-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace the hand-built ScoreStep composite literal in ScoreOneResultWithBreakdown's base==0 branch (internal/metafetch/service_scoring.go, currently starting L699) with `newScoreRecorder(0, "Title/author match", detail).breakdown()`, so every ScoreStep in the codebase outside score_breakdown.go's own methods is produced by scoreRecorder. Verify by mutation (halve a factor / change the base score), not by a green test run — a green run alone would not have caught the original defect either.

## Background (verify before editing)

- internal/metafetch/service_scoring.go:693-712 is the exact current function; the base==0 branch (L697-709) returns `0, ScoreBreakdown{Score: 0, Steps: []ScoreStep{{ID: "base", Label: "Title/author match", Op: ScoreOpBase, Operand: 0, Running: 0, Detail: "No significant word overlap with the search title — later bonuses are skipped entirely."}}}`.
- internal/metafetch/score_breakdown.go:115-123's newScoreRecorder(base, label, detail) constructs exactly this same shape: `&scoreRecorder{score: base, steps: []ScoreStep{{ID: "base", Label: label, Op: ScoreOpBase, Operand: base, Running: base, Detail: detail}}}`.
- internal/metafetch/score_breakdown.go:199-201's `(sr *scoreRecorder) breakdown()` returns `&ScoreBreakdown{Score: sr.score, Steps: sr.steps}` — the exact return shape ScoreOneResultWithBreakdown needs (deref with `*`).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ScoreStep{{' internal/metafetch/service_scoring.go   # 1 hit at L704 — ScoreOneResultWithBreakdown hand-builds a ScoreStep composite literal
  grep -n 'func newScoreRecorder' internal/metafetch/score_breakdown.go   # 1 hit at L115 — newScoreRecorder is the canonical way to build a base step
  grep -n 'rec := newScoreRecorder' internal/metafetch/service_scoring.go   # 1 hit at L140, inside ApplyNonBaseAdjustmentsWithBreakdown — the sibling function already uses scoreRecorder for its base step
  grep -rn 'ScoreStep{' internal/metafetch/*.go | grep -v _test.go | grep -v score_breakdown.go   # currently 1 hit (service_scoring.go:704); must be 0 after the fix — no other ScoreStep composite literal exists outside score_breakdown.go after this fix should hold
  ```

### Reuse — don't invent

- Use `newScoreRecorder(base, label, detail) + .breakdown()` in `internal/metafetch/score_breakdown.go` (verify: `grep -n 'func newScoreRecorder\|func (sr \*scoreRecorder) breakdown' internal/metafetch/score_breakdown.go`) — do NOT write a parallel helper.

## Step-by-step

1. Open internal/metafetch/service_scoring.go, locate the `if base == 0 { ... }` block inside `ScoreOneResultWithBreakdown` (currently ~L697-709).
2. Replace the block body with: `rec := newScoreRecorder(0, "Title/author match", "No significant word overlap with the search title — later bonuses are skipped entirely."); return 0, *rec.breakdown()` — preserve the exact same Detail string so the golden fixtures' rendered text does not change.
3. Delete the now-unused manual `ScoreStep{{...}}` literal and its `Steps:`/`Score:` struct-literal wrapper.
4. Run `go build ./internal/metafetch/...` to confirm the replacement compiles (return type must still be `(float64, ScoreBreakdown)`, not `(float64, *ScoreBreakdown)` — dereference `rec.breakdown()`).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_metadata_079.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- None expected — this is a like-for-like construction swap producing byte-identical ScoreStep values; the only risk is a typo in the Detail string breaking a fixture that string-matches it.

## Tests

- internal/metafetch/service_scoring_test.go — golden fixtures (TestDurationScoringGolden and any base==0 case) must still pin the same Score/Steps values after the refactor; run unchanged first to confirm no accidental drift.
- internal/metafetch/service_scoring_breakdown_test.go — the 'Replaying breakdown.Steps must reproduce breakdown.Score exactly' property test (referenced in its file header comment) must still pass for the base==0 case.
- Mutation test (manual, per the item's explicit requirement): temporarily change the replacement's base value from 0 to something else (e.g. 1) or alter the Detail string, re-run the golden fixture test, and confirm it FAILS — this proves the fixture actually pins the value rather than passing vacuously. Revert the mutation before committing.

Anti-over-suppression test: `N/A — no filter/guard is being added; the mutation-test step above IS the anti-over-suppression check for this refactor (per project convention: 'mutation-test before trusting a green test').` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -rn 'ScoreStep{' internal/metafetch/*.go | grep -v _test.go | grep -v score_breakdown.go` returns 0 hits.
- [ ] `go test ./internal/metafetch/... -run TestDurationScoringGolden` passes.
- [ ] The manual mutation test above demonstrably fails when the code is deliberately broken, confirming the golden fixture is load-bearing.
- [ ] Anti-over-suppression test: `N/A — no filter/guard is being added; the mutation-test step above IS the anti-over-suppression check for this refactor (per project convention: 'mutation-test before trusting a green test').` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_metadata_079.md`.

## Commit message

```
refactor(metadata): Route ScoreOneResultWithBreakdown's base==0 path through sco (SCORE-REC)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is the direct completion of #2639's sibling conversion — same shape, same file family, low risk.
