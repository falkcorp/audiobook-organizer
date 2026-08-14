### Fixed

- **Deleted books were still being processed by every full-library operation.**
  `GetAllBooksCore` and `CountAllBooks` each have two implementations — a Pebble
  keyspace scan and a memdb index walk — and only the Pebble one filtered
  soft-deleted rows. Since memdb is the production default, all ~35 full-library
  callers (organize, dedup, every backfill, the reconcile passes, the ABS library
  count) silently operated on books that were in the trash. Measured on
  production: 63,869 live books were being scanned as 67,824, the extra 3,953
  being the losers of the July dedup drain, still processed four weeks after
  deletion. The soft-delete rule now lives in a single shared predicate, and a
  conformance test holds both implementations to the same answer on the same
  fixture.
- **`AssignOrphanVGs` could resurrect deleted books** by force-setting
  `library_state=organized` on soft-deleted rows it should never have seen;
  `MergeNoVGDuplicates` could pick a soft-deleted book as the keeper and
  soft-delete the live one instead. Both are consequences of the leak above and
  are fixed by it.
- **Orphan-file cleanup now explicitly protects restorable books.** A
  soft-deleted book still owns its `book_files`, and `findOrphanBookFiles` is a
  set-difference whose output is fed to `DeleteBookFilesByIDs`. Those rows were
  previously protected only *by* the leak, so the scan now unions the
  soft-deleted set in deliberately and fails closed if it cannot read it.
- **Deluge discovery no longer re-offers trashed books** as unimported
  torrents, for the same reason and with the same previously-accidental
  protection made explicit.
