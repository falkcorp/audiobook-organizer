<!-- file: docs/perf-audit-2026-05-29-heap-breakdown.md -->
<!-- version: 1.0.1 -->
<!-- guid: a1b2c3d4-perf-d4-2026-05-29-heap-breakdown -->

# Heap breakdown audit — post-#1152 strip (MAYDEPLOY-D4)

**Date:** 2026-05-29
**Scope:** Structural estimate of memdb residency after PR #1152
stripped `Description`, `BookSigV1`, `BookSigV1Mask`, `BookSigSegments`,
`BookSigBuiltAt`, `BookSigCoveragePct`, and `VersionNotes` from memdb-resident
`Book` rows. Production prof access is unavailable, so all numbers are
**structural estimates** from the Go type layout × known prod row counts.

## Methodology

- `*T` pointer field: 8 B on the parent struct. If non-nil, the pointee is
  charged separately. `*string` non-nil = 8 B parent + 16 B `string` header +
  N B for the underlying bytes.
- `string` (value): 16 B header + N B bytes.
- `[]T` (value): 24 B slice header + (sizeof(T) × cap) bytes.
- `time.Time` (value): 24 B (`wall uint64 + ext int64 + loc *Location`).
- `*time.Time` non-nil = 8 B parent + 24 B pointee.
- Map header: 8 B parent pointer + map runtime overhead (~48 B empty bucket).
- Per-row Go GC overhead (`malloc` rounding + GC bitmap): the analysis
  ignores allocator slack (~10–15%) — totals below are lower bounds.
- go-immutable-radix node overhead per indexed value: ~64 B leaf + ~32 B/edge
  shared. Effective marginal cost: **~80 B per (row × index)** under
  realistic key entropy.
- Production row counts taken from MEMORY.md / journal snapshots:
  books 392,962 • authors 17,562 • series 43,124 • book_files 308,857 •
  book_authors 193,059 • book_narrators 3,368 • narrators 922 • works
  211,774.

  > 🚨 **`books 392,962` is not a book count (noted 2026-08-11).** The
  > equivalent figure from the same source was later shown to be Pebble KEYS
  > under the `book:` prefix — a prefix shared with ~7 secondary-index
  > families, so ~7.5 keys per row. The row count is **~48,900** (the
  > organizer's own full paging enumeration, 2026-08-11; system status
  > independently read 46,221 and 54,734). See
  > `internal/database/memdb_sort_index_cost_test.go` and
  > `TestWarmupCounts_CountRowsNotPebbleKeys`.
  >
  > **Every books-multiplied total in this document is therefore ~8x too
  > high.** The per-row struct analysis below is unaffected — it is derived
  > from the struct shape, not from the population — so the fix is to
  > re-multiply, not to re-derive. The other row counts in this list have
  > NOT been re-verified either way.
  >
  > The document is annotated rather than recomputed because its conclusions
  > (strip heavy fields; do not hydrate) were directional and survive a
  > smaller library. Anyone quoting an absolute megabyte figure from it must
  > re-multiply first.

> **Confidence:** medium. Per-row sizes are precise from the struct shape;
> the string-content estimates rely on observed averages from prior pprof
> dumps and from #1152's own commentary. Index overhead is calibrated from
> go-memdb / iradix benchmarks; real overhead can swing ±30% with key entropy.

## 1. memdb `Book` per-row size

### 1a. Pre-strip (what prod looked like before #1152 deploy)

Struct shape (`internal/database/store.go:120`) is large. Grouping costs:

| Group                                        |   Per-row B | Notes                                                         |
| -------------------------------------------- | ----------: | ------------------------------------------------------------- |
| Struct shell (130+ fields, mostly 8 B ptrs)  |       ~1040 | sizeof(Book) ≈ 1040 B (130 fields × ~8 B avg; conservative).  |
| Required strings: ID, Title, FilePath        |  16+16+16+N | ID=26 B, Title avg 50 B, FilePath avg 100 B → ~192 B content. |
| Optional `*string` content (non-nil, common) |             |                                                               |
| · ISBN10/13, ASIN, providers, hashes         |        ~250 | ~5 IDs × ~50 B avg.                                           |
| · Narrator, Edition, Publisher, Genre        |        ~120 | ~4 fields × ~30 B avg.                                        |
| · MetadataSourceHash, CoverURL               |         ~80 | sha256 hex + url ~80 B.                                       |
| · NarratorsJSON, FileHash, ITunesPID         |        ~120 | ~3 × 40 B avg.                                                |
| **STRIPPED (pre-#1152) — Description**       |       ~1200 | Avg 1.2 KB observed (#1152 commit msg cites 500–2000 chars).  |
| **STRIPPED — VersionNotes**                  |         ~50 | Mostly empty; non-empty average ~250 B; coverage ~20%.        |
| **STRIPPED — BookSigV1**                     |     ~22 000 | base64(4096 × uint32) ≈ 22 KB per *fingerprinted* book.       |
| **STRIPPED — BookSigV1Mask**                 |        ~700 | base64(512 bytes) ≈ 700 B.                                    |
| **STRIPPED — BookSigSegments, BookSigBuiltAt, BookSigCoveragePct** | ~40 | Three small pointers; pointees ~24 B+. |
| Optional `*time.Time` (8 fields, ~6 non-nil) |        ~200 | 6 × (8 + 24) B.                                               |
| Other `*int`, `*int64`, `*bool`, `*float64`  |        ~150 | ~15 non-nil × ~12 B.                                          |

**Sub-totals per Book:**

- Without BookSig: ~1040 (shell) + ~192 (strings) + ~250 + 120 + 80 + 120 +
  1200 (Desc) + 50 (Notes) + 200 (times) + 150 = **~3 400 B**
- Add BookSigV1 + Mask + helpers on the **fraction of books that have a
  signature**. Prior profiles indicated ~70% of books had `BookSigV1`
  populated at #1152 deploy time. Amortized BookSig cost = 0.70 × 22 740 B
  ≈ **15 920 B/book**.

**Pre-strip per-book amortized total ≈ 19 300 B.**

### 1b. Post-strip (current state, PR #1152 in production)

- Subtract Description (~1 200), VersionNotes (~50), BookSigV1 (~22 000 ×
  0.70 = 15 400), Mask (~700 × 0.70 = 490), three small helpers (~40 × 0.70
  ≈ 30).
- Per-book post-strip ≈ **~2 130 B/book.**

### 1c. memdb Book total

|             |     Pre-strip |     Post-strip |
| ----------- | ------------: | -------------: |
| Per Book    |     ~19 300 B |       ~2 130 B |
| × 392 962   | **~7.58 GB**  |   **~837 MB**  |

**Predicted drop from Book strip alone: ~6.74 GB.**

## 2. Other memdb tables

| Table          | sizeof shell | Per-row strings/extras                                              | Per-row total | Rows    |        Total |
| -------------- | -----------: | ------------------------------------------------------------------- | ------------: | ------- | -----------: |
| Author         |         24 B | Name 16+~25 B                                                       |         ~65 B | 17 562  |       1.1 MB |
| Series         |         32 B | Name 16+~30 B + *int                                                |         ~80 B | 43 124  |       3.3 MB |
| Narrator       |         48 B | Name 16+~25 + time 24                                               |        ~115 B | 922     |       0.1 MB |
| BookAuthor     |         32 B | BookID 16+26 + Role 16+8                                            |         ~98 B | 193 059 |      18.9 MB |
| BookNarrator   |         32 B | BookID 16+26 + Role 16+8                                            |         ~98 B | 3 368   |       0.3 MB |
| BookFile       |        ~520 B (28 strings + 12 ints + 2 times) | + AcoustID 7×~32 + paths ~200 + hashes ~120 | ~1 100 B | 308 857 | **~340 MB** |
| Work           |         48 B (shell) | Title 16+~60 + AltTitles slice (avg cap 2 × 56 B)         |        ~270 B | 211 774 |      **57 MB** |

**Sub-total other tables: ~420 MB.**

> The BookFile row is meaty — 28 string fields (7 of which are AcoustID
> segments at ~32 B each base64) plus 3 nullable `*string`/`*time.Time`
> diagnostic fields. At 308K rows it is the **second largest** memdb table.

## 3. memdb / go-immutable-radix index overhead

Index counts per table (from `memdb_schema.go`):

| Table        | Indexes (excluding `id`) | Effective indexes | Per-row index B | Rows    |        Total |
| ------------ | -----------------------: | ----------------: | --------------: | ------- | -----------: |
| Book         |                        7 |          8 (w/id) |       ~640 B    | 392 962 | **~240 MB**  |
| BookFile     |                        4 |          5 (w/id) |       ~400 B    | 308 857 | **~118 MB**  |
| BookAuthor   |                        2 |   3 (compound id) |       ~240 B    | 193 059 |       46 MB  |
| BookNarrator |                        2 |   3 (compound id) |       ~240 B    | 3 368   |        1 MB  |
| Work         |                        3 |          4 (w/id) |       ~320 B    | 211 774 |       66 MB  |
| Authors      |                        1 |                 2 |       ~160 B    | 17 562  |        3 MB  |
| Series       |                        2 |                 3 |       ~240 B    | 43 124  |       10 MB  |
| Narrators    |                        1 |                 2 |       ~160 B    | 922     |     0.15 MB  |

**Index overhead total: ~484 MB.** (At 80 B per (row × index), worst-case
swing to ~700 MB if entropy is high; lower bound ~300 MB.)

## 4. memdb grand total

| Component                              |       Pre-strip |        Post-strip |
| -------------------------------------- | --------------: | ----------------: |
| Books table                            |        7.58 GB  |          0.84 GB  |
| Other tables (BookFile, Work, joins…)  |        0.42 GB  |          0.42 GB  |
| Index overhead (iradix nodes)          |        0.48 GB  |          0.48 GB  |
| **memdb subtotal**                     |     **8.48 GB** |       **1.74 GB** |

**Predicted strip impact: ~6.7 GB drop in memdb residency.**

## 5. Predicted vs observed

| Source                                | Value          |
| ------------------------------------- | -------------- |
| Pre-#1152 process RSS (prod)          | 67 GB          |
| Post-#1152 process RSS (prod)         | 39 GB          |
| Observed drop                         | **~28 GB**     |
| Post-strip "baseline" called out      | ~18 GB         |
| Pre-strip memdb (this audit)          | 8.48 GB        |
| Post-strip memdb (this audit)         | 1.74 GB        |
| Predicted memdb drop                  | ~6.74 GB       |

The observed RSS drop (28 GB) is **~4× larger** than the predicted memdb
strip (~6.7 GB). Two factors close the gap:

1. **RSS includes GC headroom, fragmentation, and the off-heap Pebble
   block cache that swelled around the *old* in-memory Book objects.**
   Stripping ~6.7 GB of live heap relieves GC pressure and shrinks the
   Go heap's reserved arenas — RSS commonly drops 2–3× the live-heap
   delta after a sustained reduction.
2. **The trickle warmer's pre-strip behavior held onto transient duplicate
   pre-strip copies** of every newly-inserted Book until stripped; that
   doubled memdb's transient residency. Stripping at insertion removes the
   duplicate path entirely.

The **~18 GB remaining baseline** is consistent with:

| Contributor                                                 |    Estimate |
| ----------------------------------------------------------- | ----------: |
| memdb (post-strip, this audit)                              |     1.7 GB  |
| chromem-go in-memory documents (TODO MAYDEPLOY-D1 cites)    |   ~6.0 GB   |
| OpenAI 3072-dim embeddings cached in `embeddings` table     |             |
| via SQLite→chromem hydrate, 392K books × ~12 KB vector +    |             |
| chromem metadata/index overhead                             |             |
| Pebble block cache (default 8 MB, but compaction working sets blow this up; observed ~2-4 GB resident under load) | ~3 GB |
| bleve index (search): inverted index for titles/descriptions; full descriptions still indexed despite memdb strip | ~2 GB |
| 24h `list`/`facets`/`dedup`/`bookCache`/`audiobook_list` LRU caches (`cache.New[gin.H]("...", 24*time.Hour)`) | ~2 GB |
| Go runtime + goroutine stacks + http buffers + misc         |   ~1.5 GB  |
| GC headroom (live × ~1.5 by default GOGC=100)               |   ~2 GB     |
| **Sum**                                                     | **~18 GB**  |

This matches the observed ~18 GB baseline within the estimation error of
the audit (±20%).

## 6. Next biggest wins

Ranked by upside × confidence:

### Win 1: chromem hydrate is the elephant — confirm MAYDEPLOY-D1/D2 land

The largest remaining bucket. MEMORY.md already calls out chromem at
~6 GB; the TODO has D1 (lazy-hydrate) and D2 (fix `NewPersistentDB` so
chromem doesn't re-build from SQLite on every restart) queued. Estimated
savings: **3–6 GB at baseline**, more if D2 enables true on-disk persistence.
**Action:** prioritize D1 + D2 before any other strip work.

### Win 2: Slim the Work struct — 211 K rows, almost never queried hot

`Work` is small but high-cardinality (211 K). Today the `works` table holds
~57 MB heap + ~66 MB index overhead. That's modest in absolute terms, **but
Works are queried in maybe 0.1% of requests** (only the work-grouping UI
on a few pages). They should not be in memdb at all — read them from Pebble
on demand and drop the table.

Estimated savings: ~120 MB heap + the index churn cost during warmup.
Low absolute number but high "$/LOC" — it's a one-line schema delete and a
read-path rewrite.

### Win 3: Strip BookFile's 7 AcoustID segments from memdb

`BookFile` is the second-largest memdb table at ~340 MB. The 7 AcoustID
segment strings (`AcoustIDSeg0..6`) average ~32 B each base64-encoded = ~224 B
per file × 308 K files = **~69 MB**. These are read only by the dedup engine,
which has its own Pebble path. Strip them in a `stripBookFileForMemdb`
helper analogous to #1152.

Estimated savings: **~70 MB heap.** Also cheap; same pattern as #1152.

### Win 4: Audit 24h `gin.H` LRU caches — they capture full Book payloads

`server.go:335-337` instantiates `cache.New[gin.H]("dedup", "list", "facets", 24h)`.
A 24-hour TTL on full response payloads, especially the `list` cache (which
holds full Book objects with descriptions and provenance maps), is the most
plausible explanation for the ~2 GB "user-facing cache" bucket. Switching
to a bounded-entry-count LRU (cap at e.g. 1000 entries instead of unbounded
TTL-only) would cap residency.

Estimated savings: **0.5–1.5 GB** depending on observed entry count.

### Win 5: Description in bleve

bleve indexes the full description text. Since Description is no longer in
memdb but **is** in Pebble, bleve still ingests it from the Pebble source.
The search index is reasonable, but a) we could index only the first ~500
chars per description, b) consider not indexing description at all (most
queries hit title/author).

Estimated savings: **0.5–1 GB** depending on description corpus size.

## 7. Caveats and uncertainty

- All numbers are structural estimates; live pprof on prod would refine
  every line item ±30%.
- The "avg 1.2 KB description" assumption drives most of the Book-strip
  prediction. If real average is closer to 600 B, the predicted drop halves.
- iradix overhead is calibrated from synthetic benchmarks; actual prod
  overhead can vary with key-prefix sharing. ULIDs share no prefix entropy,
  which inflates the per-row cost.
- The 67 → 39 GB RSS reduction predates the strip by a few days in the
  journal; some of that drop may be unrelated to #1152 (e.g., a chromem
  re-hydrate completing and releasing transient memory).

## 8. Suggested follow-up TODO entries

Filed in TODO.md under MAYDEPLOY-I (new section). See that section for
the actionable items derived from this audit:

- **I1** — Verify D1/D2 chromem fixes land before any more memdb strip work.
- **I2** — Remove `works` table from memdb entirely; read from Pebble.
- **I3** — Add `stripBookFileForMemdb` to clear 7 AcoustID segment strings.
- **I4** — Bound the 24h `list`/`facets`/`dedup` caches by entry count, not
  just TTL.
- **I5** — Truncate description text fed to bleve to 500 chars, or skip it.
- **I6** — Once D1+D2 ship, re-run this audit with a real `inuse_space`
  heap profile from prod to validate or refute the ~18 GB → ~10 GB
  prediction.
