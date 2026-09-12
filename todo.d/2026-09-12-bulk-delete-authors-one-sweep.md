- [ ] **AUTHOR-PURGE-BULK** Give bulk author deletes a single `book_authors` junction sweep.
      `DeleteAuthor` scans the whole per-book junction on every call because there is no
      author-to-books index (`sweepAuthorFromBookAuthors`, `internal/database/pebble_store_authors.go`).
      Production measured about one delete per second on 2026-09-12. The 3,958-author
      `purge-empty-authors` apply hit its 30m timeout at 1,742 deletes and needed three runs,
      holding the scan stand-down for over an hour. Add a store method that deletes a set of
      authors with one sweep and one memdb replay. It must keep the per-item re-check and the
      journal-before-delete ordering, and hold the name-index lock (#3303) across the sweep and
      commit. Then switch `purge-empty-authors` and `purge-empty-narrators` to it.
