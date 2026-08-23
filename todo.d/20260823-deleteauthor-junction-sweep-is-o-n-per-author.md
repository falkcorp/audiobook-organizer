<!-- file: todo.d/20260823-deleteauthor-junction-sweep-is-o-n-per-author.md -->
<!-- version: 1.0.0 -->
<!-- guid: eb083815-10cf-411f-b4b7-d93294e74d86 -->
<!-- last-edited: 2026-08-23 -->

- [ ] **`DeleteAuthor` scans the whole `book_authors:` keyspace once per author deleted.**
      Correct, and fine for interactive single deletes. The concern is the bulk
      caller: `maintenance.purge-empty-authors` deletes authors in a loop, so
      the cost is (authors purged x junction size). TASK-075's report puts the
      zero-book-but-has-files bucket alone at 822 authors, and the full
      empty-author population is larger.

      Two options, and they are not equivalent:

      1. Add an author -> books reverse index in Pebble. Makes the sweep O(books
         for this author), but needs a backfill migration and a second index to
         keep consistent on every junction write.
      2. Give the bulk path a batched variant that scans the junction ONCE and
         removes a whole set of author IDs in that single pass. No new index, no
         migration, and it fixes the only caller that actually has the problem.

      Option 2 is almost certainly right — the reverse index is a large,
      permanently-maintained structure bought to fix one loop — but this is
      recorded rather than decided, because option 1 also unlocks other
      author-scoped queries and that trade is the owner's to weigh.

      Anchor: `sweepAuthorFromBookAuthors`, `internal/database/pebble_store_authors.go`.
      Introduced with the TASK-036 fix; the cost is inherent to the correct
      behaviour, not a regression.
