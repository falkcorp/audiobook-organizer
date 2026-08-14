## memdb and Pebble disagree about author→book links — ROOT-CAUSED, one step left

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

### ✅ Root cause (2026-08-14, fixed in `fix/author-getter-conformance`)

**memdb's query filtered out non-primary versions; neither Pebble path did.**
`memdb.GetBooksByAuthorID` skipped any book with `IsPrimaryVersion == false`.
Author 46627's 2 links are co-author credits on non-primary versions, so memdb
returned 0 and Pebble returned 2.

Nothing was wrong with the loader, which is why ruling out `safeInsert` was
correct and yet led nowhere: the junction rows loaded fine (`skipped_total=0`,
`book_authors=290643`). The rows were present the whole time and the *query*
discarded the books they pointed at.

A second, opposite divergence was found in the same read: **the Pebble path of
`GetBooksByAuthorIDCore` never opened the junction table at all**, so it saw
only `Book.AuthorID` and was blind to every co-author. One getter under-reported
non-primary versions, the other under-reported co-authors, and the two errors
pointing opposite ways is why aggregate counts stayed plausible for so long.

Contract now pinned by `internal/database/author_getter_conformance_test.go`:

- `...WithRoleCore` — the COMPLETE set (junction + legacy, non-primary
  **included**). Merges and deletes consult this one; a missed link is data loss.
- `...Core` — the LISTING view (junction + legacy, non-primary **excluded**).
- Both exclude soft-deleted books.

- [x] Identify why memdb and Pebble disagreed.
- [x] Write the conformance test rather than a per-path assertion — one fixture,
      both implementations, assert equal. This was the third memdb/Pebble
      divergence in a week (see the soft-deleted leak, #2392).
- [ ] **After this deploys**, drop `skip_author_ids: [46627]` from the repair
      invocation and repair that row. Everything else already applied
      2026-08-14 02:0x: 30 merged, 15 renamed, 0 failures, 145/145 book links
      verified *via the memdb-backed API — the Pebble path was not re-read
      post-apply*. Author 46627 is the ONLY remaining stranded-ampersand row;
      the other two survivors, `&#169` and
      `&#169;2013 by HarperCollinsPublishers`, are the separate HTML-entity
      defect.

**Why this blocked the repair:** the merge path relinks the books it can see and
then DELETES the author row. Run through memdb it would relink 0 books for 46627
and delete the author anyway, leaving the 2 Pebble junction rows pointing at an
author id that no longer exists — the orphaning hazard H8 documents on
`maintenance.author-split-scan`. The row was excluded by id for the 2026-08-14
apply rather than papered over with a heuristic. With the fix deployed, the warm
path sees those 2 links and the merge relinks them before deleting.
