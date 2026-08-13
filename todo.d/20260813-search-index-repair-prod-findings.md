### Search-index coverage repair — production findings (2026-08-13)

The repair from #2381 ran on prod at 19:20 EDT. Measured, not estimated:

```
search index is short of the library; marking books for reconciliation
    indexed=51086  books=67824  shortfall=16738
```

**16,738 books (24.7% of the library) were missing from the search index** and
unfindable by any web search. The drain ran clean — `removed=0 failed=0` on every
batch, ~5,000 per ~2.5 min.

Follow-ups this surfaced:

- [ ] **The store reports 67,824 live books; the API list endpoint reports 63,870.**
      A 3,954-book gap. `ListBookIDs` already excludes `MarkedForDeletion`, so these
      are live rows, and `/api/v1/audiobooks` applies no default filter when
      `library_state` is empty. Paging the endpoint returns exactly 63,870 distinct
      ids, so it is internally consistent — it simply never serves those 3,954.
      **Cause not established.** Worth confirming whether they are genuinely
      unreachable to clients or an artifact of how the total is derived; if the
      former, it is a third invisible-books population, larger than the 765 in
      `20260813-primary-version-census-corrections.md` and unrelated to it.
- [ ] **The coverage gate compares two slightly different populations.**
      `reconcileSearchIndexCoverage` tests `len(ListBookIDs()) <= DocCount()`.
      `ListBookIDs` excludes deletion-marked books; `DocCount` counts whatever Bleve
      holds, which can include docs for books since deleted. If stale docs ever
      accumulate, the comparison can read "not short" while real books are missing —
      the same "one comparison cannot distinguish two states" shape as the bug the
      gate was written to catch. Deletes do flow through `DeleteIndexedBook` from both
      the index worker and the reconciler, so this is currently latent, not active.
      Consider comparing sets rather than counts, or logging both numbers on every
      boot (the `indexed=`/`books=` pair already does this — keep it).
- [ ] **The search index has ZERO metrics.** A live `/metrics` scrape returns 50 metric
      families and **not one** mentions search, bleve, index, or dirty. This is the
      direct reason a quarter of the library was unfindable for an unknown period with
      nobody noticing — there was no signal to notice. `audiobook_organizer_books_total`
      already exists, so **half the comparison is exported already**; adding a
      `search_index_docs_total` gauge (and a dirty-backlog gauge) would have made this
      bug a visible divergence on a graph rather than a user report. Note this also
      re-confirms the earlier finding that `SearchIndexDroppedCount` is not on
      `/metrics` despite a comment saying it is, and extends it: nothing about the index
      is exported at all.
- [ ] **`audiobook_organizer_books_total` reports the PRIMARY count, not the total.**
      It is fed by `CountPrimaryBooks()` (`server_lifecycle.go:393`) while its help text
      reads *"Current total number of books in library"*. Live value **40,841** against
      **67,824** live books in the store — under-reporting the library by ~40%. Either
      rename/reword it or add a true total alongside; any dashboard built on it is
      currently wrong about the library size.
- [ ] **Re-measure the per-cohort coverage now that a true figure exists.** The
      earlier 2%-of-August / 97%-of-April figures were *sampled* (n=51 and n=39
      decided) and pointed the right direction but understated the total: 16,738 is
      more than a single month's intake, so the gap spans wider than the August
      cohort alone. Treat the sampled percentages as superseded.
