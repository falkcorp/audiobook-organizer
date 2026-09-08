### N+1 / batch-endpoint audit follow-ups (2026-09-08)

Full audit with file:line anchors, cardinality and dismissed sites:
[`docs/audits/2026-09-08-n-plus-one-batch-endpoint-audit.md`](docs/audits/2026-09-08-n-plus-one-batch-endpoint-audit.md).
Root cause of the 13.8–28.0 s search (finding 7) is fixed in #3128; these are the rest.

Do NOT add a memdb branch to `GetBookFiles` — its absence is deliberate
(`memdb_strip.go:118` strips fingerprints/transcriptions; it is the full-fidelity
escape hatch). Move Core-only callers to `GetBookFilesForIDsCore` instead.

- [ ] **abs mapper: batch the per-book file fetch** — `mapper.go:163`
      `GetBookFiles` inside the per-book errgroup → `GetBookFilesForIDsCore`
      alongside the existing pre-loop batch calls at `:102-116`. Every field abs
      reads is on `BookFileCore` (checked field-by-field); the mapper already
      sorts at `:170`. Request path, 7 call sites.
      **⚠️ Gate it on memdb being warm.** `GetBookFilesForIDsCore`'s fallback
      (`pebble_store_bookfiles.go:685`) is a FULL scan of all ~726K `book_file:`
      rows, while the `GetBookFiles` it replaces is prefix-bounded. Swapping
      naively makes the cold case far worse on a path that is live during the
      ~130 s async warmup after every restart. Keep a per-book cold fallback, or
      give the batch method a prefix-per-book fallback first.
- [ ] **`ListCachedCandidates`: use `GetBooksByIDs`** — `metadata_cache.go:129`.
      Its twin `GetCacheReviewResults` (`:205`) was already fixed; the comment at
      `:191-197` records 21.7 s / 35.2 s prod timings for this exact pattern.
      Source is unbounded (`ListCachedSummaries` takes no limit).
- [ ] **Fix `mapper.go:82-84`, which claims the relation lookups are batched.**
      They are not: `GetAuthorsByBookIDs` / `GetNarratorsByBookIDs`
      (`pebble_store_authors.go:1033`/`:1059`) loop per book AND per author
      internally, ignore `ctx`, and do no cross-book dedupe. Either make them real
      or correct the comment — do not leave both.
- [ ] **Add memdb-backed author/series/narrator batch getters.**
      `memdb_schema.go:214/231/292/313` declares warm `id` indexes on authors,
      series and narrators plus a `book_id` index on `book_authors`, and **zero
      getters read them**. Unblocks the item above, finding 5, and the dedup
      handlers.
- [ ] **`OptimizeDatabase`: batch the per-author read** —
      `operations/handler.go:297`. The book list is already memdb-fast; the author
      read is a disk get per book over an unlimited `GetAllBooksCore(0,0)`.
      Synchronous HTTP handler.
- [ ] **dedup handlers: batch the point-gets** — `dedup/handler.go:769,894,626,674,281`,
      with `Limit: 100000` hardcoded and `bothUnmatchedScanLimit = 1_000_000`.
      Synchronous HTTP.
- [ ] **`/api/me`: hoist `GetBookFilesForIDsCore` above the errgroup** —
      `abs/userdata.go:450`. `bookIDs` is already materialized at `:207-213`.
- [ ] **Add a batch chapter getter.** `GetChaptersForBook`
      (`pebble_store_chapters.go:40`) has no `…ByBookIDs` sibling anywhere; needed
      by `mapper.go:244` (per page item) and `chapters_backfill.go:349`
      (whole library).
- [ ] **`GetSyncFileIDsForBook` prefix scan** (follow-up to #3128) — the
      `sync_file:book:<bookID>:<syncFileID>` index stores the fileID as its value,
      so one scan per book replaces N point-gets with zero record gets.
      `RepointSyncFile` keeps that index current (verified). Do this on measured
      evidence after #3128 is deployed.
- [ ] **Background loops with an existing batch fix** — `relink_unlinked.go:119/126`,
      `generate_itl_tests.go:55` (serial; `GetAllBookFilesCore()` deletes the loop),
      `fix_read_by_narrator.go:56` (serial), `recompute_book_aggregates.go:180/187`
      (serial), `regroup_shattered_ai.go:160/164`, plus the `GetBookFiles` and
      `GetBookByID` rows in the audit's table.
- [ ] **Group-by-BookID, not a batch swap** — `fix_book_file_paths.go:56`,
      `enrich_book_files.go:69`, `recompute_itunes_paths.go:56`,
      `lsh_backfill.go:122`, `tag_backfill.go:147`, `dedupe_book_file_rows.go:253`
      each refetch a book's whole file list once per file.
