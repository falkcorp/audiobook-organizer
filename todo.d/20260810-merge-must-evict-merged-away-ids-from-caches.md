- [ ] **MERGE-CACHE-EVICT** A merge must evict (or dirty-flag) every merged-away
      book/file ID from **every** read cache, so the losers stop appearing after
      the merge succeeds. **Owner-reported 2026-08-10:** "you think you merged
      something then you see 2 copies still." Applies to both merge shapes —
      several files into one book, and two books into a version group.

      This is a trust bug, not a cosmetic one: a merge that visibly does nothing
      teaches the owner not to believe the merge button. Mechanism does not
      matter (evict, dirty-flag, write-through) — the invariant does: **after
      `MergeBooks` returns success, no read path may still serve a loser ID.**

      **Established by grep at `76269d57` (measured, not inferred):**

      - Merge entry points: `internal/merge/service.go:125`
        (`(*Service).MergeBooks`) and `internal/dedup/book_dedup.go:395`
        (`MergeBooks`). HTTP entry: `internal/server/handlers/duplicates/handler.go:292`.
      - Losers are **soft-deleted**, not removed: `merge.SoftDeleteBook`
        (`internal/merge/service.go:544`) sets `MarkedForDeletion` /
        `MarkedForDeletionAt` and calls `store.UpdateBook`, falling back to
        `DeleteBook` only if that write fails.
      - `UpdateBook` does write through to the in-memory copy — the
        `UpsertBookToMemDB` / `DeleteBookFromMemDB` API exists at
        `internal/database/memdb_sync.go:123` and `:182` — and calls
        `InvalidateLibraryStats`. So **memdb is the layer least likely to be at
        fault**; do not start there.
      - 🚨 **`internal/merge/` and `internal/dedup/` contain ZERO references to
        the search index** — no `IndexBook`, no delete-from-index, no dirty-set
        enqueue. The Bleve index lives in `internal/search/` (`bleve_index.go`,
        `index_builder.go`). A merge therefore never tells the index its losers
        are gone.

      **NOT established — verify before fixing, do not assume:**

      1. Whether `UpdateBook` itself enqueues into the search dirty set added by
         **#2268**. If it does, the index may self-heal on the next reconcile
         pass and the visible-duplicate window is a *latency* problem, not a
         *correctness* one — a different fix (force a reconcile on merge) than
         an explicit evict.
      2. Whether a soft-deleted book is **deleted from** the Bleve index or
         merely re-indexed carrying `MarkedForDeletion`, and whether the query
         path filters on that flag. If it is filtered only *after* pagination,
         this is the same post-filter-after-pagination defect already recorded
         for search — losers would consume page slots even once filtered.
      3. The **file-level** merge path (several files into one book) was not
         located in this pass. Find it and check it independently; do not assume
         it shares `merge.SoftDeleteBook`.
      4. Whether the version-group read path de-duplicates. Related:
         `GetBooksByVersionGroup`'s pointer index (fixed in **#2288**) — a merge
         writes `VersionGroupID`, so a stale index row is another way a loser
         could keep surfacing.

      **Acceptance criteria (the regression test to write):** merge N books into
      a version group, then **immediately** — no sleep, no refresh — re-query
      (a) the library list, (b) search, (c) the version-group endpoint, and
      assert every loser ID appears in **none** of them. A test that passes only
      after a sleep is measuring the reconciler, not the fix.

      Related: the cached-aggregates dirty-flag pattern already used elsewhere in
      this codebase, and `InvalidateLibraryStats` as the existing precedent for
      "a write invalidates a derived read."
