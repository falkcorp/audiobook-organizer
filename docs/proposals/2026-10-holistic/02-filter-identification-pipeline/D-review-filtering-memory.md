<!-- file: docs/proposals/2026-10-holistic/02-filter-identification-pipeline/D-review-filtering-memory.md -->
<!-- version: 1.1.0 -->
<!-- guid: 2e7b4c19-8d5a-4f63-a1e0-6c9d3b7f2a58 -->
<!-- last-edited: 2026-10-09 -->

# D36: Review filtering, browser memory and server-side queries

Analyst: `search`. This follows up on 02 Q7 and decision D36. Code was read on `main` at
`5129e40e5`, read-only. All measurements use synthetic data on an M1 Max. No production
calls were made.

### Round-2 review (r2)

- Accepted by the owner as **D36**; R0–R4 are now PRs 15–19 in the main doc §4 Phase 2b,
  which carries the full file lists, tests, rollback and sizes. This appendix keeps the
  measurements.
- The incremental snapshot and bounded overlay the owner approved on 2026-10-02 **shipped**
  before this review (`d9463d669`, `df23cbe95`; `metadata_cache_snapshot.go` v2.1.0). §2
  below said "rebuilt incrementally" and that is right at HEAD; the dated memory note
  calling it unbuilt is stale. R2 is therefore not blocked on a snapshot PR.
- R0's measurement procedure and its record location are spelled out in PR 15; heap
  snapshot files are never committed (they hold library titles).
- R2's page response carries `generation`, and the result LRU is keyed by both the cache
  and the book generation, so an `ids=all` list can never be applied against newer data
  than the page it was counted on.
- R3's flag is `review_metadata_server_query` in `config.go`, read through `api.getConfig()`,
  the same pattern as `review_apply_enabled`.
- Under server paging, every `refresh()` after an apply batch (seven call sites) becomes a
  page-plus-facets refetch of about 130 KB, not a reload of the index.

**The owner's question:** do not hold the whole review set in the browser, and use the
fast in-memory store on the server.

**Answer:**
- **Move the Metadata lane to server-side filter, count and page queries over the review
  snapshot the server already keeps in RAM.** The browser then holds only the visible page,
  plus IDs when the owner clicks "select all N".
- No new index is needed for this lane. A plain scan of the snapshot is measured at 0.2–9 ms
  per query at 40k rows.

## 1. What the browser holds today, per lane

| Lane | How it loads | What stays in the browser |
|---|---|---|
| **Metadata** | `getCachedReviewResults(0, 0, true, 'reviewable', {view:'index'})` loads **every reviewable row** (`web/src/components/review/lanes/useMetadataLane.ts:973`). The unreviewable bucket is loaded whole when its chip is chosen (`:1156`). Full detail rows are loaded per visible page (`:1420`). | The whole set: R rows × ~3.4 KB of heap each |
| Dupes | Server-paged, `limit: pageSize` (default 50) (`useDupesLane.ts:364-365`). Counts come from the server (`countBulkDedupCandidates`, `:691`). | One page |
| Regroup | At most `REGROUP_FETCH_LIMIT = 500` items (`useRegroupLane.ts:104`) | At most 500 rows |
| Repairs | Server-paged (`useRepairsLane.ts:522-525`). Select-all walks pages of 500 and keeps **IDs only** (`collectApplicableIds`, `:651-667`). | One page, plus an ID list |

**Only the Metadata lane holds the whole set.** The other three lanes already follow the
pattern proposed here.

**Bytes per Metadata index row.** The index view removes only `Description`
(`internal/server/handlers/metadata_cache.go:773-775`). Each row still carries:
- the book info (path, cover URL, runtime fields);
- one full candidate, **including `score_breakdown`**: about 8 steps, each with a label and
  an explanation sentence (`internal/metafetch/score_breakdown.go`);
- a 64-character hash, timestamps and flags.

The breakdown is read only by the evidence panel, which shows rows that are already
re-fetched in full for the visible page.

**Measured** with `node --expose-gc` on synthetic rows shaped like `CandidateResult`, with
typical field lengths. The script is `scratchpad/d36/heap.js`.

| Rows (R) | JSON transferred | JS heap, parsed | Heap with the 6 derived filter arrays and the row-state Map |
|---|---|---|---|
| 16,000 | 41 MB | 54 MB | 56 MB |
| 28,000 (≈ applied + fetched-unapproved, census 10-06) | 72 MB | 95 MB | 98 MB |
| 40,000 | 103 MB | 136 MB | 140 MB |
| 40,000 **without `score_breakdown`** | 47 MB | 52 MB | 56 MB |
| 40,000 **IDs only** (for select-all) | 1.2 MB | — | — |

**Copies the page keeps:**
- **One deep copy:** the parsed `results` array.
- **Arrays of references** built by the `useMemo` chain (`beforeRuntime`, `preGroupFiltered`,
  `chipBaseRows`, `chipRows`, `filteredResults`, `pageIndexRows`), plus the `rowStates` Map.
  These cost only about 8 bytes per row each, about 4 MB in total at 40k rows.
- **A transient copy during load:** the response text (72–103 MB) is alive while it is
  parsed, so the peak is about the JSON size plus the heap size, **≈ 170–240 MB**.

  The lane re-fetches the index from 7 refresh call sites (`grep -c "refresh()\|setRefreshKey"`
  gives 7). Each re-fetch repeats that peak, and the old array lives until the next garbage
  collection.

**This does not explain a 16 GB tab.** The Metadata lane accounts for about 0.1–0.25 GB.
A tab that large points to retention across refreshes, meaning old `results` arrays kept
alive by closures, or to another page. That is unverified, so the first PR takes a Chrome
heap snapshot rather than guessing.

## 2. What a server-side query costs

**The data is already in server RAM.** `reviewSnapshot` (`internal/server/handlers/metadata_cache_snapshot.go:95`)
keeps every cache row with its decoded first candidate and hash, plus a book map. It is
rebuilt incrementally from the cache and library change logs. Today every request
JSON-encodes the whole snapshot. The slow-requests plan (`docs/plans/2026-10-06-slow-page-requests.md`)
measured **2.6–4.2 s per warm call** and 64–125 s cold.

**Filter, count and sort over an in-RAM slice.** This is a standalone Go program over
synthetic rows (`scratchpad/d36/gobench/main.go`), timing filter, count and sort together,
averaged over 50 runs:

| Query | 16k rows | 40k rows | 100k rows |
|---|---|---|---|
| Chips only (source, status, stale, runtime) | 0.07 ms | 0.18 ms | 0.53 ms |
| Chips + substring title | 0.35 ms | 0.89 ms | 2.2 ms |
| Chips + RE2 title regex | 0.79 ms | 1.94 ms | 4.8 ms |
| RE2 matching every row, then sorting all of them (worst case) | 3.6 ms | 9.1 ms | 23 ms |

**Why the Library's 6.2 ms and 21.8 ms per 5k books are slower.** Those Library figures
(appendix B of the main doc) are not a property of memdb itself. They come from the
general-purpose walk around it:
- go-memdb radix iteration, plus a predicate closure for each row;
- projecting each row to `BookSummary` and then to `Book` (904 bytes);
- counting tags;
- 3–4 separate walks for each filter change.

A tight scan over a compact slice is 20–100× faster at the same row count. So "fast like
Redis" holds when the hot path reads a compact in-memory structure directly. The Review
snapshot already is one.

**Indexes:**
- **Bitmaps or posting lists per chip value.** A chip-only query would drop from about
  0.18 ms to about 0.01 ms at 40k rows. That is not worth the write-path complexity for this
  lane. They still matter for the **Library** at about 100k rows with 3–4 walks per change;
  see PR 4/PR 7 territory in the main doc, §3.5.
- **A trigram index for RE2.** Regex costs about 2–5 ms at 40–100k titles. A codesearch-style
  trigram prefilter pays off at around 10⁶ documents or with long fields. **It is not
  justified here.**

**Caching.** No new cache is needed. The snapshot is the cache: it has generations and
incremental rebuilds.

- Add a small LRU of the last ~32 result ID lists, keyed by (cache generation, book
  generation, normalized filter, sort).
- Paging, the count and "select all N" then reuse one evaluation.
- Any write moves the generation, so a cached list never outlives the data.

**Latency per keystroke:**

| | Today (client) | Proposed (server) |
|---|---|---|
| Filter work | ~1–5 ms in JS (measured 0.96 ms for a four-filter chain over 40k rows, `scratchpad/d36/jsfilter.js`) | 0.2–9 ms in Go |
| Network | none | one round trip (a few ms on the LAN, typically 50–150 ms through the remote proxy) |
| Page payload | none | 50 rows ≈ 130 KB |
| Debounce | none | 150 ms |

The server version is slightly slower per keystroke, and still well inside interactive
budgets. The real gains:
- **First paint:** 2.6–4.2 s warm and 41–103 MB transferred today, against about 20 ms and
  about 130 KB.
- **Browser memory:** about 100–250 MB peak today, against under 2 MB.

## 3. Recommendation

**Go server-side, with the browser holding the page plus IDs.** Keeping filtering in the
browser over shared data is the fallback only. The per-keystroke cost is about equal, and
the server wins on memory, first load and refresh churn.

**What moves to the server.** These are computed in the same single pass, from the existing
snapshot plus the overlay:
- the title grammar, using `querygrammar.CompileText`, the same engine the Library uses (this
  also retires the RE2-to-JS translation drift in finding F9);
- the source, status, stale, deferred, runtime and transcription chips;
- hide applied, rejected and skipped. These are already persisted by the actions, so the
  overlay sees them;
- the chip counts (facets) and the multi-book grouping key.

| | Before (40k rows) | After |
|---|---|---|
| Browser memory, steady | ~140 MB | under 2 MB (page plus counts) |
| Browser memory, peak | ~240 MB | under 2 MB |
| With select-all | (same as above) | +1.2 MB of IDs |
| First paint | 2.6–4.2 s warm, 103 MB transferred | ~10–30 ms server work plus one round trip, ~130 KB |
| Keystroke | ~1–5 ms, local | 150 ms debounce + 0.2–9 ms server + one round trip |

### PRs

**PR R0 (S) = main doc PR 15: measure first.** Take Chrome heap snapshots of the Review tab
at idle, after one refresh, and after 10 applies. Record retained `results` arrays. The exact
measurements and the audit-note location are in PR 15.
- Files: none in the repo. Results go in a note under `docs/audits/`.
- Rollback: n/a.

**PR R1 (S) = PR 16: slim the index view now.**
- Files: `internal/server/handlers/metadata_cache.go` (in the `indexView` branch, also clear
  `ScoreBreakdown`; and `CategoryTags` if the rail does not read it);
  `internal/server/handlers/metadata_cache_test.go` (assert the index rows carry no
  breakdown); `web/src/components/review/lanes/useMetadataLane.ts` (confirm the evidence
  panel reads breakdowns only from `pageResults`).
- Effect: about 2.2× less transferred and held (measured 103 MB → 47 MB of JSON at 40k rows).
- Rollback: revert.

**PR R2 (M) = PR 17: server-side review query.**
- Files:
  - new `internal/server/handlers/metadata_cache_query.go` and `metadata_cache_query_test.go`:
    parse the filters (reusing `internal/querygrammar`), one pass over `snapshot.rows` with
    the overlay, sort, slice the page, compute facet counts, and an `ids=all` mode that
    returns `{ids, total, generation}` only;
  - `internal/server/handlers/metadata_cache_snapshot.go`: precompute a lowered title on
    `snapshotRow` at build time, and add the result-list LRU keyed by snapshot generation;
  - `internal/server/handlers/metadata_cache.go`: route `view=page` to the new code;
  - `internal/server/wire_library_routes.go`: only if the work gets its own path, otherwise
    nothing.
- Tests: page, count and facets agree with the current client derivation on fixtures. The
  title-grammar conformance corpus (main doc PR 3) runs against this path.
- Rollback: the old `view=index` stays served.

**PR R3 (M) = PR 18: switch the Metadata lane to page mode, behind `review_metadata_server_query`.**
- Files:
  - `web/src/services/api.ts`: `getReviewPage` and `getReviewMatchingIds`;
  - `web/src/components/review/lanes/useMetadataLane.ts`: replace the full-index load and
    the `useMemo` filter chain with server queries, AbortController plus a 150 ms debounce,
    and keep the per-page detail fetch;
  - `web/src/components/review/QueueRail.tsx`: chip counts come from the server facets;
  - `web/src/components/review/ReviewWorkspace.tsx`: "select all N" uses the IDs call;
  - `web/src/components/review/lanes/useMetadataLane.test.ts`.
- Rollback: the flag off falls back to the index mode for one release.

**PR R4 (S) = PR 19: retire the full-index mode** once R3 has run for one release.
- Files: `useMetadataLane.ts`; the `indexView` path in `metadata_cache.go`.

**Fallback, if the owner prefers to keep the browser approach for now:** ship R0 and R1
only. That is about 56 MB held at 40k rows, with the shared conformance corpus. Revisit when
the reviewable set passes about 40k rows.
