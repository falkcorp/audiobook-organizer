- [ ] **MEMDB-LOSSY-READERS** The known-incomplete guard added in #2794 covers
      **1 of 29** `p.UseMemDB && p.mem() != nil` dispatch sites in
      `internal/database`. The other 28 still answer from a lossy projection
      with a nil error, and at least two of them gate a bulk delete.

      **Highest priority — `maintenance.purge-empty-authors`.** Its own
      description records deleting **4,975 of 12,854 authors** on this library.
      It gates on two counters, and BOTH read lossy memdb tables unguarded:

      - `GetAllAuthorBookCounts` → `internal/database/memdb_reads.go:165`
        (scans `books` + `book_authors`)
      - `GetAllAuthorFileCounts` → `internal/database/memdb_reads.go:299`
        (scans `books` + `book_files`)

      One lost book row makes an author absent from *both* maps, so
      `bookCounts[a.ID] == 0` makes it eligible AND `fileCounts[a.ID] == 0`
      *satisfies* the `require_zero_files` safety check. Both safety checks
      fail open in the same direction from a single loss, so the second
      corroborates the first instead of catching it.

      The op already states the correct principle for the ERROR case
      (`internal/plugins/maintenance/author_purge_empty.go:141-143`: "a failure
      here must not be silently treated as zero files — that would turn a
      missing signal into permission to delete") and then reads the value from
      an unguarded lossy projection.

      Two supporting defects in the same area:
      - `memdb_reads.go:184` does `if bErr != nil || raw == nil { continue }` —
        folds a lookup ERROR into "book absent", which is exactly the lossy case.
      - `internal/plugins/maintenance/author.go:57` is
        `bookCounts, _ := store.GetAllAuthorBookCounts()` — that `_` will
        swallow `ErrMemdbIncomplete` the moment a guard is added.

      Fix: wire both counters to `MemStore.requireTablesComplete` and give
      `PebbleStore` the same `ErrMemdbIncomplete` fall-through
      `GetAllSeriesBookRefCounts` has. Mechanism already exists
      (`internal/database/memdb_integrity.go`); this is applying it.

      Found by review of #2794, 2026-08-23. Not in that PR's scope.
