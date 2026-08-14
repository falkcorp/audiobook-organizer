## memdb and Pebble disagree about author→book links

Found 2026-08-14 by running `maintenance.author-conjunction-repair` twice in
dry-run against prod and getting two different answers from the same op, same
binary, same data.

| run | started | path taken | `books_relinked` |
|---|---|---|---|
| 1 | 4s after service restart, memdb not yet warm | Pebble junction scan | **86** |
| 2 | memdb warm | memdb | **84** |

Row counts were identical in both (`authors_matched=46`,
`would_merge_into_existing=31`, `would_rename_in_place=15`). The entire
difference is **author 46627 (`& Nicholas Courtney`)**: the Pebble path finds 2
book links, memdb finds 0.

`GetBooksByAuthorIDCore` and `GetBooksByAuthorIDWithRoleCore` both take the same
`p.mem().GetBooksByAuthorID(authorID, 0, 0)` branch when memdb is live, so this
is not a caller difference — the two *stores* disagree. memdb had been freshly
loaded at the restart, so its loader is dropping the links rather than lagging
behind a write.

- [ ] Identify the 2 books. They are not among `Nicholas Courtney` (43791)'s 7
      books — none of those reference 46627 — so they are books where 46627 is
      a co-author and 43791 is not linked at all.
- [ ] Find why memdb's loader drops them. Suspects: the junction load skipping
      rows whose book is soft-deleted, or an author-id index built only from
      `Book.AuthorID`. Note `/api/v1/authors/46627/books` and the authors-list
      `book_count` BOTH report 0, so every serving-layer read agrees with memdb
      and only Pebble sees them.
- [ ] Write the conformance test rather than a per-path assertion — one fixture,
      both implementations, assert equal. This is the third memdb/Pebble
      divergence in a week (see the soft-deleted leak, #2392), and per-path
      expectations cannot catch drift because the path's own author writes the
      expectation.
- [ ] Once resolved, drop `skip_author_ids: [46627]` from the repair invocation
      and repair that row. It is currently the ONLY unrepaired stranded-ampersand
      author.

**Why this blocked the repair:** the merge path relinks the books it can see and
then DELETES the author row. Run through memdb it would relink 0 books for 46627
and delete the author anyway, leaving the 2 Pebble junction rows pointing at an
author id that no longer exists — the orphaning hazard H8 documents on
`maintenance.author-split-scan`. The row was excluded by id for the 2026-08-14
apply rather than papered over with a heuristic.
