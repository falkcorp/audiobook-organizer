<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-26-rerank-scale-normalization.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2956a92b-2c58-4463-80ea-52e3192c0877 -->
<!-- last-edited: 2026-07-03 -->

# TASK-26 — LLM-rerank scale normalization (MATCH-2 / CTR-3)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Wave:** 3 · **Depends on:** TASK-25

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-26-rerank-scale-normalization" -b agent/cr-26-rerank-scale-normalization origin/main
cd "$REPO/.worktrees/cr-26-rerank-scale-normalization"
git rebase origin/main
```

## Goal

Fix the score-scale mismatch in `RerankTopK`: today it overwrites the top-K
candidates' scores with LLM scores hard-clamped to `[0,1]`, then re-sorts the
**whole** candidate list (top-K + untouched tail) by `Score`. The untouched
tail carries the full unclamped multiplier stack (author ×1.5, narrator ×1.3,
narrator-present ×1.15, series ×1.4/×2.0 — routinely 1.5–4.0, intentionally
unclamped per this codebase's design). Any tail candidate scoring `>1.0` — not
rare — outranks even an LLM-certain (`1.0`) reranked winner, silently
defeating the rerank whenever that condition holds. This is dormant in prod
today (`metadata_scoring.llm_enabled=false`) but is the first thing the
pending local-LLM (qwen2.5:7b-instruct) toggle will activate.

**Advisor-corrected framing (do not re-litigate):** the code at the
`for i := range topCands { candidates[i].Score = llmScores[i] }` line
*deliberately* avoids re-multiplying the LLM score by the boost stack — the
LLM prompt already sees author/narrator/series and re-multiplying would
double-count that evidence. That design choice is correct and must be kept.
The defect is purely the **scale mismatch** between the now-clamped top-K and
the never-clamped tail, not "systematic demotion." Do NOT globally clamp
scores elsewhere in this codebase — scores exceeding 100% (i.e. `>1.0`) are
an intentional, documented design decision used throughout matching/dedup.
Your fix must be scoped to the rerank window only.

## Background (verify before editing)

- `RerankTopK` lives in `internal/metafetch/service_scoring.go`. As of this
  writing the relevant section is around lines 576–662 (**re-verify — do not
  trust this line number**, see grep below). Its shape:
  1. Sort `candidates` descending by `Score`.
  2. Compute `bestScore := candidates[0].Score` and walk forward while each
     next candidate is within `config.AppConfig.MetadataScoring.LLMRerankEpsilon`
     of `bestScore`, up to `LLMRerankTopK` (default 5), building `ambiguousEnd`.
  3. `topCands := candidates[:ambiguousEnd]` — the ambiguous window to rerank.
  4. Call `mfs.llmScorer.Score(ctx, query, llmCands)` → `llmScores []float64`,
     each entry already clamped to `[0,1]` inside `internal/ai/llm_scorer.go`
     (`LLMScorer.Score`, the `if score < 0 { score = 0 }` / `if score > 1 { score = 1 }`
     block — re-verify line numbers below).
  5. `for i := range topCands { candidates[i].Score = llmScores[i] }` —
     **direct overwrite, no rescaling.**
  6. Re-sort the **full** `candidates` slice (topCands + untouched tail) by
     `Score` and return it.
- The bug: step 6 compares clamped `[0,1]` values (steps 4–5) against
  untouched tail values that can exceed `1.0`. There is no rescaling step.
- The fix must rescale the LLM-derived scores back into the **original**
  score range observed for the ambiguous window *before* the overwrite, so the
  reranked top-K remains comparable to the never-touched tail on the same
  unclamped scale the rest of the codebase uses. Concretely:
  - Before overwriting anything, capture `origMax := candidates[0].Score` (the
    window's original best) and `origMin := candidates[ambiguousEnd-1].Score`
    (the window's original worst — still ≥ the tail's max in the common case
    since the list was sorted descending and this window is contiguous at the
    top, though not guaranteed if the tail itself has an inflated boost
    outlier — that's exactly the scenario this fix targets).
  - For each candidate `i` in `topCands`, min-max normalize its **original**
    (pre-overwrite) `Score` into `[0,1]` relative to the window:
    `normBase := (candidates[i].Score - origMin) / (origMax - origMin)` (guard
    `origMax == origMin` → `normBase := 1.0` for all, since the window has no
    internal spread).
  - Blend the normalized base with the clamped LLM score for that candidate —
    the LLM score is authoritative for *ranking* (it already reflects the
    boost evidence per the existing code comment), so use it directly as the
    blended normalized value: `normFinal := llmScores[i]`. (Do not average or
    otherwise dilute it — a straight substitution at the normalized layer is
    the "blend": normalize base to match the LLM's scale, then let the LLM
    value stand in for the window's position on the *original* scale via the
    rescale step below. This preserves the existing "don't re-multiply"
    design intent while fixing the scale.)
  - Rescale back into the original window's range:
    `candidates[i].Score = origMin + normFinal*(origMax-origMin)`. This keeps
    every reranked score within `[origMin, origMax]` — the same bounds it
    occupied before rerank — so its position relative to the untouched tail is
    preserved (a reranked candidate can never leap into or fall out of the
    unclamped tail's range due to clamp mismatch alone).
  - Everywhere else in the codebase (dedup, base scoring, boost multipliers)
    remains untouched and unclamped — this fix is scoped entirely inside
    `RerankTopK`.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func (mfs \*Service) RerankTopK\|ambiguousEnd\|topCands\[" internal/metafetch/service_scoring.go
  grep -n "func (s \*LLMScorer) Score\|score < 0\|score > 1" internal/ai/llm_scorer.go
  grep -n "LLMRerankEpsilon\|LLMRerankTopK\|LLMEnabled" internal/config/config.go
  ```
- Confirm the exact overwrite/re-sort block still matches this brief's
  description:
  ```bash
  grep -n "candidates\[i\].Score = llmScores\[i\]\|sort.SliceStable(candidates" internal/metafetch/service_scoring.go
  ```
- Test harness reference: `internal/metafetch/service_wiring.go` exposes
  `(mfs *Service) SetMetadataLLMScorer(scorer ai.MetadataCandidateScorer)` to
  inject a scorer for tests. `internal/ai/mocks` contains an exported,
  mockery-generated `mocks.MockMetadataCandidateScorer` (see
  `internal/ai/metadata_scorer_test.go` for usage pattern) — use that mock
  from `internal/metafetch` tests rather than trying to reach the unexported
  `metadataLLMBackend` interface in package `ai`. Existing rerank tests live in
  `internal/metafetch/service_mock_test.go` under the
  `// RerankTopK` section (`TestRerankTopK`) — extend that function, don't
  create a parallel one.

## Step-by-step

1. Re-run the grep commands above and read the current body of `RerankTopK`
   and `LLMScorer.Score` to confirm line numbers and confirm no prior fix has
   landed (see Idempotency section).
2. In `RerankTopK`, immediately before the
   `for i := range topCands { candidates[i].Score = llmScores[i] }` loop,
   capture `origMax := candidates[0].Score` and
   `origMin := candidates[ambiguousEnd-1].Score` (both read **before** any
   mutation — `candidates[0]` at this point is still `bestScore`, so you may
   reuse the existing `bestScore` variable instead of re-reading
   `candidates[0].Score` if that's cleaner).
3. Replace the direct-overwrite loop with the rescale logic described in
   Background: compute `normFinal := llmScores[i]` (clamped `[0,1]` — already
   guaranteed by `LLMScorer.Score`), guard the degenerate `origMax == origMin`
   case, and set
   `candidates[i].Score = origMin + normFinal*(origMax-origMin)`.
4. Leave everything else in `RerankTopK` unchanged: the ambiguous-window
   detection, the LLM call and its error handling, the final
   `sort.SliceStable` re-sort of the full list, and the early-return paths
   (`len(candidates) < 2`, `mfs.llmScorer == nil`, `ambiguousEnd < 2`,
   LLM error / length mismatch).
5. Do NOT touch `internal/ai/llm_scorer.go`'s clamp — it is correct and
   required as-is (`Score` must return one comparable `[0,1]` value per
   candidate regardless of caller-side rescaling). Read it only to confirm
   the clamp bounds you're rescaling against.
6. Add a new subtest inside `TestRerankTopK` in
   `internal/metafetch/service_mock_test.go` that reproduces the exact
   boost-stacked-tail defect and proves it's fixed:
   - Build a `Service` via `NewService(mock)` then call
     `svc.SetMetadataLLMScorer(...)` with a `mocks.MockMetadataCandidateScorer`
     (from `internal/ai/mocks`) configured via `.EXPECT().Score(...)` (or
     `.On("Score", ...)`, matching the existing mockery-v3 usage pattern
     elsewhere in the repo — check `internal/ai/metadata_scorer_test.go` for
     the exact call shape) to return LLM scores such that the window's
     highest-original-score candidate gets a **lower** LLM score than a
     sibling in the window (proving the LLM reorders within the window), e.g.
     `llmScores := []float64{0.6, 0.95}` for a 2-candidate window whose
     original scores were `{2.0, 1.9}` (simulating a boost-stacked pair).
   - Add a third candidate **outside** the window (the "tail") with an
     unclamped score below `origMin` of the window (e.g. `Score: 1.5`, if
     `origMin` in your window is `1.9`) to prove the tail does NOT leapfrog
     the rescaled window purely due to clamp mismatch — assert the tail
     candidate's relative position versus the window is unchanged /
     consistent with its original score, and that both window candidates
     still land in `[origMin, origMax]` after rerank.
   - Assert the LLM's *preference* is honored: the candidate the LLM scored
     higher (`0.95`) ends up with a higher final `Score` than the sibling
     scored `0.6`, and both final scores lie within `[origMin, origMax]`
     (e.g. `assert.InDelta` / range assertions, not exact floats, since the
     rescale is a linear map you're allowed to state as a formula in the
     test).
   - Keep the three existing subtests (`no_llm_scorer`, `single_candidate`,
     `empty_candidates`) passing unchanged — they exercise the early-return
     paths this change does not touch.
7. Bump the file header (version bump + `last-edited`) on every file you
   touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/metafetch/... -run TestRerankTopK -v -count=1
go test ./internal/metafetch/... -count=1
go test ./internal/ai/... -count=1
go vet ./internal/metafetch/... ./internal/ai/...
```

## Acceptance criteria

- [ ] `RerankTopK` no longer directly overwrites `topCands[i].Score` with a
      raw clamped LLM score; the assigned score is rescaled into the window's
      original `[origMin, origMax]` range.
- [ ] The LLM's relative preference among `topCands` is preserved after
      rescale (higher LLM score → higher final `Score`, within the window).
- [ ] A boost-stacked tail candidate (score `>1.0`, outside the rerank
      window) is not spuriously outranked-or-outranking purely due to the
      clamp/unclamp scale mismatch — new test demonstrates this concretely.
- [ ] `internal/ai/llm_scorer.go`'s `[0,1]` clamp is unchanged — no global
      clamping introduced anywhere else in the codebase (dedup, base scoring,
      boost multipliers all remain intentionally unclamped).
- [ ] The three pre-existing `TestRerankTopK` subtests
      (`no_llm_scorer`, `single_candidate`, `empty_candidates`) still pass
      unmodified.
- [ ] `go test ./internal/metafetch/...` and `go test ./internal/ai/...` are
      green; `go vet` clean on both packages.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(metafetch): rescale LLM rerank scores into original window range (MATCH-2)

RerankTopK overwrote the ambiguous top-K's scores with LLM scores hard-clamped
to [0,1], then re-sorted against an untouched tail carrying the full unclamped
boost-multiplier stack (routinely 1.5-4.0, intentionally unclamped by design).
Any tail candidate scoring >1.0 could outrank even an LLM-certain (1.0)
reranked winner. Rescale the clamped LLM score back into the window's original
[origMin, origMax] range before re-sorting, preserving the LLM's relative
ranking within the window while keeping it comparable to the never-touched
tail on the same scale. Dormant today (llm_enabled=false) but is the first
thing the pending local-LLM toggle activates.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-26-rerank-scale-normalization
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `RerankTopK` already rescales the LLM score into the ambiguous window's
original range (or otherwise demonstrably keeps reranked top-K scores
comparable to the untouched tail's unclamped scale — e.g. via a documented
per-tier normalized confidence, per the CTR-3 recommendation in
`docs/consultancy/04-code-quality.md`) rather than assigning the raw clamped
`[0,1]` LLM score directly to `candidates[i].Score`, this task is done —
verify with:
```bash
grep -n "candidates\[i\].Score = llmScores\[i\]\|origMin\|origMax" internal/metafetch/service_scoring.go
```
If the direct-overwrite line (`candidates[i].Score = llmScores[i]`) is gone
and replaced by a rescale expression, no further work is needed — just
confirm the new `TestRerankTopK` boost-stacked-tail subtest (or an equivalent
one) already exists; if it does not, add it per Step 6 as a standalone
follow-up without touching the (already-fixed) production code.

Rollback = revert the commit; `LLMScorer.Score`'s `[0,1]` clamp and every
other scoring path (dedup, base scoring, boost multipliers) are untouched by
this change and remain in effect either way.
