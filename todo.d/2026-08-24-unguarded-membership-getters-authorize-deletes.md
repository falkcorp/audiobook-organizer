- [ ] 🔴 **ORPHAN-FILES-HARD-DELETE-FAIL-OPEN** `internal/plugins/maintenance/orphan_book_files.go`
      classifies `book_file` rows as orphans by testing membership against a map
      built from TWO unguarded dual-dispatch getters, then **hard-deletes** them.
      This is worse than SERIES-MERGE-UNGUARDED-DENOMINATOR, which only strands.
      - `:232` `store.GetAllBooksCore(0, 0)` and `:256` `store.ListSoftDeletedBooks(0, 0, nil)`
        both read memdb unconditionally when warm, with no `requireTablesComplete`.
      - `:236-238` / `:264` fold both results into one `valid` set; `:277` treats
        any `book_file` whose `BookID` is absent from it as an orphan; `:136`
        calls `DeleteBookFilesByIDs(ids)`.
      - So ONE lost memdb `books` row — or any `memTableUnknown` taint, which
        taints every table — silently removes book R from `valid`, and **every
        `book_file` row belonging to R is hard-deleted**. R survives as a fileless
        shell. Pure fail-open membership test with no independent corroboration.
      ⚠️ Context that raises the priority: 41.8% of `book_file` rows already have
      no bytes (`project_missing_bookfile_rows_download_404`), so a job that
      deletes file rows on a short read is operating on an already-damaged
      population. `:251` records that this same `valid` set has had one
      correctness incident already (soft-deleted rows leaking into it).
      **Fix is ~3 lines and the pattern is already in-tree:** both getters have a
      complete Pebble twin directly below the memdb branch, identical in shape to
      the fall-through PR #2839 shipped for the series getter, and
      `ListSoftDeletedBooks`' twin is already hardened against undecodable rows.
      Found by the silent-failure sweep on PR #2839; hand-verified.

- [ ] 🔴 **AUTHOR-MEMBERSHIP-UNGUARDED** `GetBooksByAuthorIDWithRoleCore`
      (`internal/database/pebble_store.go:2086`) is the author-side structural
      twin of the series getter PR #2839 guarded, with the same defect and four
      delete sites. A lost memdb `books`/`book_authors` row yields a short member
      list with a nil error, that book is never relinked, and `DeleteAuthor`
      runs — leaving a dangling author_id. **Not recoverable: the author's name
      lived only in the row that was deleted.**
      - Hand-verified: `internal/plugins/maintenance/author.go:171` →
        `DeleteAuthor` at `:250`, unconditional; and
        `internal/server/handlers/entities/handler.go:463` → `DeleteAuthor` at
        `:517`, unconditional (`POST /authors/:id/split`).
      - Sweep-reported, NOT hand-verified: `entities_ops.go:91` → `:160`;
        `author_conjunction_repair.go:291` → `:372`. Re-verify before acting.
      🚨 The getter's OWN doc comment (`pebble_store.go:2087-2092`) already says
      "a link they cannot see is one they will not rewrite before deleting the
      author — which orphans it." The author understood the hazard for the
      filtered-view case and fixed that half; the lost-row half was never wired
      up. A documented hazard is not a control.
      Lower severity, same class: `GetAllAuthors` (`authors.go:22`) →
      `cleanup_orphan_author_embeddings.go:141` → `embeddingStore.Delete` `:168`
      (embeddings are recomputable, so this degrades rather than destroys).

- [ ] **MEMDB-LOSSY-READERS headline is STALE — correct it before acting on it.**
      `todo.d/20260823-memdb-lossy-projection-unguarded-readers.md` names
      `purge-empty-authors` (4,975 of 12,854 authors) as its worked example,
      gating deletion on two unguarded counters. At HEAD that is no longer true:
      `author_purge_empty.go:184` gates deletion on `database.AuthorRefCounts` →
      the **guarded** `GetAllAuthorBookRefCounts`, failing closed, and
      `bookCounts` is demoted to candidate SELECTION (a short count adds false
      candidates, which refCounts then rescues). That half is closed. Two
      supporting defects in that fragment DO still stand: `memdb_reads.go:184`
      folds a lookup error into "book absent", and
      `internal/plugins/maintenance/author.go:57` is
      `bookCounts, _ := store.GetAllAuthorBookCounts()` — that `_` will swallow
      `ErrMemdbIncomplete` the moment a guard lands on that getter.
      Also update its count: **33 externally-reachable dual-dispatch getters, 3
      guarded** (it says "1 of 29"). The enumeration is closed — `mem()` has
      exactly one definition (`pebble_store.go:149`) and no other store wrapper
      dispatches to memdb.

- [ ] **SERIES-MERGE-PERSERIES-SCAN-COST** Four callers of
      `GetBooksBySeriesIDAllVersions` call it once per series inside a loop and
      none hoists or caches: `cleanup_series.go:105` (inside
      `for _, ser := range allSeries` at `:73`), `duplicates_helpers.go:291`,
      `series_dedup.go:419`, `series_dedup.go:634`. Once memdb is tainted,
      `lostRows` is sticky until restart, so every iteration takes a full
      `"book:"` prefix Pebble scan — a range `memdb_warmup.go:206-208` measures at
      ~7.5 keys per admitted book row. On a 41k-book library that is
      O(series × 7.5 × books), single-threaded, on the nightly maintenance
      window, and CLAUDE.md's concurrency rule applies to a loop of that shape.
      ⚠️ Filed because PR #2839 inherited the cost note from
      `GetAllSeriesBookRefCounts`, whose justification is "No caller counts
      inside a loop." That premise is FALSE for this getter. The correctness
      trade is still right — a stranded book is unrecoverable and a slow window
      is not — but it is a real standing cost, and the doc comment now says so
      instead of repeating the inherited claim. Hoist the map per operation, the
      way the ref-count callers already do.
