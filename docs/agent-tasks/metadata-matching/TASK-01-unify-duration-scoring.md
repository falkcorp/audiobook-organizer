<!-- file: docs/agent-tasks/metadata-matching/TASK-01-unify-duration-scoring.md -->
<!-- version: 1.0.0 -->
<!-- guid: 99cbd435-c139-419e-bba7-a5b3740a82ed -->
<!-- last-edited: 2026-07-10 -->

# TASK-01 — Unify the two duration-scoring functions behind one tier table (INIT-3-T2)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). Config extraction (T1) MUST default to today's literal values — zero behavior change until an operator tunes them.
**File-ownership:** none (TASK-02 edits the same file but is serialized into wave 2 AFTER this task merges — never run them concurrently).

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · code-refactor subagent · **Why:** semantic unification of two divergent bucket systems needs judgment, not mechanics · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-matching-unify-duration-scoring" -b agent/metadata-matching-unify-duration-scoring origin/main
cd "$REPO/.worktrees/metadata-matching-unify-duration-scoring"
git rebase origin/main
```

## Goal

Make `durationScoreMultiplier` and `computeDurationScore` in
`internal/metafetch/service_scoring.go` derive from ONE canonical ratio-based tier table so they
can never disagree — while keeping BOTH function names and signatures exactly as they are (all
call sites untouched), and landing golden fixtures that capture the CURRENT outputs of both
functions BEFORE the swap. REUSE the existing functions; do not add a third public duration API.

## Background (verify before editing)

- `durationScoreMultiplier` (~line 172) uses ABSOLUTE-DELTA-SECONDS buckets
  (60/300/600/1200/1800/3600/7200) returning multiplicative ×1.30..×0.50.
- `computeDurationScore` (~line 215) uses DELTA-RATIO buckets (0.05/0.10/0.20/0.50/1.00) returning
  additive +20/+15/+10/0/−10/−20. It feeds the `MetadataCandidate.DurationScore` breakdown field.
- They can disagree: 100-min delta on a 40-hour book → multiplier ×0.75 ("likely different
  edition") but ratio 0.04 → +20 ("essentially the same edition").
- Both treat `bookDurationSec <= 0 || candidateDurationSec <= 0` as UNKNOWN → ×1.0 / +0
  (non-disqualifying). This semantic MUST be preserved exactly.
- Live call sites (do NOT edit them): multiplier at `internal/metafetch/service_search.go`
  lines ~386 and ~455 and `service_scoring.go` ~428; additive at `service_search.go` ~413 and ~473.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func durationScoreMultiplier\|func computeDurationScore' internal/metafetch/service_scoring.go
  # expect 2 hits, ~lines 172 and 215
  grep -rn "durationScoreMultiplier\|computeDurationScore" internal | grep -v _test
  # expect call sites in service_search.go (~386,413,455,473) and service_scoring.go (~428)
  ```
  Zero hits on either grep = STOP and report; the file has changed since planning.

## Step-by-step

1. **Fixture-capture commit first.** In `internal/metafetch/service_scoring_test.go`, add
   `TestDurationScoringGolden`: a table over a grid of `(bookDurationSec, candidateDurationSec)`
   pairs — include 0/negative (unknown), deltas straddling every bucket edge of BOTH systems
   (59/61, 299/301, 599/601, 1199/1201, 1799/1801, 3599/3601, 7199/7201 seconds on short AND very
   long books e.g. 2h and 40h), asserting the CURRENT return values of both functions. Run
   `go test ./internal/metafetch/ -run TestDurationScoringGolden` green; commit this alone
   (`test(metafetch): golden fixtures for duration scoring pre-unification (INIT-3-T2)`).
2. In `internal/metafetch/service_scoring.go`, introduce ONE unexported canonical table, e.g.
   `var durationTiers = []struct{ MaxRatio float64; Multiplier float64; Score float64 }{...}`,
   ratio-based (`|candDur-bookDur| / bookDur`). Choose tier edges/values so the ADDITIVE outputs
   are unchanged from today's `computeDurationScore`, and the multiplier becomes a monotonic
   ratio-based mapping documented in the function comment. Rewrite both functions as lookups into
   this table. Keep both exact signatures:
   `func durationScoreMultiplier(bookDurationSec, candidateDurationSec int) float64` and
   `func computeDurationScore(bookDurationSec, candidateDurationSec int) float64`.
3. **Unknown semantics spelled out:** if either duration is `<= 0`, return `1.0` (multiplier) /
   `0` (score) — UNKNOWN, never disqualifying. State this in the table's doc comment.
4. Update `TestDurationScoringGolden` expectations for the multiplier cells that change; add a
   comment on EVERY changed cell explaining the old value, the new value, and why (this enumerated
   diff is the review artifact). Additive-score cells must NOT change.
5. Purely internal transform: do not touch `service_search.go`, do not change any signature, do
   not rename `MetadataCandidate.DurationScore`.
6. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added — scoring values
   only).
7. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
# caveat: staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
# you changed; the merge gate is Minimal CI green.
go test ./internal/metafetch/ -run TestDurationScoringGolden -v
```

## Acceptance criteria

- [ ] `grep -n "durationTiers" internal/metafetch/service_scoring.go` hits (canonical table exists)
- [ ] `grep -c "case delta <=" internal/metafetch/service_scoring.go` returns 0 (old delta-second switch gone)
- [ ] `git diff origin/main --stat -- internal/metafetch/service_search.go` shows no changes
- [ ] `TestDurationScoringGolden` green; every changed multiplier cell carries an inline justification comment; zero additive-score cells changed; `(0, X)` and `(X, 0)` rows assert ×1.0 / +0
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean (`make ci`, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
refactor(metafetch): unify duration scoring behind one ratio tier table (INIT-3-T2)

durationScoreMultiplier (delta-second buckets) and computeDurationScore
(ratio buckets) assessed the same question with different systems and could
disagree. Both now derive from a single canonical tier table; golden
fixtures captured pre-unification outputs and enumerate every changed cell.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/metadata-matching-unify-duration-scoring
gh pr create --fill
gh pr merge <number> --rebase
```

(When running under a coordinated sweep, STOP after commit — the coordinator owns push/PR/merge.)

## Idempotency / Rollback

If `grep -n "durationTiers" internal/metafetch/service_scoring.go` hits AND
`grep -n "case delta <=" internal/metafetch/service_scoring.go` returns 0 hits, the transform is
already done — run the acceptance checks instead of re-applying. Rollback = revert the commit(s);
both functions return to their independent bucket systems and the fixtures revert with them.
