- [ ] **BOOK-PURGE-UNDECODABLE** Add an in-app way to clear a `book:<id>` row that does not
      decode. Since #3346, once the `book_atpath` index is built, one such row makes every
      `LiveBookIDsAtPath` call fail (fail-closed, on purpose). `UpdateBook` and `DeleteBook`
      cannot lift that, because both read the old row through `GetBookByID` and fail on the
      decode. Today the only fix is to rewrite or remove the row out of band, then run
      `maintenance.book-atpath-index-backfill`. Proposed op: `maintenance.book-purge-undecodable`
      (`CapLibraryWrite`). It hard-deletes each undecodable `book:<id>` row, its `book_sig:<id>`
      sidecar and its `book_atpath_undecodable:<id>` marker in one batch. Caveat: the secondary
      index keys (`book:hash:`, `book:originalhash:`, `book:organizedhash:`, `book:work:`,
      `book:versiongroup:`, ISBN/ASIN, `book:path:`, `book_atpath:`) are derived from fields that
      cannot be read, so the op cannot find them and they are left dangling. Either scan those
      prefixes for values equal to the id, or document them as harmless extras that each reader
      already point-verifies. Needs a dry-run mode and tests.
