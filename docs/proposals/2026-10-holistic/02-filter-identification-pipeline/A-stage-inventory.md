<!-- file: docs/proposals/2026-10-holistic/02-filter-identification-pipeline/A-stage-inventory.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3b8e1f52-6c0a-4d97-a2f4-9e7d1c5b8a30 -->
<!-- last-edited: 2026-10-08 -->

# Appendix A: stage inventory at HEAD `f7211eb39`

Each row gives the stage's algorithm, its complexity, how often it runs, and how it caches.
Here N is the number of books (about 100k), P the number of primary books (about 40.6k), F
the number of `book_file` rows (about 742k), and kₐ the number of books by author a. The
population figures are derived from dated notes, not measured at HEAD.

## Filtering and search

| Stage | Where | Algorithm | Complexity | Frequency | Cache |
|---|---|---|---|---|---|
| Grammar compile | `internal/querygrammar/querygrammar.go`; `internal/audiobooks/filter_compiled.go:76` | RE2, glob and numeric compile, once per request | O(\|query\|) | Per request | None needed |
| Library page, filter only | `internal/audiobooks/service_query.go:358-530` → `internal/database/memdb_summaries.go:84` | memdb iteration on the `is_primary_version` or sort index, with an in-loop predicate closure. Stops at offset+limit matches. | O(P) worst case; O(offset+limit) when the filter is broad | Each page, after a 300 ms debounce (`web/src/pages/Library.tsx:606`) | `listCache` only for queries with no heavy filter |
| Library count | `service_query.go:897` → `memdb_summaries.go:406` | Full walk with the same predicate | O(P) | Each list request with filters (`internal/server/audiobooks_helpers.go:117-127`) | None |
| Scoped tag facets | `internal/audiobooks/service_tag_facets.go:74-140` | `MatchingBookIDs` (a full walk building a `[]Book`), then tag counts | O(P) + O(matches × tags) | Each filter change | `tagFacets`, keyed by change-log generation, plus singleflight |
| Select all N | `MatchingBookIDs`, or `resolveFilterToBookIDs` (`internal/server/metadata_ops.go:521`) | Same full walk | O(P) | On click | Search result cache only when there is free text |
| Free text | `searchWithBleve` (Bleve scorch, BM25-style tf-idf); `searchPostFilterWindow` = 10,000 over-fetch when post-filters apply | Inverted index | O(postings) + O(window) post-filter | Each page | `searchcache` (128 MiB, incremental patch from the change log) |
| Stripped-field filter | `internal/audiobooks/service_filtering.go:314` | One Pebble `GetBookByID` for each row that survives the cheap filters | O(survivors) point reads | Each walk | None |
| Per-user filter | predicate in `service_filtering.go` (~:1066) | One `GetUserBookState` per row | O(P) point reads | Each walk | None |
| Review lane filters | `web/src/components/review/lanes/useMetadataLane.ts:960-1270` | Client-side array filters over the full index snapshot | O(R), with R ≈ 16k reviewable rows | Every keystroke (no debounce) | The server review snapshot (`reviewSnapshot`) |

## Identification

| Stage | Where | Algorithm | Complexity per book | Frequency | Cache |
|---|---|---|---|---|---|
| Tags | `internal/metadata` `ExtractMetadata` (in the scan) | TagLib/ffprobe header | O(files) | Scan | `ScanState` |
| Path parse | `internal/scanner` `extractInfoFromPath`; `foldernames` | Regex and heuristics | O(1) | Scan; reparse fixer | None |
| AI parse | `library.ai-parse` | LLM, fills empty fields only; gives up after N failures | 1 LLM call | Manual or scheduled | Give-up marker |
| Question | `metafetch.resolveSearchInputs` (`service_search.go:436`), `metabatch.ResolveCandidateSearchQueryMemo` (`search_query.go:153`), `buildQueryVariants` | Heuristic parse; up to `maxQueryVariants = 4` | O(1) plus reads from the folder memo | Each search | Folder memo per run |
| Chain walk | `metafetch.WalkSourceChain` (`source_chain_walk.go:192`) | The first source in priority order that returns anything wins; no scoring | ≤ #sources calls | `library.bulk-metadata-fetch`, maintenance bulk job | `metadata_fetch_cache:<book>:<provider>` (non-empty only) |
| Fan-out search | `searchMetadataForBook` (`service_search.go:1107`), `runSearchFanout` (`search_fanout.go:174`) | Variants × sources, in rounds; stops at the first "strong" match | ≤ 4 Audible + ≤ 2 Open Library + Google + an Audnexus ASIN lookup across ≤ 3+ regions | `metadata.candidate-fetch` (every 6 h, `internal/scheduler/tasks.go:717`), interactive, upgrade | `…:<provider>#q<sha8>` per book (non-empty only); a pooled per-provider row |
| ASIN backfill | `internal/plugins/metafetch/asin_backfill.go:809-827` | Audible `SearchIdentities` by ISBN or title+author, then a strict single-hit gate | 1–3 Audible calls | Every 6 h (`asinBackfillInterval`) | **None** (retry-after marker only) |
| Base score | `ScoreBaseCandidates` (`service_scoring.go:795`) | Token F1, or an embedding tier; the LLM rerank of the top-K is optional (`:908`) | O(candidates × words); 1 LLM call when the top is ambiguous | Each search | None |
| Adjustments | `service_search.go` (author, narrator, series, ASIN ×2.0, has-narrator, runtime tier) | Product of multipliers, unclamped | O(candidates) | Each search | None |
| Gate | `internal/applygate` | Score ≥ 0.90 (unbounded scale), identity, sequence, evidence rules, review-only source, manual-only | O(candidates) | Bulk apply, upgrade | None |
| Candidate store | `metadata_cache:<bookID>` (`internal/database/pebble_store_metadata_cache.go`) | Scored list plus `LastEmptyFetchAt` | — | Written by each fetch | **Deleted** on a title or author-name change (`internal/database/pebble_store.go:3392`) |
| Local catalog | `internal/catalog`, `catalog.harvest-authors` | Per-author Audible listing; `EntryIDsByAuthorName` exact key, else a substring scan over every author key | O(k) exact; O(#author keys) substring | Interactive "Search again" only | `cat_*` keys |

## Dedup and fingerprint signals

| Stage | Where | Algorithm | Complexity | Notes |
|---|---|---|---|---|
| Exact title | `internal/dedup/engine.go:1661` | Author block, every pair, min Levenshtein over title forms | 2 × Σₐ kₐ² × forms² per full scan | Skips books with a nil `AuthorID` |
| Duration match | `engine.go:1794` | Author block, every pair, ±2 % runtime, Levenshtein ≤ 6 | Same as exact title | Same skip |
| Embedding | `collectors_embedding.go`, HNSW (`internal/server/registry_wire.go:41-71`) | ANN top-K | O(log n) per query | chromem is brute force if selected |
| Meta-fuzzy | `collectors_metadata.go` | Levenshtein over embedding top-K + LSH candidates | O(K) | Already blocked |
| Head-print LSH | `pebble_store_lsh.go`, `collectors_acoustid.go` | Banded LSH, then Hamming | Sub-linear | Prints cover only the first 120 s |
| Book signature | `engine.go:5012` | Every pair of signed books | O(n²/2) | Signatures stay garbage until the re-fingerprint (dated note) |
| Window prints | `internal/database/fingerprint_window.go` (`fpwin:`), `internal/fingerprint/window_similarity.go:86` | `WindowSetSimilarity` exists | — | **No production caller** |
| Chapters | `collectors_chapters.go:65-126` | Per-chapter boundary match | O(candidates × chapters) | Scored now (`SigChapterStructure`) |
| Compose | `internal/dedup/unified/compose.go` | Noisy-OR over primary signals, plus supporting boosts | O(signals) | Assumes independence |
