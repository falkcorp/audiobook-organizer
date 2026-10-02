### Fixed

#### Book writes no longer pile tombstones into ranges that folder lookups scan

Every `UpdateBook` and `DeleteBook` staged a Delete of the book's
`book_atpath_undecodable:<id>` marker, which almost never exists. Each one left
a Pebble tombstone, and every `LiveBookIDsAtPath` / `LiveBookPathsUnderDir`
lookup range-scanned that family, so after an ASIN/ISBN backfill the "empty"
scan walked tens of thousands of tombstones per folder. The metadata cache
review page (`?all=true`, 39,689 rows) took 2m52s, past the UI timeout, with
about 51s in that scan and 47% of CPU in GC.

The store now keeps an in-memory set of the marker ids that may exist, loaded
at open and kept a superset of disk by the backfill (add before commit,
re-note after) and by `UpdateBook`/`DeleteBook` (drop only after the delete
commits, and only if nothing re-added the id). Writers delete a marker only
when the set holds its id; readers point-read only those ids and never scan
the family, so existing tombstones no longer cost anything either.

The same absent-key tombstone pattern is gone from `UpdateBook`'s
`metadata_cache:` invalidation (scanned by the cache review page on every
request) and from `DeleteBook`'s `book_sig:`, `emb:v:book:`, `chapters:`,
`book_authors:`, `book_narrators:`, `user_tag:book:`, `alt_titles:book:` and
`metadata_cache:` deletes, which are now staged only when the key exists.

Follow-ups from review of the same change:

- `maintenance.book-atpath-index-verify` range-scans the marker family on disk
  again and reports `markers_not_in_set`: markers on disk that the in-memory
  set lacks, the state in which lookups would skip an undecodable row and fail
  open. Verify stays read-only and fails the op, naming
  `maintenance.book-atpath-index-backfill` as the repair.
- `DeleteBook` now holds the book's `book_authors` stripe from the
  `book_authors:<id>` probe through the commit, so a concurrent
  `SetBookAuthors` cannot commit a credit row between the probe and the delete
  and leave it naming a deleted book.
- Book-atpath backfill, rebuild and verify runs are serialized store-wide. The
  startup backfill shares no concurrency key with the rebuild op, so two runs
  could overlap and one could drop a still-undecodable marker from the set.
