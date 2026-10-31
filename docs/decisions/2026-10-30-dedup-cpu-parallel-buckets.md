<!-- file: docs/decisions/2026-10-30-dedup-cpu-parallel-buckets.md -->
<!-- version: 1.0.1 -->
<!-- guid: 6b0e2d54-6f3a-4c1e-9d7b-2a8f5c1e3b97 -->
<!-- last-edited: 2026-10-30 -->

# Dedup CPU: parallel buckets, score once, allocation-free fuzzy distances

TODO entry STARTUP-GC-DEDUP-CPU (06-P13 follow-up). Three parts, all done in one change.

## Decision log

### 1. Where the parallelism goes

- Chosen: parallelise the loop in `bucketMetadataDuplicates` (internal/database/pebble_store.go), not the pair loop inside `groupMetadataBucket`. A bucket is a disjoint partition of the entries (the bucket key is part of each entry), so each worker reads only its own bucket and writes only its own slot of a `[][][]BookCore`. There is no shared mutable state, hence no mutex. The brief's "dedup-key maps, counters, Label closure" do not exist on this path: no `registry.RunItems` is involved.
- Chosen: `errgroup.Group` with `SetLimit(metadataBucketWorkers)`, `metadataBucketWorkers = runtime.NumCPU()`. It is a package variable only so a test can pin 1 against N, following `scanWorkerCount` in internal/dedup/author.go.
- Chosen: a serial pre-pass picks the eligible buckets (2..200 members) and logs the oversize ones, so the `slog.Warn` order and count are unchanged and no goroutine logs.
- Chosen: results are merged serially in first-seen bucket order, so the output equals the serial output by construction for any worker count. Rejected: collecting in completion order and sorting afterwards (needs a sort key the serial code never had, and is easy to get subtly different).
- Rejected: parallelising inside one bucket. A bucket is at most 200 members (19,900 pairs); the union-find is order-dependent shared state and would need locking for little gain. The cap bounds the per-bucket cost and the pool spreads the buckets.
- Both backends get it: `bucketMetadataDuplicates` is shared by `PebbleStore.GetDuplicateBooksByMetadataCore`'s scan fallback and the MemStore twin (memdb_reads.go).

### 2. Score each pair once from precomputed forms

- Chosen: the scorer lives in internal/matcher (`Prepare`, `Prepared`, `SymmetricScore`, `Scratch`), because the old doc comment on `metadataTitleSimilarity` cites a spec rule against a second metric. It is not a second metric: `SymmetricScore(a, b)` equals `max(ScoreMatch(a, b), ScoreMatch(b, a))` exactly, which is what the old `metadataTitleSimilarity` computed.
- What is computed once: normalization, lower-cased rune decoding, word splitting and the sorted unique token list, in `Prepare`, once per title per bucket. What is computed once per pair instead of twice: the whole-string edit distance and the token-set ratio (both symmetric). Directional terms (prefix, substring, word-start, per-word Levenshtein) are computed for both directions.
- Chosen: skip a term whose ceiling cannot beat the score already reached (per-word and token-set terms cap at 70, the whole-string term at 50). This cannot change a max.
- Chosen: `groupMetadataBucket` skips scoring a pair whose members already share a union-find component. Unioning them again is a no-op, so the components are unchanged. This is outside the three literal parts of the TODO entry but it is the same loop and removes comparisons outright; the property test compares against the previous grouping.
- Chosen: `Prepare` runs per bucket inside `groupMetadataBucket`, not in `newMetadataDupEntry`. Each entry is in exactly one bucket, so it is still once per item, and the (large) majority of singleton buckets never pay for it or hold the memory.
- Chosen: delete `matcher.ScoreMatch`, `TokenSetRatio` and `tokenSimilarity`. Their only production caller was the dedup path this change replaces, and `make lint-deadcode-ratchet` fails on a new unreachable function ("delete the dead code"). The old implementations live on as the oracle in `internal/matcher/fuzzy_equivalence_test.go`. Rejected: keeping them and adding them to scripts/deadcode-baseline.txt (forbidden by hand), or keeping test-only copies named `ScoreMatch` so the old tests compile unchanged (they would then test test code). The existing `fuzzy_test.go` cases now run through `SymmetricScore`; its golden values were directional floors, and the symmetric score is at least as large, so they hold as written.
- Behaviour note: nothing outside the dedup path called `ScoreMatch` (grepped the whole tree), so no caller changes meaning.

### 3. Allocation-free Levenshtein and Jaro-Winkler

- Chosen: a `sync.Pool` of pointer-typed scratch structs (rune buffers plus DP rows / match bitmaps) behind the existing string entry points, and a caller-owned scratch variant for loops (`levenshteinRunes`, `jwScratch.similarity`, and `matcher.Scratch` for the scorer). The phase-3 author scan workers each hold one `jwScratch` for the life of the worker. Rejected: threading a scratch through every public signature (churn across callers for no gain; the pool is per-P and costs a few ns).
- Chosen: decode with `for _, r := range s` and fold with `unicode.ToLower` per rune, instead of `strings.ToLower` plus `[]rune(s)`. `strings.ToLower` is `strings.Map(unicode.ToLower, s)` for non-ASCII and agrees rune for rune on ASCII; ranging over invalid bytes yields U+FFFD exactly as `[]rune(s)` does. The property tests use an alphabet with mixed case, accents, Greek final sigma, dotted capital I, CJK and raw invalid bytes, and require bit-identical results to the previous implementations (kept in the test files as oracles, not in the package).
- Chosen: the Jaro-Winkler match bitmaps are `clear`ed on every call (a reused `true` from a longer comparison would silently change scores). `TestJaroWinklerScratchIsResetBetweenCalls` and the random test fail if the clear is removed (mutation-checked).
- `sync.Pool` drops a random quarter of Puts under `-race`, so the pooled-entry-point zero-allocation assertions skip under `-race` (`raceEnabled` test constants, same pattern as internal/database); the scratch-based assertions run always.

## Measurements

Apple M1 Max, `GOMAXPROCS=2`, niced, on a machine that was also running CI (load average 15 to 30), so absolute ns/op carry noise of roughly 10 to 40 percent; allocs/op and B/op are exact. Before = origin/main fc3e9f5d3 with only the benchmarks added; after = this change. `-benchtime 200000x -count 3` for the distance functions, `-benchtime 5x -count 3` for the bucket benchmarks (three runs listed as a range).

| benchmark | before | after |
| --- | --- | --- |
| `LevenshteinDistance` (internal/matcher) | 2,504-2,522 ns/op, 536 B/op, 4 allocs/op | 2,347-2,354 ns/op, 0 B/op, 0 allocs/op |
| `LevenshteinDistanceCaseSensitive` | 2,365-2,412 ns/op, 476 B/op, 2 allocs/op | 2,284-2,357 ns/op, 0 B/op, 0 allocs/op |
| `JaroWinklerSimilarity` (internal/dedup) | 205-218 ns/op, 19 B/op, 2 allocs/op | 79.7-81.2 ns/op, 0 B/op, 0 allocs/op |
| `GroupMetadataBucket` (one 200-member bucket, 19,900 pairs) | 536-623 ms/op, 142,084,000 B/op, 1,546,514 allocs/op | 209-212 ms/op, 598,000 B/op, 1,587-1,591 allocs/op |
| `BucketMetadataDuplicates2000` (20 buckets x 100), serial | 2.99-3.19 s/op, 709,747,000 B/op, 7,694,906 allocs/op | `workers=1`: 1.05-1.08 s/op, 10,855,000 B/op, 18,654-18,662 allocs/op |
| same, `workers=NumCPU` (10) | n/a (was serial) | 0.556-0.612 s/op, 10,865,000 B/op, 18.7K allocs/op |

Notes:

- The distance functions' ns/op did not move (the DP dominates); the win there is allocations, which matter because these run inside a process whose GC cost scales with a large live heap (the point of 06-P13). Jaro-Winkler also got faster because it no longer allocates and no longer runs `strings.ToLower` and `[]rune` over both inputs.
- The bucket benchmark improves 2.9x single-core from scoring each pair once on prepared forms with the exact skips, and the allocation count falls by 99.8 percent. The `workers=10` row ran at `GOMAXPROCS=2`, so the 1.9x it shows is the 2-core ceiling, not what a host with more cores will see.
- These are a loaded laptop's numbers, not the production host's. Re-run `go test ./internal/database -run NONE -bench 'GroupMetadataBucket|BucketMetadataDuplicates2000' -benchmem` there if the absolute figure matters.
- With `-benchtime 2x` the benchmarks warm the pool first so a short run still reports 0 allocs/op for the three distance functions.

## Executive-summary check

Checked against docs/process/executive-summaries.md: not qualifying. One performance change with byte-identical output, no data-loss or corruption angle, one tracked TODO item. No executive-summary file added.

## Verification

- internal/matcher: `TestLevenshteinMatchesReference` (30,000 random pairs, both flavours, both argument orders), `TestSymmetricScoreMatchesScoreMatch` (30,000 pairs against a verbatim copy of the old `ScoreMatch`, exact equality, one `Scratch` reused throughout, plus the token-set ratio bit for bit), `TestDistanceFunctionsAllocateNothing`, `TestPreparedIsImmutableAcrossScorers`.
- internal/dedup: `TestJaroWinklerMatchesReference` (40,000 pairs, bit-identical floats, pooled and long-lived scratch), `TestJaroWinklerScratchIsResetBetweenCalls`, `TestJaroWinklerDegenerateInputs`, `TestJaroWinklerAllocatesNothing`.
- internal/database: `TestGroupMetadataBucketMatchesReference` (new grouping vs old grouping across thresholds), `TestBucketMetadataDuplicatesParallelMatchesSerial` (1 vs 2, 4 and 32 workers, repeated, with singleton and over-cap buckets present; mutation-checked by merging in completion order, which fails it), `TestBucketMetadataDuplicatesStillSkipsOversizeBuckets`. All run under `-race`.
