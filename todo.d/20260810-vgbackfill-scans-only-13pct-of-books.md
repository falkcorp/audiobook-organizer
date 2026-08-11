- [x] ~~**VGBACKFILL-SCAN-BOUNDS** The version-group backfill scans only ~13% of
      the library.~~ **RETRACTED 2026-08-11 — there was no under-scan.** Kept
      rather than deleted so nobody re-derives the same wrong conclusion from
      the same logs.

      **What was claimed:** `scanned=48874` against `books=366922` from the same
      boot = 13.3%, therefore the backfill's Pebble iterator bounds
      (`book:0` .. `book:;`, admitting only digit-leading IDs) were excluding
      ~318k rows.

      **What is actually true:** `books=366922` was never a book count. The
      memdb warmup's `warmIter` returned the number of Pebble KEYS it visited
      under the `book:` prefix, and that prefix is shared with roughly seven
      secondary-index families — `book:path:`, `book:hash:`,
      `book:originalhash:`, `book:organizedhash:`, `book:versiongroup:`,
      `book:work:`, `book:asin:`/`book:isbn13:`. The row callback skips those
      via `strings.Count(key, ":") != 1`, but the skipped keys were still
      counted and then published under the label `books`. About 7.5 keys per
      book row on production.

      The real library is **~46k–55k books**, corroborated three ways in the
      same logs: `total_books=46221` and `total_books=54734` from system
      status, and the organizer's own `Fetched 48896 total books from
      database`. So `scanned=48874` was a **complete** scan, and the digit-only
      iterator bounds — while genuinely fragile — were not excluding anything,
      because production book IDs are ULIDs and a ULID minted this century
      starts with `0`.

      **How the error was made, because the shape recurs:** the original entry
      explicitly listed this explanation ("the two numbers could also disagree
      because `books=366922` counts something the `book:<id>` keyspace does
      not") and marked the whole thing NOT YET CONFIRMED. It was then upgraded
      to CONFIRMED on 2026-08-11 when a *second* subsystem — the organizer's
      `GetAllBooksCore` paging loop — reported 48,896 against the same
      `books=366922`. Two independent readings agreeing looked like
      corroboration. They were not independent: both were compared against the
      **same unverified denominator**. Agreement between two numerators says
      nothing about the denominator they share.

      **Fixed in the same PR as this retraction:** warmup now reports rows
      inserted into memdb, and reports keys scanned separately under its own
      name so the two can never be confused again. Pinned by
      `TestWarmupCounts_CountRowsNotPebbleKeys`, which fails with
      `expected: 4, actual: 20` against the old counting.

- [ ] **VGBACKFILL-BOUNDS-FRAGILE** Separately, and still worth doing: the
      version-group backfill's iterator bounds `book:0` .. `book:;` admit only
      IDs whose first byte is `0x30`–`0x3A`. That is correct today only because
      every production book ID happens to be a ULID starting with `0`. It is
      not enforced anywhere — `CreateBook` mints a ULID only `if book.ID == ""`,
      so a caller supplying a letter-leading ID would become permanently
      invisible to this scan with no error. Replace the bounds with a prefix
      scan over `book:` → `book;` and let the existing one-colon structural
      filter reject the secondary indexes, which is what it was written for.
      This is a latent-correctness fix, **not** the cause of any observed
      under-scan.
