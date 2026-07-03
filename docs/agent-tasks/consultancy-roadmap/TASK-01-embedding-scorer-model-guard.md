<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-01-embedding-scorer-model-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: 91e70f13-83e9-4097-a24b-086e9d0fa911 -->
<!-- last-edited: 2026-07-03 -->

# TASK-01 — EmbeddingScorer model/dim guard + F1 fallback on degenerate scores (consultancy-roadmap)

**Priority:** P0 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-01-embedding-scorer-model-guard" -b agent/cr-01-embedding-scorer-model-guard origin/main
cd "$REPO/.worktrees/cr-01-embedding-scorer-model-guard"
git rebase origin/main
```

## Goal

Fix MATCH-1 / BUG-1 / QUAL-4: `EmbeddingScorer`'s BookID fast-path returns a
cached vector with no model/dimension check, so during the live OpenAI
(3072-dim) → bge-m3 (1024-dim) re-embed, `CosineSimilarity` silently returns 0
for every not-yet-re-embedded book, the scorer returns a nil error, no F1
fallback ever triggers, and every candidate is dropped below
`EmbeddingMinScore` — metadata search returns **zero results** for any
un-re-embedded book right now in prod. Fix both halves:

1. The fast-path in `queryVector` must validate the stored vector's model (or
   at minimum its dimension) against the live client before using it; on
   mismatch it must fall through to a live re-embed instead of returning the
   stale vector.
2. `ScoreBaseCandidates` must treat a degenerate all-zero score result as
   scorer failure and fall back to the F1 tier, instead of accepting it as a
   successful (but useless) `tier="embedding"` result.

## Background (verify before editing)

- `internal/ai/embedding_scorer.go` — `EmbeddingScorer.queryVector` (verified
  at lines 91-99 as of this writing) returns `existing.Vector` from the
  `EmbeddingStore` BookID fast-path with **no check** of `existing.Model`
  against the live client's model:
  ```go
  func (s *EmbeddingScorer) queryVector(ctx context.Context, q Query) ([]float32, error) {
  	if q.BookID != "" && s.store != nil {
  		if existing, err := s.store.Get("book", q.BookID); err == nil && existing != nil && len(existing.Vector) > 0 {
  			return existing.Vector, nil
  		}
  	}
  	text := BuildEmbeddingText("book", q.Title, q.Author, q.Narrator)
  	return s.api.EmbedOne(ctx, text)
  }
  ```
  `existing.Model` (the field exists — `database.Embedding.Model`,
  `internal/database/embedding_store.go` struct around line 131, populated
  from the persisted `embRec.Model` around line 95) is available but unused
  here.
- The `embeddingAPI` interface in the same file (around lines 17-20) currently
  only exposes `EmbedOne` / `EmbedBatch` — it has **no** `Model()` method, so
  there is nothing to compare `existing.Model` against yet. The production
  implementation, `*ai.EmbeddingClient`, already has a `Model() string` method
  (`internal/ai/embedding_client.go`, verify with the grep below) — the
  interface just needs to expose it.
- `database.CosineSimilarity` (`internal/database/embedding_store.go`, verify
  line with grep below) returns `0` whenever `len(a) != len(b)` — this is the
  mechanism that turns a stale 3072-dim cached vector plus a fresh 1024-dim
  candidate vector into a silent zero score, not an error.
- `internal/metafetch/service_scoring.go` — `ScoreBaseCandidates` (verify
  current line range with the grep below) calls the scorer and accepts its
  result as the winning tier whenever `err == nil && len(scores) ==
  len(results)`, with no check that the scores aren't all degenerate (e.g. all
  exactly `0`):
  ```go
  scores, err := mfs.metadataScorer.Score(ctx, query, cands)
  if err == nil && len(scores) == len(results) {
  	return scores, mfs.metadataScorer.Name()
  }
  ```
  If this returns `(scores, "embedding")` with every entry `0`, the F1
  fallback tail (a few lines below, using `computeF1Base`) never runs, and
  `internal/metafetch/service_search.go`'s per-candidate threshold filter
  (`if score <= minScore { continue }`, `minScore =
  config.AppConfig.MetadataScoring.EmbeddingMinScore` — 0.82 by default) drops
  every candidate.
- **Precedent to imitate, not import:** `internal/dedup/engine.go` already
  solved the exact same "stale cross-model vector" problem for the dedup
  embed path with `embeddingModelMatches` (verify with grep below) — treating
  an empty stored model as a mismatch (forces re-embed) and comparing
  `storedModel == de.embedClient.Model()` otherwise. Mirror that logic inside
  `internal/ai` (do not import `internal/dedup` from `internal/ai` — that
  would invert the package dependency direction); write a small
  package-local equivalent instead.
- `service_search.go` is listed as an expected file for this task because the
  regression test for this fix should exercise the full
  `SearchMetadataForBookWithOptions` (or equivalent, verify current name)
  path end-to-end, proving a real, non-empty result set survives the
  threshold filter once the scorer is fixed — not because the production
  code in that file is expected to change. If, after reading the current
  threshold-filter loop, you find a change there is genuinely required to
  make the F1 fallback observable, make the smallest change that achieves it
  and explain why in the PR description; otherwise leave the file's
  production code untouched and add only the test.

- **Re-verify these anchors before editing** — line numbers in the
  consultancy report and in this brief are from 2026-07-02/03 and may have
  drifted:
  ```bash
  grep -n "func (s \*EmbeddingScorer) queryVector\|type embeddingAPI interface\|func (s \*EmbeddingScorer) Score(" internal/ai/embedding_scorer.go
  grep -n "func (c \*EmbeddingClient) Model()" internal/ai/embedding_client.go
  grep -n "func CosineSimilarity\|type Embedding struct\|Model  *string\|Model     string" internal/database/embedding_store.go
  grep -n "func (mfs \*Service) ScoreBaseCandidates" -A 40 internal/metafetch/service_scoring.go
  grep -n "EmbeddingMinScore\|minScore = 0.0\|if score <= minScore" internal/metafetch/service_search.go
  grep -n "func (de \*Engine) embeddingModelMatches" -A 20 internal/dedup/engine.go
  grep -n "type fakeEmbedAPI\|func (f \*fakeEmbedAPI)\|TestEmbeddingScorer_BookIDFastPath" internal/ai/embedding_scorer_test.go
  ```
  Confirm in particular that `TestEmbeddingScorer_BookIDFastPath` (in
  `internal/ai/embedding_scorer_test.go`) still seeds
  `Model: "text-embedding-3-large"` on the stored `database.Embedding` — this
  existing test's fast-path assertion (`api.embedOne == 0`) will regress once
  you add a model check unless `fakeEmbedAPI` reports a matching model (see
  step 3 below).

## Step-by-step

1. **Expose the client's model through the scorer's API seam.**
   In `internal/ai/embedding_scorer.go`, add `Model() string` to the
   `embeddingAPI` interface. `*ai.EmbeddingClient` already implements this
   (re-verify with the grep above) so production wiring needs no change.

2. **Add a model-match guard and use it in `queryVector`.**
   Add a small unexported method, e.g.:
   ```go
   // modelMatches reports whether a stored embedding's model matches the
   // scorer's live API model. A mismatch — e.g. after switching the
   // embedding backend from OpenAI (text-embedding-3-large, 3072-dim) to a
   // local model (bge-m3, 1024-dim) — must force a live re-embed even though
   // a cached vector exists; otherwise CosineSimilarity silently returns 0
   // against the mismatched vector. Empty stored model (pre-model-tagging
   // rows) counts as a mismatch so those are re-embedded too. Mirrors
   // internal/dedup/engine.go's embeddingModelMatches.
   func (s *EmbeddingScorer) modelMatches(storedModel string) bool {
   	if storedModel == "" {
   		return false
   	}
   	return storedModel == s.api.Model()
   }
   ```
   Then change `queryVector`'s fast-path condition to also require
   `s.modelMatches(existing.Model)`, e.g.:
   ```go
   if existing, err := s.store.Get("book", q.BookID); err == nil && existing != nil &&
   	len(existing.Vector) > 0 && s.modelMatches(existing.Model) {
   	return existing.Vector, nil
   }
   ```
   On mismatch, fall through to the existing `s.api.EmbedOne(...)` live-embed
   path unchanged.

3. **Fix the test double so existing tests stay green.**
   In `internal/ai/embedding_scorer_test.go`, add a `Model() string` method to
   `fakeEmbedAPI` (a field like `model string`, defaulting to whatever the
   existing fast-path test's seeded `Model` value is — `"text-embedding-3-large"`
   — so `TestEmbeddingScorer_BookIDFastPath` keeps passing without
   modification). Set this field explicitly wherever a test constructs
   `fakeEmbedAPI` and relies on the fast path being taken.

4. **Add the model-mismatch regression test.**
   In `internal/ai/embedding_scorer_test.go`, add a new test (pattern after
   `TestEmbeddingScorer_BookIDFastPath`) that:
   - Seeds a `database.Embedding` for a book with `Model: "text-embedding-3-large"`
     and a 3072-length `Vector` (values don't need to be realistic, just the
     right length — e.g. `make([]float32, 3072)` with a couple of non-zero
     entries).
   - Constructs a `fakeEmbedAPI` whose `Model()` returns `"bge-m3"` (simulating
     the post-cutover live client) and whose `textToVec` returns a
     1024-length vector.
   - Calls `scorer.Score(...)` with that book's `BookID` and asserts:
     - `api.embedOne` is now `1` (the fast path was correctly bypassed due to
       the model mismatch — contrast with the existing
       `TestEmbeddingScorer_BookIDFastPath`, which asserts `0`).
     - The returned scores are computed from the live 1024-dim query vector
       against 1024-dim candidate vectors (i.e. real cosine values, not the
       degenerate 0 that a naive 3072-vs-1024 comparison would have produced).

5. **Fix the degenerate-all-zero-score gap in `ScoreBaseCandidates`.**
   In `internal/metafetch/service_scoring.go`, change the accept condition so
   an all-zero (or empty) scorer result is treated as failure and falls
   through to the F1 tier below it, e.g.:
   ```go
   scores, err := mfs.metadataScorer.Score(ctx, query, cands)
   degenerate := len(scores) > 0 && allZero(scores)
   if err == nil && len(scores) == len(results) && !degenerate {
   	return scores, mfs.metadataScorer.Name()
   }
   if degenerate {
   	slog.Warn("metadata-scorer returned all-zero scores, falling back to F1",
   		"name", mfs.metadataScorer.Name(), "count", len(scores))
   } else if err != nil {
   	slog.Warn("metadata-scorer failed, falling back to F1", "name", mfs.metadataScorer.Name(), "error", err)
   } else {
   	slog.Warn("metadata-scorer returned wrong score count, falling back to F1", "name", mfs.metadataScorer.Name(), "got", len(scores), "want", len(results))
   }
   ```
   Add a small local helper `allZero(scores []float64) bool` (or inline the
   loop) in the same file. Do not touch the existing F1 fallback tail itself
   (the `computeF1Base` loop) — only the acceptance condition above it. Do
   not attempt to fix the unrelated pre-existing malformed `slog.Warn` calls
   you see nearby (duplicate `"value"`/`"count"` keys, stray `%.3f` left in a
   message string elsewhere in this file) — those are a separate tracked
   defect (BUG-4/QUAL-1), out of scope here; leave them as-is except for the
   two `slog.Warn` lines you are directly editing in this step.

6. **Add a `ScoreBaseCandidates` degenerate-fallback unit test.**
   Add a new test in `internal/metafetch` (new file
   `service_scoring_test.go` if one doesn't already exist — re-verify with
   `ls internal/metafetch/*_test.go`) that:
   - Builds a minimal `*Service` (mirror how `TestNewService` or other
     existing `internal/metafetch` tests construct one) and calls
     `SetMetadataScorer` with a fake `ai.MetadataCandidateScorer` whose
     `Score` returns an all-zero slice and a nil error.
   - Sets `config.AppConfig.MetadataScoring.EmbeddingEnabled = true` for the
     duration of the test (save/restore the prior value).
   - Calls `mfs.ScoreBaseCandidates(...)` with a couple of
     `metadata.BookMetadata` results and asserts the returned tier is `"f1"`
     (not `"embedding"`) and the scores match what `computeF1Base` would
     produce directly.
   - Add a second case with a scorer returning genuine non-zero scores and
     assert the tier stays `"embedding"` (proves the fix doesn't over-trigger
     the fallback).

7. **End-to-end regression test (the `service_search.go` piece).**
   Add or extend a test in `internal/metafetch` that drives the full search
   path (`SearchMetadataForBookWithOptions` or the current equivalent —
   re-verify the name) with a metadata scorer stub simulating the degenerate
   all-zero case, and assert the search returns a non-empty result set
   (proving the F1 fallback from step 5 actually reaches the caller through
   the existing threshold-filter loop, with no production change needed in
   `service_search.go` itself). If this test reveals the threshold-filter
   loop needs an additional change to observe the fallback, make the minimal
   change and document why in the PR description — do not silently expand
   scope beyond what the test proves is necessary.

8. Bump the file header (version bump + `last-edited`) on every file you
   touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/ai/... -run EmbeddingScorer -count=1 -v
go test ./internal/ai/... -count=1
go test ./internal/metafetch/... -count=1
go vet ./internal/ai/... ./internal/metafetch/...
```

## Acceptance criteria

- [ ] `embeddingAPI` exposes `Model() string`; `*ai.EmbeddingClient` satisfies
      it without modification.
- [ ] `EmbeddingScorer.queryVector`'s BookID fast-path is skipped (falls
      through to a live `EmbedOne`) whenever the stored vector's `Model`
      does not match the live API's `Model()`, including when the stored
      model is empty.
- [ ] `TestEmbeddingScorer_BookIDFastPath` (pre-existing) still passes
      unmodified in behavior (same-model fast path still short-circuits
      `EmbedOne`).
- [ ] New regression test proves a 3072-dim stored vector vs. a 1024-dim
      live client bypasses the fast path and produces real (non-degenerate)
      cosine scores.
- [ ] `ScoreBaseCandidates` treats an all-zero (or empty) scorer result as
      failure and returns the F1 tier instead, with a test proving both the
      degenerate-fallback case and the non-degenerate (no over-trigger) case.
- [ ] End-to-end test shows a search that would previously have returned
      zero results (due to the degenerate embedding tier) now returns
      results via the F1 fallback.
- [ ] `go build ./...`, `go vet ./internal/ai/... ./internal/metafetch/...`,
      and `go test ./internal/ai/... ./internal/metafetch/...` are all green.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(ai): guard EmbeddingScorer model/dim mismatch and add F1 fallback on degenerate scores (MATCH-1/BUG-1/QUAL-4)

EmbeddingScorer's BookID fast-path returned the cached vector with no model
check, so during the OpenAI(3072)->bge-m3(1024) re-embed, CosineSimilarity
silently scored every not-yet-re-embedded book's candidates at 0 with a nil
error, and ScoreBaseCandidates accepted that as a successful "embedding" tier
result with no F1 fallback -- metadata search returned zero candidates for
any un-re-embedded book. Add a model-match guard mirroring dedup's
embeddingModelMatches, and treat an all-zero scorer result as failure so it
falls through to F1.

Co-Authored-By: Claude <model> <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-01-embedding-scorer-model-guard
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/ai/embedding_scorer.go`'s `queryVector` already checks
`existing.Model` (or vector length) against the live client's model before
using the fast-path vector, AND `internal/metafetch/service_scoring.go`'s
`ScoreBaseCandidates` already rejects all-zero scorer results and falls back
to F1, this task is done — verify with:

```bash
grep -n "modelMatches\|existing.Model" internal/ai/embedding_scorer.go
grep -n "allZero\|degenerate" internal/metafetch/service_scoring.go
```

If the consultancy citations turn out to already be fixed (e.g. a prior PR
added the guard under a different name), do not re-implement — note what you
found in the PR description and close this task as a no-op rather than
inventing duplicate work. Rollback = revert the commit; the pre-existing
`isNonPrimaryVersion`-style call sites and the F1 fallback tail in
`ScoreBaseCandidates` are untouched by this change and remain in effect.
