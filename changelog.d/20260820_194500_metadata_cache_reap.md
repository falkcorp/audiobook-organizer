<!-- file: changelog.d/20260820_194500_metadata_cache_reap.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c3e0a95-71d4-4f26-b0a8-5e97d1c4f382 -->
<!-- last-edited: 2026-08-20 -->

### Added

#### `maintenance.metadata-cache-reap` — clear cache rows whose book is gone

The by-cause split added earlier today named 3,354 orphaned metadata-cache rows
— 23% of the cache — pointing at books that no longer exist. Nothing could read
them and nothing could clear them; the only remedy the tooltip could offer had
no implementation behind it. This op is that implementation.

It deletes, which is worth stating plainly next to `missing-file-repoint`'s
"NEVER deletes a row" and the 2026-08-19 decision that removed
`missing-file-repair`'s delete path. That decision is not being reversed. It
concerned `book_file` rows — library records pointing at audio the owner owns,
where a wrong delete destroys the only pointer to real bytes. A metadata-cache
row is derived, regenerable, already expendable under its own 30-day TTL, and
keyed to a book nothing in the product can reach.

Three properties make it safe to run and safe to interrupt:

- **Absence and failure are different buckets.** `GetBookByID` returns
  `(nil, nil)` for a missing key and `(nil, err)` for a real fault. Only the
  first is an orphan; a lookup error is counted, reported and skipped, so a bad
  day for the store cannot read as thousands of books to forget.
- **Every row is re-resolved at delete time**, not trusted from the plan. A book
  restored or re-imported between the scan and the write stops being an orphan
  and is spared.
- **Dry run is the default**, the delete cap defaults to 500, and a per-row TSV
  naming every scanned row is written on every run — for a delete op the report
  is the only record of what was destroyed and the only way to know what to
  re-fetch.

Soft-deleted books are not orphans, and this was measured rather than assumed:
the library holds 16,124 of them, but `GetBookByID` applies no soft-delete
filter, so their cache rows resolve and are never reaped.
