### Fixed

#### Author merge, split and reclassify no longer strip credits from trashed books

Every path that relinks an author's books and then deletes the author built
its relink list from `GetBooksByAuthorIDWithRoleCore`, which excludes books in
the trash. `DeleteAuthor`'s junction sweep removes the author from every
`book_authors` row, trashed books included, so each merge, split or
reclassify erased the credit on any trashed book the author had and left that
book's legacy `AuthorID` pointing at the deleted row. Restoring such a book
from the trash produced a book with no author.

- New getter `GetBooksByAuthorIDForRelinkCore` returns every linked book in any
  state (live, non-primary, trashed), memdb and Pebble paths pinned to agree,
  with the same `ErrMemdbIncomplete` fall-through. The listing getter is
  unchanged.
- Switched to it: `mergeAuthorInto` (author-conjunction-repair,
  author-duplicate-merge, author-strip-merge merge), author-strip-merge's
  `unlinkAndDeleteAuthor`, the maintenance and scheduler author-split ops,
  `entities.author-merge`, the split-composite and reclassify-as-narrator
  handlers, and the AI dedup merge/alias apply.
- New `database.VerifyAuthorUnlinked` runs before every one of those deletes
  except the AI apply: an author still credited by any book after the relink
  is kept and the failure reported (409 from the two handlers). This also
  closes the paths that logged a failed book and deleted anyway.
- The AI apply is exempt because its reassign rewrites only the junction and
  leaves the legacy `AuthorID`, which the verify step would read as still
  linked. That leftover id resolves to the kept author only through the
  tombstone the apply writes after the delete; a failed `CreateAuthorTombstone`
  there was silently ignored and is now logged at Warn (merge and alias).
- `AuthorRefCounts` is now computed from live / trashed / dangling buckets
  (`AuthorRefBucketCounts`). author-duplicate-merge holds a row back only when
  live + trashed references exceed what it can move, so junction rows whose
  book no longer exists no longer block a merge; the held-back log line
  reports all three buckets.
