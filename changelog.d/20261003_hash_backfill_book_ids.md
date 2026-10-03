### Added

- **`backfill-file-hashes` takes `book_ids`:** the maintenance job can now hash the rows of a named set of books instead of the whole library. The fragment-consolidation fixer proves a duplicate copy by file hash, so a repair that needs hashes for one book's copies (the Bible's 3,541 rows on prod) no longer has to wait for a 700k-row backfill. Empty `book_ids` keeps the old whole-library behaviour.
