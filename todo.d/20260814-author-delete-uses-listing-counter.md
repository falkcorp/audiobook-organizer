### Author delete paths guard with the listing counter, same shape as the series bug

- [ ] **`BulkDeleteAuthors` and `DeleteEmptyAuthor` decide "is this author empty?"
  with `GetBooksByAuthorIDCore`**, which `internal/database/memdb_reads.go:529`
  documents as a *listing* view — it applies the primary-version filter and
  returns only live books. The repo already knows this is the wrong getter for
  this class of caller; the comment on `GetBooksByAuthorIDAllVersions` says so
  explicitly:

  > `GetBooksByAuthorIDWithRoleCore` is what merges and deletes consult to find
  > the links they must rewrite before removing an author. For that caller a
  > missed link is data loss — the author gets deleted and the junction row is
  > left pointing at a row that no longer exists.

  The delete handlers are exactly that caller and still use the listing getter,
  so an author whose only books are trashed or non-primary counts as zero and is
  deletable, stranding those books and their `book_authors` junction rows.

  This is the author-side twin of the series bug fixed in #2400 (weekly prune)
  and the UI delete paths. It was NOT fixed alongside them because the series fix
  could reuse `SeriesBookRefStore`/`GetAllSeriesBookRefCounts`, and no author
  equivalent exists — the fix needs a `GetAllAuthorBookRefCounts` that counts
  `Book.AuthorID` **and** `book_authors` junction rows in every book state,
  across both the memdb and Pebble implementations, with a conformance test
  (see `internal/database/author_getter_conformance_test.go`, and the
  memdb-vs-Pebble divergence it already caught: 86 links warm vs 84 cold).

  Until then the risk is live but small — it needs a user to bulk-delete authors
  from the UI. Not a background job, so nothing is accumulating on its own.
