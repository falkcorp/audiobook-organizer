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

- [ ] 🔴🔥 **AUTHOR-MEMBERSHIP-UNGUARDED — CONFIRMED FIRED IN PROD 2026-08-24
      05:00 UTC, not just a filed risk.** `GetBooksByAuthorIDWithRoleCore`
      (`internal/database/pebble_store.go:2086`) is the author-side structural
      twin of the series getter PR #2839 guarded.
      - `maintenance.author_split_scan` ran unattended in the nightly window,
        reached task 3/10, and had processed 10,681/14,951 authors — **1,400
        already split** — before another session caught and canceled it
        (`DELETE /operations/v2/01M0S29XYASPQ9HY73RYP9MEQN`), then disabled
        `maintenance.author_split`, `scheduled.author_split.enabled`, and
        `maintenance.enabled` at the config level so it cannot relaunch.
        Blast radius of the 1,400 (all/some/none actually hit the bug) is
        UNMEASURED — an audit was handed to the other session, not yet run as
        of this writing.
      - **Corrected failure signature** (my original note below was wrong about
        WHERE the damage lands — verified by reading both functions end to end,
        not inferred): `DeleteAuthor` (`pebble_store_authors.go:157`) does NOT
        depend on the split job's book list for its own cleanup —
        `sweepAuthorFromBookAuthors` (`:220`) is an unconditional, raw Pebble
        scan over every `book_authors:` key, independent of memdb. **The
        `book_authors` junction is safe.** The real exposure is the
        DENORMALIZED `book.AuthorID` field: `runAuthorSplitScan`
        (`internal/plugins/maintenance/author.go:171-248`) only rewrites it
        for books the (possibly short) getter returned. A book the getter
        missed keeps `AuthorID` pointing at the composite author row that
        `DeleteAuthor` then deletes unconditionally at `:250` — a dangling FK
        on the BOOK record, not the junction. A second, harder-to-detect case:
        a book whose junction link got swept but was never relinked to the new
        individual author(s) at all (silently demoted to no author for that
        slot), because it was invisible to the getter throughout.
      - **Audit query for the confirmed exposure:** the set of live author IDs
        minus every book's `AuthorID`; any book pointing at an ID outside that
        set is a hit. This should never occur in healthy operation — no other
        known code path produces a dangling `book.AuthorID` — so a nonzero
        count is unambiguous blast radius, no need to know which of the 1,400
        splits caused it. Handed to the other session with this exact query;
        follow up for the result before scoping a repair.
      - Hand-verified other call sites, unrelated to tonight's incident:
        `internal/server/handlers/entities/handler.go:463` → `DeleteAuthor` at
        `:517`, unconditional (`POST /authors/:id/split`).
      - Sweep-reported, NOT hand-verified: `entities_ops.go:91` → `:160`;
        `author_conjunction_repair.go:291` → `:372`. Re-verify before acting.
      🚨 The getter's OWN doc comment (`pebble_store.go:2087-2092`) already says
      "a link they cannot see is one they will not rewrite before deleting the
      author — which orphans it." The author understood the hazard for the
      filtered-view case and fixed that half; the lost-row half was never wired
      up. A documented hazard is not a control — and this is now the second
      incident (after `feedback_a_documented_hazard_is_not_a_control.md`'s
      2026-08-23 pair) where a written-up hazard sat un-tested until it fired
      for real.
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
