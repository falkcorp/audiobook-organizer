<!-- file: docs/proposals/2026-10-holistic/02-filter-identification-pipeline/B-complexity-and-measurements.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5d2a9c74-1e8b-4f36-b0c5-7a4e2d9f6b13 -->
<!-- last-edited: 2026-10-08 -->

# Appendix B: measurements and derivations

All runs were on an Apple M1 Max laptop at HEAD `f7211eb39`, with synthetic fixtures and no
production calls.

## B1. Measured

```
go test -run '^$' -bench 'BenchmarkOwnerQuery_100k' -benchtime 20x -benchmem ./internal/audiobooks/
BenchmarkOwnerQuery_100k-10   20   50283496 ns/op   122816045 B/op   560000 allocs/op
```

This measures `matchesFieldFiltersRT`, the convenience path that **compiles the filters on
every row** (`internal/audiobooks/service_filtering.go:267-287`). The 5.6 allocations and
~1.2 KB per book come from that per-row compile. Production compiles once per request, so
these figures are an **upper bound** and do not describe production (finding F4).

```
go test -run '^$' -bench 'ScopedTagFacets' -benchtime 5x -benchmem ./internal/audiobooks/
BenchmarkScopedTagFacets_Cold/primary-only-10        5   6170900 ns/op  5428920 B/op  32080 allocs/op
BenchmarkScopedTagFacets_Cold/primary+organized-10   5  21823367 ns/op  5054739 B/op  42151 allocs/op
BenchmarkScopedTagFacets_Cold/primary+tag-narrow-10  5   1180958 ns/op  1130030 B/op   1064 allocs/op
```

The fixture is 5,000 books, of which 4,000 are primary, with six tags each. This benchmark
runs the real pushdown path, a full `MatchingBookIDs` walk followed by the tag count.

- Adding `library_state=organized` routes the request through the heavy-pushdown branch,
  and that branch costs 3.5× the light one.
- A tag narrowing, which becomes `RestrictToIDs`, costs 0.19× the light one. This is the
  shape a posting-list index would give every enum filter.

## B2. Scaled (derived, linear in rows walked)

| Walk | 5k fixture | ~40k primary walk (×10) | ~100k rows (×25) |
|---|---|---|---|
| primary only | 6.2 ms | ~62 ms | ~155 ms |
| primary + one enum filter | 21.8 ms | ~218 ms | ~545 ms |
| tag-narrowed | 1.2 ms | ~12 ms | ~30 ms |

A settled filter change triggers 3–4 such walks: page, count, facets, and select-all when
clicked. That makes a single change roughly 0.2–2 s of CPU in the worst case, before any
stripped-field point reads. This is derived, not measured on production. PR 1 adds the
missing production-path benchmark.

## B3. Identification call budget (derived)

The upper bound on provider calls per book in one fan-out search with no early stop:
- 4 Audible variants;
- 2 Open Library asks;
- 1–4 Google calls (per the 09-05 quota note);
- an Audnexus ASIN lookup, which tries 3 regions first and then falls back.

Take the zero-candidate population, about 8.9k books (10-05 census, dated). With **no
negative caching**, every pass costs about 8.9k × (4 + 2 + ~2) ≈ 71k calls in the worst
case.

- At Audible's 8/s, the Audible share alone is 8.9k × 4 / 8 ≈ 74 minutes per pass, repeated
  on every pass that selects these books.
- The stale and empty handling (`LastEmptyFetchAt`) keeps the scheduled tick from
  re-selecting them every 6 h. A forced refetch, or any change to the question fingerprint,
  re-asks them all.
- Google's 1,000-per-day key limits rescue through Google to about 400 books per day
  (09-05 note).

With P1 negative caching inside its TTL, a repeat pass costs **0** calls for an unchanged
question.

## B4. Dedup pairwise (derived)

- **`BookSignatureScan`, before.** n(n−1)/2 masked Hamming comparisons. At n = 40k that is
  8.0×10⁸.
- **After.** LSH with b bands of r rows, tuned so that P(collide) ≥ 0.99 at the
  `FuzzyMinSimilarity` threshold. Candidates ≈ n·b·E[bucket size]. At b = 20 with buckets of
  about 1–5, that is on the order of 10⁶ verifications, a reduction of more than 100×.
- **Exact title and duration, before.** Every author's block is fetched once per book per
  check and compared in both directions:
  - fetches: Σₐ kₐ × 2 block fetches, each of size kₐ;
  - comparisons: 2 × Σₐ kₐ(kₐ−1);
  - normalization: `allNormalizedTitleForms` runs once per pair.
- **After.** Each block is fetched once and every pair is compared once (i<j). That gives
  Σₐ kₐ(kₐ−1)/2 integer prefilters (series number, duration bucket), with Levenshtein
  bounded at k = 3 or 6 (Ukkonen band, O(k·len)) only on survivors. Normalization is O(kₐ),
  memoized.

  One author with k = 3,000 (a junk "Unknown" or franchise row) costs 18M ordered pair
  evaluations per check before and 4.5M cheap prefilters after.
