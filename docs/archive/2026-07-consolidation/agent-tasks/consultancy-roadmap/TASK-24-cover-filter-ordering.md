<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-24-cover-filter-ordering.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0c3dc53a-2cac-43b1-985a-4b4014b1bc66 -->
<!-- last-edited: 2026-07-03 -->

# TASK-24 — Cover-filter ordering: stop dropping top-scored candidates (MATCH-5 / BUG-6 / QUAL-5)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku · **Wave:** 2 · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-24-cover-filter-ordering" -b agent/cr-24-cover-filter-ordering origin/main
cd "$REPO/.worktrees/cr-24-cover-filter-ordering"
git rebase origin/main
```

## Goal

Stop the metadata-search cover filter from unconditionally deleting
cover-less candidates that are strong evidence: candidates from the direct
ASIN lookup, transcription-confirmed matches, and (at minimum) the
single highest-scored candidate in the batch. Replace the unconditional
hard-drop with a filter that exempts those cases, without touching how
scoring, sorting, capping, or rerank work.

## Background (verify before editing)

- `internal/metafetch/service_search.go` — `SearchMetadataForBookWithOptions`
  builds a `[]MetadataCandidate` slice from all configured sources, then
  appends a direct ASIN-lookup result (if the query looks like/contains an
  ASIN) as its own candidate with `Source: "Audnexus (Audible)"` and a score
  forced to `1.0` when the computed score is `<= 0`. **Immediately after
  that**, a cover-presence filter runs: it builds `withCover` containing only
  candidates with non-empty `CoverURL`, and — if `withCover` is non-empty —
  replaces `candidates` with `withCover`, unconditionally dropping every
  cover-less candidate regardless of score. This happens **before** the
  series-number tiebreaker, **before** `sort.Slice` by score, **before** the
  50-cap, and **before** the optional LLM rerank (`RerankTopK`). A cover-less
  exact match (including the ASIN candidate, which frequently has no
  `CoverURL` from Audnexus) can therefore be deleted while a low-scored
  wrong-book candidate with a cover survives and becomes the top (and,
  in auto-apply paths, the auto-applied) result.
- There is **no existing config knob** that toggles or disables this cover
  filter (verified: `grep -rn "CoversRequired\|RequireCover\|covers_required\|require_cover"
  internal/config internal/metafetch` returns nothing). Do not invent one —
  just change the filter's exemption logic in place.
- There is **no separate direct-ISBN-lookup path** in this function (ISBN is
  only passed as search context to sources, not looked up directly the way
  ASIN is) — despite the consultancy note mentioning "ASIN/ISBN", only the
  ASIN direct-lookup candidate needs the identifier exemption. Identify it by
  `c.Source == "Audnexus (Audible)"` (the literal string used at the ASIN
  branch's candidate-construction site — re-verify below).
- `MetadataCandidate` (in `internal/metafetch/service.go`) already carries a
  `TranscriptionBoosted bool` field — true when the candidate matched the
  book's Whisper-transcribed title/author/narrator. Use it as the second
  exemption category.
- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "Filter out results without cover\|var withCover\|if len(withCover) > 0\|Source: *\"Audnexus (Audible)\"\|TranscriptionBoosted: *transcriptionBoosted\|func (mfs \*Service) SearchMetadataForBookWithOptions\|sort.Slice(candidates" internal/metafetch/service_search.go
  ```
  Confirm the filter block still looks like:
  ```go
  // Filter out results without cover images — they're typically low-quality
  // entries that clutter the results. Keep them only if ALL results lack covers.
  var withCover []MetadataCandidate
  for _, c := range candidates {
      if c.CoverURL != "" {
          withCover = append(withCover, c)
      }
  }
  if len(withCover) > 0 {
      candidates = withCover
  }
  ```
  and that it runs strictly before the "Series-number tiebreaker" comment
  block and the `sort.Slice(candidates, ...)` call.

## Step-by-step

1. In `internal/metafetch/service_search.go`, extract the cover-filter block
   into a standalone, exported-from-package-but-lowercase helper function so
   it is unit-testable in isolation:
   ```go
   // filterCoverlessCandidates drops candidates with no CoverURL, except:
   //   - the direct ASIN-lookup candidate (Source == "Audnexus (Audible)"),
   //   - any TranscriptionBoosted candidate,
   //   - the single highest-scored candidate in the input slice.
   // If every candidate lacks a cover, the input is returned unchanged.
   func filterCoverlessCandidates(candidates []MetadataCandidate) []MetadataCandidate {
       if len(candidates) == 0 {
           return candidates
       }
       bestIdx := 0
       for i := range candidates {
           if candidates[i].Score > candidates[bestIdx].Score {
               bestIdx = i
           }
       }
       var withCover []MetadataCandidate
       for i, c := range candidates {
           switch {
           case c.CoverURL != "":
               withCover = append(withCover, c)
           case c.Source == "Audnexus (Audible)":
               withCover = append(withCover, c)
           case c.TranscriptionBoosted:
               withCover = append(withCover, c)
           case i == bestIdx:
               withCover = append(withCover, c)
           }
       }
       if len(withCover) > 0 {
           return withCover
       }
       return candidates
   }
   ```
   Place it near the other small helpers in the file (e.g. next to
   `computeDurationScore` / `durationScoreMultiplier` — re-verify their
   location with `grep -n "^func " internal/metafetch/service_search.go`).
2. Replace the inline filter block in `SearchMetadataForBookWithOptions` with
   a single call: `candidates = filterCoverlessCandidates(candidates)`.
   Do not move where in the function this call happens — it must still run
   before the series-number tiebreaker and `sort.Slice`.
3. Do not change `MetadataCandidate`, the ASIN-lookup branch, the
   `TranscriptionBoosted` field, scoring, sorting, the 50-cap, or
   `RerankTopK`. This is a pure extraction + exemption change.
4. Add `internal/metafetch/service_search_test.go` (new file — none of the
   existing `internal/metafetch/*_test.go` cover this filter) with table-style
   tests calling `filterCoverlessCandidates` directly:
   - **ASIN candidate survives:** one cover-less candidate with
     `Source: "Audnexus (Audible)"` and `Score: 1.0`, one candidate with a
     cover and a lower score → both candidates remain in the output.
   - **Transcription-boosted candidate survives:** one cover-less candidate
     with `TranscriptionBoosted: true`, one candidate with a cover and a
     higher score → both remain.
   - **Top-scored cover-less candidate survives even with no special flag:**
     one cover-less candidate with the highest `Score` and no
     `TranscriptionBoosted`/ASIN `Source`, one lower-scored candidate with a
     cover → both remain, and the cover-less one is still present (order is
     not asserted, only membership).
   - **Ordinary cover-less non-top candidate is still dropped:** three
     candidates — one with a cover (mid score), one cover-less top-scored,
     one cover-less low-scored with no special flag → the low-scored
     cover-less one is dropped, the other two remain (regression guard for
     the original filter's intent).
   - **All-coverless input is returned unchanged** (existing behavior,
     `len(withCover) == 0` fallback path) — one or two cover-less candidates,
     none matching any exemption → both remain untouched.
5. Bump the file header (version bump + `last-edited`) on every file touched
   per `.standards/instructions/file-headers.md`, including the new test
   file.

## How to test

```bash
go build ./...
go test ./internal/metafetch/... -run TestFilterCoverless -v -count=1
go test ./internal/metafetch/... -count=1
go vet ./internal/metafetch/...
```

## Acceptance criteria

- [ ] Cover filter logic lives in a standalone `filterCoverlessCandidates`
      function, called from `SearchMetadataForBookWithOptions` at the same
      point in the pipeline (before the series-number tiebreaker and sort).
- [ ] A cover-less candidate from the direct ASIN lookup
      (`Source == "Audnexus (Audible)"`) is never dropped for lacking a cover.
- [ ] A cover-less `TranscriptionBoosted` candidate is never dropped for
      lacking a cover.
- [ ] The single highest-scored candidate in the batch is never dropped for
      lacking a cover, even without a special flag.
- [ ] A cover-less candidate that is neither the top score nor
      ASIN/transcription-flagged is still dropped when at least one other
      candidate has a cover (no regression on the filter's original intent).
- [ ] All-cover-less input is returned unchanged (existing fallback
      preserved).
- [ ] `go test ./internal/metafetch/...` is green; `go vet` is clean.
- [ ] File headers bumped on every changed/added file.

## Commit message

```
fix(metafetch): exempt strong-evidence candidates from cover-presence filter (MATCH-5/BUG-6)

The cover filter in SearchMetadataForBookWithOptions ran after scoring but
before sort/cap/rerank and unconditionally dropped every cover-less
candidate whenever at least one candidate had a cover — including the
direct ASIN-lookup candidate (Audnexus frequently omits CoverURL) and the
single highest-scored candidate overall. Extract the filter into
filterCoverlessCandidates and exempt ASIN-direct, TranscriptionBoosted, and
top-scored candidates so a cosmetic preference can no longer displace the
best-evidence match.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-24-cover-filter-ordering
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `filterCoverlessCandidates` (or an equivalently named helper) already
exists and already exempts ASIN-direct, `TranscriptionBoosted`, and
top-scored candidates, this task is done — verify with
`grep -n "filterCoverlessCandidates\|Source == \"Audnexus (Audible)\"" internal/metafetch/service_search.go`
and confirm the exemptions are present in the filter body, not just the ASIN
branch. If the consultancy citation has drifted (e.g. the filter block has
moved to a different file or the ASIN `Source` string literal has changed),
re-locate it with the grep commands in Background before assuming the issue
is already fixed. Rollback = revert the commit; this change is purely
additive to the exemption logic and does not alter scoring, sorting, capping,
or rerank, so reverting restores the exact prior (unconditional-drop)
behavior with no other side effects.
