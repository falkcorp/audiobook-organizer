<!-- file: docs/agent-tasks/dedup-label-quality/TASK-08-engine-ratio-align.md -->
<!-- version: 1.0.0 -->
<!-- guid: 07652e67-4f66-4087-89d6-29e3aadea900 -->
<!-- last-edited: 2026-07-10 -->

# TASK-08 — Align engine part-vs-whole ratio with the dataset rule: 0.6 → 0.5 (INIT-1 T8, deferred wave)

**Gate:** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval. (This task is a code PR — autonomous lane — but its TIMING is externally gated, see File-ownership.)
**File-ownership:** ⛔ INIT-2 OWNS all structural edits to `internal/dedup/engine.go`. This task is INIT-1's SINGLE engine.go touch and lands AFTER INIT-2's engine.go waves merge, rebased on top — never a concurrent wave on engine.go. Before starting, CONFIRM with the coordinator/owner that INIT-2's engine.go work has merged; if any INIT-2 engine.go PR is open, STOP and wait.

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · single-const behavior change + boundary test subagent · **Why:** one-line change but it narrows a live candidate-suppression veto — needs the boundary test and the external-timing check, not just a sed · **Depends on:** EXTERNAL — INIT-2 engine.go waves merged; no INIT-1 task prereq

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
# ⛔ TIMING GATE: do not proceed until INIT-2's engine.go waves are merged (ask the coordinator).
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-label-quality-engine-ratio-align" -b agent/dedup-label-quality-engine-ratio-align origin/main
cd "$REPO/.worktrees/dedup-label-quality-engine-ratio-align"
git rebase origin/main
```

## Goal

Make the engine's part-vs-whole veto agree with the dataset mining rule: change `partVsWholeDurationRatioMax` from `0.6` to `0.5` in `internal/dedup/engine.go`, matching `partVsWholeRatioMax = 0.5` in `internal/dedup/dataset/rules.go`. Today the engine suppresses candidate pairs at ratio < 0.6 while the dataset rule labels at < 0.5 — the 0.5–0.6 band is vetoed by the engine but would NOT be labeled part-vs-whole by the miner, an inconsistency that skews which pairs ever reach the gold set. This NARROWS the veto (fewer pairs suppressed → slightly more candidates emitted). One const + a comment + a boundary test; nothing else.

## Background (verify before editing)

- Engine side: `const partVsWholeDurationRatioMax = 0.6` (~engine.go:107) used by `func (de *Engine) isPartVsWholeMismatch(a, b *database.Book) bool` (~engine.go:1528): `float64(partDuration) < partVsWholeDurationRatioMax*float64(wholeDuration)`.
- Dataset side (the source of truth being aligned to): `const partVsWholeRatioMax = 0.5` in `internal/dedup/dataset/rules.go`.
- Existing tests for the veto live in `internal/dedup/engine_part_vs_whole_test.go` — extend there; some cases may pin 0.6-era expectations and need updating (that IS the behavior change; update them deliberately and say so in the PR).
- Effect direction (spell it out in the PR body): pairs with duration ratio in [0.5, 0.6) are no longer vetoed at emission; downstream rules/labels handle them. This is candidate-emission behavior, not stored-data mutation.

- **Re-verify these anchors before editing** — line numbers drift, and INIT-2's merged waves may have moved/renamed things (if the const or func is gone/renamed, STOP and report):
  ```bash
  grep -n 'func (de \*Engine) isPartVsWholeMismatch\|partVsWholeDurationRatioMax' internal/dedup/engine.go
  grep -n 'func partVsWhole\|func missingFile\|partVsWholeRatioMax' internal/dedup/dataset/rules.go
  grep -rn "PartVsWhole" internal/dedup/engine_part_vs_whole_test.go | head -5   # test target, >=1 hit (capital P — the file's only identifier is TestUpsertExactCandidate_PartVsWholeGuard; a lowercase-p pattern matches nothing)
  ```

## Step-by-step

1. Confirm the timing gate (INIT-2 engine.go waves merged) and run the anchor greps above.
2. `internal/dedup/engine.go` — change the const value `0.6` → `0.5` and extend its comment: `// aligned with dataset/rules.go partVsWholeRatioMax (INIT-1 T8)`. Touch NOTHING else in engine.go — no refactors, no drive-bys, INIT-2 owns this file structurally.
3. `internal/dedup/engine_part_vs_whole_test.go` — add `TestIsPartVsWholeMismatchAlignedRatio`: ratio 0.49 → mismatch (vetoed); ratio 0.55 → NOT a mismatch (this is the changed band — the anti-over-suppression case proving the veto narrowed rather than widened); ratio 0.7 → NOT a mismatch (unchanged). Update any existing assertions that pinned the 0.6 boundary, listing each in the PR description.
4. Edge-case semantics (keep exactly as-is, assert unchanged): zero/unknown durations on either side must keep today's behavior (whatever guard `isPartVsWholeMismatch` already has for non-positive durations — do not "improve" it).
5. Bump the file header (version + last-edited) on both files; keep existing guids.

## How to test

```bash
go test ./internal/dedup/... -race
go test ./... -short
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "partVsWholeDurationRatioMax = 0.5" internal/dedup/engine.go` hits (and `grep -n "partVsWholeDurationRatioMax = 0.6" internal/dedup/engine.go` returns 0)
- [ ] `go test ./internal/dedup/ -run TestIsPartVsWholeMismatchAlignedRatio -v` passes, including the 0.55-ratio NOT-vetoed case (anti-over-suppression: the veto narrowed, candidates in [0.5,0.6) now survive emission)
- [ ] zero/unknown-duration behavior unchanged (existing guard untouched, covered by existing tests still green)
- [ ] `git diff origin/main -- internal/dedup/engine.go` shows ONLY the const line + comment + header bump (ownership discipline)
- [ ] `go test ./... -short` green; `make ci` green
- [ ] File headers bumped on both changed files

## Commit message

```
fix(dedup): align engine part-vs-whole veto ratio 0.6 -> 0.5 with dataset rule (INIT-1 T8)

The engine vetoed candidate emission at duration ratio < 0.6 while the
gold-label miner labels part-vs-whole at < 0.5, so the [0.5, 0.6) band was
suppressed before it could ever be labeled. Single-const alignment; lands
after INIT-2's engine.go waves per the file-ownership partition.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-label-quality-engine-ratio-align
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "partVsWholeDurationRatioMax = 0.5" internal/dedup/engine.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the single-const commit; candidate emission returns to the 0.6 veto immediately, and no stored data changed either way (the veto affects only future emission).
