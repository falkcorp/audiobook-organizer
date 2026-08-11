- [ ] **ROWCOUNT-REVERIFY** Re-measure every production table row count once the
      row/key-separated warmup counter is deployed, and correct what it moves.

      **Why this is open rather than done.** The `books` figure that appears
      throughout this repo (392,962 → 366,922 → 366,916, depending on vintage)
      was never a book count. It was the number of Pebble KEYS under the
      `book:` prefix, which is shared with roughly seven secondary-index
      families — `book:path:`, `book:hash:`, `book:originalhash:`,
      `book:organizedhash:`, `book:versiongroup:`, `book:work:`,
      `book:asin:`/`book:isbn13:` — at about 7.5 keys per row. The warmup now
      reports rows and keys separately, pinned by
      `TestWarmupCounts_CountRowsNotPebbleKeys`, but production has not yet
      run the fixed counter.

      **Best current number: ~48,900 books.** From the organizer's own full
      paging enumeration on 2026-08-11 (`Fetched 48896 total books from
      database`), corroborated by system-status readings of 46,221 and 54,734.
      This is the strongest available evidence, not a verified count.

      **What has already been corrected** (2026-08-11): the inflated figure was
      removed or annotated in `memdb_sort_index_cost_test.go`, `config.go`,
      `memdb_sort_indexers.go`, `pebble_store_versiongroup_backfill.go`,
      `library_list_warmer.go`, `bleve_translator.go`, `dedup/lifecycle.go`,
      `web/src/pages/Library.tsx`, `docs/design/2026-08-09-search-backend-options.md`
      and `docs/perf-audit-2026-05-29-heap-breakdown.md`.

      **The one that changed an answer, and needs an owner decision:** the sort
      index memory cost. The per-book measurement (+3,750 B/book for all nine
      indexes, measured at 100,000 books) was always correct; only the
      population it was multiplied by was wrong. Re-extrapolated:

      | | old (366,916) | corrected (~48,900) |
      |---|---|---|
      | all nine sort keys | +1,312 MB | **~+175 MB** |
      | per sort key | ~146 MB | **~19 MB** |

      "+1.3 GB on a box already at 1.25 GB resident" reads as prohibitive.
      "+175 MB" does not. The sort indexes shipped default-off on the strength
      of the larger number; whether to enable some or all of them is worth
      re-deciding on the corrected one.

      **Still to do:**
      1. After the next deploy, capture the warmup line and record rows AND
         keys for every table, not just books.
      2. Re-verify `book_files`, `works`, `book_authors`, `series`, `authors`.
         These came from the same counter, but each prefix has its own index
         families (or none), so they may or may not be inflated. **Do not
         assume they are wrong and do not assume they are right** — that
         assume-by-analogy step is what produced the original error.
      3. Re-multiply the absolute totals in
         `docs/perf-audit-2026-05-29-heap-breakdown.md`. Its per-row struct
         analysis is derived from struct shape and is unaffected.

      **The recurrence to avoid.** This number survived three separate
      opportunities to catch it: the design doc noticed the 392K-vs-44,888
      discrepancy and resolved it in the wrong direction by inventing
      "non-primary versions" to explain the gap; a later pass "corrected" 392K
      to 366,916, propagating the error while appearing to fix it; and a
      backfill audit reached "13.3% under-scan" by dividing a real scan count
      by the inflated one. Two numerators agreeing tells you nothing about the
      denominator they share.
