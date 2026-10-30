<!-- file: docs/decisions/2026-10-30-dedup-search-projection.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c098f54-3102-46ae-bd97-e2fd60673aba -->
<!-- last-edited: 2026-10-30 -->

# Dedup search: store-side book projection

Task: TODO.md "Dedup search resolves book IDs with a full `GetAllBooksCore` read".

## Decision log

Chosen: `BookBulkReader.ListBookSearchRows() ([]BookSearchRow, error)`, returning
`{ID, Title, FilePath, AuthorName}` for every non-deleted book, author name joined inside
the store. `resolveBookIDsMatching` keeps the substring matching and runs it over those rows.

Rejected alternatives:

- **Match inside the store and return only matching IDs** (the TODO's ideal). It puts the
  Dupes search rule (lower-cased contains over title, path, author) into the database layer,
  and a callback/needle parameter is awkward to mock. The projection already removes the
  dominant cost (a full `BookCore` per row); the matching loop is three `Contains` calls.
- **Return `AuthorID` and keep `GetAllAuthors` in the handler.** It leaves two reads from
  two snapshots and two methods on `DedupStore`. Joining in the store uses one memdb read
  transaction and shrinks `DedupStore` by one method.
- **Callback/iterator form (`ForEachBookSearchRow`).** Avoids the row slice but runs handler
  code inside a memdb read transaction. A slice of four-string rows is small enough.
- **Narrow the consumer interface only.** Not applicable: no existing method gives the
  projection, and `DedupStore` ends up with the same or fewer methods than before.

Semantics held identical: soft-deleted books excluded (`bookIsSoftDeleted`), a dangling or
absent author gives an empty name that cannot match a non-empty needle, author name matched
as `strings.ToLower(name)` contains the needle. The Pebble fallback reuses `GetAllAuthors`
so both paths resolve names the same way.

Measurement: the "measure first" item was satisfied by `BenchmarkResolveBookIDsMatching`
(old reader vs projection on a 5,000-book store), not a production probe. Numbers are in
the commit body.
