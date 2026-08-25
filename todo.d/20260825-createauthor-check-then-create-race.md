## `CreateAuthor` is check-then-create with no atomicity — mints duplicate author rows

`PebbleStore.CreateAuthor` (`internal/database/pebble_store_authors.go`) calls
`GetAuthorByName` and, on a miss, mints a new row. The two steps are not atomic,
so concurrent callers with the same name each create their own row.

**Measured 2026-08-25:** 24 concurrent `CreateAuthor` calls with an identical
name produced **24 distinct author rows**, reproducibly across three runs. This
is not an occasional race — the dedup check almost never observes a concurrent
write.

The scanner resolves authors from inside its worker pool, so an import that first
meets an author on several books at once mints a row per worker. Production has
two rows named `Unknown Author` (54845 with 0 books, 54846 with 2,128), and
17,947 author rows in total. Consistent with the earlier finding that ~212
authors' books carry a dangling `AuthorID`.

Consequences beyond the duplicate rows themselves: the `author:name:` index maps
a normalized name to exactly one id, so every duplicate beyond the indexed one is
unreachable by name lookup. Any logic that identifies an author by resolving its
name to an id is wrong for those rows — this already nearly made the
`Unknown Author` nomination-gate fix inert (see
`docs/audits/2026-08-25-unknown-author-feedback-loop.md`).

- [ ] Make the lookup and insert atomic (single Pebble batch with a conditional
      write), so a concurrent caller cannot mint a second row.
- [ ] Decide how to merge the duplicate author rows already present, and whether
      book `AuthorID`s pointing at unindexed duplicates should be repointed at
      the indexed row.
- [ ] Add a concurrency test asserting N concurrent `CreateAuthor` calls with one
      name yield exactly one row. A serial test cannot observe this.

Lane: `internal/database`. Found from the scanner side while fixing the
`Unknown Author` gate; deliberately not fixed there.
