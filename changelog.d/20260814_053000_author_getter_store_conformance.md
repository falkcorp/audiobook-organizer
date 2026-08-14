### Fixed

- **Author book-lookups disagreed between the two stores, and a merge could orphan
  junction rows.** `GetBooksByAuthorIDWithRoleCore` is what every merge and delete
  path calls to find the books it must relink before removing an author. Its memdb
  implementation silently dropped co-author credits on non-primary versions, so a
  merge could delete an author while `book_authors` rows still pointed at it. Its
  sibling `GetBooksByAuthorIDCore` diverged in the opposite direction — the Pebble
  implementation never opened the junction table at all, so a co-author's books were
  invisible to listings served during the ~132 s memdb warmup. Two opposite-signed
  errors kept aggregate counts plausible; only a co-author on a non-primary version
  exposed either. Detected when the same repair op reported 86 books relinked
  seconds after a restart and 84 warm against identical data.

  Both methods now agree across both stores: `...WithRoleCore` returns the complete
  set (junction + legacy, non-primary included), `...Core` returns the listing view
  (junction + legacy, non-primary excluded). Both exclude soft-deleted books. A new
  conformance suite holds each method's two implementations to the same answer on
  one fixture, in the pattern established for the soft-delete contract.
